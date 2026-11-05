package meetings

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/vitalbh/elan/internal/db"
	"github.com/vitalbh/elan/internal/llm"
)

// fakeRunner answers each call with reply(prompt) and records the prompts.
type fakeRunner struct {
	prompts []string
	reqs    []llm.Request
	reply   func(n int, prompt string) ([]byte, error)
}

func (f *fakeRunner) JSON(_ context.Context, req llm.Request) ([]byte, error) {
	f.reqs = append(f.reqs, req)
	f.prompts = append(f.prompts, req.Prompt)
	return f.reply(len(f.prompts), req.Prompt)
}

func openDB(t *testing.T) *sql.DB {
	t.Helper()
	conn, err := db.Open(filepath.Join(t.TempDir(), "elan.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	return conn
}

type testMeeting struct {
	id, title, folder string
	recurring         bool
	started           string // "" = NULL
	distilled         bool
	transcript        string
}

func insertMeeting(t *testing.T, conn *sql.DB, m testMeeting) {
	t.Helper()
	var started any
	if m.started != "" {
		started = m.started
	}
	distilled := ""
	if m.distilled {
		distilled = "h-" + m.id
	}
	_, err := conn.Exec(`INSERT INTO meetings (id, title, folder_id, folder_name, recurring, started_at,
		source_modified, content_hash, header, notes, transcript, distilled_hash, ingested_at)
		VALUES (?, ?, ?, ?, ?, ?, 'x', ?, 'cabeçalho', 'Resumo\nnotas de ' || ?, ?, ?, 'now')`,
		m.id, m.title, m.folder, m.folder+" (recurring)", m.recurring, started,
		"h-"+m.id, m.title, m.transcript, distilled)
	if err != nil {
		t.Fatal(err)
	}
}

func insertItem(t *testing.T, conn *sql.DB, meeting, kind, text string) int64 {
	t.Helper()
	res, err := conn.Exec(`INSERT INTO meeting_items (meeting_id, kind, text, created_at) VALUES (?, ?, ?, 'now')`,
		meeting, kind, text)
	if err != nil {
		t.Fatal(err)
	}
	id, _ := res.LastInsertId()
	return id
}

func titleOf(prompt string) string {
	first, _, _ := strings.Cut(prompt, "\n")
	return strings.TrimPrefix(first, "# Reunião: ")
}

func summaryReply(title string, extra ...string) []byte {
	items := []string{fmt.Sprintf(`{"kind":"summary","text":"Resumo de %s.","people":[],"importance":2,"supersedes":[]}`, title)}
	items = append(items, extra...)
	return []byte(`{"items":[` + strings.Join(items, ",") + `]}`)
}

func TestDistillOrderAndReplace(t *testing.T) {
	conn := openDB(t)
	insertMeeting(t, conn, testMeeting{id: "c", title: "C sem data", folder: "f3"})
	insertMeeting(t, conn, testMeeting{id: "b", title: "B", folder: "f2", started: "2025-02-01T10:00:00-03:00"})
	insertMeeting(t, conn, testMeeting{id: "a", title: "A", folder: "f1", started: "2025-01-01T10:00:00-03:00"})
	insertMeeting(t, conn, testMeeting{id: "d", title: "D pronta", folder: "f4", started: "2024-01-01T10:00:00-03:00", distilled: true})

	r := &fakeRunner{reply: func(_ int, p string) ([]byte, error) {
		return summaryReply(titleOf(p),
			`{"kind":"action","text":"Ana vai revisar o contrato do projeto X.","people":[" Ana ","ana",""],"importance":3,"supersedes":[7]}`), nil
	}}
	var progress []string
	st, err := NewDistiller(conn, r).Distill(context.Background(), 10, func(title string) { progress = append(progress, title) })
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(progress, ","); got != "A,B,C sem data" {
		t.Fatalf("order %q", got)
	}
	if st != (DistillStats{Distilled: 3, Items: 6}) {
		t.Fatalf("stats %+v", st)
	}
	if r.reqs[0].Model != "sonnet" || len(r.reqs[0].Schema) == 0 || r.reqs[0].System == "" {
		t.Fatalf("request %+v", r.reqs[0])
	}

	var people string
	if err := conn.QueryRow(`SELECT people FROM meeting_items WHERE meeting_id = 'a' AND kind = 'action'`).Scan(&people); err != nil {
		t.Fatal(err)
	}
	if people != `["Ana"]` {
		t.Fatalf("people %s", people)
	}
	var pending int
	conn.QueryRow(`SELECT COUNT(*) FROM meetings WHERE distilled_hash <> content_hash`).Scan(&pending) //nolint:errcheck
	if pending != 0 {
		t.Fatalf("%d pending", pending)
	}

	// Content of A changes: its items are replaced, not appended.
	if _, err := conn.Exec(`UPDATE meetings SET content_hash = 'h2' WHERE id = 'a'`); err != nil {
		t.Fatal(err)
	}
	r.reply = func(int, string) ([]byte, error) {
		return []byte(`{"items":[{"kind":"fact","text":"Fato novo.","people":[],"importance":1,"supersedes":[]}]}`), nil
	}
	if _, err := NewDistiller(conn, r).Distill(context.Background(), 10, nil); err != nil {
		t.Fatal(err)
	}
	var n int
	var kinds string
	conn.QueryRow(`SELECT COUNT(*), group_concat(kind) FROM meeting_items WHERE meeting_id = 'a'`).Scan(&n, &kinds) //nolint:errcheck
	if n != 1 || kinds != "fact" {
		t.Fatalf("a after redistill: %d %q", n, kinds)
	}
	var dh string
	conn.QueryRow(`SELECT distilled_hash FROM meetings WHERE id = 'a'`).Scan(&dh) //nolint:errcheck
	if dh != "h2" {
		t.Fatalf("distilled_hash %q", dh)
	}
}

func TestDistillLimitAndFailure(t *testing.T) {
	conn := openDB(t)
	insertMeeting(t, conn, testMeeting{id: "a", title: "A", folder: "f", started: "2025-01-01T10:00:00-03:00"})
	insertMeeting(t, conn, testMeeting{id: "b", title: "B", folder: "f", started: "2025-01-02T10:00:00-03:00"})
	insertMeeting(t, conn, testMeeting{id: "c", title: "C", folder: "f", started: "2025-01-03T10:00:00-03:00"})

	r := &fakeRunner{reply: func(n int, p string) ([]byte, error) {
		if titleOf(p) == "A" {
			return []byte(`{"items":[{"kind":"opinion","text":"x","people":[],"importance":2,"supersedes":[]}]}`), nil
		}
		return summaryReply(titleOf(p)), nil
	}}
	st, err := NewDistiller(conn, r).Distill(context.Background(), 2, nil)
	if err == nil || !strings.Contains(err.Error(), "kind inválido") {
		t.Fatalf("want A's validation error, got %v", err)
	}
	if st != (DistillStats{Distilled: 1, Failed: 1, Items: 1, Pending: 2}) {
		t.Fatalf("stats %+v", st)
	}
	if len(r.prompts) != 2 {
		t.Fatalf("%d calls, want 2 (limit)", len(r.prompts))
	}
	var dh string
	conn.QueryRow(`SELECT distilled_hash FROM meetings WHERE id = 'a'`).Scan(&dh) //nolint:errcheck
	if dh != "" {
		t.Fatal("failed meeting must stay pending")
	}
}

func TestDistillStopsOnLimitError(t *testing.T) {
	conn := openDB(t)
	insertMeeting(t, conn, testMeeting{id: "a", title: "A", folder: "f", started: "2025-01-01T10:00:00-03:00"})
	insertMeeting(t, conn, testMeeting{id: "b", title: "B", folder: "f", started: "2025-01-02T10:00:00-03:00"})
	insertMeeting(t, conn, testMeeting{id: "c", title: "C", folder: "f", started: "2025-01-03T10:00:00-03:00"})
	reset := time.Date(2026, 10, 30, 17, 0, 0, 0, time.UTC)
	r := &fakeRunner{reply: func(n int, p string) ([]byte, error) {
		if n == 2 {
			return nil, &llm.LimitError{Message: "session limit", ResetAt: reset}
		}
		return summaryReply(titleOf(p)), nil
	}}
	st, err := NewDistiller(conn, r).Distill(context.Background(), 10, nil)
	le, ok := llm.IsLimit(err)
	if !ok || !le.ResetAt.Equal(reset) {
		t.Fatalf("want LimitError, got %v", err)
	}
	if st.Distilled != 1 || st.Failed != 0 || len(r.prompts) != 2 {
		t.Fatalf("stats %+v calls %d", st, len(r.prompts))
	}
	var dh string
	conn.QueryRow(`SELECT distilled_hash FROM meetings WHERE id = 'b'`).Scan(&dh) //nolint:errcheck
	if dh != "" {
		t.Fatal("b must stay pending")
	}
}

func TestDistillSupersedes(t *testing.T) {
	conn := openDB(t)
	// Series s: m1 (Jan, distilled), m2 (Feb, pending), m3 (Mar, distilled).
	insertMeeting(t, conn, testMeeting{id: "m1", title: "Série 1", folder: "s", recurring: true, started: "2025-01-10T10:00:00-03:00", distilled: true})
	insertMeeting(t, conn, testMeeting{id: "m2", title: "Série 2", folder: "s", recurring: true, started: "2025-02-10T10:00:00-03:00"})
	insertMeeting(t, conn, testMeeting{id: "m3", title: "Série 3", folder: "s", recurring: true, started: "2025-03-10T10:00:00-03:00", distilled: true})
	// Another series, earlier.
	insertMeeting(t, conn, testMeeting{id: "o1", title: "Outra", folder: "t", recurring: true, started: "2025-01-05T10:00:00-03:00", distilled: true})

	d1 := insertItem(t, conn, "m1", "decision", "Decidido usar Postgres no projeto Atlas.")
	d1b := insertItem(t, conn, "m1", "decision", "Deploy do Atlas às sextas.")
	s1 := insertItem(t, conn, "m1", "summary", "Resumo m1.")
	gone := insertItem(t, conn, "m1", "decision", "Decisão já substituída.")
	repl := insertItem(t, conn, "m1", "decision", "Substituta.")
	if _, err := conn.Exec(`UPDATE meeting_items SET superseded_by = ? WHERE id = ?`, repl, gone); err != nil {
		t.Fatal(err)
	}
	other := insertItem(t, conn, "o1", "decision", "Decisão de outra série.")
	later := insertItem(t, conn, "m3", "decision", "Decisão de reunião posterior.")

	r := &fakeRunner{reply: func(int, string) ([]byte, error) {
		return summaryReply("Série 2",
			fmt.Sprintf(`{"kind":"decision","text":"Atlas passa a usar SQLite em vez de Postgres.","people":[],"importance":3,"supersedes":[%d,%d,%d,%d,%d,999]}`, d1, other, later, s1, gone),
			fmt.Sprintf(`{"kind":"action","text":"Ana migra o Atlas.","people":["Ana"],"importance":2,"supersedes":[%d]}`, d1b),
		), nil
	}}
	st, err := NewDistiller(conn, r).Distill(context.Background(), 10, nil)
	if err != nil {
		t.Fatal(err)
	}
	if st.Superseded != 1 {
		t.Fatalf("superseded %d, want 1", st.Superseded)
	}

	p := r.prompts[0]
	for _, want := range []int64{d1, d1b, repl} {
		if !strings.Contains(p, fmt.Sprintf("[id %d]", want)) {
			t.Errorf("prompt lacks earlier valid decision %d", want)
		}
	}
	for _, not := range []int64{other, later, s1, gone} {
		if strings.Contains(p, fmt.Sprintf("[id %d]", not)) {
			t.Errorf("prompt offers %d", not)
		}
	}

	var newID int64
	if err := conn.QueryRow(`SELECT id FROM meeting_items WHERE meeting_id = 'm2' AND kind = 'decision'`).Scan(&newID); err != nil {
		t.Fatal(err)
	}
	check := func(id int64, want sql.NullInt64) {
		t.Helper()
		var got sql.NullInt64
		if err := conn.QueryRow(`SELECT superseded_by FROM meeting_items WHERE id = ?`, id).Scan(&got); err != nil {
			t.Fatal(err)
		}
		if got != want {
			t.Errorf("item %d superseded_by %v, want %v", id, got, want)
		}
	}
	check(d1, sql.NullInt64{Int64: newID, Valid: true})
	check(d1b, sql.NullInt64{}) // only decisions supersede
	check(other, sql.NullInt64{})
	check(later, sql.NullInt64{})
	check(s1, sql.NullInt64{})
	check(gone, sql.NullInt64{Int64: repl, Valid: true})

	// Re-distilling m2 recreates its ids: d1 falls back to valid (SET NULL)
	// and is offered again.
	if _, err := conn.Exec(`UPDATE meetings SET content_hash = 'h2' WHERE id = 'm2'`); err != nil {
		t.Fatal(err)
	}
	r.reply = func(int, string) ([]byte, error) { return summaryReply("Série 2"), nil }
	if _, err := NewDistiller(conn, r).Distill(context.Background(), 10, nil); err != nil {
		t.Fatal(err)
	}
	check(d1, sql.NullInt64{})
	if !strings.Contains(r.prompts[1], fmt.Sprintf("[id %d]", d1)) {
		t.Error("d1 not offered again after re-distillation")
	}
}

func TestNonRecurringGetsNoSeriesDecisions(t *testing.T) {
	conn := openDB(t)
	insertMeeting(t, conn, testMeeting{id: "a", title: "A", folder: "f", started: "2025-01-01T10:00:00-03:00", distilled: true})
	insertMeeting(t, conn, testMeeting{id: "b", title: "B", folder: "f", started: "2025-01-02T10:00:00-03:00"})
	insertItem(t, conn, "a", "decision", "Decisão.")
	r := &fakeRunner{reply: func(_ int, p string) ([]byte, error) { return summaryReply(titleOf(p)), nil }}
	if _, err := NewDistiller(conn, r).Distill(context.Background(), 10, nil); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(r.prompts[0], "Decisões anteriores") {
		t.Fatal("non-recurring meeting got series decisions")
	}
}

func TestParseItems(t *testing.T) {
	item := func(kind, text string, imp int) string {
		return fmt.Sprintf(`{"kind":%q,"text":%q,"people":[],"importance":%d,"supersedes":[1]}`, kind, text, imp)
	}
	wrap := func(items ...string) []byte { return []byte(`{"items":[` + strings.Join(items, ",") + `]}`) }

	bad := map[string][]byte{
		"not json":      []byte(`nope`),
		"no items":      []byte(`{}`),
		"unknown field": []byte(`{"items":[],"extra":1}`),
		"bad kind":      wrap(item("opinion", "x", 2)),
		"empty text":    wrap(item("fact", "  ", 2)),
		"importance":    wrap(item("fact", "x", 4)),
		"two summaries": wrap(item("summary", "a", 2), item("summary", "b", 2)),
	}
	for name, raw := range bad {
		if _, err := parseItems(raw); err == nil {
			t.Errorf("%s: want error", name)
		}
	}

	items, err := parseItems(wrap(item("summary", " s ", 0), item("decision", "d", 3), item("fact", "f", 1)))
	if err != nil {
		t.Fatal(err)
	}
	if items[0].Text != "s" || items[0].Importance != 2 || items[0].Supersedes != nil {
		t.Errorf("summary %+v", items[0])
	}
	if len(items[1].Supersedes) != 1 || len(items[2].Supersedes) != 1 {
		t.Errorf("supersedes kept on decisions and facts: %+v %+v", items[1], items[2])
	}
	if acts, _ := parseItems(wrap(item("action", "a", 2))); acts[0].Supersedes != nil {
		t.Errorf("supersedes dropped on actions: %+v", acts[0])
	}
	if items, err := parseItems([]byte(`{"items":[]}`)); err != nil || len(items) != 0 {
		t.Errorf("empty items: %v %v", items, err)
	}
}

func TestPromptTruncation(t *testing.T) {
	if got := truncateMiddle("abcdef", 10); got != "abcdef" {
		t.Fatalf("short: %q", got)
	}
	long := strings.Repeat("início ", 1000) + strings.Repeat("x", 100000) + " FIM"
	got := truncateMiddle(long, 40000)
	if !strings.HasPrefix(got, "início") || !strings.HasSuffix(got, "FIM") || !strings.Contains(got, "caracteres omitidos") {
		t.Fatal("truncation must keep head, tail and a marker")
	}
	if n := len([]rune(got)); n > 40100 {
		t.Fatalf("truncated to %d runes", n)
	}

	m := distillMeeting{Title: "T", FolderName: "F", Notes: "notas", Transcript: long,
		StartedAt: sql.NullString{String: "2025-01-01T10:00:00-03:00", Valid: true}}
	p := buildPrompt(m, nil, nil, 1000)
	if len([]rune(p)) > 1300 || !strings.Contains(p, "notas") || !strings.Contains(p, "2025-01-01") {
		t.Fatalf("prompt (%d runes) not bounded or missing parts", len([]rune(p)))
	}
	if strings.Contains(buildPrompt(distillMeeting{Title: "T"}, nil, nil, 1000), "## Transcrição") {
		t.Fatal("empty transcript section rendered")
	}
}
