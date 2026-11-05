package meetings

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/vitalbh/elan/internal/llm"
)

func insertAction(t *testing.T, conn *sql.DB, meeting, text string, people ...string) int64 {
	t.Helper()
	if people == nil {
		people = []string{}
	}
	p, _ := json.Marshal(people)
	res, err := conn.Exec(`INSERT INTO meeting_items (meeting_id, kind, text, people, created_at) VALUES (?, 'action', ?, ?, 'now')`,
		meeting, text, string(p))
	if err != nil {
		t.Fatal(err)
	}
	id, _ := res.LastInsertId()
	return id
}

func mustExec(t *testing.T, conn *sql.DB, q string, args ...any) {
	t.Helper()
	if _, err := conn.Exec(q, args...); err != nil {
		t.Fatal(err)
	}
}

type itemStatus struct {
	Status, Source string
	DoneBy         sql.NullInt64
	DoneIn         string
}

func statusOf(t *testing.T, conn *sql.DB, id int64) itemStatus {
	t.Helper()
	var s itemStatus
	var src, in sql.NullString
	if err := conn.QueryRow(`SELECT status, status_source, done_by, done_in FROM meeting_items WHERE id = ?`, id).
		Scan(&s.Status, &src, &s.DoneBy, &in); err != nil {
		t.Fatal(err)
	}
	s.Source, s.DoneIn = src.String, in.String
	return s
}

