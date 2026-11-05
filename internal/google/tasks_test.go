package google

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// fakeTasks answers the Tasks API endpoints the client uses from an
// in-memory set of lists and records the patches.
type fakeTasks struct {
	t      *testing.T
	lists  []map[string]any
	tasks  map[string][]map[string]any // by list id
	status int
	body   string

	mu      sync.Mutex
	patches []map[string]any
	deleted []string
}

func (f *fakeTasks) client(t *testing.T) *Tasks {
	srv := httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(srv.Close)
	open := func(context.Context) (*http.Client, error) { return srv.Client(), nil }
	return NewTasksClient(open, TasksOptions{Location: testLoc, Endpoint: srv.URL + "/"})
}

func (f *fakeTasks) find(list, id string) map[string]any {
	for _, it := range f.tasks[list] {
		if it["id"] == id {
			return it
		}
	}
	return nil
}

func (f *fakeTasks) serve(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	if f.status != 0 {
		w.WriteHeader(f.status)
		io.WriteString(w, f.body)
		return
	}
	var body map[string]any
	if b, _ := io.ReadAll(r.Body); len(b) > 0 {
		if err := json.Unmarshal(b, &body); err != nil {
			f.t.Errorf("bad body %s", b)
		}
	}
	notFound := func() {
		w.WriteHeader(http.StatusNotFound)
		io.WriteString(w, `{"error":{"code":404,"message":"Not Found"}}`)
	}
	p := strings.Split(strings.Trim(strings.TrimPrefix(r.URL.Path, "/tasks/v1/"), "/"), "/")
	switch {
	case len(p) == 3 && p[0] == "users" && p[2] == "lists" && r.Method == http.MethodGet:
		json.NewEncoder(w).Encode(map[string]any{"items": f.lists})
	case len(p) == 4 && p[0] == "users" && p[2] == "lists" && r.Method == http.MethodGet:
		id := p[3]
		if id == "@default" {
			id = f.lists[0]["id"].(string)
		}
		for _, l := range f.lists {
			if l["id"] == id {
				json.NewEncoder(w).Encode(l)
				return
			}
		}
		notFound()
	case len(p) == 3 && p[0] == "lists" && p[2] == "tasks" && r.Method == http.MethodGet:
		q := r.URL.Query()
		var items []map[string]any
		for _, it := range f.tasks[p[1]] {
			if it["status"] == "completed" && q.Get("showCompleted") != "true" {
				continue
			}
			items = append(items, it)
		}
		json.NewEncoder(w).Encode(map[string]any{"items": items})
	case len(p) == 3 && p[0] == "lists" && p[2] == "tasks" && r.Method == http.MethodPost:
		body["id"] = "new"
		body["status"] = "needsAction"
		body["updated"] = "2026-11-01T12:00:00.000Z"
		f.tasks[p[1]] = append(f.tasks[p[1]], body)
		json.NewEncoder(w).Encode(body)
	case len(p) == 4 && p[0] == "lists" && p[2] == "tasks":
		it := f.find(p[1], p[3])
		if it == nil {
			notFound()
			return
		}
		switch r.Method {
		case http.MethodGet:
			json.NewEncoder(w).Encode(it)
		case http.MethodPatch:
			f.patches = append(f.patches, body)
			for k, v := range body {
				if v == nil {
					delete(it, k)
				} else {
					it[k] = v
				}
			}
			if body["status"] == "completed" {
				it["completed"] = "2026-11-01T15:00:00.000Z"
			}
			json.NewEncoder(w).Encode(it)
		case http.MethodDelete:
			f.deleted = append(f.deleted, p[3])
			w.WriteHeader(http.StatusNoContent)
		}
	default:
		f.t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		notFound()
	}
}

func newFakeTasks(t *testing.T) *fakeTasks {
	return &fakeTasks{t: t,
		lists: []map[string]any{{"id": "L1", "title": "Minhas tarefas"}, {"id": "L2", "title": "elan · reuniões"}},
		tasks: map[string][]map[string]any{
			"L1": {
				{"id": "a", "title": "Pagar boleto", "status": "needsAction", "due": "2026-11-05T00:00:00.000Z", "position": "002", "updated": "2026-10-30T10:00:00.000Z"},
				{"id": "b", "title": "Sem data", "status": "needsAction", "position": "001", "updated": "2026-10-30T10:00:00.000Z"},
				{"id": "b1", "title": "Sub", "status": "needsAction", "parent": "b", "position": "000", "updated": "2026-10-30T10:00:00.000Z"},
				{"id": "c", "title": "Feita", "status": "completed", "completed": "2026-10-29T13:00:00.000Z", "due": "2026-10-28T00:00:00.000Z", "position": "003", "updated": "2026-10-29T13:00:00.000Z"},
			},
			"L2": {{"id": "x", "title": "Ação", "status": "needsAction", "updated": "2026-10-30T10:00:00.000Z"}},
		}}
}

