package meetings

import (
	"context"
	"errors"
	"testing"
	"time"
)

// fakeRemote is a Google Tasks list for the push and the back-sync at once:
// a write moves the task's updated time, as the real API does.
type fakeRemote struct {
	tasks      map[string]*remoteState
	clock      time.Time
	gets, sets int
	fail       error
}

type remoteState struct {
	RemoteTask
	updated time.Time
}

func (f *fakeRemote) GetTask(_ context.Context, list, id string) (RemoteTask, error) {
	f.gets++
	if f.fail != nil {
		return RemoteTask{}, f.fail
	}
	t, ok := f.tasks[id]
	if !ok {
		return RemoteTask{}, ErrTaskGone
	}
	return t.RemoteTask, nil
}

func (f *fakeRemote) SetTask(_ context.Context, list, id string, t RemoteTask) error {
	f.sets++
	if f.fail != nil {
		return f.fail
	}
	f.tasks[id] = &remoteState{RemoteTask: t, updated: f.clock}
	return nil
}

func (f *fakeRemote) ListTasks(_ context.Context, list string, _ time.Time) ([]TaskState, error) {
	var out []TaskState
	for id, t := range f.tasks {
		out = append(out, TaskState{ID: id, Completed: t.Completed, At: t.updated})
	}
	return out, nil
}

// ownerSets changes a task as the owner would in the Google Tasks app.
func (f *fakeRemote) ownerSets(id string, completed bool, at time.Time) {
	t := f.tasks[id]
	t.Completed, t.updated = completed, at
}

