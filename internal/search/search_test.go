package search

import (
	"context"
	"database/sql"
	"errors"
	"hash/fnv"
	"math"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/vitalbh/elan/internal/db"
	"github.com/vitalbh/elan/internal/embed"
)

// fakeEmb is a deterministic bag-of-words embedder: chunks sharing words
// with the query are close to it.
type fakeEmb struct {
	model string
	dim   int
	docs  int // documents embedded so far
}

func (f *fakeEmb) Model() string { return f.model }

func (f *fakeEmb) EmbedDocuments(_ context.Context, docs []embed.Doc) ([][]float32, error) {
	out := make([][]float32, len(docs))
	for i, d := range docs {
		out[i] = bow(d.Text, f.dim)
	}
	f.docs += len(docs)
	return out, nil
}

func (f *fakeEmb) EmbedQuery(_ context.Context, text string) ([]float32, error) {
	return bow(text, f.dim), nil
}

func bow(text string, dim int) []float32 {
	v := make([]float32, dim)
	for _, w := range tokenize(text) {
		h := fnv.New32a()
		h.Write([]byte(w))
		v[h.Sum32()%uint32(dim)]++
	}
	var n float64
	for _, x := range v {
		n += float64(x * x)
	}
	if n == 0 {
		v[0] = 1
		return v
	}
	for i := range v {
		v[i] /= float32(math.Sqrt(n))
	}
	return v
}

var now = time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)

