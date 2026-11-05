package mcpserver

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/vitalbh/elan/internal/db"
	"github.com/vitalbh/elan/internal/google"
	"github.com/vitalbh/elan/internal/meetings"
)

// fakeTasks keeps lists and tasks in memory.
type fakeTasks struct {
	lists   []google.TaskList
	tasks   map[string][]google.Task // by list id
	queries []google.TaskQuery
	created *google.NewTask
	patch   *google.TaskPatch
	err     error
}

func (f *fakeTasks) Lists(context.Context) ([]google.TaskList, error) { return f.lists, f.err }

func (f *fakeTasks) FindList(_ context.Context, ref string) (google.TaskList, error) {
	if f.err != nil {
		return google.TaskList{}, f.err
	}
	for _, l := range f.lists {
		if (ref == "" && l.Default) || l.ID == ref || strings.EqualFold(l.Title, ref) {
			return l, nil
		}
	}
	return google.TaskList{}, google.ErrTaskListNotFound
}

func (f *fakeTasks) ListTasks(_ context.Context, listID string, q google.TaskQuery) ([]google.Task, error) {
	f.queries = append(f.queries, q)
	return f.tasks[listID], f.err
}

func (f *fakeTasks) FindTask(_ context.Context, listRef, id string) (google.Task, error) {
	for _, l := range f.lists {
		if listRef != "" && l.ID != listRef && !strings.EqualFold(l.Title, listRef) {
			continue
		}
		for _, t := range f.tasks[l.ID] {
			if t.ID == id {
				t.List = l.Title
				return t, nil
			}
		}
	}
	return google.Task{}, google.ErrTaskNotFound
}

func (f *fakeTasks) Create(_ context.Context, listID string, n google.NewTask) (google.Task, error) {
	f.created = &n
	return google.Task{ID: "new", ListID: listID, Title: n.Title, Due: n.Due}, f.err
}

func (f *fakeTasks) Update(_ context.Context, listID, id string, p google.TaskPatch) (google.Task, error) {
	f.patch = &p
	t := google.Task{ID: id, ListID: listID, Updated: time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)}
	if p.Done != nil && *p.Done {
		t.Done = true
		c := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
		t.CompletedAt = &c
	}
	if p.Title != nil {
		t.Title = *p.Title
	}
	return t, nil
}

