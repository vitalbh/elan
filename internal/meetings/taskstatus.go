package meetings

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// TaskState is an exported task as the status back-sync sees it.
type TaskState struct {
	ID        string
	Completed bool
	Deleted   bool
	// At is when the task last changed (completion time when completed).
	At      time.Time
	WebLink string
}

// TaskStatusSource lists the tasks of a Google Tasks list changed at or
// after updatedMin (all of them when zero), completed and deleted ones
// included.
type TaskStatusSource interface {
	ListTasks(ctx context.Context, listID string, updatedMin time.Time) ([]TaskState, error)
}

// cursorSkew rewinds each list's cursor a little, so a task updated while
// the previous sync ran is not missed.
const cursorSkew = 5 * time.Minute

// TaskStatusSync brings the status of exported Google Tasks back to their
// action items: completed → done, deleted → dropped (source tasks).
type TaskStatusSync struct {
	db  *sql.DB
	src TaskStatusSource
	now func() time.Time
}

// NewTaskStatusSync builds a TaskStatusSync.
func NewTaskStatusSync(db *sql.DB, src TaskStatusSource) *TaskStatusSync {
	return &TaskStatusSync{db: db, src: src, now: time.Now}
}

// TaskStatusStats summarises a back-sync.
type TaskStatusStats struct {
	Lists, Seen, Done, Dropped, Reopened int
}

// Sync reads only the lists that hold exported tasks. The first sync of a
// list reads every task and treats an exported task that is gone (purged)
// as deleted; later syncs only read what changed since the last one.
//
// The rules are applyTo's.
func (s *TaskStatusSync) Sync(ctx context.Context) (TaskStatusStats, error) {
	var st TaskStatusStats
	lists, err := s.lists(ctx)
	if err != nil {
		return st, err
	}
	var errs []error
	for _, list := range lists {
		if err := s.syncList(ctx, list, &st); err != nil {
			if ctx.Err() != nil {
				return st, err
			}
			errs = append(errs, fmt.Errorf("lista %s: %w", list, err))
			continue
		}
		st.Lists++
	}
	return st, errors.Join(errs...)
}

func (s *TaskStatusSync) lists(ctx context.Context) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT DISTINCT list_id FROM task_exports ORDER BY list_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

type exportRow struct {
	meetingID, textHash, webLink string
}

func (s *TaskStatusSync) syncList(ctx context.Context, list string, st *TaskStatusStats) error {
	start := s.now().UTC()
	var cursor sql.NullString
	if err := s.db.QueryRowContext(ctx, `SELECT synced_at FROM task_list_sync WHERE list_id = ?`, list).
		Scan(&cursor); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	var updatedMin time.Time
	if cursor.Valid {
		t, err := time.Parse(time.RFC3339, cursor.String)
		if err != nil {
			return fmt.Errorf("cursor %q: %w", cursor.String, err)
		}
		updatedMin = t.Add(-cursorSkew)
	}
	tasks, err := s.src.ListTasks(ctx, list, updatedMin)
	if err != nil {
		return err
	}
	exports, err := s.exports(ctx, list)
	if err != nil {
		return err
	}
	seen := map[string]bool{}
	for _, t := range tasks {
		e, ok := exports[t.ID]
		if !ok {
			continue // a task the owner created by hand
		}
		seen[t.ID] = true
		st.Seen++
		if t.WebLink != "" && t.WebLink != e.webLink {
			if _, err := s.db.ExecContext(ctx, `UPDATE task_exports SET web_link = ? WHERE list_id = ? AND task_id = ?`,
				t.WebLink, list, t.ID); err != nil {
				return err
			}
		}
		target := StatusOpen
		switch {
		case t.Deleted:
			target = StatusDropped
		case t.Completed:
			target = StatusDone
		}
		if err := s.apply(ctx, e, target, t.At, st); err != nil {
			return err
		}
	}
	if !cursor.Valid {
		for id, e := range exports {
			if !seen[id] {
				if err := s.apply(ctx, e, StatusDropped, start, st); err != nil {
					return err
				}
			}
		}
	}
	_, err = s.db.ExecContext(ctx, `
		INSERT INTO task_list_sync (list_id, synced_at) VALUES (?, ?)
		ON CONFLICT (list_id) DO UPDATE SET synced_at = excluded.synced_at`,
		list, start.Format(time.RFC3339))
	return err
}

