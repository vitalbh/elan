package meetings

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/vitalbh/elan/internal/llm"
)

// Item kinds of meeting_items.
const (
	KindSummary  = "summary"
	KindDecision = "decision"
	KindAction   = "action"
	KindFact     = "fact"
)

const (
	// DefaultTranscriptBudget caps the transcript sent to the model, in
	// characters. The notes are the primary source; the transcript is
	// supporting evidence and can be 100k+ characters.
	DefaultTranscriptBudget = 40000
	// notesBudget caps the notes, as a guard (Gemini notes are ~10-20k).
	notesBudget = 60000
	// maxSeriesDecisions caps the earlier decisions of a series put in the
	// prompt (the most recent ones win).
	maxSeriesDecisions = 80
	// maxSeriesActions caps the earlier open actions of a series put in the
	// prompt (the most recent ones win).
	maxSeriesActions = 60
)

// Distiller turns meetings into meeting_items through an LLM.
type Distiller struct {
	db     *sql.DB
	runner llm.Runner
	// Model is passed to the runner ("sonnet" when empty).
	Model string
	// TranscriptBudget overrides DefaultTranscriptBudget when > 0.
	TranscriptBudget int
	// Timeout bounds one meeting's LLM call (default 10 minutes).
	Timeout time.Duration
	// KnownProjects are the projects of config.json, offered to the model
	// so it names an action's project with the configured name.
	KnownProjects []Project
	// Related, when set, adds open actions of other meetings that look
	// related to the one being distilled to those of its series.
	Related RelatedActions
	now     func() time.Time
}

// NewDistiller builds a Distiller.
func NewDistiller(db *sql.DB, runner llm.Runner) *Distiller {
	return &Distiller{db: db, runner: runner, now: time.Now}
}

// DistillStats summarises a distillation run.
type DistillStats struct {
	Distilled, Failed, Items, Superseded int
	// Completed and Dropped count earlier actions this run closed.
	Completed, Dropped int
	// Pending is how many meetings still need distillation after the run.
	Pending int
}

// distillMeeting is the slice of a meeting the distiller uses.
type distillMeeting struct {
	ID, Title, FolderID, FolderName string
	Recurring                       bool
	StartedAt                       sql.NullString
	Header, Notes, Transcript       string
	ContentHash                     string
}

// seriesDecision is a still valid decision of an earlier meeting of the same
// recurring series.
type seriesDecision struct {
	ID        int64
	Kind      string // decision or fact
	Date      string
	Meeting   string
	Text      string
	MeetingID string
}

// Item is one distilled item as the model returns it.
type Item struct {
	Kind       string   `json:"kind"`
	Text       string   `json:"text"`
	People     []string `json:"people"`
	Importance int      `json:"importance"`
	Supersedes []int64  `json:"supersedes"`
	// Completes and Drops are ids of earlier open actions of the series this
	// item reports as done or as cancelled.
	Completes []int64 `json:"completes"`
	Drops     []int64 `json:"drops"`
	// Category ("code" or "other") and Project classify actions; empty on
	// the other kinds.
	Category string `json:"category"`
	Project  string `json:"project"`
}

type distillOutput struct {
	Items []Item `json:"items"`
}

