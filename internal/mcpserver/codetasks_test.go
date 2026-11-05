package mcpserver

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/vitalbh/elan/internal/db"
	"github.com/vitalbh/elan/internal/meetings"
)

func setupCodeTasks(t *testing.T) *mcp.ClientSession {
	t.Helper()
	ctx := context.Background()
	conn, err := db.Open(filepath.Join(t.TempDir(), "elan.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	for _, q := range []string{
		`INSERT INTO meetings (id, title, folder_id, folder_name, started_at, source_modified, content_hash, ingested_at)
			VALUES ('m1', 'Pagamentos', 'f1', 'f1', strftime('%Y-%m-%dT%H:%M:%SZ', 'now', '-2 days'), 'x', 'h', 'now')`,
		`INSERT INTO meeting_items (id, meeting_id, kind, text, people, category, project, created_at) VALUES
			(10, 'm1', 'action', 'Vital vai corrigir o checkout do Pix.', '["Vital"]', 'code', 'pix', 'now'),
			(11, 'm1', 'action', 'Vital vai ligar para a Ana.', '["Vital"]', 'other', '', 'now')`,
	} {
		if _, err := conn.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	ct := meetings.NewCodeTasks(conn, []string{"Vital"}, meetings.NewProjects([]meetings.Project{{Name: "Pix", Folder: "/src/pix"}}))
	srv := New(Deps{CodeTasks: ct, Version: "test"})
	clientT, serverT := mcp.NewInMemoryTransports()
	if _, err := srv.Connect(ctx, serverT, nil); err != nil {
		t.Fatal(err)
	}
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "test"}, nil).Connect(ctx, clientT, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cs.Close() })
	return cs
}

func TestCodeTaskTools(t *testing.T) {
	cs := setupCodeTasks(t)

	tools, err := cs.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	names := map[string]bool{}
	for _, tool := range tools.Tools {
		names[tool.Name] = true
	}
	for _, n := range []string{"codetask_create", "codetasks_list", "codetask_update", "codetask_suggestions", "codetask_promote"} {
		if !names[n] {
			t.Errorf("tool %s not registered", n)
		}
	}

	empty, _ := call[CodeTasksOut](t, cs, "codetasks_list", map[string]any{})
	if empty.CodeTasks == nil || len(empty.CodeTasks) != 0 {
		t.Fatalf("empty list: %+v", empty)
	}
	sugg, _ := call[SuggestionsOut](t, cs, "codetask_suggestions", map[string]any{})
	if len(sugg.Suggestions) != 1 || sugg.Suggestions[0].ItemID != 10 || sugg.Suggestions[0].Folder != "/src/pix" {
		t.Fatalf("suggestions: %+v", sugg)
	}

	created, res := call[meetings.CodeTask](t, cs, "codetask_create", map[string]any{
		"text": "Trocar o gateway do Pix", "project": "pix", "meeting_id": "m1", "context": "Só no staging.",
	})
	if res.IsError || created.ID == 0 || created.Project != "Pix" || created.Folder != "/src/pix" ||
		created.MeetingTitle != "Pagamentos" || created.Context != "Só no staging." || created.SourceItemID != nil {
		t.Fatalf("create: %+v %+v", created, res)
	}
	if _, res := call[meetings.CodeTask](t, cs, "codetask_create", map[string]any{"text": "x", "meeting_id": "nope"}); !res.IsError {
		t.Error("missing meeting should be a tool error")
	}

	promoted, _ := call[meetings.CodeTask](t, cs, "codetask_promote", map[string]any{"item_id": 10})
	if promoted.SourceItemID == nil || *promoted.SourceItemID != 10 || promoted.MeetingID != "m1" {
		t.Fatalf("promote: %+v", promoted)
	}
	again, _ := call[meetings.CodeTask](t, cs, "codetask_promote", map[string]any{"item_id": 10})
	if again.ID != promoted.ID {
		t.Errorf("promote twice: %d vs %d", again.ID, promoted.ID)
	}
	sugg, _ = call[SuggestionsOut](t, cs, "codetask_suggestions", map[string]any{})
	if len(sugg.Suggestions) != 0 {
		t.Errorf("promoted still suggested: %+v", sugg)
	}

	upd, res := call[meetings.CodeTask](t, cs, "codetask_update", map[string]any{"id": promoted.ID, "status": "done"})
	if res.IsError || upd.Status != "done" {
		t.Fatalf("update: %+v %+v", upd, res)
	}
	if _, res := call[meetings.CodeTask](t, cs, "codetask_update", map[string]any{"id": promoted.ID, "status": "x"}); !res.IsError {
		t.Error("bad status should be a tool error")
	}

	open, _ := call[CodeTasksOut](t, cs, "codetasks_list", map[string]any{})
	if len(open.CodeTasks) != 1 || open.CodeTasks[0].ID != created.ID {
		t.Errorf("open: %+v", open)
	}
	done, _ := call[CodeTasksOut](t, cs, "codetasks_list", map[string]any{"status": "done"})
	if len(done.CodeTasks) != 1 || done.CodeTasks[0].ID != promoted.ID {
		t.Errorf("done: %+v", done)
	}
	all, _ := call[CodeTasksOut](t, cs, "codetasks_list", map[string]any{"status": "all"})
	if len(all.CodeTasks) != 2 || all.CodeTasks[0].ID != created.ID {
		t.Errorf("all: %+v", all)
	}
}