func TestDistillCompletesAndDrops(t *testing.T) {
	conn := openDB(t)
	ctx := context.Background()
	insertMeeting(t, conn, testMeeting{id: "m1", title: "Série 1", folder: "s", recurring: true, started: "2025-01-10T10:00:00-03:00", distilled: true})
	insertMeeting(t, conn, testMeeting{id: "m2", title: "Série 2", folder: "s", recurring: true, started: "2025-02-10T10:00:00-03:00"})
	insertMeeting(t, conn, testMeeting{id: "m3", title: "Série 3", folder: "s", recurring: true, started: "2025-03-10T10:00:00-03:00", distilled: true})
	insertMeeting(t, conn, testMeeting{id: "o1", title: "Outra", folder: "t", recurring: true, started: "2025-01-05T10:00:00-03:00", distilled: true})

	a1 := insertAction(t, conn, "m1", "Ana vai migrar o Atlas.", "Ana")
	a2 := insertAction(t, conn, "m1", "Bruno vai revisar o contrato.", "Bruno")
	d1 := insertItem(t, conn, "m1", "decision", "Atlas usa Postgres.")
	manual := insertAction(t, conn, "m1", "Ana reaberta à mão.", "Ana")
	closed := insertAction(t, conn, "m1", "Já concluída.", "Ana")
	mustExec(t, conn, `UPDATE meeting_items SET status_source = 'manual', status_at = 'x' WHERE id = ?`, manual)
	mustExec(t, conn, `UPDATE meeting_items SET status = 'done', status_source = 'tasks', status_at = 'x' WHERE id = ?`, closed)
	later := insertAction(t, conn, "m3", "Ação de reunião posterior.")
	other := insertAction(t, conn, "o1", "Ação de outra série.")

	r := &fakeRunner{reply: func(int, string) ([]byte, error) {
		return []byte(fmt.Sprintf(`{"items":[
			{"kind":"summary","text":"Resumo.","people":[],"importance":2,"supersedes":[],"completes":[%d,%d,%d,%d,%d,%d,999],"drops":[]},
			{"kind":"fact","text":"O contrato foi cancelado.","people":[],"importance":2,"supersedes":[],"completes":[],"drops":[%d,%d]},
			{"kind":"action","text":"Carla vai ligar para o cliente.","people":["Carla"],"importance":2,"supersedes":[],"completes":[],"drops":[]}
		]}`, a1, d1, later, other, manual, closed, a2, a1)), nil
	}}
	st, err := NewDistiller(conn, r).Distill(ctx, 10, nil)
	if err != nil {
		t.Fatal(err)
	}
	if st.Completed != 1 || st.Dropped != 1 {
		t.Fatalf("stats %+v", st)
	}
	_, p, _ := strings.Cut(r.prompts[0], "## Ações anteriores em aberto")
	for _, want := range []int64{a1, a2} {
		if !strings.Contains(p, fmt.Sprintf("[id %d] (2025-01-10; ", want)) {
			t.Errorf("prompt lacks open action %d:\n%s", want, p)
		}
	}
	for _, not := range []int64{d1, manual, closed, later, other} {
		if strings.Contains(p, fmt.Sprintf("[id %d]", not)) {
			t.Errorf("prompt offers %d", not)
		}
	}

	var summary, carla int64
	conn.QueryRow(`SELECT id FROM meeting_items WHERE meeting_id = 'm2' AND kind = 'summary'`).Scan(&summary)                   //nolint:errcheck
	conn.QueryRow(`SELECT id FROM meeting_items WHERE meeting_id = 'm2' AND kind = 'action'`).Scan(&carla)                      //nolint:errcheck
	if got := statusOf(t, conn, a1); got != (itemStatus{"done", "distill", sql.NullInt64{Int64: summary, Valid: true}, "m2"}) { // completes wins over drops
		t.Errorf("a1 %+v", got)
	}
	if got := statusOf(t, conn, a2); got.Status != "dropped" || got.DoneIn != "m2" {
		t.Errorf("a2 %+v", got)
	}
	for _, id := range []int64{d1, manual, later, other} {
		if got := statusOf(t, conn, id); got.Status != "open" {
			t.Errorf("item %d closed: %+v", id, got)
		}
	}
	if got := statusOf(t, conn, closed); got.Source != "tasks" {
		t.Errorf("closed action changed: %+v", got)
	}
	var checked string
	conn.QueryRow(`SELECT actions_checked_hash FROM meetings WHERE id = 'm2'`).Scan(&checked) //nolint:errcheck
	if checked != "h-m2" {
		t.Errorf("actions_checked_hash %q", checked)
	}

	// The owner closes m2's own action by hand; m2 is re-distilled with the
	// same action (other case and spacing) and without completes.
	if _, err := NewActions(conn, nil).SetStatus(ctx, carla, StatusDone); err != nil {
		t.Fatal(err)
	}
	mustExec(t, conn, `UPDATE meetings SET content_hash = 'h2' WHERE id = 'm2'`)
	r.reply = func(int, string) ([]byte, error) {
		return []byte(`{"items":[
			{"kind":"summary","text":"Resumo.","people":[],"importance":2,"supersedes":[],"completes":[],"drops":[]},
			{"kind":"action","text":"  carla vai ligar  para o cliente.","people":["Carla"],"importance":2,"supersedes":[],"completes":[],"drops":[]},
			{"kind":"action","text":"Ação nova.","people":[],"importance":2,"supersedes":[],"completes":[],"drops":[]}
		]}`), nil
	}
	if _, err := NewDistiller(conn, r).Distill(ctx, 10, nil); err != nil {
		t.Fatal(err)
	}
	var newCarla int64
	conn.QueryRow(`SELECT id FROM meeting_items WHERE meeting_id = 'm2' AND kind = 'action' AND text LIKE '%carla%'`).Scan(&newCarla) //nolint:errcheck
	if newCarla == carla || newCarla == 0 {
		t.Fatalf("item not recreated: %d", newCarla)
	}
	if got := statusOf(t, conn, newCarla); got.Status != "done" || got.Source != "manual" {
		t.Errorf("status not carried over: %+v", got)
	}
	var fresh string
	conn.QueryRow(`SELECT status FROM meeting_items WHERE meeting_id = 'm2' AND text = 'Ação nova.'`).Scan(&fresh) //nolint:errcheck
	if fresh != "open" {
		t.Errorf("new action %q", fresh)
	}
	// What m2 had closed is reopened and offered again.
	for _, id := range []int64{a1, a2} {
		if got := statusOf(t, conn, id); got != (itemStatus{Status: "open"}) {
			t.Errorf("item %d not reopened: %+v", id, got)
		}
		if !strings.Contains(r.prompts[1], fmt.Sprintf("[id %d]", id)) {
			t.Errorf("item %d not offered again", id)
		}
	}
}

