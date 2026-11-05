package search

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/vitalbh/elan/internal/embed"
)

// IndexOptions controls Index.
type IndexOptions struct {
	// Rebuild drops the whole index (chunks, FTS, vectors, recorded
	// embedder) and builds it again. Required after changing the model.
	Rebuild bool
	// Progress, when set, is called after each embedded batch.
	Progress func(done, total int)
}

// SourceStats counts the chunks of one source type after an Index run.
type SourceStats struct {
	Chunks   int // chunks in the index now
	Embedded int // new or changed chunks embedded in this run
	Deleted  int // chunks removed (source gone or changed)
}

// IndexStats summarises an Index run.
type IndexStats struct {
	Model    string
	Dim      int
	Sources  map[string]SourceStats // by source type
	Duration time.Duration
}

// chunk is a chunk to be stored.
type chunk struct {
	SourceType string
	SourceID   string
	Seq        int
	MeetingID  string
	Title      string
	Text       string
	Kind       string
	FolderID   string
	Superseded bool
	At         string
	Hash       string
}

func (c *chunk) hash() {
	h := sha256.New()
	sup := "0"
	if c.Superseded {
		sup = "1"
	}
	for _, f := range []string{c.SourceType, c.SourceID, c.MeetingID, c.Title, c.Text, c.Kind, c.FolderID, sup, c.At} {
		h.Write([]byte(f))
		h.Write([]byte{0})
	}
	c.Hash = hex.EncodeToString(h.Sum(nil))
}

// embedBatch is how many chunks are embedded and committed together.
const embedBatch = 32

// Index brings the index up to date with meetings, meeting_items and notes.
// It is incremental and idempotent: a chunk whose content hash is already in
// the index is kept (not re-embedded), chunks of removed or changed sources
// are deleted. Fails with ErrIndexMismatch when the configured embedder is
// not the one that built the index, unless Rebuild is set.
func (s *Service) Index(ctx context.Context, opts IndexOptions) (IndexStats, error) {
	start := s.now()
	dim, err := s.probeDim(ctx)
	if err != nil {
		return IndexStats{}, err
	}
	st := IndexStats{Model: s.emb.Model(), Dim: dim, Sources: map[string]SourceStats{}}
	if err := s.prepare(ctx, dim, opts.Rebuild); err != nil {
		return st, err
	}

	want, err := s.collect(ctx)
	if err != nil {
		return st, err
	}

	// Match wanted chunks against stored ones by (source, hash); the seq may
	// move when a paragraph is inserted before it, the content does not.
	type key struct{ typ, id, hash string }
	type stored struct {
		id  int64
		seq int
	}
	have := map[key][]stored{}
	haveType := map[int64]string{}
	rows, err := s.db.QueryContext(ctx, `SELECT id, source_type, source_id, seq, content_hash FROM chunks`)
	if err != nil {
		return st, err
	}
	for rows.Next() {
		var k key
		var sc stored
		if err := rows.Scan(&sc.id, &k.typ, &k.id, &sc.seq, &k.hash); err != nil {
			rows.Close()
			return st, err
		}
		have[k] = append(have[k], sc)
		haveType[sc.id] = k.typ
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return st, err
	}

	var fresh []chunk
	type reseq struct {
		id  int64
		seq int
	}
	var moves []reseq
	for _, c := range want {
		k := key{c.SourceType, c.SourceID, c.Hash}
		if list := have[k]; len(list) > 0 {
			sc := list[0]
			have[k] = list[1:]
			delete(haveType, sc.id)
			if sc.seq != c.Seq {
				moves = append(moves, reseq{sc.id, c.Seq})
			}
			continue
		}
		fresh = append(fresh, c)
	}

	// haveType now holds only chunks nobody wants.
	if len(haveType) > 0 || len(moves) > 0 {
		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			return st, err
		}
		for id, typ := range haveType {
			if err := deleteChunk(ctx, tx, id); err != nil {
				tx.Rollback()
				return st, err
			}
			ss := st.Sources[typ]
			ss.Deleted++
			st.Sources[typ] = ss
		}
		for _, m := range moves {
			if _, err := tx.ExecContext(ctx, `UPDATE chunks SET seq = ? WHERE id = ?`, m.seq, m.id); err != nil {
				tx.Rollback()
				return st, err
			}
		}
		if err := tx.Commit(); err != nil {
			return st, err
		}
	}

	for start := 0; start < len(fresh); start += embedBatch {
		batch := fresh[start:min(start+embedBatch, len(fresh))]
		if err := s.insertBatch(ctx, batch, dim); err != nil {
			return st, err
		}
		for _, c := range batch {
			ss := st.Sources[c.SourceType]
			ss.Embedded++
			st.Sources[c.SourceType] = ss
		}
		if opts.Progress != nil {
			opts.Progress(start+len(batch), len(fresh))
		}
	}

	rows, err = s.db.QueryContext(ctx, `SELECT source_type, COUNT(*) FROM chunks GROUP BY source_type`)
	if err != nil {
		return st, err
	}
	defer rows.Close()
	for rows.Next() {
		var typ string
		var n int
		if err := rows.Scan(&typ, &n); err != nil {
			return st, err
		}
		ss := st.Sources[typ]
		ss.Chunks = n
		st.Sources[typ] = ss
	}
	st.Duration = s.now().Sub(start)
	return st, rows.Err()
}

