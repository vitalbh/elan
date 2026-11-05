package mcpserver

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/vitalbh/elan/internal/db"
	"github.com/vitalbh/elan/internal/meetings"
)

func setupActions(t *testing.T) *mcp.ClientSession {
	t.Helper()
	ctx := context.Background()
	conn, err := db.Open(filepath.Join(t.TempDir(), "elan.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	for _, q := range []string{
		`INSERT INTO meetings (id, title, folder_id, folder_name, started_at, source_modified, content_hash, ingested_at)
			VALUES ('m1', 'Pagamentos', 'f1', 'f1', datetime('now', '-2 days'), 'x', 'h', 'now'),
			       ('m0', 'Antiga', 'f1', 'f1', datetime('now', '-60 days'), 'x', 'h', 'now')`,
		`UPDATE meetings SET started_at = replace(started_at, ' ', 'T') || 'Z'`,
		`INSERT INTO meeting_items (id, meeting_id, kind, text, people, created_at) VALUES
			(10, 'm1', 'action', 'Vital vai mandar a proposta do Pix.', '["Vital"]', 'now'),
			(11, 'm1', 'action', 'Ana vai revisar o contrato.', '["Ana"]', 'now'),
			(12, 'm0', 'action', 'Vital vai migrar o banco.', '["Vital"]', 'now'),
			(13, 'm1', 'decision', 'Pix entra em novembro.', '[]', 'now')`,
	} {
		if _, err := conn.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	srv := New(Deps{Actions: meetings.NewActions(conn, []string{"Vital"}), Version: "test"})
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

func actionIDs(out ActionsOut) []int64 {
	ids := []int64{}
	for _, a := range out.Actions {
		ids = append(ids, a.ID)
	}
	return ids
}

func TestActionTools(t *testing.T) {
	cs := setupActions(t)

	mine, _ := call[ActionsOut](t, cs, "actions_pending", map[string]any{"owner": "mine"})
	if len(mine.Actions) != 1 || mine.Actions[0].ID != 10 || !mine.Actions[0].Mine || mine.Actions[0].MeetingTitle != "Pagamentos" {
		t.Fatalf("mine: %+v", mine)
	}
	stale, _ := call[ActionsOut](t, cs, "actions_pending", map[string]any{"owner": "mine", "include_stale": true})
	if got := actionIDs(stale); len(got) != 2 || got[1] != 12 || !stale.Actions[1].Stale {
		t.Fatalf("mine + stale: %+v", stale)
	}
	others, _ := call[ActionsOut](t, cs, "actions_pending", map[string]any{"owner": "others"})
	if got := actionIDs(others); len(got) != 1 || got[0] != 11 {
		t.Fatalf("others: %+v", others)
	}
	byPerson, _ := call[ActionsOut](t, cs, "actions_pending", map[string]any{"person": "ana"})
	if got := actionIDs(byPerson); len(got) != 1 || got[0] != 11 {
		t.Fatalf("person: %+v", byPerson)
	}
	if _, res := call[ActionsOut](t, cs, "actions_pending", map[string]any{"since": "ontem"}); !res.IsError {
		t.Fatal("bad date should be a tool error")
	}
	if _, res := call[ActionsOut](t, cs, "actions_pending", map[string]any{"owner": "eu"}); !res.IsError {
		t.Fatal("bad owner should be a tool error")
	}

	st, _ := call[meetings.ActionState](t, cs, "action_update", map[string]any{"id": 10, "status": "done"})
	if st.ID != 10 || st.Status != "done" || st.Source != "manual" {
		t.Fatalf("action_update: %+v", st)
	}
	if mine, _ := call[ActionsOut](t, cs, "actions_pending", map[string]any{"owner": "mine"}); len(mine.Actions) != 0 {
		t.Fatalf("done action still pending: %+v", mine)
	}
	if _, res := call[meetings.ActionState](t, cs, "action_update", map[string]any{"id": 13, "status": "done"}); !res.IsError {
		t.Fatal("updating a decision should be a tool error")
	}
	if _, res := call[meetings.ActionState](t, cs, "action_update", map[string]any{"id": 11, "status": "feito"}); !res.IsError {
		t.Fatal("bad status should be a tool error")
	}
}
