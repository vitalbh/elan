package timeline

import (
	"context"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/vitalbh/elan/internal/db"
	"github.com/vitalbh/elan/internal/search"
)

type fakeSearch struct {
	q   search.Query
	res []search.Result
}

func (f *fakeSearch) Search(_ context.Context, q search.Query) ([]search.Result, error) {
	f.q = q
	return f.res, nil
}

func TestBuild(t *testing.T) {
	conn, err := db.Open(filepath.Join(t.TempDir(), "elan.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	exec := func(q string, args ...any) int64 {
		t.Helper()
		r, err := conn.Exec(q, args...)
		if err != nil {
			t.Fatal(err)
		}
		id, _ := r.LastInsertId()
		return id
	}
	exec(`INSERT INTO meetings (id, title, folder_id, folder_name, started_at, source_modified, content_hash, ingested_at) VALUES
		('m1', 'Boleto 1', 'p', 'p', '2026-09-01T10:00:00-03:00', 'x', 'h', 'now'),
		('m2', 'Boleto 2', 'p', 'p', '2026-10-01T10:00:00-03:00', 'x', 'h', 'now')`)
	old := exec(`INSERT INTO meeting_items (meeting_id, kind, text, created_at) VALUES ('m1', 'fact', 'Boleto sai em novembro.', 'now')`)
	act := exec(`INSERT INTO meeting_items (meeting_id, kind, text, people, status, created_at) VALUES ('m1', 'action', 'Ana integra o Boleto.', '["Ana"]', 'done', 'now')`)
	dec := exec(`INSERT INTO meeting_items (meeting_id, kind, text, created_at) VALUES ('m1', 'decision', 'Boleto só no Vega.', 'now')`)
	newer := exec(`INSERT INTO meeting_items (meeting_id, kind, text, created_at) VALUES ('m2', 'fact', 'Boleto ficou para janeiro.', 'now')`)
	exec(`UPDATE meeting_items SET superseded_by = ? WHERE id = ?`, newer, old)

	hit := func(id int64, bm25, vec int) search.Result {
		return search.Result{SourceType: search.SourceMeetingItem, SourceID: strconv.FormatInt(id, 10), BM25Rank: bm25, VecRank: vec}
	}
	fs := &fakeSearch{res: []search.Result{hit(newer, 1, 1), hit(old, 2, 3), hit(act, 0, 4), hit(dec, 0, 40), hit(9999, 3, 0)}}
	tl, err := New(conn, fs).Build(context.Background(), Query{Topic: "boleto", Person: "ana"})
	if err != nil {
		t.Fatal(err)
	}
	if !fs.q.IncludeSuperseded || fs.q.Person != "ana" || len(fs.q.SourceTypes) != 1 || fs.q.SourceTypes[0] != search.SourceMeetingItem {
		t.Fatalf("search query %+v", fs.q)
	}
	if len(tl.Entries) != 2 || tl.Entries[0].MeetingID != "m2" || tl.Entries[1].MeetingID != "m1" {
		t.Fatalf("entries %+v, want newest first", tl.Entries)
	}
	if md := tl.Markdown(); strings.Index(md, "Boleto 2") > strings.Index(md, "Boleto 1") {
		t.Errorf("markdown should list the newest meeting first:\n%s", md)
	}
	first := tl.Entries[1].Items
	if len(first) != 2 || first[0].Kind != "fact" || first[1].Kind != "action" {
		t.Fatalf("m1 items %+v (the far vector-only decision must be dropped)", first)
	}
	if first[0].ReplacedBy != "Boleto ficou para janeiro." || first[0].ReplacedOn == nil || first[1].Status != "done" {
		t.Fatalf("m1 items %+v", first)
	}
	md := tl.Markdown()
	for _, want := range []string{"# Linha do tempo: boleto", "## 01/09/2026 — Boleto 1", "**Fato:** Boleto sai em novembro. — ~~substituída em 01/10 por:~~ Boleto ficou para janeiro.", "**Ação:** Ana integra o Boleto. (feita)", "## 01/10/2026 — Boleto 2"} {
		if !strings.Contains(md, want) {
			t.Errorf("markdown missing %q:\n%s", want, md)
		}
	}
	if _, err := New(conn, fs).Build(context.Background(), Query{Topic: " "}); err == nil {
		t.Error("empty topic should fail")
	}
}
