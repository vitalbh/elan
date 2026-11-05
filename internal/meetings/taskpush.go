package meetings

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// ErrTaskGone means the exported task no longer exists in Google Tasks.
var ErrTaskGone = errors.New("tarefa não existe mais no Google Tasks")

// DroppedMarker is appended to the notes of an exported task whose action
// was cancelled in elan: the task is completed, not deleted, so the owner
// still sees it and why it left the list.
const DroppedMarker = "(cancelada no elan)"

// maxPushErrors stops a push after this many failed tasks (e.g. no login):
// the next run retries them.
const maxPushErrors = 3

// RemoteTask is what the push reads and writes of an exported task.
type RemoteTask struct {
	Completed bool
	Notes     string
}

// TaskWriter reads and changes exported tasks (Google Tasks in production).
// GetTask returns ErrTaskGone when the task was deleted.
type TaskWriter interface {
	GetTask(ctx context.Context, listID, taskID string) (RemoteTask, error)
	SetTask(ctx context.Context, listID, taskID string, t RemoteTask) error
}

// TaskPush sends the status of actions changed in elan (by hand, by the
// distiller or by reconcile) to their exported Google Tasks: done → task
// completed; dropped → completed with DroppedMarker in the notes; open →
// task reopened. It is the other half of TaskStatusSync.
type TaskPush struct {
	db  *sql.DB
	w   TaskWriter
	now func() time.Time
}

// NewTaskPush builds a TaskPush. w may be nil for dry runs.
func NewTaskPush(db *sql.DB, w TaskWriter) *TaskPush {
	return &TaskPush{db: db, w: w, now: time.Now}
}

// PushChange is one exported task the push looked at.
type PushChange struct {
	ActionID int64  `json:"action_id"`
	Text     string `json:"text"`
	ListID   string `json:"list_id"`
	TaskID   string `json:"task_id"`
	// Status is the action's status being pushed.
	Status string `json:"status"`
	// Result: planned (dry run), pushed, unchanged (Tasks already had it),
	// gone (task deleted) or failed.
	Result string `json:"result"`
}

// PushStats summarises a push.
type PushStats struct {
	Pushed, Unchanged, Gone, Failed int
	Changes                         []PushChange
}

type pushRow struct {
	exportRow
	id                   int64
	listID, taskID       string
	pushedStatus, pushed string
}

// Push sends what changed since the last push. A status that came from
// Google Tasks (source tasks) is never sent back, only recorded as what
// Tasks has. Each task is read before it is written and only written when
// it differs, so a retried or repeated push is harmless. A failed task is
// retried on the next run.
func (p *TaskPush) Push(ctx context.Context, dryRun bool) (PushStats, error) {
	var st PushStats
	rows, err := p.db.QueryContext(ctx, `
		SELECT id, meeting_id, text_hash, list_id, task_id, pushed_status, pushed_at
		FROM task_exports ORDER BY id`)
	if err != nil {
		return st, err
	}
	var exports []pushRow
	for rows.Next() {
		var r pushRow
		if err := rows.Scan(&r.id, &r.meetingID, &r.textHash, &r.listID, &r.taskID, &r.pushedStatus, &r.pushed); err != nil {
			rows.Close()
			return st, err
		}
		exports = append(exports, r)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return st, err
	}

	var errs []error
	for _, r := range exports {
		if ctx.Err() != nil {
			return st, ctx.Err()
		}
		item, ok, err := findExportAction(ctx, p.db, r.exportRow)
		if err != nil {
			return st, err
		}
		if !ok {
			continue
		}
		statusAt, _ := time.Parse(time.RFC3339, item.at.String)
		pushedAt, _ := time.Parse(time.RFC3339, r.pushed)
		newer := !statusAt.IsZero() && statusAt.After(pushedAt)

		if item.source.String == SourceTasks {
			// Tasks already has it: remember, so a later change in elan
			// compares against it.
			if !dryRun && (r.pushedStatus != item.status || newer) {
				if err := p.record(ctx, r.id, item); err != nil {
					return st, err
				}
			}
			continue
		}
		known := r.pushedStatus
		if known == "" {
			if !newer {
				continue // never changed in elan, or changed before the baseline
			}
			known = StatusOpen // as exported
		}
		if item.status == known && !newer {
			continue
		}
		ch := PushChange{ActionID: item.id, ListID: r.listID, TaskID: r.taskID, Status: item.status}
		_ = p.db.QueryRowContext(ctx, `SELECT text FROM meeting_items WHERE id = ?`, item.id).Scan(&ch.Text)
		if dryRun {
			ch.Result = "planned"
			st.Changes = append(st.Changes, ch)
			continue
		}
		ch.Result, err = p.pushOne(ctx, r, item.status)
		switch {
		case err != nil:
			if ctx.Err() != nil {
				return st, ctx.Err()
			}
			ch.Result = "failed"
			st.Failed++
			errs = append(errs, fmt.Errorf("tarefa %s (ação %d): %w", r.taskID, item.id, err))
		case ch.Result == "pushed":
			st.Pushed++
		case ch.Result == "gone":
			st.Gone++
		default:
			st.Unchanged++
		}
		st.Changes = append(st.Changes, ch)
		if err == nil {
			if err := p.record(ctx, r.id, item); err != nil {
				return st, err
			}
		}
		if len(errs) >= maxPushErrors {
			break
		}
	}
	return st, errors.Join(errs...)
}

// pushOne makes the task reflect status, reading it first.
func (p *TaskPush) pushOne(ctx context.Context, r pushRow, status string) (string, error) {
	cur, err := p.w.GetTask(ctx, r.listID, r.taskID)
	if errors.Is(err, ErrTaskGone) {
		return "gone", nil
	}
	if err != nil {
		return "", err
	}
	want := RemoteTask{Completed: status != StatusOpen, Notes: withDroppedMarker(cur.Notes, status == StatusDropped)}
	if cur == want {
		return "unchanged", nil
	}
	if err := p.w.SetTask(ctx, r.listID, r.taskID, want); err != nil {
		if errors.Is(err, ErrTaskGone) {
			return "gone", nil
		}
		return "", err
	}
	return "pushed", nil
}

// record notes that Google Tasks reflects the action's current status.
func (p *TaskPush) record(ctx context.Context, exportID int64, item actionRow) error {
	at := item.at.String
	if _, err := time.Parse(time.RFC3339, at); err != nil {
		at = p.now().UTC().Format(time.RFC3339)
	}
	_, err := p.db.ExecContext(ctx, `UPDATE task_exports SET pushed_status = ?, pushed_at = ? WHERE id = ?`,
		item.status, at, exportID)
	return err
}

// withDroppedMarker returns notes with DroppedMarker at the end (on) or
// without it (off).
func withDroppedMarker(notes string, on bool) string {
	clean := strings.TrimSpace(strings.ReplaceAll(notes, DroppedMarker, ""))
	if !on {
		if clean == strings.TrimSpace(notes) {
			return notes // untouched: no marker to remove
		}
		return clean
	}
	if clean == "" {
		return DroppedMarker
	}
	return clean + "\n\n" + DroppedMarker
}
