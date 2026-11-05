package meetings

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/vitalbh/elan/internal/db"
)

type fakeSource struct {
	docs    []Doc
	texts   map[string]string
	exports int
}

func (f *fakeSource) List(context.Context) ([]Doc, error) { return f.docs, nil }

func (f *fakeSource) Export(_ context.Context, id string) (string, error) {
	f.exports++
	t, ok := f.texts[id]
	if !ok {
		return "", errors.New("sem acesso")
	}
	return t, nil
}

func TestSync(t *testing.T) {
	conn, err := db.Open(filepath.Join(t.TempDir(), "elan.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	t0 := time.Date(2026, 10, 29, 18, 0, 0, 0, time.UTC)
	src := &fakeSource{
		docs: []Doc{
			{ID: "a", Name: "Reunião X - 2026/10/29 14:00 GMT-03:00 - Anotações do Gemini", FolderID: "f1", FolderName: "Reunião X (recurring)", Modified: t0},
			{ID: "b", Name: "Outra - 2026/10/29 15:00 GMT-03:00 - Anotações do Gemini", FolderID: "f2", FolderName: "Outra - 2026/10/29 15:00 GMT-03:00", Modified: t0},
			{ID: "c", Name: "Sem acesso", FolderID: "f3", FolderName: "x", Modified: t0},
		},
		texts: map[string]string{"a": docWithTranscript, "b": "d\nOutra\n\nResumo\nok\n"},
	}
	svc := NewService(conn, src)

	st, err := svc.Sync(context.Background())
	if err == nil {
		t.Fatal("want the error of doc c")
	}
	if st != (Stats{Seen: 3, New: 2, Failed: 1}) {
		t.Fatalf("first sync %+v", st)
	}

	var recurring int
	var transcript string
	if err := conn.QueryRow(`SELECT recurring, transcript FROM meetings WHERE id = 'a'`).Scan(&recurring, &transcript); err != nil {
		t.Fatal(err)
	}
	if recurring != 1 || transcript == "" {
		t.Fatalf("doc a: recurring=%d transcript=%q", recurring, transcript)
	}

	// Same modifiedTime: nothing is exported again.
	src.exports = 0
	delete(src.texts, "c")
	src.docs = src.docs[:2]
	st, err = svc.Sync(context.Background())
	if err != nil || st != (Stats{Seen: 2, Unchanged: 2}) || src.exports != 0 {
		t.Fatalf("second sync %+v err=%v exports=%d", st, err, src.exports)
	}

	// New modifiedTime, same text: exported, not rewritten.
	src.docs[0].Modified = t0.Add(time.Hour)
	st, _ = svc.Sync(context.Background())
	if st.Unchanged != 2 || src.exports != 1 {
		t.Fatalf("touch sync %+v exports=%d", st, src.exports)
	}

	// New text: rewritten.
	src.docs[1].Modified = t0.Add(2 * time.Hour)
	src.texts["b"] = "d\nOutra\n\nResumo\nmudou\n"
	st, _ = svc.Sync(context.Background())
	if st.Updated != 1 {
		t.Fatalf("edit sync %+v", st)
	}
	var notes string
	conn.QueryRow(`SELECT notes FROM meetings WHERE id = 'b'`).Scan(&notes)
	if notes != "Resumo\nmudou" {
		t.Fatalf("notes %q", notes)
	}
}