// prepare drops the index on rebuild and makes sure chunks_vec exists for the
// configured embedder.
func (s *Service) prepare(ctx context.Context, dim int, rebuild bool) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if rebuild {
		for _, q := range []string{
			`DROP TABLE IF EXISTS chunks_vec`,
			`DELETE FROM chunks`,
			`INSERT INTO chunks_fts (chunks_fts) VALUES ('rebuild')`,
			`DELETE FROM index_meta`,
		} {
			if _, err := tx.ExecContext(ctx, q); err != nil {
				return err
			}
		}
	}
	m, ok, err := readMeta(ctx, tx)
	if err != nil {
		return err
	}
	if ok {
		if m.Model != s.emb.Model() || m.Dim != dim {
			return mismatch(m, s.emb.Model(), dim)
		}
		return tx.Commit()
	}
	// No embedder recorded: vectors of an unknown model must not survive.
	if _, err := tx.ExecContext(ctx, `DROP TABLE IF EXISTS chunks_vec`); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM chunks`); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, fmt.Sprintf(`CREATE VIRTUAL TABLE chunks_vec USING vec0 (
		embedding float[%d] distance_metric=cosine,
		source_type text,
		at text,
		folder_id text,
		superseded integer
	)`, dim)); err != nil {
		return fmt.Errorf("criar chunks_vec: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO index_meta (id, model, dim, created_at) VALUES (1, ?, ?, ?)`,
		s.emb.Model(), dim, s.now().UTC().Format(time.RFC3339)); err != nil {
		return err
	}
	return tx.Commit()
}

func deleteChunk(ctx context.Context, tx *sql.Tx, id int64) error {
	if _, err := tx.ExecContext(ctx, `DELETE FROM chunks_vec WHERE rowid = ?`, id); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, `DELETE FROM chunks WHERE id = ?`, id)
	return err
}

