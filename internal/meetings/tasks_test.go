package meetings

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/vitalbh/elan/internal/db"
)

type fakeSink struct {
	lists map[string]string
	tasks []string // "list|title|notes"
}

func (f *fakeSink) EnsureList(_ context.Context, title string) (string, error) {
	if f.lists == nil {
		f.lists = map[string]string{}
	}
	if id, ok := f.lists[title]; ok {
		return id, nil
	}
	f.lists[title] = "L" + title
	return f.lists[title], nil
}

func (f *fakeSink) Insert(_ context.Context, listID, title, notes string) (string, error) {
	f.tasks = append(f.tasks, listID+"|"+title+"|"+notes)
	return "T" + title, nil
}

func TestTaskExporter(t *testing.T) {
	conn, err := db.Open(filepath.Join(t.TempDir(), "elan.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	exec := func(q string) {
		t.Helper()
		if _, err := conn.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	exec(`INSERT INTO meetings (id, title, folder_id, folder_name, started_at, source_modified, content_hash, ingested_at) VALUES
		('old', 'Antiga', 'f', 'f', '2026-09-01T10:00:00-03:00', 'x', 'h', 'now'),
		('new', 'Nova', 'f', 'f', '2026-10-28T10:00:00-03:00', 'x', 'h', 'now')`)
	exec(`INSERT INTO meeting_items (meeting_id, kind, text, people, created_at) VALUES
		('old', 'action', 'Vital vai revisar o contrato antigo.', '["Vital"]', 'now'),
		('new', 'action', 'Vital vai mandar a proposta do Boleto.', '["Vitál "]', 'now'),
		('new', 'action', 'Carlos Lima vai falar com o jurídico.', '["Carlos Lima","Ana"]', 'now'),
		('new', 'action', 'Ana vai atualizar a planilha.', '["Ana"]', 'now'),
		('new', 'action', 'Carlos Eduardo vai revisar o layout.', '["Carlos Eduardo"]', 'now'),
		('new', 'decision', 'Vital decidiu usar Boleto.', '["Vital"]', 'now')`)

	sink := &fakeSink{}
	e := NewTaskExporter(conn, sink, []string{"Carlos Lima", "Vital", "Carlos"})
	since := time.Date(2026, 10, 24, 0, 0, 0, 0, time.UTC)
	ctx := context.Background()

	pending, err := e.Pending(ctx, since)
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 2 {
		t.Fatalf("pending %+v, want the two owner actions of the new meeting", pending)
	}
	n, err := e.Export(ctx, "elan · reuniões", pending)
	if err != nil || n != 2 {
		t.Fatalf("export n=%d err=%v", n, err)
	}
	if !strings.Contains(sink.tasks[0], "Reunião: Nova (28/10/2026)") || !strings.Contains(sink.tasks[0], "docs.google.com/document/d/new") {
		t.Fatalf("notes: %q", sink.tasks[0])
	}

	// Re-distilled meeting: new item ids, same text → nothing new.
	exec(`DELETE FROM meeting_items WHERE meeting_id = 'new'`)
	exec(`INSERT INTO meeting_items (meeting_id, kind, text, people, created_at) VALUES
		('new', 'action', 'Vital  vai mandar a proposta do Boleto.', '["Vital"]', 'now')`)
	if pending, _ = e.Pending(ctx, since); len(pending) != 0 {
		t.Fatalf("re-distilled pending %+v, want none", pending)
	}

	if _, err := NewTaskExporter(conn, sink, nil).Pending(ctx, since); err == nil {
		t.Fatal("no owner names should be an error")
	}
}

func TestTaskTitle(t *testing.T) {
	long := strings.Repeat("a", 200)
	if got := []rune(taskTitle(long)); len(got) != 140 || got[139] != '…' {
		t.Fatalf("title %d runes", len(got))
	}
}