func TestOpenActions(t *testing.T) {
	conn := openDB(t)
	ctx := context.Background()
	insertMeeting(t, conn, testMeeting{id: "new", title: "Nova", folder: "f", started: "2026-10-28T10:00:00-03:00", distilled: true})
	insertMeeting(t, conn, testMeeting{id: "old", title: "Antiga", folder: "f", started: "2026-08-01T10:00:00-03:00", distilled: true})
	mine := insertAction(t, conn, "new", "Vital vai mandar a proposta.", "Vital")
	ana := insertAction(t, conn, "new", "Ana vai atualizar a planilha.", "Ana Paula")
	insertAction(t, conn, "new", "Alguém vai marcar a reunião.")
	done := insertAction(t, conn, "new", "Vital vai revisar o layout.", "Vital")
	mustExec(t, conn, `UPDATE meeting_items SET status = 'done', status_source = 'manual' WHERE id = ?`, done)
	oldMine := insertAction(t, conn, "old", "Vital vai revisar o contrato antigo.", "Vitál ")
	insertItem(t, conn, "new", "decision", "Vital decidiu usar Pix.")
	mustExec(t, conn, `INSERT INTO task_exports (meeting_id, text_hash, list_id, task_id, web_link, created_at)
		VALUES ('new', ?, 'L1', 'T1', 'https://tasks.google.com/x', 'now')`, textHash(" vital vai mandar a proposta."))

	a := NewActions(conn, []string{"Vital"})
	a.now = func() time.Time { return time.Date(2026, 10, 31, 13, 0, 0, 0, time.UTC) }
	ids := func(q ActionQuery) []int64 {
		t.Helper()
		list, err := a.OpenActions(ctx, q)
		if err != nil {
			t.Fatal(err)
		}
		out := []int64{}
		for _, o := range list {
			out = append(out, o.ID)
		}
		return out
	}
	eq := func(name string, got []int64, want ...int64) {
		t.Helper()
		if fmt.Sprint(got) != fmt.Sprint(append([]int64{}, want...)) {
			t.Errorf("%s: got %v, want %v", name, got, want)
		}
	}

	list, err := a.OpenActions(ctx, ActionQuery{Owner: OwnerMine})
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 {
		t.Fatalf("mine: %+v", list)
	}
	o := list[0]
	if o.ID != mine || !o.Mine || o.Stale || o.AgeDays != 3 || o.MeetingTitle != "Nova" || o.MeetingAt == nil ||
		o.TaskID != "T1" || o.TaskListID != "L1" || o.TaskLink != "https://tasks.google.com/x" || o.People[0] != "Vital" {
		t.Errorf("mine: %+v", o)
	}

	stale, _ := a.OpenActions(ctx, ActionQuery{Owner: OwnerMine, IncludeStale: true})
	if len(stale) != 2 || stale[1].ID != oldMine || !stale[1].Stale || stale[1].AgeDays < 90 || stale[1].TaskID != "" {
		t.Errorf("mine + stale: %+v", stale)
	}
	eq("others", ids(ActionQuery{Owner: OwnerOthers}), ana)
	eq("all", ids(ActionQuery{}), mine, ana, mine+2)
	eq("all + stale", ids(ActionQuery{Owner: OwnerAll, IncludeStale: true}), mine, ana, mine+2, oldMine)
	eq("stale after 100d", ids(ActionQuery{Owner: OwnerMine, StaleAfter: 100 * 24 * time.Hour}), mine, oldMine)
	eq("person ana", ids(ActionQuery{Person: "ana"}), ana)
	eq("person paula", ids(ActionQuery{Person: " PAULA"}), ana)
	eq("person partial word", ids(ActionQuery{Person: "an"}))
	eq("since", ids(ActionQuery{IncludeStale: true, Since: time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)}), mine, ana, mine+2)
	eq("limit", ids(ActionQuery{Limit: 1}), mine)

	if _, err := NewActions(conn, nil).OpenActions(ctx, ActionQuery{Owner: OwnerMine}); err == nil {
		t.Error("mine without owner names should fail")
	}
	if _, err := a.OpenActions(ctx, ActionQuery{Owner: "eu"}); err == nil {
		t.Error("bad owner should fail")
	}

	// Manual updates.
	if _, err := a.SetStatus(ctx, mine, "feito"); err == nil {
		t.Error("bad status should fail")
	}
	var decision int64
	conn.QueryRow(`SELECT id FROM meeting_items WHERE kind = 'decision'`).Scan(&decision) //nolint:errcheck
	if _, err := a.SetStatus(ctx, decision, StatusDone); !errors.Is(err, ErrActionNotFound) {
		t.Errorf("decision: %v", err)
	}
	st, err := a.SetStatus(ctx, mine, StatusDropped)
	if err != nil || st.Status != "dropped" || st.Source != "manual" || st.Text != "Vital vai mandar a proposta." {
		t.Fatalf("set status: %+v %v", st, err)
	}
	eq("mine after drop", ids(ActionQuery{Owner: OwnerMine}))
	if _, err := a.SetStatus(ctx, done, StatusOpen); err != nil {
		t.Fatal(err)
	}
	eq("mine after reopen", ids(ActionQuery{Owner: OwnerMine}), done)
}