func (s *TaskStatusSync) exports(ctx context.Context, list string) (map[string]exportRow, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT task_id, meeting_id, text_hash, web_link FROM task_exports WHERE list_id = ?`, list)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]exportRow{}
	for rows.Next() {
		var id string
		var e exportRow
		if err := rows.Scan(&id, &e.meetingID, &e.textHash, &e.webLink); err != nil {
			return nil, err
		}
		out[id] = e
	}
	return out, rows.Err()
}

// apply moves the action of an exported task to target.
func (s *TaskStatusSync) apply(ctx context.Context, e exportRow, target string, at time.Time, st *TaskStatusStats) error {
	item, ok, err := findExportAction(ctx, s.db, e)
	if err != nil || !ok {
		return err // no action with that text any more (re-distilled with new wording)
	}
	changed, err := s.applyTo(ctx, item, target, at)
	if err != nil || !changed {
		return err
	}
	switch target {
	case StatusDone:
		st.Done++
	case StatusDropped:
		st.Dropped++
	default:
		st.Reopened++
	}
	return nil
}

// applyTo moves an action to the status its task has in Google Tasks, as of
// at. A task that was completed or deleted closes an open action. A task
// that is open reopens a closed action when the back-sync had closed it, or
// when the task changed after the action was closed (the owner reopened in
// Google Tasks a task elan had completed). A manual status set after the
// task's last change wins.
func (s *TaskStatusSync) applyTo(ctx context.Context, item actionRow, target string, at time.Time) (bool, error) {
	statusAt, statusAtErr := time.Parse(time.RFC3339, item.at.String)
	if item.source.String == SourceManual && statusAtErr == nil && !at.After(statusAt) {
		return false, nil
	}
	switch {
	case target != StatusOpen && item.status == StatusOpen:
	case target == StatusOpen && item.status != StatusOpen &&
		(item.source.String == SourceTasks || (statusAtErr == nil && at.After(statusAt))):
	default:
		return false, nil
	}
	if at.IsZero() {
		at = s.now()
	}
	_, err := s.db.ExecContext(ctx, `
		UPDATE meeting_items
		SET status = ?, status_at = ?, status_source = 'tasks', done_by = NULL, done_in = NULL
		WHERE id = ?`, target, at.UTC().Format(time.RFC3339), item.id)
	return err == nil, err
}

// ApplyTask brings the state of one task, just changed by elan itself (the
// MCP tool tasks_complete), to its action, with the back-sync's rules. It
// returns the action's state, or nil when the task is not an exported
// action (or its action is gone).
func (s *TaskStatusSync) ApplyTask(ctx context.Context, listID string, t TaskState) (*ActionState, error) {
	var e exportRow
	err := s.db.QueryRowContext(ctx,
		`SELECT meeting_id, text_hash, web_link FROM task_exports WHERE list_id = ? AND task_id = ?`, listID, t.ID).
		Scan(&e.meetingID, &e.textHash, &e.webLink)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	item, ok, err := findExportAction(ctx, s.db, e)
	if err != nil || !ok {
		return nil, err
	}
	target := StatusOpen
	switch {
	case t.Deleted:
		target = StatusDropped
	case t.Completed:
		target = StatusDone
	}
	if _, err := s.applyTo(ctx, item, target, t.At); err != nil {
		return nil, err
	}
	st := &ActionState{ID: item.id}
	var src, at sql.NullString
	err = s.db.QueryRowContext(ctx, `SELECT text, status, status_source, status_at FROM meeting_items WHERE id = ?`, item.id).
		Scan(&st.Text, &st.Status, &src, &at)
	st.Source, st.StatusAt = src.String, at.String
	return st, err
}

type actionRow struct {
	id         int64
	status     string
	source, at sql.NullString
}

// ApplyTask is TaskStatusSync.ApplyTask for callers that hold an Actions
// (the MCP server): a task elan just completed or reopened in Google Tasks
// moves its action like the back-sync would.
func (a *Actions) ApplyTask(ctx context.Context, listID string, t TaskState) (*ActionState, error) {
	return (&TaskStatusSync{db: a.db, now: a.now}).ApplyTask(ctx, listID, t)
}

// findExportAction returns the action an exported task was made from: the
// meeting's action item with the same normalized text.
func findExportAction(ctx context.Context, q dbtx, e exportRow) (actionRow, bool, error) {
	rows, err := q.QueryContext(ctx, `
		SELECT id, text, status, status_source, status_at FROM meeting_items
		WHERE meeting_id = ? AND kind = 'action' ORDER BY id`, e.meetingID)
	if err != nil {
		return actionRow{}, false, err
	}
	defer rows.Close()
	for rows.Next() {
		var a actionRow
		var text string
		if err := rows.Scan(&a.id, &text, &a.status, &a.source, &a.at); err != nil {
			return actionRow{}, false, err
		}
		if textHash(text) == e.textHash {
			return a, true, nil
		}
	}
	return actionRow{}, false, rows.Err()
}