func TestTasksTools(t *testing.T) {
	ctx := context.Background()
	conn, err := db.Open(filepath.Join(t.TempDir(), "elan.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	text := "Vital manda a proposta."
	for _, q := range []string{
		`INSERT INTO meetings (id, title, folder_id, folder_name, recurring, started_at, source_modified, content_hash, ingested_at)
			VALUES ('m1', 'Pagamentos', 'f1', 'Pagamentos', 0, '2026-10-26T10:00:00-03:00', 'x', 'h', 'now')`,
		`INSERT INTO meeting_items (meeting_id, kind, text, people, created_at) VALUES ('m1', 'action', '` + text + `', '["Vital"]', 'now')`,
	} {
		if _, err := conn.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	exported := meetings.NewTaskExporter(conn, &recordingSink{}, []string{"Vital"})
	pending, err := exported.Pending(ctx, time.Time{})
	if err != nil || len(pending) != 1 {
		t.Fatalf("pending %v %v", pending, err)
	}
	if _, err := exported.Export(ctx, "elan · reuniões", pending); err != nil {
		t.Fatal(err)
	}

	ft := &fakeTasks{
		lists: []google.TaskList{{ID: "L1", Title: "Minhas tarefas", Default: true}, {ID: "L2", Title: "elan · reuniões"}},
		tasks: map[string][]google.Task{
			"L1": {{ID: "a", ListID: "L1", Title: "Pagar boleto", Due: "2026-11-05"}},
			"L2": {{ID: "T1", ListID: "L2", Title: text}},
		},
	}
	srv := New(Deps{Actions: meetings.NewActions(conn, []string{"Vital"}), Tasks: ft, Version: "test"})
	ct, st := mcp.NewInMemoryTransports()
	if _, err := srv.Connect(ctx, st, nil); err != nil {
		t.Fatal(err)
	}
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "test"}, nil).Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cs.Close() })

	tools, err := cs.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	desc := map[string]string{}
	for _, tl := range tools.Tools {
		desc[tl.Name] = tl.Description
	}
	for _, n := range []string{"tasks_lists", "tasks_list", "tasks_create", "tasks_complete", "tasks_update"} {
		if _, ok := desc[n]; !ok {
			t.Errorf("tool %s missing", n)
		}
	}
	for _, n := range []string{"tasks_create", "tasks_complete", "tasks_update"} {
		if !strings.Contains(desc[n], "DE VERDADE") || !strings.Contains(desc[n], "explicitamente") {
			t.Errorf("%s must say it acts on the real Google Tasks on explicit request: %q", n, desc[n])
		}
	}

	lists, _ := call[TaskListsOut](t, cs, "tasks_lists", nil)
	if len(lists.Lists) != 2 || !lists.Lists[0].Default {
		t.Errorf("lists %+v", lists)
	}

	all, _ := call[TasksListOut](t, cs, "tasks_list", nil)
	if len(all.Tasks) != 2 || all.Tasks[0].List != "Minhas tarefas" || all.Tasks[1].List != "elan · reuniões" {
		t.Errorf("all lists: %+v", all)
	}
	one, _ := call[TasksListOut](t, cs, "tasks_list", map[string]any{"list": "minhas tarefas", "due_before": "2026-11-06", "limit": 1})
	if len(one.Tasks) != 1 || one.Tasks[0].Due != "2026-11-05" || ft.queries[len(ft.queries)-1].DueBefore != "2026-11-06" {
		t.Errorf("one list: %+v %+v", one, ft.queries)
	}
	if _, res := call[TasksListOut](t, cs, "tasks_list", map[string]any{"list": "Compras"}); !res.IsError {
		t.Error("unknown list should be a tool error")
	}

	created, _ := call[google.Task](t, cs, "tasks_create", map[string]any{"title": "Ligar", "due": "2026-11-10"})
	if created.ListID != "L1" || created.List != "Minhas tarefas" || ft.created.Due != "2026-11-10" {
		t.Errorf("create goes to the default list: %+v", created)
	}

	// Completing an exported task closes its action (source tasks).
	done, _ := call[TaskCompleteOut](t, cs, "tasks_complete", map[string]any{"id": "T1"})
	if !done.Task.Done || done.Action == nil || done.Action.Status != meetings.StatusDone || done.Action.Source != meetings.SourceTasks {
		t.Fatalf("complete exported: %+v", done)
	}
	reopened, _ := call[TaskCompleteOut](t, cs, "tasks_complete", map[string]any{"id": "T1", "reopen": true})
	if reopened.Task.Done || reopened.Action == nil || reopened.Action.Status != meetings.StatusOpen || *ft.patch.Done {
		t.Errorf("reopen exported: %+v", reopened)
	}
	plain, _ := call[TaskCompleteOut](t, cs, "tasks_complete", map[string]any{"id": "a"})
	if !plain.Task.Done || plain.Action != nil {
		t.Errorf("complete plain task: %+v", plain)
	}
	if _, res := call[TaskCompleteOut](t, cs, "tasks_complete", map[string]any{"id": "nope"}); !res.IsError {
		t.Error("unknown task should be a tool error")
	}

	upd, _ := call[google.Task](t, cs, "tasks_update", map[string]any{"id": "a", "title": "Pagar boleto hoje", "clear_due": true})
	if upd.Title != "Pagar boleto hoje" || ft.patch.Due == nil || *ft.patch.Due != "" || ft.patch.Notes != nil {
		t.Errorf("update: %+v %+v", upd, ft.patch)
	}
	if _, res := call[google.Task](t, cs, "tasks_update", map[string]any{"id": "a"}); !res.IsError {
		t.Error("empty update should be a tool error")
	}

	// Without a Google login only the tasks tools fail, clearly.
	ft.err = errors.New("Google Tasks indisponível: " + google.ErrNotLoggedIn.Error())
	_, res := call[TaskListsOut](t, cs, "tasks_lists", nil)
	if !res.IsError || !strings.Contains(res.Content[0].(*mcp.TextContent).Text, "google-login") {
		t.Errorf("no login: %+v", res)
	}
}

// recordingSink stands in for Google Tasks in the export: task T1 in L2.
type recordingSink struct{}

func (recordingSink) EnsureList(context.Context, string) (string, error) { return "L2", nil }

func (recordingSink) Insert(context.Context, string, string, string) (string, error) {
	return "T1", nil
}