type fakeTaskSource struct {
	tasks map[string][]TaskState
	calls []string // "list|updatedMin"
}

func (f *fakeTaskSource) ListTasks(_ context.Context, list string, updatedMin time.Time) ([]TaskState, error) {
	min := ""
	if !updatedMin.IsZero() {
		min = updatedMin.UTC().Format(time.RFC3339)
	}
	f.calls = append(f.calls, list+"|"+min)
	return f.tasks[list], nil
}

func TestTaskStatusSync(t *testing.T) {
	conn := openDB(t)
	ctx := context.Background()
	insertMeeting(t, conn, testMeeting{id: "m", title: "M", folder: "f", started: "2026-10-20T10:00:00-03:00", distilled: true})
	ids := map[string]int64{}
	for _, name := range []string{"completed", "deleted", "unchanged", "purged", "manual", "reopened", "renamed"} {
		text := "Vital vai fazer " + name + "."
		ids[name] = insertAction(t, conn, "m", text, "Vital")
		mustExec(t, conn, `INSERT INTO task_exports (meeting_id, text_hash, list_id, task_id, created_at) VALUES ('m', ?, 'L', ?, 'now')`,
			textHash(text), "T"+name)
	}
	// renamed: the item was re-distilled with another wording.
	mustExec(t, conn, `UPDATE meeting_items SET text = 'Outra redação.' WHERE id = ?`, ids["renamed"])
	mustExec(t, conn, `UPDATE meeting_items SET status_source = 'manual', status_at = '2026-10-30T00:00:00Z' WHERE id = ?`, ids["manual"])
	mustExec(t, conn, `UPDATE meeting_items SET status = 'done', status_source = 'tasks', status_at = '2026-10-21T00:00:00Z' WHERE id = ?`, ids["reopened"])

	at := time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)
	src := &fakeTaskSource{tasks: map[string][]TaskState{"L": {
		{ID: "Tcompleted", Completed: true, At: at, WebLink: "https://tasks/c"},
		{ID: "Tdeleted", Deleted: true, At: at},
		{ID: "Tunchanged", At: at},
		{ID: "Tmanual", Completed: true, At: at}, // before the manual reopen
		{ID: "Treopened", At: at},
		{ID: "Trenamed", Completed: true, At: at},
		{ID: "Thandmade", Completed: true, At: at},
	}}}
	s := NewTaskStatusSync(conn, src)
	now := time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC)
	s.now = func() time.Time { return now }
	st, err := s.Sync(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if st != (TaskStatusStats{Lists: 1, Seen: 6, Done: 1, Dropped: 2, Reopened: 1}) {
		t.Errorf("stats %+v", st)
	}
	want := map[string]itemStatus{
		"completed": {Status: "done", Source: "tasks"},
		"deleted":   {Status: "dropped", Source: "tasks"},
		"unchanged": {Status: "open"},
		"purged":    {Status: "dropped", Source: "tasks"},
		"manual":    {Status: "open", Source: "manual"},
		"reopened":  {Status: "open", Source: "tasks"},
		"renamed":   {Status: "open"},
	}
	for name, w := range want {
		if got := statusOf(t, conn, ids[name]); got != w {
			t.Errorf("%s: %+v, want %+v", name, got, w)
		}
	}
	var link, statusAt string
	conn.QueryRow(`SELECT web_link FROM task_exports WHERE task_id = 'Tcompleted'`).Scan(&link)         //nolint:errcheck
	conn.QueryRow(`SELECT status_at FROM meeting_items WHERE id = ?`, ids["completed"]).Scan(&statusAt) //nolint:errcheck
	if link != "https://tasks/c" || statusAt != "2026-10-25T12:00:00Z" {
		t.Errorf("link %q status_at %q", link, statusAt)
	}

	// Second sync: incremental from the cursor; nothing changed, nothing
	// returned, and missing tasks are not treated as purged.
	src.tasks = map[string][]TaskState{}
	now = now.Add(30 * time.Minute)
	if st, err := s.Sync(ctx); err != nil || st.Done+st.Dropped+st.Reopened != 0 {
		t.Fatalf("second sync %+v %v", st, err)
	}
	if src.calls[0] != "L|" || src.calls[1] != "L|2026-10-31T11:55:00Z" {
		t.Errorf("calls %v", src.calls)
	}
	if got := statusOf(t, conn, ids["unchanged"]); got.Status != "open" {
		t.Errorf("unchanged after incremental sync: %+v", got)
	}
}

