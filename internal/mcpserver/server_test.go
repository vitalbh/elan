package mcpserver

import (
	"context"
	"encoding/json"
	"hash/fnv"
	"path/filepath"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/vitalbh/elan/internal/db"
	"github.com/vitalbh/elan/internal/embed"
	"github.com/vitalbh/elan/internal/meetings"
	"github.com/vitalbh/elan/internal/notes"
	"github.com/vitalbh/elan/internal/search"
)

// fakeEmbedder hashes words into a small vector: texts sharing words are close.
type fakeEmbedder struct{}

func (fakeEmbedder) Model() string { return "fake:latest" }

func (fakeEmbedder) vec(text string) []float32 {
	v := make([]float32, 16)
	for _, w := range strings.Fields(strings.ToLower(text)) {
		h := fnv.New32a()
		h.Write([]byte(w))
		v[h.Sum32()%16]++
	}
	v[0] += 0.01 // never the zero vector
	return v
}

func (f fakeEmbedder) EmbedDocuments(_ context.Context, docs []embed.Doc) ([][]float32, error) {
	out := make([][]float32, len(docs))
	for i, d := range docs {
		out[i] = f.vec(d.Title + " " + d.Text)
	}
	return out, nil
}

func (f fakeEmbedder) EmbedQuery(_ context.Context, q string) ([]float32, error) {
	return f.vec(q), nil
}

func setup(t *testing.T) *mcp.ClientSession {
	t.Helper()
	ctx := context.Background()
	conn, err := db.Open(filepath.Join(t.TempDir(), "elan.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	exec := func(q string) {
		t.Helper()
		if _, err := conn.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	exec(`INSERT INTO meetings (id, title, folder_id, folder_name, recurring, started_at,
		source_modified, content_hash, distilled_hash, notes, transcript, ingested_at)
		VALUES ('m1', 'Pagamentos', 'f1', 'Pagamentos (recurring)', 1, '2026-10-20T14:00:00-03:00',
		'x', 'h1', 'h1', 'Resumo
Integração do Boleto no Vega Pay.', 'Ana: vamos fazer o boleto automático', 'now')`)
	exec(`INSERT INTO meeting_items (meeting_id, kind, text, people, importance, created_at) VALUES
		('m1', 'summary', 'Reunião sobre a integração do Boleto no Vega Pay.', '[]', 2, 'now'),
		('m1', 'decision', 'O time do Vega Pay vai começar a integração do Boleto.', '["Ana"]', 3, 'now')`)

	svc := search.NewService(conn, fakeEmbedder{})
	if _, err := svc.Index(ctx, search.IndexOptions{}); err != nil {
		t.Fatal(err)
	}
	srv := New(Deps{Search: svc, Meetings: meetings.NewReader(conn), Notes: notes.NewService(conn), Indexer: svc, Version: "test"})
	ct, st := mcp.NewInMemoryTransports()
	if _, err := srv.Connect(ctx, st, nil); err != nil {
		t.Fatal(err)
	}
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "test"}, nil).Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cs.Close() })
	return cs
}

func call[T any](t *testing.T, cs *mcp.ClientSession, tool string, args map[string]any) (T, *mcp.CallToolResult) {
	t.Helper()
	var out T
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: tool, Arguments: args})
	if err != nil {
		t.Fatalf("%s: %v", tool, err)
	}
	if !res.IsError {
		b, _ := json.Marshal(res.StructuredContent)
		if err := json.Unmarshal(b, &out); err != nil {
			t.Fatalf("%s: decode %s: %v", tool, b, err)
		}
	}
	return out, res
}

func TestTools(t *testing.T) {
	cs := setup(t)

	tools, err := cs.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(tools.Tools) != 5 {
		t.Fatalf("%d tools, want 5", len(tools.Tools))
	}

	found, _ := call[SearchOut](t, cs, "search", map[string]any{"query": "integração boleto"})
	if len(found.Results) == 0 || found.Results[0].MeetingID != "m1" || found.Results[0].Date != "2026-10-20" {
		t.Fatalf("search: %+v", found)
	}
	for _, r := range found.Results {
		if r.Type == search.SourceTranscript {
			t.Fatal("transcripts must be out of the default search")
		}
	}

	m, _ := call[meetings.Meeting](t, cs, "get_meeting", map[string]any{"meeting_id": "m1"})
	if m.Title != "Pagamentos" || !m.Distilled || len(m.Items) != 2 || m.Items[0].Kind != "summary" || m.Items[1].People[0] != "Ana" {
		t.Fatalf("get_meeting: %+v", m)
	}
	if _, res := call[meetings.Meeting](t, cs, "get_meeting", map[string]any{"meeting_id": "nope"}); !res.IsError {
		t.Fatal("unknown meeting should be a tool error")
	}

	list, _ := call[ListOut](t, cs, "list_meetings", map[string]any{"since": "2026-10-01", "title": "pagamento"})
	if len(list.Meetings) != 1 || list.Meetings[0].Summary == "" {
		t.Fatalf("list_meetings: %+v", list)
	}
	if empty, _ := call[ListOut](t, cs, "list_meetings", map[string]any{"since": "2026-11-01"}); len(empty.Meetings) != 0 {
		t.Fatalf("list_meetings since: %+v", empty)
	}
	if _, res := call[ListOut](t, cs, "list_meetings", map[string]any{"since": "ontem"}); !res.IsError {
		t.Fatal("bad date should be a tool error")
	}

	tr, _ := call[TranscriptOut](t, cs, "meeting_transcript", map[string]any{"meeting_id": "m1", "max_chars": 10})
	if tr.Text != "Ana: vamos" || tr.NextOffset != 10 || tr.Total == 0 {
		t.Fatalf("meeting_transcript: %+v", tr)
	}

	n, _ := call[NoteOut](t, cs, "note_save", map[string]any{"text": "O fornecedor de CDN renova contrato em março"})
	if n.ID == 0 || !n.Indexed {
		t.Fatalf("note_save: %+v", n)
	}
	found, _ = call[SearchOut](t, cs, "search", map[string]any{"query": "contrato CDN"})
	if len(found.Results) == 0 || found.Results[0].Type != search.SourceNote {
		t.Fatalf("saved note not searchable: %+v", found)
	}
	if _, res := call[NoteOut](t, cs, "note_save", map[string]any{"text": "  "}); !res.IsError {
		t.Fatal("empty note should be a tool error")
	}
}