func TestTaskPush(t *testing.T) {
	conn := openDB(t)
	ctx := context.Background()
	insertMeeting(t, conn, testMeeting{id: "m", title: "M", folder: "f", started: "2026-10-20T10:00:00-03:00", distilled: true})
	ids := map[string]int64{}
	old := time.Date(2026, 10, 20, 15, 0, 0, 0, time.UTC)
	remote := &fakeRemote{tasks: map[string]*remoteState{}}
	for _, name := range []string{"manual", "dropped", "distill", "untouched", "fromtasks", "legacy"} {
		text := "Vital vai fazer " + name + "."
		ids[name] = insertAction(t, conn, "m", text, "Vital")
		mustExec(t, conn, `INSERT INTO task_exports (meeting_id, text_hash, list_id, task_id, created_at) VALUES ('m', ?, 'L', ?, 'now')`,
			textHash(text), "T"+name)
		remote.tasks["T"+name] = &remoteState{RemoteTask: RemoteTask{Notes: "notas"}, updated: old}
	}
	// legacy: exported and closed before the push existed (baseline).
	mustExec(t, conn, `UPDATE task_exports SET pushed_at = '2026-10-31T00:00:00Z' WHERE task_id = 'Tlegacy'`)
	mustExec(t, conn, `UPDATE meeting_items SET status = 'done', status_source = 'manual', status_at = '2026-10-30T00:00:00Z' WHERE id = ?`, ids["legacy"])
	mustExec(t, conn, `UPDATE meeting_items SET status = 'done', status_source = 'distill', status_at = '2026-11-01T10:00:00Z' WHERE id = ?`, ids["distill"])
	// fromtasks: the owner completed it in Google Tasks; the back-sync brought it.
	remote.ownerSets("Tfromtasks", true, time.Date(2026, 11, 1, 9, 0, 0, 0, time.UTC))
	mustExec(t, conn, `UPDATE meeting_items SET status = 'done', status_source = 'tasks', status_at = '2026-11-01T09:00:00Z' WHERE id = ?`, ids["fromtasks"])

	actions := NewActions(conn, []string{"Vital"})
	now := time.Date(2026, 11, 1, 10, 0, 0, 0, time.UTC)
	actions.now = func() time.Time { return now }
	if _, err := actions.SetStatus(ctx, ids["manual"], StatusDone); err != nil {
		t.Fatal(err)
	}
	if _, err := actions.SetStatus(ctx, ids["dropped"], StatusDropped); err != nil {
		t.Fatal(err)
	}

	push := NewTaskPush(conn, remote)
	push.now = func() time.Time { return now }
	remote.clock = now.Add(5 * time.Minute)
	run := func(label string, dry bool) PushStats {
		t.Helper()
		st, err := push.Push(ctx, dry)
		if err != nil {
			t.Fatalf("%s: %v", label, err)
		}
		return st
	}

	// Dry run: plans, touches nothing.
	if st := run("dry", true); len(st.Changes) != 3 || remote.gets+remote.sets != 0 {
		t.Fatalf("dry run %+v, calls %d/%d", st, remote.gets, remote.sets)
	}
	st := run("first", false)
	if st.Pushed != 3 || st.Unchanged+st.Gone+st.Failed != 0 {
		t.Fatalf("first push %+v", st)
	}
	wantRemote := func(name string, completed bool, notes string) {
		t.Helper()
		if got := remote.tasks["T"+name].RemoteTask; got != (RemoteTask{Completed: completed, Notes: notes}) {
			t.Errorf("remote %s: %+v", name, got)
		}
	}
	wantRemote("manual", true, "notas")
	wantRemote("dropped", true, "notas\n\n"+DroppedMarker)
	wantRemote("distill", true, "notas")
	wantRemote("untouched", false, "notas")
	wantRemote("legacy", false, "notas")

	// Idempotent: nothing changed, the API is not even read.
	calls := remote.gets + remote.sets
	if st := run("again", false); st.Pushed+len(st.Changes) != 0 || remote.gets+remote.sets != calls {
		t.Errorf("second push %+v, calls %d → %d", st, calls, remote.gets+remote.sets)
	}

	// No ping-pong: the back-sync sees the tasks the push completed and
	// changes no action (dropped stays dropped, sources stay).
	sync := NewTaskStatusSync(conn, remote)
	sync.now = func() time.Time { return now.Add(10 * time.Minute) }
	if s, err := sync.Sync(ctx); err != nil || s.Done+s.Dropped+s.Reopened != 0 {
		t.Fatalf("back-sync after push %+v %v", s, err)
	}
	for name, w := range map[string]itemStatus{
		"manual": {Status: "done", Source: "manual"}, "dropped": {Status: "dropped", Source: "manual"},
		"distill": {Status: "done", Source: "distill"}, "untouched": {Status: "open"},
	} {
		if got := statusOf(t, conn, ids[name]); got.Status != w.Status || got.Source != w.Source {
			t.Errorf("%s after back-sync: %+v", name, got)
		}
	}
	if st := run("after back-sync", false); len(st.Changes) != 0 {
		t.Errorf("push after back-sync %+v", st)
	}

	// The owner reopens in Google Tasks what elan completed: the action
	// reopens (source tasks) and the push does not send it back.
	remote.ownerSets("Tdistill", false, now.Add(time.Hour))
	if s, err := sync.Sync(ctx); err != nil || s.Reopened != 1 {
		t.Fatalf("back-sync of a reopen %+v %v", s, err)
	}
	if got := statusOf(t, conn, ids["distill"]); got.Status != "open" || got.Source != "tasks" {
		t.Errorf("distill after reopen in Tasks: %+v", got)
	}
	calls = remote.gets + remote.sets
	if st := run("after reopen in Tasks", false); len(st.Changes) != 0 || remote.gets+remote.sets != calls {
		t.Errorf("push after reopen in Tasks %+v", st)
	}
	// …but a later close in elan is pushed again.
	now = now.Add(2 * time.Hour)
	remote.clock = now
	if _, err := actions.SetStatus(ctx, ids["distill"], StatusDone); err != nil {
		t.Fatal(err)
	}
	if st := run("close again", false); st.Pushed != 1 {
		t.Errorf("close again %+v", st)
	}
	wantRemote("distill", true, "notas")

	// Reopened in elan: the task reopens and loses the marker.
	now = now.Add(time.Minute)
	if _, err := actions.SetStatus(ctx, ids["dropped"], StatusOpen); err != nil {
		t.Fatal(err)
	}
	if st := run("reopen", false); st.Pushed != 1 {
		t.Errorf("reopen %+v", st)
	}
	wantRemote("dropped", false, "notas")

	// A re-distillation reopened what its meeting had closed (source NULL):
	// the task elan had completed reopens too.
	mustExec(t, conn, `UPDATE meeting_items SET status = 'open', status_source = NULL, status_at = NULL WHERE id = ?`, ids["distill"])
	if st := run("redistilled", false); st.Pushed != 1 {
		t.Errorf("redistilled %+v", st)
	}
	wantRemote("distill", false, "notas")

	// A failure is retried on the next run.
	now = now.Add(time.Minute)
	if _, err := actions.SetStatus(ctx, ids["untouched"], StatusDropped); err != nil {
		t.Fatal(err)
	}
	remote.fail = errors.New("offline")
	if st, err := push.Push(ctx, false); err == nil || st.Failed != 1 {
		t.Fatalf("failing push %+v %v", st, err)
	}
	remote.fail = nil
	if st := run("retry", false); st.Pushed != 1 {
		t.Errorf("retry %+v", st)
	}
	wantRemote("untouched", true, "notas\n\n"+DroppedMarker)

	// A task deleted in Google Tasks is skipped without error, once.
	now = now.Add(time.Minute)
	delete(remote.tasks, "Tmanual")
	if _, err := actions.SetStatus(ctx, ids["manual"], StatusOpen); err != nil {
		t.Fatal(err)
	}
	if st := run("gone", false); st.Gone != 1 {
		t.Errorf("gone %+v", st)
	}
	if st := run("gone again", false); len(st.Changes) != 0 {
		t.Errorf("gone again %+v", st)
	}
	wantRemote("legacy", false, "notas")
}

