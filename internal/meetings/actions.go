package meetings

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
)

// Status of an action item.
const (
	StatusOpen    = "open"
	StatusDone    = "done"
	StatusDropped = "dropped"
)

// Who set an action's status (meeting_items.status_source).
const (
	SourceDistill = "distill"
	SourceTasks   = "tasks"
	SourceManual  = "manual"
)

// DefaultStaleAfter is the age past which an open action counts as stale.
const DefaultStaleAfter = 30 * 24 * time.Hour

// ErrActionNotFound means there is no action item with that id.
var ErrActionNotFound = errors.New("ação não encontrada")

// dbtx is what *sql.DB and *sql.Tx have in common.
type dbtx interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// seriesAction is an open action of an earlier meeting (of the same
// recurring series, or a related one), offered to the model so it can
// report it done.
type seriesAction struct {
	ID     int64
	Date   string
	Text   string
	People []string
	// Meeting is the title of the meeting it came from, set for actions
	// of other meetings (empty for the series' own).
	Meeting string
}

// seriesActions returns the open actions of earlier meetings of m's
// recurring series, oldest first, capped to the most recent
// maxSeriesActions. Actions m itself closed count as open: m's answer is
// about to be replaced. Actions the owner reopened by hand are left out:
// a manual status wins.
func seriesActions(ctx context.Context, q dbtx, m distillMeeting) ([]seriesAction, error) {
	if !m.Recurring {
		return nil, nil
	}
	rows, err := q.QueryContext(ctx, `
		SELECT id, date, text, people FROM (
			SELECT i.id, COALESCE(m.started_at, '') AS date, i.text, i.people,
				julianday(m.started_at) AS jd
			FROM meeting_items i JOIN meetings m ON m.id = i.meeting_id
			WHERE m.folder_id = @folder AND m.recurring = 1 AND m.id <> @id
				AND i.kind = 'action'
				AND ((i.status = 'open' AND i.status_source IS NOT 'manual')
					OR (i.status_source = 'distill' AND i.done_in = @id))
				AND m.started_at IS NOT NULL
				AND (@started IS NULL OR julianday(m.started_at) < julianday(@started))
			ORDER BY jd DESC, i.id DESC
			LIMIT @lim
		) ORDER BY jd, id`,
		sql.Named("folder", m.FolderID), sql.Named("id", m.ID),
		sql.Named("started", m.StartedAt), sql.Named("lim", maxSeriesActions))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []seriesAction
	for rows.Next() {
		var a seriesAction
		var people string
		if err := rows.Scan(&a.ID, &a.Date, &a.Text, &people); err != nil {
			return nil, err
		}
		_ = json.Unmarshal([]byte(people), &a.People)
		out = append(out, a)
	}
	return out, rows.Err()
}

func actionIDs(actions []seriesAction) map[int64]bool {
	ids := make(map[int64]bool, len(actions))
	for _, a := range actions {
		ids[a.ID] = true
	}
	return ids
}

// reopenClosedBy reopens the actions a meeting's previous distillation (or
// reconciliation) had closed, so its new answer judges them again.
func reopenClosedBy(ctx context.Context, q dbtx, meetingID string) error {
	_, err := q.ExecContext(ctx, `
		UPDATE meeting_items
		SET status = 'open', status_at = NULL, status_source = NULL, done_by = NULL, done_in = NULL
		WHERE status_source = 'distill' AND done_in = ?`, meetingID)
	return err
}

// closeActions marks earlier actions of the series done (completes) or
// dropped (drops) as reported by meetingID; doneBy is the reporting item,
// when there is one. Ids outside allowed, of other kinds, of the same
// meeting, already closed or set by hand are ignored. An id in both lists
// counts as done.
func closeActions(ctx context.Context, q dbtx, meetingID string, doneBy sql.NullInt64,
	completes, drops []int64, allowed map[int64]bool, now string) (done, dropped int, err error) {
	apply := func(ids []int64, status string) (int, error) {
		n := 0
		for _, id := range ids {
			if !allowed[id] {
				continue
			}
			res, err := q.ExecContext(ctx, `
				UPDATE meeting_items
				SET status = ?, status_at = ?, status_source = 'distill', done_by = ?, done_in = ?
				WHERE id = ? AND kind = 'action' AND status = 'open'
					AND status_source IS NOT 'manual' AND meeting_id <> ?`,
				status, now, doneBy, meetingID, id, meetingID)
			if err != nil {
				return n, err
			}
			if k, _ := res.RowsAffected(); k > 0 {
				n++
			}
		}
		return n, nil
	}
	if done, err = apply(completes, StatusDone); err != nil {
		return done, 0, err
	}
	dropped, err = apply(drops, StatusDropped)
	return done, dropped, err
}

