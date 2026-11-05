package meetings

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// ErrNotFound means there is no meeting with that id.
var ErrNotFound = errors.New("reunião não encontrada")

// Meeting is a stored meeting with its distilled items.
type Meeting struct {
	ID            string       `json:"id"`
	Title         string       `json:"title"`
	StartedAt     *time.Time   `json:"started_at,omitempty"`
	Folder        string       `json:"folder"`
	Recurring     bool         `json:"recurring"`
	Header        string       `json:"header,omitempty"`
	Notes         string       `json:"notes,omitempty"`
	HasTranscript bool         `json:"has_transcript"`
	Distilled     bool         `json:"distilled"`
	Items         []StoredItem `json:"items,omitempty"`
}

// StoredItem is a distilled item as stored.
type StoredItem struct {
	ID           int64    `json:"id"`
	Kind         string   `json:"kind"`
	Text         string   `json:"text"`
	People       []string `json:"people,omitempty"`
	Importance   int      `json:"importance"`
	SupersededBy *int64   `json:"superseded_by,omitempty"`
	// Status of an action: open, done or dropped (empty for other kinds).
	Status string `json:"status,omitempty"`
}

// Summary is a meeting in a listing.
type Summary struct {
	ID        string     `json:"id"`
	Title     string     `json:"title"`
	StartedAt *time.Time `json:"started_at,omitempty"`
	Folder    string     `json:"folder"`
	Recurring bool       `json:"recurring"`
	// Summary is the distilled summary item, empty before distillation.
	Summary string `json:"summary,omitempty"`
}

// ListQuery filters a listing. Zero values mean no filter.
type ListQuery struct {
	Since, Until time.Time // Since inclusive, Until exclusive
	// Title matches the meeting title or folder, case-insensitively.
	Title string
	Limit int // default 20
}

// Reader reads meetings from the database.
type Reader struct {
	db *sql.DB
}

// NewReader builds a Reader.
func NewReader(db *sql.DB) *Reader { return &Reader{db: db} }

// Get returns a meeting with its notes and items (superseded ones included,
// marked).
func (r *Reader) Get(ctx context.Context, id string) (Meeting, error) {
	var m Meeting
	var started sql.NullString
	var recurring, hasTranscript, distilled bool
	err := r.db.QueryRowContext(ctx, `
		SELECT id, title, started_at, folder_name, recurring, header, notes,
			transcript <> '', distilled_hash = content_hash
		FROM meetings WHERE id = ?`, id,
	).Scan(&m.ID, &m.Title, &started, &m.Folder, &recurring, &m.Header, &m.Notes, &hasTranscript, &distilled)
	if errors.Is(err, sql.ErrNoRows) {
		return Meeting{}, fmt.Errorf("%w: %s", ErrNotFound, id)
	}
	if err != nil {
		return Meeting{}, err
	}
	m.StartedAt = parseStarted(started)
	m.Recurring, m.HasTranscript, m.Distilled = recurring, hasTranscript, distilled

	rows, err := r.db.QueryContext(ctx, `
		SELECT id, kind, text, people, importance, superseded_by, status
		FROM meeting_items WHERE meeting_id = ?
		ORDER BY CASE kind WHEN 'summary' THEN 0 WHEN 'decision' THEN 1 WHEN 'action' THEN 2 ELSE 3 END, id`, id)
	if err != nil {
		return Meeting{}, err
	}
	defer rows.Close()
	for rows.Next() {
		var it StoredItem
		var people string
		var by sql.NullInt64
		var status string
		if err := rows.Scan(&it.ID, &it.Kind, &it.Text, &people, &it.Importance, &by, &status); err != nil {
			return Meeting{}, err
		}
		if it.Kind == KindAction {
			it.Status = status
		}
		_ = json.Unmarshal([]byte(people), &it.People)
		if by.Valid {
			it.SupersededBy = &by.Int64
		}
		m.Items = append(m.Items, it)
	}
	return m, rows.Err()
}

// List returns meetings newest first.
func (r *Reader) List(ctx context.Context, q ListQuery) ([]Summary, error) {
	if q.Limit <= 0 {
		q.Limit = 20
	}
	where := []string{"1 = 1"}
	var args []any
	if !q.Since.IsZero() {
		where = append(where, "julianday(m.started_at) >= julianday(?)")
		args = append(args, q.Since.Format(time.RFC3339))
	}
	if !q.Until.IsZero() {
		where = append(where, "julianday(m.started_at) < julianday(?)")
		args = append(args, q.Until.Format(time.RFC3339))
	}
	if t := strings.TrimSpace(q.Title); t != "" {
		where = append(where, "(m.title LIKE ? OR m.folder_name LIKE ?)")
		like := "%" + t + "%"
		args = append(args, like, like)
	}
	args = append(args, q.Limit)
	rows, err := r.db.QueryContext(ctx, `
		SELECT m.id, m.title, m.started_at, m.folder_name, m.recurring,
			COALESCE((SELECT text FROM meeting_items i WHERE i.meeting_id = m.id AND i.kind = 'summary' LIMIT 1), '')
		FROM meetings m
		WHERE `+strings.Join(where, " AND ")+`
		ORDER BY m.started_at IS NULL, julianday(m.started_at) DESC
		LIMIT ?`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Summary
	for rows.Next() {
		var s Summary
		var started sql.NullString
		if err := rows.Scan(&s.ID, &s.Title, &started, &s.Folder, &s.Recurring, &s.Summary); err != nil {
			return nil, err
		}
		s.StartedAt = parseStarted(started)
		out = append(out, s)
	}
	return out, rows.Err()
}

// Transcript returns a slice of a meeting's transcript, starting at offset
// (in characters) and at most max characters long, plus the total length.
func (r *Reader) Transcript(ctx context.Context, id string, offset, max int) (text string, total int, err error) {
	var full string
	err = r.db.QueryRowContext(ctx, `SELECT transcript FROM meetings WHERE id = ?`, id).Scan(&full)
	if errors.Is(err, sql.ErrNoRows) {
		return "", 0, fmt.Errorf("%w: %s", ErrNotFound, id)
	}
	if err != nil {
		return "", 0, err
	}
	runes := []rune(full)
	total = len(runes)
	if offset < 0 {
		offset = 0
	}
	if offset > total {
		offset = total
	}
	end := total
	if max > 0 && offset+max < total {
		end = offset + max
	}
	return string(runes[offset:end]), total, nil
}

func parseStarted(s sql.NullString) *time.Time {
	if !s.Valid {
		return nil
	}
	t, err := time.Parse(time.RFC3339, s.String)
	if err != nil {
		return nil
	}
	return &t
}