func (s *Service) insertBatch(ctx context.Context, batch []chunk, dim int) error {
	docs := make([]embed.Doc, len(batch))
	for i, c := range batch {
		docs[i] = embed.Doc{Title: c.Title, Text: c.Text}
	}
	vecs, err := s.emb.EmbedDocuments(ctx, docs)
	if err != nil {
		return err
	}
	if len(vecs) != len(batch) {
		return fmt.Errorf("embedder devolveu %d vetores para %d chunks", len(vecs), len(batch))
	}
	now := s.now().UTC().Format(time.RFC3339)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for i, c := range batch {
		if len(vecs[i]) != dim {
			return fmt.Errorf("%w: vetor com dim %d, índice com dim %d", ErrIndexMismatch, len(vecs[i]), dim)
		}
		var meetingID any
		if c.MeetingID != "" {
			meetingID = c.MeetingID
		}
		res, err := tx.ExecContext(ctx, `INSERT INTO chunks (source_type, source_id, seq, meeting_id, title, text,
			kind, folder_id, superseded, at, content_hash, created_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			c.SourceType, c.SourceID, c.Seq, meetingID, c.Title, c.Text, c.Kind, c.FolderID, boolInt(c.Superseded),
			c.At, c.Hash, now)
		if err != nil {
			return err
		}
		id, err := res.LastInsertId()
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO chunks_vec (rowid, embedding, source_type, at, folder_id, superseded)
			VALUES (?, ?, ?, ?, ?, ?)`, id, serialize(vecs[i]), c.SourceType, c.At, c.FolderID, boolInt(c.Superseded)); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// collect builds every chunk the index should hold, from the source tables.
func (s *Service) collect(ctx context.Context) ([]chunk, error) {
	var out []chunk
	add := func(c chunk) {
		c.hash()
		out = append(out, c)
	}

	rows, err := s.db.QueryContext(ctx, `SELECT id, title, folder_id, COALESCE(started_at, ingested_at), notes, transcript FROM meetings`)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var id, title, folder, at, notes, transcript string
		if err := rows.Scan(&id, &title, &folder, &at, &notes, &transcript); err != nil {
			rows.Close()
			return nil, err
		}
		at = normTime(at)
		for i, text := range splitText(notes, maxChunkRunes) {
			add(chunk{SourceType: SourceMeetingNotes, SourceID: id, Seq: i, MeetingID: id, Title: title, Text: text, FolderID: folder, At: at})
		}
		for i, text := range splitText(transcript, maxChunkRunes) {
			add(chunk{SourceType: SourceTranscript, SourceID: id, Seq: i, MeetingID: id, Title: title, Text: text, FolderID: folder, At: at})
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	rows, err = s.db.QueryContext(ctx, `SELECT i.id, i.meeting_id, m.title, m.folder_id, COALESCE(m.started_at, i.created_at),
		i.kind, i.text, i.people, i.superseded_by IS NOT NULL
		FROM meeting_items i JOIN meetings m ON m.id = i.meeting_id`)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var id int64
		var meetingID, title, folder, at, kind, text, people string
		var superseded bool
		if err := rows.Scan(&id, &meetingID, &title, &folder, &at, &kind, &text, &people, &superseded); err != nil {
			rows.Close()
			return nil, err
		}
		var names []string
		if json.Unmarshal([]byte(people), &names) == nil && len(names) > 0 {
			text += "\nPessoas: " + strings.Join(names, ", ")
		}
		// Items are short; a pathological one is cut, never dropped.
		for i, piece := range splitText(text, maxChunkRunes) {
			add(chunk{SourceType: SourceMeetingItem, SourceID: strconv.FormatInt(id, 10), Seq: i, MeetingID: meetingID,
				Title: title, Text: piece, Kind: kind, FolderID: folder, Superseded: superseded, At: normTime(at)})
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	rows, err = s.db.QueryContext(ctx, `SELECT id, text, created_at FROM notes`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		var text, at string
		if err := rows.Scan(&id, &text, &at); err != nil {
			return nil, err
		}
		for i, piece := range splitText(text, maxChunkRunes) {
			add(chunk{SourceType: SourceNote, SourceID: strconv.FormatInt(id, 10), Seq: i, Text: piece, At: normTime(at)})
		}
	}
	return out, rows.Err()
}

// normTime turns an RFC 3339 time into RFC 3339 UTC, so stored times compare
// as strings. Unknown formats become "".
func normTime(s string) string {
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// serialize encodes a vector as sqlite-vec's float32 BLOB (little endian).
func serialize(v []float32) []byte {
	b := make([]byte, 4*len(v))
	for i, f := range v {
		binary.LittleEndian.PutUint32(b[4*i:], math.Float32bits(f))
	}
	return b
}