func TestApplyTask(t *testing.T) {
	conn := openDB(t)
	ctx := context.Background()
	insertMeeting(t, conn, testMeeting{id: "m", title: "M", folder: "f", started: "2026-10-20T10:00:00-03:00", distilled: true})
	text := "Vital manda a proposta."
	id := insertAction(t, conn, "m", text, "Vital")
	mustExec(t, conn, `INSERT INTO task_exports (meeting_id, text_hash, list_id, task_id, created_at) VALUES ('m', ?, 'L', 'T1', 'now')`, textHash(text))
	actions := NewActions(conn, []string{"Vital"})

	at := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	st, err := actions.ApplyTask(ctx, "L", TaskState{ID: "T1", Completed: true, At: at})
	if err != nil || st == nil || st.ID != id || st.Status != StatusDone || st.Source != SourceTasks || st.StatusAt != "2026-11-01T12:00:00Z" {
		t.Fatalf("complete: %+v %v", st, err)
	}
	// The push only records it: Tasks already has it.
	remote := &fakeRemote{tasks: map[string]*remoteState{"T1": {RemoteTask: RemoteTask{Completed: true}}}}
	if ps, err := NewTaskPush(conn, remote).Push(ctx, false); err != nil || len(ps.Changes) != 0 || remote.gets != 0 {
		t.Errorf("push after tasks_complete %+v %v", ps, err)
	}
	var pushed string
	conn.QueryRow(`SELECT pushed_status FROM task_exports WHERE task_id = 'T1'`).Scan(&pushed) //nolint:errcheck
	if pushed != StatusDone {
		t.Errorf("pushed_status %q", pushed)
	}

	st, err = actions.ApplyTask(ctx, "L", TaskState{ID: "T1", At: at.Add(time.Minute)})
	if err != nil || st.Status != StatusOpen {
		t.Errorf("reopen: %+v %v", st, err)
	}
	if st, err := actions.ApplyTask(ctx, "L", TaskState{ID: "handmade", Completed: true}); st != nil || err != nil {
		t.Errorf("not exported: %+v %v", st, err)
	}
}

func TestWithDroppedMarker(t *testing.T) {
	for _, c := range []struct {
		in   string
		on   bool
		want string
	}{
		{"", true, DroppedMarker},
		{"a", true, "a\n\n" + DroppedMarker},
		{"a\n\n" + DroppedMarker, true, "a\n\n" + DroppedMarker},
		{"a\n\n" + DroppedMarker, false, "a"},
		{" a ", false, " a "},
	} {
		if got := withDroppedMarker(c.in, c.on); got != c.want {
			t.Errorf("withDroppedMarker(%q, %v) = %q, want %q", c.in, c.on, got, c.want)
		}
	}
}