// Distill processes up to limit meetings whose content changed since their
// last distillation, oldest first, so earlier meetings of a series are
// distilled before the decisions they hold are offered to later ones.
// progress, when set, is called with each meeting's title before its call.
//
// A meeting that fails (bad answer, invalid JSON) is counted and skipped; it
// stays pending. A usage limit (*llm.LimitError) or a cancelled ctx stops
// the run and is returned as is.
func (d *Distiller) Distill(ctx context.Context, limit int, progress func(title string)) (DistillStats, error) {
	var st DistillStats
	ids, err := d.pending(ctx, limit)
	if err != nil {
		return st, err
	}
	var errs []error
	for _, id := range ids {
		if err := ctx.Err(); err != nil {
			return st, err
		}
		m, err := d.load(ctx, id)
		if err != nil {
			return st, err
		}
		if progress != nil {
			progress(m.Title)
		}
		res, err := d.distillOne(ctx, m)
		if err != nil {
			if _, ok := llm.IsLimit(err); ok || ctx.Err() != nil {
				return st, err
			}
			st.Failed++
			errs = append(errs, fmt.Errorf("%s: %w", m.Title, err))
			continue
		}
		st.Distilled++
		st.Items += res.items
		st.Superseded += res.superseded
		st.Completed += res.completed
		st.Dropped += res.dropped
	}
	if err := d.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM meetings WHERE distilled_hash <> content_hash`).Scan(&st.Pending); err != nil {
		errs = append(errs, err)
	}
	return st, errors.Join(errs...)
}

// pending lists meetings to distill, oldest first, undated last.
func (d *Distiller) pending(ctx context.Context, limit int) ([]string, error) {
	if limit <= 0 {
		limit = -1 // SQLite: no limit
	}
	rows, err := d.db.QueryContext(ctx, `
		SELECT id FROM meetings
		WHERE distilled_hash <> content_hash
		ORDER BY started_at IS NULL, julianday(started_at), id
		LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

func (d *Distiller) load(ctx context.Context, id string) (distillMeeting, error) {
	return loadMeeting(ctx, d.db, id)
}

func loadMeeting(ctx context.Context, q dbtx, id string) (distillMeeting, error) {
	var m distillMeeting
	err := q.QueryRowContext(ctx, `
		SELECT id, title, folder_id, folder_name, recurring, started_at, header, notes, transcript, content_hash
		FROM meetings WHERE id = ?`, id).Scan(
		&m.ID, &m.Title, &m.FolderID, &m.FolderName, &m.Recurring, &m.StartedAt,
		&m.Header, &m.Notes, &m.Transcript, &m.ContentHash)
	return m, err
}

// seriesDecisions returns the still valid decisions of earlier meetings of
// m's recurring series, oldest first. An undated meeting counts every dated
// meeting of the series as earlier. Decisions superseded by m's own current
// items count as valid: those items are about to be replaced.
func (d *Distiller) seriesDecisions(ctx context.Context, m distillMeeting) ([]seriesDecision, error) {
	if !m.Recurring {
		return nil, nil
	}
	rows, err := d.db.QueryContext(ctx, `
		SELECT id, kind, date, title, text, meeting_id FROM (
			SELECT i.id, i.kind, COALESCE(m.started_at, '') AS date, m.title, i.text, m.id AS meeting_id,
				julianday(m.started_at) AS jd
			FROM meeting_items i JOIN meetings m ON m.id = i.meeting_id
			WHERE m.folder_id = @folder AND m.recurring = 1 AND m.id <> @id
				AND (i.kind = 'decision' OR (i.kind = 'fact' AND i.importance >= 2))
				AND (i.superseded_by IS NULL OR i.superseded_by IN (
					SELECT id FROM meeting_items WHERE meeting_id = @id))
				AND m.started_at IS NOT NULL
				AND (@started IS NULL OR julianday(m.started_at) < julianday(@started))
			ORDER BY jd DESC, i.id DESC
			LIMIT @lim
		) ORDER BY jd, id`,
		sql.Named("folder", m.FolderID), sql.Named("id", m.ID),
		sql.Named("started", m.StartedAt), sql.Named("lim", maxSeriesDecisions))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []seriesDecision
	for rows.Next() {
		var s seriesDecision
		if err := rows.Scan(&s.ID, &s.Kind, &s.Date, &s.Meeting, &s.Text, &s.MeetingID); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// saveResult counts what one meeting's save changed.
type saveResult struct {
	items, superseded, completed, dropped int
}

func (d *Distiller) distillOne(ctx context.Context, m distillMeeting) (saveResult, error) {
	prior, err := d.seriesDecisions(ctx, m)
	if err != nil {
		return saveResult{}, err
	}
	actions, err := offeredActions(ctx, d.db, m, d.Related)
	if err != nil {
		return saveResult{}, err
	}
	budget := d.TranscriptBudget
	if budget <= 0 {
		budget = DefaultTranscriptBudget
	}
	model := d.Model
	if model == "" {
		model = "sonnet"
	}
	timeout := d.Timeout
	if timeout <= 0 {
		timeout = 10 * time.Minute
	}
	callCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	raw, err := d.runner.JSON(callCtx, llm.Request{
		System: distillSystem,
		Prompt: buildPrompt(m, prior, actions, budget) + knownProjectsHint(d.KnownProjects),
		Model:  model,
		Schema: distillSchema,
	})
	if err != nil {
		return saveResult{}, err
	}
	out, err := parseItems(raw)
	if err != nil {
		return saveResult{}, err
	}
	allowed := make(map[int64]bool, len(prior))
	for _, p := range prior {
		allowed[p.ID] = true
	}
	return d.save(ctx, m, out, allowed, actionIDs(actions))
}

// parseItems decodes and validates the model's answer.
func parseItems(raw []byte) ([]Item, error) {
	var out distillOutput
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&out); err != nil {
		return nil, fmt.Errorf("JSON da destilação: %w", err)
	}
	if out.Items == nil {
		return nil, errors.New("JSON da destilação sem items")
	}
	summaries := 0
	items := make([]Item, 0, len(out.Items))
	for i, it := range out.Items {
		it.Kind = strings.TrimSpace(it.Kind)
		it.Text = strings.TrimSpace(it.Text)
		switch it.Kind {
		case KindSummary:
			summaries++
		case KindDecision, KindAction, KindFact:
		default:
			return nil, fmt.Errorf("item %d: kind inválido %q", i, it.Kind)
		}
		if it.Text == "" {
			return nil, fmt.Errorf("item %d: texto vazio", i)
		}
		if it.Importance == 0 {
			it.Importance = 2
		}
		if it.Importance < 1 || it.Importance > 3 {
			return nil, fmt.Errorf("item %d: importance %d fora de 1..3", i, it.Importance)
		}
		it.People = cleanPeople(it.People)
		if it.Kind != KindDecision && it.Kind != KindFact {
			it.Supersedes = nil // only decisions and facts replace decisions and facts
		}
		if err := cleanClass(&it); err != nil {
			return nil, fmt.Errorf("item %d: %w", i, err)
		}
		items = append(items, it)
	}
	if summaries > 1 {
		return nil, fmt.Errorf("%d itens summary, esperado no máximo 1", summaries)
	}
	return items, nil
}

func cleanPeople(in []string) []string {
	out := make([]string, 0, len(in))
	seen := map[string]bool{}
	for _, p := range in {
		p = strings.TrimSpace(p)
		k := strings.ToLower(p)
		if p == "" || seen[k] {
			continue
		}
		seen[k] = true
		out = append(out, p)
	}
	return out
}

// save replaces m's items in one transaction and marks it distilled.
//
// Re-distilling a meeting recreates its item ids: decisions of earlier
// meetings that its old items superseded fall back to superseded_by = NULL
// (ON DELETE SET NULL), are offered again in the prompt and may be marked
// again by the new items. That is acceptable. Actions follow the same idea:
// earlier actions that m had closed (status_source distill, done_in m) are
// reopened and judged again by the new answer.
//
// m's own actions keep their status: it is carried over to the new item with
// the same normalized text (textHash), so a closed action does not come back
// as open. An action whose text changed starts open again. Code tasks
// promoted from m's actions are re-linked the same way; when the text
// changed, their source_item_id becomes NULL (ON DELETE SET NULL).
//
// actionsAllowed are the earlier open actions offered in the prompt; ids
// outside it in completes/drops are ignored.
func (d *Distiller) save(ctx context.Context, m distillMeeting, items []Item, allowed, actionsAllowed map[int64]bool) (saveResult, error) {
	var r saveResult
	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return r, err
	}
	defer tx.Rollback() //nolint:errcheck // no-op after commit

	if err := reopenClosedBy(ctx, tx, m.ID); err != nil {
		return r, err
	}
	carried, err := ownStatuses(ctx, tx, m.ID)
	if err != nil {
		return r, err
	}
	carriedCode, err := ownCodeInfo(ctx, tx, m.ID)
	if err != nil {
		return r, err
	}
	linked, err := linkedCodeTasks(ctx, tx, m.ID)
	if err != nil {
		return r, err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM meeting_items WHERE meeting_id = ?`, m.ID); err != nil {
		return r, err
	}
	now := d.now().UTC().Format(time.RFC3339)
	for _, it := range items {
		people, err := json.Marshal(it.People)
		if err != nil {
			return r, err
		}
		st := carried[textHash(it.Text)]
		if it.Kind != KindAction {
			st = carriedStatus{Status: StatusOpen}
		}
		res, err := tx.ExecContext(ctx, `
			INSERT INTO meeting_items (meeting_id, kind, text, people, importance, created_at,
				status, status_at, status_source, done_by, done_in)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			m.ID, it.Kind, it.Text, string(people), it.Importance, now,
			st.status(), st.At, st.Source, st.DoneBy, st.DoneIn)
		if err != nil {
			return r, err
		}
		newID, err := res.LastInsertId()
		if err != nil {
			return r, err
		}
		if err := saveCodeInfo(ctx, tx, newID, it, carriedCode[textHash(it.Text)]); err != nil {
			return r, err
		}
		if err := relinkCodeTask(ctx, tx, linked, newID, it); err != nil {
			return r, err
		}
		for _, old := range it.Supersedes {
			if !allowed[old] {
				continue // not a still valid decision or fact of an earlier meeting of the series
			}
			res, err := tx.ExecContext(ctx, `
				UPDATE meeting_items SET superseded_by = ?
				WHERE id = ? AND kind IN ('decision', 'fact') AND superseded_by IS NULL AND meeting_id <> ?`,
				newID, old, m.ID)
			if err != nil {
				return r, err
			}
			if n, _ := res.RowsAffected(); n > 0 {
				r.superseded++
			}
		}
		done, dropped, err := closeActions(ctx, tx, m.ID, sql.NullInt64{Int64: newID, Valid: true},
			it.Completes, it.Drops, actionsAllowed, now)
		if err != nil {
			return r, err
		}
		r.completed += done
		r.dropped += dropped
	}
	if _, err := tx.ExecContext(ctx,
		`UPDATE meetings SET distilled_hash = ?, actions_checked_hash = ? WHERE id = ?`,
		m.ContentHash, m.ContentHash, m.ID); err != nil {
		return r, err
	}
	if err := tx.Commit(); err != nil {
		return r, err
	}
	r.items = len(items)
	return r, nil
}