func TestReconcile(t *testing.T) {
	conn := openDB(t)
	ctx := context.Background()
	insertMeeting(t, conn, testMeeting{id: "m1", title: "S1", folder: "s", recurring: true, started: "2025-01-10T10:00:00-03:00", distilled: true})
	insertMeeting(t, conn, testMeeting{id: "m2", title: "S2", folder: "s", recurring: true, started: "2025-02-10T10:00:00-03:00", distilled: true})
	insertMeeting(t, conn, testMeeting{id: "m3", title: "S3", folder: "s", recurring: true, started: "2025-03-10T10:00:00-03:00", distilled: true})
	insertMeeting(t, conn, testMeeting{id: "o1", title: "O1", folder: "o", recurring: true, started: "2025-01-01T10:00:00-03:00", distilled: true})
	insertMeeting(t, conn, testMeeting{id: "x1", title: "X1", folder: "x", started: "2025-04-01T10:00:00-03:00", distilled: true})
	insertMeeting(t, conn, testMeeting{id: "u1", title: "U1", folder: "s", recurring: true, started: "2025-05-01T10:00:00-03:00"}) // not distilled
	a1 := insertAction(t, conn, "m1", "Ana vai migrar o Atlas.", "Ana")
	a2 := insertAction(t, conn, "m1", "Bruno vai revisar o contrato.", "Bruno")
	b1 := insertAction(t, conn, "m2", "Carla vai ligar.", "Carla")
	insertAction(t, conn, "o1", "Outra série.")

	r := &fakeRunner{reply: func(_ int, p string) ([]byte, error) {
		switch titleOf(p) {
		case "S2":
			return []byte(fmt.Sprintf(`{"completes":[%d,%d,999],"drops":[]}`, a1, b1)), nil
		case "S3":
			return []byte(fmt.Sprintf(`{"completes":[%d],"drops":[%d]}`, a1, a2)), nil
		}
		return nil, fmt.Errorf("unexpected call for %q", titleOf(p))
	}}
	rec := NewReconciler(conn, r)
	st, err := rec.Reconcile(ctx, 1, nil)
	if err != nil {
		t.Fatal(err)
	}
	// o1 and m1 have no earlier actions: checked without a call.
	if st != (ReconcileStats{Checked: 3, Calls: 1, Completed: 1, Pending: 1}) {
		t.Errorf("first run %+v", st)
	}
	if len(r.reqs) != 1 || string(r.reqs[0].Schema) != string(reconcileSchema) || !strings.Contains(r.prompts[0], fmt.Sprintf("[id %d]", a1)) ||
		strings.Contains(r.prompts[0], fmt.Sprintf("[id %d]", b1)) {
		t.Fatalf("first call %+v", r.prompts)
	}
	if got := statusOf(t, conn, a1); got != (itemStatus{Status: "done", Source: "distill", DoneIn: "m2"}) {
		t.Errorf("a1 %+v", got)
	}
	if got := statusOf(t, conn, b1); got.Status != "open" {
		t.Errorf("b1 (same meeting) %+v", got)
	}

	st, err = rec.Reconcile(ctx, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	if st != (ReconcileStats{Checked: 1, Calls: 1, Dropped: 1}) {
		t.Errorf("second run %+v", st)
	}
	if strings.Contains(r.prompts[1], fmt.Sprintf("[id %d]", a1)) || !strings.Contains(r.prompts[1], fmt.Sprintf("[id %d]", b1)) {
		t.Errorf("m3 prompt:\n%s", r.prompts[1])
	}
	if got := statusOf(t, conn, a2); got != (itemStatus{Status: "dropped", Source: "distill", DoneIn: "m3"}) {
		t.Errorf("a2 %+v", got)
	}
	if got := statusOf(t, conn, a1); got.DoneIn != "m2" {
		t.Errorf("a1 overwritten: %+v", got)
	}

	if st, err := rec.Reconcile(ctx, 0, nil); err != nil || st != (ReconcileStats{}) || len(r.reqs) != 2 {
		t.Errorf("third run %+v %v, %d calls", st, err, len(r.reqs))
	}
}

func TestReconcileStopsOnLimit(t *testing.T) {
	conn := openDB(t)
	insertMeeting(t, conn, testMeeting{id: "m1", title: "S1", folder: "s", recurring: true, started: "2025-01-10T10:00:00-03:00", distilled: true})
	insertMeeting(t, conn, testMeeting{id: "m2", title: "S2", folder: "s", recurring: true, started: "2025-02-10T10:00:00-03:00", distilled: true})
	insertAction(t, conn, "m1", "Ana vai migrar o Atlas.", "Ana")
	r := &fakeRunner{reply: func(int, string) ([]byte, error) { return nil, &llm.LimitError{Message: "limit"} }}
	st, err := NewReconciler(conn, r).Reconcile(context.Background(), 0, nil)
	if _, ok := llm.IsLimit(err); !ok {
		t.Fatalf("want limit error, got %v", err)
	}
	if st.Pending != 1 || st.Checked != 1 {
		t.Errorf("stats %+v", st)
	}
	var checked string
	conn.QueryRow(`SELECT actions_checked_hash FROM meetings WHERE id = 'm2'`).Scan(&checked) //nolint:errcheck
	if checked != "" {
		t.Error("m2 must stay unchecked")
	}
}