func TestTasksRead(t *testing.T) {
	f := newFakeTasks(t)
	c := f.client(t)
	ctx := context.Background()

	lists, err := c.Lists(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(lists) != 2 || !lists[0].Default || lists[1].Default {
		t.Errorf("lists %+v", lists)
	}
	for ref, want := range map[string]string{"": "L1", "@default": "L1", "L2": "L2", "ELAN · REUNIÕES": "L2"} {
		if l, err := c.FindList(ctx, ref); err != nil || l.ID != want {
			t.Errorf("FindList(%q) = %+v, %v", ref, l, err)
		}
	}
	if _, err := c.FindList(ctx, "Compras"); !errors.Is(err, ErrTaskListNotFound) || !strings.Contains(err.Error(), "Minhas tarefas") {
		t.Errorf("unknown list: %v", err)
	}

	open, err := c.ListTasks(ctx, "L1", TaskQuery{})
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, x := range open {
		ids = append(ids, x.ID)
	}
	if strings.Join(ids, ",") != "b,b1,a" {
		t.Errorf("open tasks in order: %v", ids)
	}
	// The due date is midnight UTC: in São Paulo that instant is the 4th at
	// 21:00, but the task is due on the 5th.
	if open[2].Due != "2026-11-05" || open[2].Updated.Location() != testLoc {
		t.Errorf("due %q updated %v", open[2].Due, open[2].Updated)
	}

	all, err := c.ListTasks(ctx, "L1", TaskQuery{IncludeCompleted: true, DueBefore: "2026-11-05"})
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 1 || all[0].ID != "c" || !all[0].Done || all[0].CompletedAt == nil || all[0].CompletedAt.Hour() != 10 {
		t.Errorf("due before (exclusive) with completed: %+v", all)
	}
	if _, err := c.ListTasks(ctx, "L1", TaskQuery{DueBefore: "05/11"}); err == nil {
		t.Error("bad due filter should fail")
	}

	task, err := c.FindTask(ctx, "", "x")
	if err != nil || task.ListID != "L2" || task.List != "elan · reuniões" {
		t.Errorf("FindTask across lists: %+v %v", task, err)
	}
	if _, err := c.FindTask(ctx, "", "nope"); !errors.Is(err, ErrTaskNotFound) {
		t.Errorf("FindTask unknown: %v", err)
	}
}

func TestTasksWrite(t *testing.T) {
	f := newFakeTasks(t)
	c := f.client(t)
	ctx := context.Background()

	created, err := c.Create(ctx, "L1", NewTask{Title: " Ligar ", Notes: "n", Due: "2026-11-10"})
	if err != nil {
		t.Fatal(err)
	}
	if created.ID != "new" || created.Due != "2026-11-10" || f.find("L1", "new")["due"] != "2026-11-10T00:00:00.000Z" ||
		f.find("L1", "new")["title"] != "Ligar" {
		t.Errorf("created %+v, stored %v", created, f.find("L1", "new"))
	}
	if _, err := c.Create(ctx, "L1", NewTask{Title: "x", Due: "amanhã"}); err == nil {
		t.Error("bad due should fail")
	}
	if _, err := c.Create(ctx, "L1", NewTask{Title: " "}); err == nil {
		t.Error("empty title should fail")
	}

	done, err := c.SetCompleted(ctx, "L1", "a", true)
	if err != nil || !done.Done || done.CompletedAt == nil {
		t.Errorf("complete: %+v %v", done, err)
	}
	reopened, err := c.SetCompleted(ctx, "L1", "a", false)
	if err != nil || reopened.Done || reopened.CompletedAt != nil {
		t.Errorf("reopen: %+v %v", reopened, err)
	}
	if last := f.patches[len(f.patches)-1]; last["status"] != "needsAction" || last["completed"] != nil {
		t.Errorf("reopen patch %v", last)
	} else if _, ok := last["completed"]; !ok {
		t.Errorf("reopen must null completed: %v", last)
	}

	title, empty := "Pagar boleto hoje", ""
	upd, err := c.Update(ctx, "L1", "a", TaskPatch{Title: &title, Due: &empty})
	if err != nil || upd.Title != title || upd.Due != "" {
		t.Errorf("update: %+v %v", upd, err)
	}
	if _, err := c.Update(ctx, "L1", "a", TaskPatch{}); err == nil {
		t.Error("empty patch should fail")
	}
	if _, err := c.SetCompleted(ctx, "L1", "nope", true); !errors.Is(err, ErrTaskNotFound) {
		t.Errorf("complete unknown: %v", err)
	}
	if err := c.Delete(ctx, "L1", "b"); err != nil || len(f.deleted) != 1 {
		t.Errorf("delete: %v %v", err, f.deleted)
	}
}

func TestTasksErrors(t *testing.T) {
	ctx := context.Background()
	f := &fakeTasks{t: t, status: http.StatusForbidden,
		body: `{"error":{"code":403,"message":"Request had insufficient authentication scopes.","errors":[{"reason":"insufficientPermissions"}]}}`}
	if _, err := f.client(t).Lists(ctx); !errors.Is(err, ErrTasksScope) {
		t.Errorf("scope: %v", err)
	}
	f = &fakeTasks{t: t, status: http.StatusForbidden,
		body: `{"error":{"code":403,"message":"Tasks API has not been used in project 1 before or it is disabled.","errors":[{"reason":"accessNotConfigured"}]}}`}
	if _, err := f.client(t).ListTasks(ctx, "L1", TaskQuery{}); !errors.Is(err, ErrTasksAPIDisabled) {
		t.Errorf("api disabled: %v", err)
	}
	missing := NewTasksClient(func(context.Context) (*http.Client, error) { return nil, ErrNotLoggedIn }, TasksOptions{})
	if _, err := missing.Lists(ctx); !errors.Is(err, ErrNotLoggedIn) || !strings.Contains(err.Error(), "google-login") {
		t.Errorf("no login: %v", err)
	}
}
