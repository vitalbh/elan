// Package notes stores the notes the owner captures (CLI, MCP). The database
// is their source of truth.
package notes

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"
)

// ErrEmpty means the note has no text.
var ErrEmpty = errors.New("nota vazia")

// Note is a captured note.
type Note struct {
	ID        int64     `json:"id"`
	Text      string    `json:"text"`
	Source    string    `json:"source"`
	CreatedAt time.Time `json:"created_at"`
}

// Service stores notes.
type Service struct {
	db  *sql.DB
	now func() time.Time
}

// NewService builds a Service.
func NewService(db *sql.DB) *Service { return &Service{db: db, now: time.Now} }

// Save stores a note. source says where it came from ("cli", "mcp").
func (s *Service) Save(ctx context.Context, text, source string) (Note, error) {
	text = strings.TrimSpace(text)
	if text == "" {
		return Note{}, ErrEmpty
	}
	n := Note{Text: text, Source: source, CreatedAt: s.now().UTC().Truncate(time.Second)}
	res, err := s.db.ExecContext(ctx, `INSERT INTO notes (text, source, created_at) VALUES (?, ?, ?)`,
		n.Text, n.Source, n.CreatedAt.Format(time.RFC3339))
	if err != nil {
		return Note{}, err
	}
	n.ID, err = res.LastInsertId()
	return n, err
}

// Recent returns the newest notes first, at most n (n <= 0: 20).
func (s *Service) Recent(ctx context.Context, n int) ([]Note, error) {
	if n <= 0 {
		n = 20
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id, text, source, created_at FROM notes ORDER BY created_at DESC, id DESC LIMIT ?`, n)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Note{}
	for rows.Next() {
		var n Note
		var created string
		if err := rows.Scan(&n.ID, &n.Text, &n.Source, &created); err != nil {
			return nil, err
		}
		n.CreatedAt, _ = time.Parse(time.RFC3339, created)
		out = append(out, n)
	}
	return out, rows.Err()
}
