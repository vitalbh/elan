package meetings

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Doc is a meeting doc as listed by a Source.
type Doc struct {
	ID         string // id of the real doc, shortcuts already resolved
	Name       string
	FolderID   string
	FolderName string
	Modified   time.Time
}

// Source lists meeting docs and exports their text.
type Source interface {
	List(ctx context.Context) ([]Doc, error)
	Export(ctx context.Context, id string) (string, error)
}

// Stats summarises a sync.
type Stats struct {
	Seen, New, Updated, Unchanged, Failed int
}

// Service keeps the meetings table in sync with a Source.
type Service struct {
	db  *sql.DB
	src Source
	now func() time.Time
}

// NewService builds a Service.
func NewService(db *sql.DB, src Source) *Service {
	return &Service{db: db, src: src, now: time.Now}
}

// Sync ingests new and changed docs. A doc whose Drive modifiedTime did not
// change is skipped without exporting it; a changed doc is re-exported and
// rewritten only when its content hash changed. Failures of single docs are
// counted and returned joined, the rest of the sync goes on.
func (s *Service) Sync(ctx context.Context) (Stats, error) {
	docs, err := s.src.List(ctx)
	if err != nil {
		return Stats{}, err
	}
	var st Stats
	var errs []error
	for _, d := range docs {
		if err := ctx.Err(); err != nil {
			return st, err
		}
		st.Seen++
		outcome, err := s.syncDoc(ctx, d)
		if err != nil {
			st.Failed++
			errs = append(errs, fmt.Errorf("%s: %w", d.Name, err))
			continue
		}
		switch outcome {
		case outcomeNew:
			st.New++
		case outcomeUpdated:
			st.Updated++
		default:
			st.Unchanged++
		}
	}
	return st, errors.Join(errs...)
}

type outcome int

const (
	outcomeUnchanged outcome = iota
	outcomeNew
	outcomeUpdated
)

func (s *Service) syncDoc(ctx context.Context, d Doc) (outcome, error) {
	modified := d.Modified.UTC().Format(time.RFC3339Nano)
	var storedModified, storedHash string
	err := s.db.QueryRowContext(ctx,
		`SELECT source_modified, content_hash FROM meetings WHERE id = ?`, d.ID,
	).Scan(&storedModified, &storedHash)
	exists := err == nil
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return 0, err
	}
	if exists && storedModified == modified {
		return outcomeUnchanged, nil
	}

	text, err := s.src.Export(ctx, d.ID)
	if err != nil {
		return 0, fmt.Errorf("export: %w", err)
	}
	sum := sha256.Sum256([]byte(text))
	hash := hex.EncodeToString(sum[:])
	if exists && storedHash == hash {
		_, err := s.db.ExecContext(ctx, `UPDATE meetings SET source_modified = ? WHERE id = ?`, modified, d.ID)
		return outcomeUnchanged, err
	}

	p := ParseDoc(d.Name, text)
	var started any
	if p.StartedAt != nil {
		started = p.StartedAt.Format(time.RFC3339)
	}
	_, err = s.db.ExecContext(ctx, `
		INSERT INTO meetings (id, title, folder_id, folder_name, recurring, started_at,
			source_modified, content_hash, header, notes, transcript, ingested_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (id) DO UPDATE SET
			title = excluded.title,
			folder_id = excluded.folder_id,
			folder_name = excluded.folder_name,
			recurring = excluded.recurring,
			started_at = excluded.started_at,
			source_modified = excluded.source_modified,
			content_hash = excluded.content_hash,
			header = excluded.header,
			notes = excluded.notes,
			transcript = excluded.transcript,
			ingested_at = excluded.ingested_at`,
		d.ID, p.Title, d.FolderID, d.FolderName, isRecurring(d.FolderName), started,
		modified, hash, p.Header, p.Notes, p.Transcript, s.now().UTC().Format(time.RFC3339),
	)
	if err != nil {
		return 0, err
	}
	if exists {
		return outcomeUpdated, nil
	}
	return outcomeNew, nil
}

// isRecurring reports whether a Meet folder groups a recurring series.
func isRecurring(folder string) bool {
	return strings.HasSuffix(strings.TrimSpace(folder), "(recurring)")
}