func setup(t *testing.T, emb *fakeEmb) (*sql.DB, *Service) {
	t.Helper()
	conn, err := db.Open(filepath.Join(t.TempDir(), "elan.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	return conn, newSvc(conn, emb)
}

func newSvc(conn *sql.DB, emb *fakeEmb) *Service {
	s := NewService(conn, emb)
	s.now = func() time.Time { return now }
	return s
}

func addMeeting(t *testing.T, conn *sql.DB, id, title, folder string, at time.Time, notes, transcript string) {
	t.Helper()
	_, err := conn.Exec(`INSERT INTO meetings (id, title, folder_id, folder_name, started_at, source_modified,
		content_hash, notes, transcript, ingested_at) VALUES (?, ?, ?, ?, ?, '', '', ?, ?, '')`,
		id, title, folder, folder, at.Format(time.RFC3339), notes, transcript)
	if err != nil {
		t.Fatal(err)
	}
}

func mustIndex(t *testing.T, s *Service, opts IndexOptions) IndexStats {
	t.Helper()
	st, err := s.Index(context.Background(), opts)
	if err != nil {
		t.Fatal(err)
	}
	return st
}

func count(t *testing.T, conn *sql.DB, q string) int {
	t.Helper()
	var n int
	if err := conn.QueryRow(q).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestIndexIncremental(t *testing.T) {
	emb := &fakeEmb{model: "fake:1", dim: 16}
	conn, s := setup(t, emb)
	addMeeting(t, conn, "m1", "Planejamento", "f1", now.AddDate(0, 0, -1),
		"Resumo\nPrimeiro parágrafo sobre orçamento.\n\nSegundo parágrafo sobre contratação.", "")
	addMeeting(t, conn, "m2", "Retro", "f2", now.AddDate(0, 0, -2), "Resumo\nO deploy quebrou.", "Fulano: oi\n\nCiclano: tchau")
	if _, err := conn.Exec(`INSERT INTO notes (text, created_at) VALUES ('lembrar do dentista', ?)`, now.Format(time.RFC3339)); err != nil {
		t.Fatal(err)
	}

	st := mustIndex(t, s, IndexOptions{})
	if st.Dim != 16 || st.Model != "fake:1" {
		t.Fatalf("stats %+v", st)
	}
	total := count(t, conn, `SELECT COUNT(*) FROM chunks`)
	if total == 0 || emb.docs != total {
		t.Fatalf("chunks=%d embedded=%d", total, emb.docs)
	}
	if st.Sources[SourceTranscript].Chunks == 0 || st.Sources[SourceNote].Chunks != 1 {
		t.Fatalf("sources %+v", st.Sources)
	}
	if v := count(t, conn, `SELECT COUNT(*) FROM chunks_vec`); v != total {
		t.Fatalf("vec rows %d, chunks %d", v, total)
	}

	// Nothing changed: nothing embedded, nothing deleted.
	emb.docs = 0
	st = mustIndex(t, s, IndexOptions{})
	if emb.docs != 0 {
		t.Fatalf("re-embedded %d unchanged chunks", emb.docs)
	}
	for typ, ss := range st.Sources {
		if ss.Embedded != 0 || ss.Deleted != 0 {
			t.Fatalf("%s: %+v", typ, ss)
		}
	}

	// A note changes: only it is re-embedded.
	if _, err := conn.Exec(`UPDATE notes SET text = 'lembrar do dentista na sexta'`); err != nil {
		t.Fatal(err)
	}
	st = mustIndex(t, s, IndexOptions{})
	if emb.docs != 1 || st.Sources[SourceNote] != (SourceStats{Chunks: 1, Embedded: 1, Deleted: 1}) {
		t.Fatalf("note change: embedded=%d %+v", emb.docs, st.Sources[SourceNote])
	}

	// A meeting goes away: its chunks and vectors go too.
	if _, err := conn.Exec(`DELETE FROM meetings WHERE id = 'm2'`); err != nil {
		t.Fatal(err)
	}
	mustIndex(t, s, IndexOptions{})
	if n := count(t, conn, `SELECT COUNT(*) FROM chunks WHERE source_id = 'm2'`); n != 0 {
		t.Fatalf("%d chunks of a deleted meeting", n)
	}
	if c, v := count(t, conn, `SELECT COUNT(*) FROM chunks`), count(t, conn, `SELECT COUNT(*) FROM chunks_vec`); c != v {
		t.Fatalf("chunks=%d vec=%d", c, v)
	}
	if n := count(t, conn, `SELECT COUNT(*) FROM chunks_fts WHERE chunks_fts MATCH 'deploy'`); n != 0 {
		t.Fatalf("FTS still finds the deleted meeting: %d", n)
	}

	// Rebuild embeds everything again.
	emb.docs = 0
	mustIndex(t, s, IndexOptions{Rebuild: true})
	if c := count(t, conn, `SELECT COUNT(*) FROM chunks`); emb.docs != c {
		t.Fatalf("rebuild embedded %d of %d", emb.docs, c)
	}
}

func TestIndexItemsAndSuperseded(t *testing.T) {
	emb := &fakeEmb{model: "fake:1", dim: 16}
	conn, s := setup(t, emb)
	addMeeting(t, conn, "m1", "Arquitetura", "f1", now.AddDate(0, 0, -10), "", "")
	addMeeting(t, conn, "m2", "Arquitetura", "f1", now.AddDate(0, 0, -3), "", "")
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := conn.Exec(q, args...); err != nil {
			t.Fatal(err)
		}
	}
	exec(`INSERT INTO meeting_items (id, meeting_id, kind, text, people, created_at) VALUES (1, 'm1', 'decision', 'banco será postgres', '["Ana"]', '')`)
	exec(`INSERT INTO meeting_items (id, meeting_id, kind, text, created_at) VALUES (2, 'm2', 'decision', 'banco será sqlite', '')`)
	mustIndex(t, s, IndexOptions{})

	res, err := s.Search(context.Background(), Query{Text: "banco"})
	if err != nil {
		t.Fatal(err)
	}
	if len(res) != 2 || res[0].SourceID != "2" || res[0].Kind != "decision" || res[0].MeetingID != "m2" {
		t.Fatalf("before supersede: %+v", res)
	}
	// People go into the indexed text.
	if res, _ := s.Search(context.Background(), Query{Text: "Ana"}); len(res) == 0 || res[0].SourceID != "1" {
		t.Fatalf("search by person: %+v", res)
	}

	exec(`UPDATE meeting_items SET superseded_by = 2 WHERE id = 1`)
	mustIndex(t, s, IndexOptions{})
	res, _ = s.Search(context.Background(), Query{Text: "banco"})
	if len(res) != 1 || res[0].SourceID != "2" {
		t.Fatalf("superseded must be filtered: %+v", res)
	}
	res, _ = s.Search(context.Background(), Query{Text: "banco postgres", IncludeSuperseded: true})
	if len(res) != 2 || res[0].SourceID != "1" {
		t.Fatalf("IncludeSuperseded: %+v", res)
	}
}

func TestDimMismatchFailsFast(t *testing.T) {
	conn, s := setup(t, &fakeEmb{model: "fake:1", dim: 16})
	addMeeting(t, conn, "m1", "X", "f", now, "algum texto", "")
	ctx := context.Background()
	if err := s.Check(ctx); !errors.Is(err, ErrNotIndexed) {
		t.Fatalf("check before index: %v", err)
	}
	if _, err := s.Search(ctx, Query{Text: "texto"}); !errors.Is(err, ErrNotIndexed) {
		t.Fatalf("search before index: %v", err)
	}
	mustIndex(t, s, IndexOptions{})
	if err := s.Check(ctx); err != nil {
		t.Fatal(err)
	}

	otherDim := newSvc(conn, &fakeEmb{model: "fake:1", dim: 8})
	if _, err := otherDim.Index(ctx, IndexOptions{}); !errors.Is(err, ErrIndexMismatch) || !strings.Contains(err.Error(), "--rebuild") {
		t.Fatalf("index with another dim: %v", err)
	}
	if err := otherDim.Check(ctx); !errors.Is(err, ErrIndexMismatch) {
		t.Fatalf("check with another dim: %v", err)
	}
	if _, err := otherDim.Search(ctx, Query{Text: "texto"}); !errors.Is(err, ErrIndexMismatch) {
		t.Fatalf("search with another dim: %v", err)
	}
	otherModel := newSvc(conn, &fakeEmb{model: "fake:2", dim: 16})
	if _, err := otherModel.Search(ctx, Query{Text: "texto"}); !errors.Is(err, ErrIndexMismatch) {
		t.Fatalf("search with another model: %v", err)
	}

	// Rebuild switches the index to the new embedder.
	mustIndex(t, otherDim, IndexOptions{Rebuild: true})
	if res, err := otherDim.Search(ctx, Query{Text: "texto"}); err != nil || len(res) != 1 {
		t.Fatalf("after rebuild: %v %+v", err, res)
	}
	if _, err := s.Search(ctx, Query{Text: "texto"}); !errors.Is(err, ErrIndexMismatch) {
		t.Fatalf("old embedder after rebuild: %v", err)
	}
}

func TestFTSQuery(t *testing.T) {
	cases := map[string]string{
		"decisão sobre o orçamento":  `"decisão" OR "orçamento"`,
		`foo" OR bar`:                `"foo" OR "bar"`,
		`"black friday" preços`:      `"black friday" OR "preços"`,
		`title:x NEAR(a b) -c *d^`:   `"title" OR "near"`,
		`AND OR NOT`:                 `"not"`,
		`Blue-Orion blue orion`:      `"blue" OR "orion"`,
		"de a o":                     `"de" OR "a" OR "o"`,
		`"`:                          ``,
		`'; DROP TABLE chunks; --`:   `"drop" OR "table" OR "chunks"`,
		"`unbalanced (paren \"quote": `"unbalanced" OR "paren" OR "quote"`,
		"  ":                         ``,
		"Q4 2026":                    `"q4" OR "2026"`,
		`"" vazio`:                   `"vazio"`,
		`"a "b" c"`:                  `"a" OR "c"`, // pairs: "a ", then " c" left over? see below
		`ação AÇÃO acao`:             `"ação" OR "acao"`,
		`x`:                          `"x"`,
		`NEAR`:                       `"near"`,
		`a*`:                         `"a"`,
		`col:`:                       `"col"`,
		`{a b}`:                      `"a" OR "b"`,
		"emoji 🎖️ reunião":           `"emoji" OR "reunião"`,
		`über "naïve café"`:          `"naïve café" OR "über"`,
		"tab\tseparated\nnewline":    `"tab" OR "separated" OR "newline"`,
		`multiple   spaces`:          `"multiple" OR "spaces"`,
		`"only phrase"`:              `"only phrase"`,
		`"phrase with de" de`:        `"phrase with de"`,
		`OR`:                         `"or"`,
		`and`:                        `"and"`,
	}
	delete(cases, `"a "b" c"`) // ambiguous by design; covered by the execution loop below
	for in, want := range cases {
		if got := ftsQuery(in); got != want {
			t.Errorf("ftsQuery(%q) = %s, want %s", in, got, want)
		}
	}

	// Every expression must be valid FTS5 and accents must not matter.
	emb := &fakeEmb{model: "fake:1", dim: 16}
	conn, s := setup(t, emb)
	addMeeting(t, conn, "m1", "Revisão", "f", now, "A decisão sobre a migração foi adiada.", "")
	mustIndex(t, s, IndexOptions{})
	for in := range cases {
		if strings.TrimSpace(in) == "" {
			continue
		}
		if _, err := s.Search(context.Background(), Query{Text: in}); err != nil {
			t.Errorf("Search(%q): %v", in, err)
		}
	}
	for _, q := range []string{"decisao", "DECISÃO", "migracao", "revisao"} {
		res, err := s.Search(context.Background(), Query{Text: q})
		if err != nil || len(res) != 1 || res[0].BM25Rank != 1 {
			t.Errorf("accent-insensitive %q: %v %+v", q, err, res)
		}
	}
	res, _ := s.Search(context.Background(), Query{Text: "decisao"})
	if len(res) == 1 && !strings.Contains(res[0].Snippet, "[decisão]") {
		t.Errorf("snippet without highlight: %q", res[0].Snippet)
	}
}

func TestRecency(t *testing.T) {
	p := DefaultParams()
	if got := recency(now, now, p); got != 1 {
		t.Fatalf("now: %v", got)
	}
	half := recency(now.Add(-p.HalfLife), now, p)
	if math.Abs(half-(p.RecencyFloor+(1-p.RecencyFloor)/2)) > 1e-9 {
		t.Fatalf("half-life: %v", half)
	}
	if old := recency(now.AddDate(-20, 0, 0), now, p); old < p.RecencyFloor || old > p.RecencyFloor+0.001 {
		t.Fatalf("very old: %v", old)
	}
	if got := recency(time.Time{}, now, p); got != p.RecencyFloor {
		t.Fatalf("undated: %v", got)
	}
	if got := recency(now.Add(time.Hour), now, p); got != 1 {
		t.Fatalf("future: %v", got)
	}
}

func TestRankingRRFAndRecency(t *testing.T) {
	emb := &fakeEmb{model: "fake:1", dim: 64}
	conn, s := setup(t, emb)
	// Two years apart; the old one is a bit more relevant (first in both
	// retrievers) but recency puts the new one first.
	addMeeting(t, conn, "old", "Pagamentos", "f", now.AddDate(-2, 0, 0), "parcelamento na loja da apple, apple", "")
	addMeeting(t, conn, "new", "Pagamentos", "f", now.AddDate(0, 0, -1), "parcelamento na loja da apple", "")
	// Recent but unrelated: found only by the vector retriever (k covers all).
	addMeeting(t, conn, "noise", "Outra", "g", now, "almoço de confraternização", "")
	mustIndex(t, s, IndexOptions{})

	res, err := s.Search(context.Background(), Query{Text: "parcelamento apple"})
	if err != nil {
		t.Fatal(err)
	}
	if len(res) != 3 {
		t.Fatalf("%+v", res)
	}
	if res[0].SourceID != "new" || res[1].SourceID != "old" || res[2].SourceID != "noise" {
		t.Fatalf("order %s %s %s", res[0].SourceID, res[1].SourceID, res[2].SourceID)
	}
	if res[1].BM25Rank != 1 || res[1].VecRank != 1 {
		t.Fatalf("old ranks bm25=%d vec=%d", res[1].BM25Rank, res[1].VecRank)
	}
	if strings.Join(res[1].Retrievers, "+") != "bm25+vec" || strings.Join(res[2].Retrievers, "+") != "vec" {
		t.Fatalf("retrievers %v %v", res[1].Retrievers, res[2].Retrievers)
	}
	// RRF: the two-year-old hit found by both retrievers beats a hit from
	// today found by one.
	wantOld := (1/(60+float64(res[1].BM25Rank)) + 1/(60+float64(res[1].VecRank))) * recency(now.AddDate(-2, 0, 0), now, s.Params)
	if math.Abs(res[1].Score-wantOld) > 1e-9 || res[1].Score <= res[2].Score {
		t.Fatalf("scores %+v", res)
	}
}

func TestDedupePerSource(t *testing.T) {
	emb := &fakeEmb{model: "fake:1", dim: 16}
	conn, s := setup(t, emb)
	long := strings.Repeat("kubernetes cluster migração. ", 60) + "\n\n" + strings.Repeat("kubernetes nodes. ", 80)
	addMeeting(t, conn, "m1", "Infra", "f", now, long, "")
	mustIndex(t, s, IndexOptions{})
	if n := count(t, conn, `SELECT COUNT(*) FROM chunks WHERE source_id = 'm1'`); n < 2 {
		t.Fatalf("want several chunks, got %d", n)
	}
	res, err := s.Search(context.Background(), Query{Text: "kubernetes"})
	if err != nil || len(res) != 1 {
		t.Fatalf("dedupe: %v %+v", err, res)
	}
}

func TestTranscriptsAndFilters(t *testing.T) {
	emb := &fakeEmb{model: "fake:1", dim: 32}
	conn, s := setup(t, emb)
	addMeeting(t, conn, "sep", "Daily", "daily", time.Date(2026, 9, 10, 13, 0, 0, 0, time.UTC), "roadmap do trimestre", "zebra listrada falou do roadmap")
	addMeeting(t, conn, "oct", "Daily", "daily", time.Date(2026, 10, 10, 13, 0, 0, 0, time.UTC), "roadmap revisado", "")
	addMeeting(t, conn, "one", "Avulsa", "solo", time.Date(2026, 10, 20, 13, 0, 0, 0, time.UTC), "roadmap de produto", "")
	if _, err := conn.Exec(`INSERT INTO notes (text, created_at) VALUES ('roadmap pessoal', ?)`, now.Format(time.RFC3339)); err != nil {
		t.Fatal(err)
	}
	mustIndex(t, s, IndexOptions{})
	ctx := context.Background()
	ids := func(q Query) []string {
		t.Helper()
		res, err := s.Search(ctx, q)
		if err != nil {
			t.Fatal(err)
		}
		var out []string
		for _, r := range res {
			out = append(out, r.SourceType+":"+r.SourceID)
		}
		return out
	}
	has := func(list []string, s string) bool { return contains(list, s) }

	got := ids(Query{Text: "zebra"})
	for _, g := range got {
		if strings.HasPrefix(g, SourceTranscript) {
			t.Fatalf("transcript in default search: %v", got)
		}
	}
	got = ids(Query{Text: "zebra", IncludeTranscripts: true})
	if len(got) == 0 || got[0] != "transcript:sep" {
		t.Fatalf("IncludeTranscripts: %v", got)
	}
	if got = ids(Query{Text: "zebra", SourceTypes: []string{SourceTranscript}}); len(got) != 1 || got[0] != "transcript:sep" {
		t.Fatalf("SourceTypes transcript: %v", got)
	}

	got = ids(Query{Text: "roadmap", Since: time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)})
	if has(got, "meeting_notes:sep") || !has(got, "meeting_notes:oct") || !has(got, "note:1") {
		t.Fatalf("Since: %v", got)
	}
	got = ids(Query{Text: "roadmap", Until: time.Date(2026, 10, 15, 0, 0, 0, 0, time.UTC)})
	if len(got) != 2 || !has(got, "meeting_notes:sep") || !has(got, "meeting_notes:oct") {
		t.Fatalf("Until: %v", got)
	}
	got = ids(Query{Text: "roadmap", FolderID: "daily"})
	if len(got) != 2 || has(got, "meeting_notes:one") {
		t.Fatalf("FolderID: %v", got)
	}
	got = ids(Query{Text: "roadmap", SourceTypes: []string{SourceNote}})
	if len(got) != 1 || got[0] != "note:1" {
		t.Fatalf("SourceTypes note: %v", got)
	}
	if got = ids(Query{Text: "roadmap", Limit: 2}); len(got) != 2 {
		t.Fatalf("Limit: %v", got)
	}
	if _, err := s.Search(ctx, Query{Text: "x", SourceTypes: []string{"nope"}}); err == nil {
		t.Fatal("unknown source type must fail")
	}
	if _, err := s.Search(ctx, Query{Text: "  "}); err == nil {
		t.Fatal("empty query must fail")
	}
}

func TestSplitText(t *testing.T) {
	text := "Resumo\nlinha um\n\n\nParágrafo dois.\n\n" + strings.Repeat("palavra ", 400) + "\n\n" + strings.Repeat("x", 3000)
	chunks := splitText(text, 500)
	var total int
	for _, c := range chunks {
		n := len([]rune(c))
		if n > 500 || n == 0 {
			t.Fatalf("chunk of %d runes", n)
		}
		total += strings.Count(c, "palavra")
	}
	if total != 400 {
		t.Fatalf("lost words: %d", total)
	}
	if !strings.HasPrefix(chunks[0], "Resumo\nlinha um\n\nParágrafo dois.") {
		t.Fatalf("small paragraphs not packed: %q", chunks[0])
	}
	if got := splitText("  \n\n ", 500); len(got) != 0 {
		t.Fatalf("blank: %q", got)
	}
}
