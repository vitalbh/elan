// Package search keeps a hybrid search index over meetings, meeting items and
// notes, and queries it. The index lives in the same SQLite database: chunks
// (source of the derived index), chunks_fts (FTS5, BM25) and chunks_vec
// (sqlite-vec). It is derived data: `Index` with Rebuild recreates it from the
// source tables at any time.
package search

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/vitalbh/elan/internal/embed"
	"github.com/vitalbh/elan/internal/people"
)

// Source types of a chunk.
const (
	SourceMeetingNotes = "meeting_notes" // section of the Gemini notes of a meeting
	SourceMeetingItem  = "meeting_item"  // one distilled item (summary, decision, action, fact)
	SourceNote         = "note"          // a note saved by the owner
	SourceTranscript   = "transcript"    // piece of a raw transcript; out of the default ranking
)

// ErrNotIndexed is returned by Search and Check before the first `elan index`.
var ErrNotIndexed = errors.New("índice de busca vazio: rode `elan index`")

// ErrIndexMismatch is wrapped by the errors returned when the configured
// embedder differs (model or dimension) from the one that built the index.
var ErrIndexMismatch = errors.New("embedder diferente do índice")

// Params tunes the ranking.
type Params struct {
	// Candidates is how many hits each retriever (BM25 and vector) returns
	// before fusion.
	Candidates int
	// RRFK is the k of Reciprocal Rank Fusion: score = Σ 1/(k + rank).
	RRFK float64
	// HalfLife is the age at which the recency boost is half gone.
	HalfLife time.Duration
	// RecencyFloor is the multiplier of an infinitely old hit (and of hits
	// without a date). A hit from now gets 1. Keeps old but very relevant
	// hits in the results.
	RecencyFloor float64
	// MaxPerMeeting caps the results of a single meeting (its notes, items
	// and transcript chunks together), so one meeting does not fill the
	// whole list. 0 means no cap.
	MaxPerMeeting int
}

// DefaultParams returns the ranking used by the CLI and the MCP server.
func DefaultParams() Params {
	return Params{Candidates: 50, RRFK: 60, HalfLife: 180 * 24 * time.Hour, RecencyFloor: 0.7, MaxPerMeeting: 2}
}

// Service indexes and searches the profile's database.
type Service struct {
	db  *sql.DB
	emb embed.Embedder
	// Params tunes the ranking; NewService sets DefaultParams.
	Params Params
	// People expands person filters with aliases (nil: names only).
	People *people.Directory
	now    func() time.Time
}

// NewService builds a Service. db must come from db.Open (sqlite-vec loaded,
// migrations applied).
func NewService(db *sql.DB, emb embed.Embedder) *Service {
	return &Service{db: db, emb: emb, Params: DefaultParams(), now: time.Now}
}

type meta struct {
	Model string
	Dim   int
}

func readMeta(ctx context.Context, q interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}) (meta, bool, error) {
	var m meta
	err := q.QueryRowContext(ctx, `SELECT model, dim FROM index_meta WHERE id = 1`).Scan(&m.Model, &m.Dim)
	if errors.Is(err, sql.ErrNoRows) {
		return meta{}, false, nil
	}
	if err != nil {
		return meta{}, false, err
	}
	return m, true, nil
}

func mismatch(m meta, model string, dim int) error {
	got := model
	if dim > 0 {
		got = fmt.Sprintf("%s (dim %d)", model, dim)
	}
	return fmt.Errorf("%w: índice criado com %s (dim %d), configurado %s; rode `elan index --rebuild`",
		ErrIndexMismatch, m.Model, m.Dim, got)
}

// Check verifies that the index exists and was built by the configured
// embedder: same model and same dimension (probed with one embedding). Meant
// for startup of long-running adapters (MCP, daemon): fail fast instead of
// mixing vector spaces.
func (s *Service) Check(ctx context.Context) error {
	m, ok, err := readMeta(ctx, s.db)
	if err != nil {
		return err
	}
	if !ok {
		return ErrNotIndexed
	}
	if m.Model != s.emb.Model() {
		return mismatch(m, s.emb.Model(), 0)
	}
	dim, err := s.probeDim(ctx)
	if err != nil {
		return err
	}
	if dim != m.Dim {
		return mismatch(m, s.emb.Model(), dim)
	}
	return nil
}

func (s *Service) probeDim(ctx context.Context) (int, error) {
	v, err := s.emb.EmbedQuery(ctx, "dimensão")
	if err != nil {
		return 0, err
	}
	if len(v) == 0 {
		return 0, errors.New("embedder devolveu vetor vazio")
	}
	return len(v), nil
}