// carriedStatus is the status of one of a meeting's own actions, kept
// across a re-distillation.
type carriedStatus struct {
	Status string
	At     sql.NullString
	Source sql.NullString
	DoneBy sql.NullInt64
	DoneIn sql.NullString
}

func (c carriedStatus) status() string {
	if c.Status == "" {
		return StatusOpen
	}
	return c.Status
}

// ownStatuses returns the statuses ever set on a meeting's actions, by
// textHash of their text.
func ownStatuses(ctx context.Context, q dbtx, meetingID string) (map[string]carriedStatus, error) {
	rows, err := q.QueryContext(ctx, `
		SELECT text, status, status_at, status_source, done_by, done_in
		FROM meeting_items
		WHERE meeting_id = ? AND kind = 'action' AND status_source IS NOT NULL`, meetingID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]carriedStatus{}
	for rows.Next() {
		var text string
		var c carriedStatus
		if err := rows.Scan(&text, &c.Status, &c.At, &c.Source, &c.DoneBy, &c.DoneIn); err != nil {
			return nil, err
		}
		out[textHash(text)] = c
	}
	return out, rows.Err()
}

// ActionOwner selects whose actions a query returns.
type ActionOwner string

// Values of ActionOwner.
const (
	// OwnerAll: every open action ("" means the same).
	OwnerAll ActionOwner = "all"
	// OwnerMine: actions whose people include one of the owner's names.
	OwnerMine ActionOwner = "mine"
	// OwnerOthers: actions assigned to someone, none of them the owner. Every
	// meeting in the database is one the owner attended, so this is "what
	// the owner is waiting on from others".
	OwnerOthers ActionOwner = "others"
)

// ActionQuery filters OpenActions. Zero values mean no filter.
type ActionQuery struct {
	Owner ActionOwner
	// Person keeps actions where some person's name contains these words
	// (case- and accent-insensitive, whole words: "ana" matches "Ana Paula").
	Person string
	// Since keeps actions of meetings at or after it.
	Since time.Time
	// IncludeStale also returns open actions older than StaleAfter.
	IncludeStale bool
	// StaleAfter overrides DefaultStaleAfter when > 0.
	StaleAfter time.Duration
	// Limit caps the result (default 50).
	Limit int
}

// OpenAction is an action item still open.
type OpenAction struct {
	ID           int64      `json:"id"`
	Text         string     `json:"text"`
	People       []string   `json:"people,omitempty"`
	MeetingID    string     `json:"meeting_id"`
	MeetingTitle string     `json:"meeting_title"`
	MeetingAt    *time.Time `json:"meeting_at,omitempty"`
	// AgeDays counts from the meeting (from its ingestion when undated).
	AgeDays int `json:"age_days"`
	// Stale: open for longer than the query's StaleAfter.
	Stale bool `json:"stale"`
	// Mine: one of the people is the owner.
	Mine bool `json:"mine"`
	// The Google Task the action was exported to, if any.
	TaskID     string `json:"task_id,omitempty"`
	TaskListID string `json:"task_list_id,omitempty"`
	TaskLink   string `json:"task_link,omitempty"`
}

// ActionState is an action's status after an update.
type ActionState struct {
	ID       int64  `json:"id"`
	Text     string `json:"text"`
	Status   string `json:"status"`
	Source   string `json:"source,omitempty"`
	StatusAt string `json:"status_at,omitempty"`
}

// Actions reads and updates the status of action items.
type Actions struct {
	db    *sql.DB
	names map[string]bool
	now   func() time.Time
}

// NewActions builds an Actions. ownerNames identify the owner in the items'
// people, as in NewTaskExporter.
func NewActions(db *sql.DB, ownerNames []string) *Actions {
	names := map[string]bool{}
	for _, n := range ownerNames {
		if f := foldName(n); f != "" {
			names[f] = true
		}
	}
	return &Actions{db: db, names: names, now: time.Now}
}

