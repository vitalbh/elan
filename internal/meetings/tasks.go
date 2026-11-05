package meetings

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"
	"unicode"

	"golang.org/x/text/unicode/norm"
)

// TaskSink receives the owner's action items (Google Tasks in production).
type TaskSink interface {
	EnsureList(ctx context.Context, title string) (string, error)
	Insert(ctx context.Context, listID, title, notes string) (string, error)
}

// OwnerAction is an action item assigned to the owner, not exported yet.
type OwnerAction struct {
	MeetingID    string
	MeetingTitle string
	StartedAt    *time.Time
	Text         string
	textHash     string
}

// TaskExporter sends the owner's action items to a TaskSink, once each.
type TaskExporter struct {
	db    *sql.DB
	sink  TaskSink
	names map[string]bool
	now   func() time.Time
}

// NewTaskExporter builds an exporter. ownerNames are matched exactly
// (case- and accent-insensitive) against the item's people.
func NewTaskExporter(db *sql.DB, sink TaskSink, ownerNames []string) *TaskExporter {
	names := map[string]bool{}
	for _, n := range ownerNames {
		names[foldName(n)] = true
	}
	return &TaskExporter{db: db, sink: sink, names: names, now: time.Now}
}

// Pending lists the owner's action items of meetings started at or after
// since that were not exported yet, oldest meeting first.
func (e *TaskExporter) Pending(ctx context.Context, since time.Time) ([]OwnerAction, error) {
	if len(e.names) == 0 {
		return nil, fmt.Errorf("sem nomes do dono: preencha owner.names no config.json do perfil")
	}
	rows, err := e.db.QueryContext(ctx, `
		SELECT i.meeting_id, m.title, m.started_at, i.text, i.people
		FROM meeting_items i JOIN meetings m ON m.id = i.meeting_id
		WHERE i.kind = 'action' AND julianday(m.started_at) >= julianday(?)
		ORDER BY julianday(m.started_at), i.id`, since.Format(time.RFC3339))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []OwnerAction
	for rows.Next() {
		var a OwnerAction
		var started sql.NullString
		var people string
		if err := rows.Scan(&a.MeetingID, &a.MeetingTitle, &started, &a.Text, &people); err != nil {
			return nil, err
		}
		if !e.isOwner(people) {
			continue
		}
		a.StartedAt = parseStarted(started)
		a.textHash = textHash(a.Text)
		out = append(out, a)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return e.dropExported(ctx, out)
}

// Export sends the pending actions to the list and records each one, so a
// later run (or a re-distilled meeting with the same text) does not repeat it.
func (e *TaskExporter) Export(ctx context.Context, list string, actions []OwnerAction) (int, error) {
	if len(actions) == 0 {
		return 0, nil
	}
	listID, err := e.sink.EnsureList(ctx, list)
	if err != nil {
		return 0, fmt.Errorf("lista %q: %w", list, err)
	}
	n := 0
	for _, a := range actions {
		taskID, err := e.sink.Insert(ctx, listID, taskTitle(a.Text), taskNotes(a))
		if err != nil {
			return n, err
		}
		if _, err := e.db.ExecContext(ctx, `
			INSERT INTO task_exports (meeting_id, text_hash, list_id, task_id, created_at)
			VALUES (?, ?, ?, ?, ?)`,
			a.MeetingID, a.textHash, listID, taskID, e.now().UTC().Format(time.RFC3339)); err != nil {
			return n, fmt.Errorf("tarefa criada (%s) mas não registrada: %w", taskID, err)
		}
		n++
	}
	return n, nil
}

func (e *TaskExporter) isOwner(peopleJSON string) bool {
	var people []string
	if json.Unmarshal([]byte(peopleJSON), &people) != nil {
		return false
	}
	for _, p := range people {
		if e.names[foldName(p)] {
			return true
		}
	}
	return false
}

func (e *TaskExporter) dropExported(ctx context.Context, actions []OwnerAction) ([]OwnerAction, error) {
	out := actions[:0]
	for _, a := range actions {
		var n int
		if err := e.db.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM task_exports WHERE meeting_id = ? AND text_hash = ?`, a.MeetingID, a.textHash,
		).Scan(&n); err != nil {
			return nil, err
		}
		if n == 0 {
			out = append(out, a)
		}
	}
	return out, nil
}

// taskTitle keeps the title short enough to read in the Tasks app; the full
// text goes in the notes.
func taskTitle(text string) string {
	r := []rune(strings.TrimSpace(text))
	if len(r) <= 140 {
		return string(r)
	}
	return strings.TrimSpace(string(r[:139])) + "…"
}

func taskNotes(a OwnerAction) string {
	var b strings.Builder
	b.WriteString(a.Text)
	b.WriteString("\n\nReunião: ")
	b.WriteString(a.MeetingTitle)
	if a.StartedAt != nil {
		b.WriteString(" (" + a.StartedAt.Format("02/01/2006") + ")")
	}
	b.WriteString("\nhttps://docs.google.com/document/d/" + a.MeetingID)
	return b.String()
}

func textHash(text string) string {
	sum := sha256.Sum256([]byte(strings.Join(strings.Fields(strings.ToLower(text)), " ")))
	return hex.EncodeToString(sum[:])
}

// foldName lowercases, trims and strips accents, so "Vitál " matches "vital".
func foldName(s string) string {
	var b strings.Builder
	for _, r := range norm.NFD.String(strings.ToLower(strings.TrimSpace(s))) {
		if !unicode.Is(unicode.Mn, r) {
			b.WriteRune(r)
		}
	}
	return strings.Join(strings.Fields(b.String()), " ")
}