// OpenActions lists open action items, newest meeting first.
func (a *Actions) OpenActions(ctx context.Context, q ActionQuery) ([]OpenAction, error) {
	switch q.Owner {
	case "", OwnerAll:
	case OwnerMine, OwnerOthers:
		if len(a.names) == 0 {
			return nil, errors.New("sem nomes do dono: preencha owner.names no config.json do perfil")
		}
	default:
		return nil, fmt.Errorf("owner inválido %q: use mine, others ou all", q.Owner)
	}
	if q.Limit <= 0 {
		q.Limit = 50
	}
	staleAfter := q.StaleAfter
	if staleAfter <= 0 {
		staleAfter = DefaultStaleAfter
	}
	person := foldName(q.Person)

	where := "i.kind = 'action' AND i.status = 'open'"
	var args []any
	if !q.Since.IsZero() {
		where += " AND julianday(COALESCE(m.started_at, m.ingested_at)) >= julianday(?)"
		args = append(args, q.Since.Format(time.RFC3339))
	}
	rows, err := a.db.QueryContext(ctx, `
		SELECT i.id, i.text, i.people, m.id, m.title, m.started_at, COALESCE(m.started_at, m.ingested_at)
		FROM meeting_items i JOIN meetings m ON m.id = i.meeting_id
		WHERE `+where+`
		ORDER BY julianday(COALESCE(m.started_at, m.ingested_at)) DESC, i.id`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	now := a.now()
	var out []OpenAction
	for rows.Next() && len(out) < q.Limit {
		var o OpenAction
		var people, at string
		var started sql.NullString
		if err := rows.Scan(&o.ID, &o.Text, &people, &o.MeetingID, &o.MeetingTitle, &started, &at); err != nil {
			return nil, err
		}
		_ = json.Unmarshal([]byte(people), &o.People)
		o.MeetingAt = parseStarted(started)
		if t, err := time.Parse(time.RFC3339, at); err == nil {
			age := now.Sub(t)
			o.AgeDays = int(age.Hours() / 24)
			o.Stale = age > staleAfter
		}
		if o.Stale && !q.IncludeStale {
			continue
		}
		o.Mine = a.isOwner(o.People)
		switch q.Owner {
		case OwnerMine:
			if !o.Mine {
				continue
			}
		case OwnerOthers:
			if o.Mine || len(o.People) == 0 {
				continue
			}
		}
		if person != "" && !hasPerson(o.People, person) {
			continue
		}
		out = append(out, o)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	rows.Close()
	return out, a.attachTasks(ctx, out)
}

// SetStatus sets an action's status by hand (open, done or dropped). A
// manual status wins over what the distiller or the Google Tasks back-sync
// would set later.
func (a *Actions) SetStatus(ctx context.Context, id int64, status string) (ActionState, error) {
	if err := validStatus(status); err != nil {
		return ActionState{}, err
	}
	return setActionStatus(ctx, a.db, id, status, a.now().UTC().Format(time.RFC3339))
}

// validStatus checks a status set by hand.
func validStatus(status string) error {
	switch status {
	case StatusOpen, StatusDone, StatusDropped:
		return nil
	default:
		return fmt.Errorf("status inválido %q: use open, done ou dropped", status)
	}
}

// setActionStatus is SetStatus within q (a transaction or the database).
func setActionStatus(ctx context.Context, q dbtx, id int64, status, now string) (ActionState, error) {
	res, err := q.ExecContext(ctx, `
		UPDATE meeting_items
		SET status = ?, status_at = ?, status_source = 'manual', done_by = NULL, done_in = NULL
		WHERE id = ? AND kind = 'action'`, status, now, id)
	if err != nil {
		return ActionState{}, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ActionState{}, fmt.Errorf("%w: %d", ErrActionNotFound, id)
	}
	st := ActionState{ID: id, Status: status, Source: SourceManual, StatusAt: now}
	err = q.QueryRowContext(ctx, `SELECT text FROM meeting_items WHERE id = ?`, id).Scan(&st.Text)
	return st, err
}

func (a *Actions) isOwner(people []string) bool {
	for _, p := range people {
		if a.names[foldName(p)] {
			return true
		}
	}
	return false
}

// hasPerson reports whether some name contains the folded words of q.
func hasPerson(people []string, q string) bool {
	for _, p := range people {
		if strings.Contains(" "+foldName(p)+" ", " "+q+" ") {
			return true
		}
	}
	return false
}

// attachTasks fills the Google Task of the actions that were exported.
func (a *Actions) attachTasks(ctx context.Context, actions []OpenAction) error {
	if len(actions) == 0 {
		return nil
	}
	meetings := map[string]bool{}
	for _, o := range actions {
		meetings[o.MeetingID] = true
	}
	ids := make([]string, 0, len(meetings))
	for id := range meetings {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	args := make([]any, len(ids))
	for i, id := range ids {
		args[i] = id
	}
	rows, err := a.db.QueryContext(ctx, `
		SELECT meeting_id, text_hash, list_id, task_id, web_link FROM task_exports
		WHERE meeting_id IN (?`+strings.Repeat(", ?", len(ids)-1)+`)`, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	type task struct{ list, id, link string }
	tasks := map[string]task{}
	for rows.Next() {
		var meeting, hash string
		var t task
		if err := rows.Scan(&meeting, &hash, &t.list, &t.id, &t.link); err != nil {
			return err
		}
		tasks[meeting+"|"+hash] = t
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for i := range actions {
		if t, ok := tasks[actions[i].MeetingID+"|"+textHash(actions[i].Text)]; ok {
			actions[i].TaskID, actions[i].TaskListID, actions[i].TaskLink = t.id, t.list, t.link
		}
	}
	return nil
}
