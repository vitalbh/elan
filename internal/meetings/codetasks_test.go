package meetings

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

func setClass(t *testing.T, conn *sql.DB, id int64, category, project string) {
	t.Helper()
	mustExec(t, conn, `UPDATE meeting_items SET category = ?, project = ? WHERE id = ?`, category, project, id)
}

func TestParseItemsClassification(t *testing.T) {
	raw := []byte(`{"items":[
		{"kind":"summary","text":"s","people":[],"importance":2,"supersedes":[],"completes":[],"drops":[],"category":"code","project":"X"},
		{"kind":"action","text":"a","people":["Ana"],"importance":2,"supersedes":[],"completes":[],"drops":[],"category":" Code ","project":"  Blue   Orion "},
		{"kind":"action","text":"b","people":[],"importance":2,"supersedes":[],"completes":[],"drops":[]}
	]}`)
	items, err := parseItems(raw)
	if err != nil {
		t.Fatal(err)
	}
	if items[0].Category != "" || items[0].Project != "" {
		t.Errorf("summary keeps no class: %+v", items[0])
	}
	if items[1].Category != CategoryCode || items[1].Project != "Blue Orion" {
		t.Errorf("action class: %+v", items[1])
	}
	if items[2].Category != "" {
		t.Errorf("unclassified action: %+v", items[2])
	}
	bad := []byte(`{"items":[{"kind":"action","text":"a","people":[],"importance":2,"supersedes":[],"completes":[],"drops":[],"category":"infra","project":""}]}`)
	if _, err := parseItems(bad); err == nil {
		t.Error("invalid category: want error")
	}
}

func TestDistillClassifiesAndCarries(t *testing.T) {
	conn := openDB(t)
	ctx := context.Background()
	insertMeeting(t, conn, testMeeting{id: "m1", title: "Reunião", folder: "f", started: "2025-01-10T10:00:00-03:00"})
	kept := insertAction(t, conn, "m1", "Ana vai migrar o Atlas.", "Ana")
	setClass(t, conn, kept, CategoryCode, "Atlas")
	mustExec(t, conn, `UPDATE meeting_items SET started_at = '2025-01-11T10:00:00Z', branch = 'elan/migrar-o-atlas' WHERE id = ?`, kept)

	r := &fakeRunner{reply: func(int, string) ([]byte, error) {
		return []byte(`{"items":[
			{"kind":"summary","text":"Resumo.","people":[],"importance":2,"supersedes":[],"completes":[],"drops":[],"category":"","project":""},
			{"kind":"action","text":"ana vai  migrar o Atlas.","people":["Ana"],"importance":2,"supersedes":[],"completes":[],"drops":[],"category":"other","project":"Outro"},
			{"kind":"action","text":"Bruno vai corrigir o deploy do Vega.","people":["Bruno"],"importance":2,"supersedes":[],"completes":[],"drops":[],"category":"code","project":"Vega"},
			{"kind":"action","text":"Carla vai ligar.","people":["Carla"],"importance":2,"supersedes":[],"completes":[],"drops":[]}
		]}`), nil
	}}
	d := NewDistiller(conn, r)
	d.KnownProjects = []Project{{Name: "Vega", Aliases: []string{"vega.example.com"}, Description: "site do Vega"}, {Name: "Atlas"}}
	if _, err := d.Distill(ctx, 0, nil); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(r.prompts[0], "## Projetos conhecidos") ||
		!strings.Contains(r.prompts[0], "- Atlas:\n- Vega: apelidos vega.example.com. site do Vega\n") {
		t.Errorf("prompt without known projects:\n%s", r.prompts[0])
	}
	if !strings.Contains(r.reqs[0].System, `category "code"`) || !strings.Contains(string(r.reqs[0].Schema), `"category"`) {
		t.Error("system prompt or schema without classification")
	}
	rows, err := conn.Query(`SELECT text, COALESCE(category, ''), project, COALESCE(started_at, ''), COALESCE(branch, '')
		FROM meeting_items WHERE kind = 'action' ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	got := map[string][4]string{}
	for rows.Next() {
		var text string
		var v [4]string
		if err := rows.Scan(&text, &v[0], &v[1], &v[2], &v[3]); err != nil {
			t.Fatal(err)
		}
		got[text] = v
	}
	want := map[string][4]string{
		"ana vai  migrar o Atlas.":               {"code", "Atlas", "2025-01-11T10:00:00Z", "elan/migrar-o-atlas"}, // carried
		"Bruno vai corrigir o deploy do Vega.": {"code", "Vega", "", ""},
		"Carla vai ligar.":                       {"", "", "", ""},
	}
	for text, w := range want {
		if got[text] != w {
			t.Errorf("%q: got %v, want %v", text, got[text], w)
		}
	}
}

func TestProjectsResolve(t *testing.T) {
	p := NewProjects([]Project{
		{Name: "Blue Orion", Folder: "/src/blueorion"},
		{Name: "Vega", Folder: "/src/vega", Aliases: []string{"vega.example.com"}},
		{Name: "Nimbus", Folder: "/src/nimbus", Aliases: []string{"Nimbos"}},
	})
	cases := map[string]string{
		"blueorion":     "/src/blueorion",
		"BLUE ORION":    "/src/blueorion",
		"Blue-Orion":    "/src/blueorion",
		"véga":        "/src/vega",
		"vega.example.com": "/src/vega",
		"nimbos":       "/src/nimbus",
		"Editor Pro":    "",
		"":              "",
	}
	for in, want := range cases {
		if got := p.folder(in); got != want {
			t.Errorf("%q: got %q, want %q", in, got, want)
		}
	}
	if pr, ok := p.Resolve("vega.example.com"); !ok || pr.Name != "Vega" {
		t.Errorf("alias resolves to its project: %+v %v", pr, ok)
	}
	var names []string
	for _, pr := range p.List() {
		names = append(names, pr.Name)
	}
	if strings.Join(names, ",") != "Blue Orion,Nimbus,Vega" {
		t.Errorf("names %v", names)
	}
	var nilP *Projects
	if _, ok := nilP.Resolve("x"); ok || nilP.List() != nil {
		t.Error("nil Projects resolves nothing")
	}
}

func codeTasksFixture(t *testing.T) (*sql.DB, *CodeTasks, map[string]int64) {
	t.Helper()
	conn := openDB(t)
	insertMeeting(t, conn, testMeeting{id: "m1", title: "Planejamento", folder: "f", started: "2025-03-01T10:00:00-03:00"})
	insertMeeting(t, conn, testMeeting{id: "m2", title: "Antiga", folder: "g", started: "2024-10-01T10:00:00-03:00"})
	ids := map[string]int64{
		"mine":     insertAction(t, conn, "m1", "Vital vai corrigir o deploy do Vega.", "Vital"),
		"other":    insertAction(t, conn, "m1", "Ana vai migrar o Atlas.", "Ana"),
		"notcode":  insertAction(t, conn, "m1", "Vital vai ligar para o cliente.", "Vital"),
		"done":     insertAction(t, conn, "m1", "Vital vai subir o hotfix.", "Vital"),
		"unmapped": insertAction(t, conn, "m1", "Vital vai ajustar o Editor Pro.", "Vital"),
		"stale":    insertAction(t, conn, "m2", "Vital vai refatorar o login.", "Vital"),
	}
	setClass(t, conn, ids["mine"], CategoryCode, "vega")
	setClass(t, conn, ids["other"], CategoryCode, "Atlas")
	setClass(t, conn, ids["notcode"], CategoryOther, "")
	setClass(t, conn, ids["done"], CategoryCode, "Vega")
	setClass(t, conn, ids["unmapped"], CategoryCode, "Editor Pro")
	setClass(t, conn, ids["stale"], CategoryCode, "Vega")
	mustExec(t, conn, `UPDATE meeting_items SET status = 'done' WHERE id = ?`, ids["done"])
	mustExec(t, conn, `INSERT INTO meeting_items (meeting_id, kind, text, created_at) VALUES ('m1', 'summary', 'Planejamento do trimestre do Vega.', 'now')`)
	mustExec(t, conn, `INSERT INTO meeting_items (meeting_id, kind, text, importance, created_at) VALUES ('m1', 'decision', 'O deploy do Vega passa a ser semanal.', 3, 'now')`)
	old := insertItem(t, conn, "m1", "decision", "Decisão substituída.")
	mustExec(t, conn, `UPDATE meeting_items SET superseded_by = ? WHERE id = ?`, ids["other"], old)

	c := NewCodeTasks(conn, []string{"Vital"}, NewProjects([]Project{
		{Name: "Vega", Folder: "/src/vega"},
		{Name: "Atlas", Folder: "/src/atlas"},
	}))
	now := time.Date(2025, 3, 11, 12, 0, 0, 0, time.UTC)
	c.actions.now = func() time.Time { return now }
	return conn, c, ids
}

func taskIDs(list []CodeTask) []int64 {
	out := make([]int64, len(list))
	for i, t := range list {
		out[i] = t.ID
	}
	return out
}

func suggestionIDs(list []Suggestion) []int64 {
	out := make([]int64, len(list))
	for i, s := range list {
		out[i] = s.ItemID
	}
	return out
}

func TestCodeTasksAddAndPromote(t *testing.T) {
	_, c, ids := codeTasksFixture(t)
	ctx := context.Background()

	added, err := c.Add(ctx, NewCodeTask{Text: "  Trocar o  logger do Atlas ", Project: "atlas", Context: "Use slog.\nSem dependência nova."})
	if err != nil {
		t.Fatal(err)
	}
	if added.Text != "Trocar o logger do Atlas" || added.Project != "Atlas" || added.Folder != "/src/atlas" ||
		added.Context != "Use slog.\nSem dependência nova." || added.Status != StatusOpen || added.MeetingID != "" ||
		added.MeetingAt != nil || added.SourceItemID != nil || added.StartedAt != nil || added.Branch != "" ||
		added.CreatedAt.IsZero() || added.AgeDays != 0 {
		t.Errorf("added %+v", added)
	}
	free, err := c.Add(ctx, NewCodeTask{Text: "Ajustar o build", Project: "Editor Pro", MeetingID: "m1"})
	if err != nil {
		t.Fatal(err)
	}
	if free.Project != "Editor Pro" || free.Folder != "" || free.MeetingID != "m1" || free.MeetingTitle != "Planejamento" || free.MeetingAt == nil {
		t.Errorf("free project with meeting: %+v", free)
	}
	if _, err := c.Add(ctx, NewCodeTask{Text: "  "}); err == nil {
		t.Error("empty text: want error")
	}
	if _, err := c.Add(ctx, NewCodeTask{Text: "x", MeetingID: "nope"}); !errors.Is(err, ErrNotFound) {
		t.Errorf("missing meeting: %v", err)
	}

	p, err := c.Promote(ctx, ids["mine"])
	if err != nil {
		t.Fatal(err)
	}
	if p.Text != "Vital vai corrigir o deploy do Vega." || p.Project != "Vega" || p.Folder != "/src/vega" ||
		p.MeetingID != "m1" || p.MeetingTitle != "Planejamento" || p.MeetingAt == nil || p.Context != "" ||
		p.SourceItemID == nil || *p.SourceItemID != ids["mine"] || p.Status != StatusOpen {
		t.Errorf("promoted %+v", p)
	}
	again, err := c.Promote(ctx, ids["mine"])
	if err != nil || again.ID != p.ID {
		t.Errorf("promote twice: %+v %v", again, err)
	}
	if _, err := c.Promote(ctx, 9999); !errors.Is(err, ErrActionNotFound) {
		t.Errorf("missing action: %v", err)
	}
	var summary int64
	_ = c.db.QueryRow(`SELECT id FROM meeting_items WHERE kind = 'summary'`).Scan(&summary)
	if _, err := c.Promote(ctx, summary); !errors.Is(err, ErrActionNotFound) {
		t.Errorf("promote a summary: %v", err)
	}
}

func TestCodeTasksListAndSuggestions(t *testing.T) {
	_, c, ids := codeTasksFixture(t)
	ctx := context.Background()

	sugg, err := c.Suggestions(ctx, SuggestionQuery{})
	if err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(suggestionIDs(sugg)) != fmt.Sprint([]int64{ids["mine"], ids["unmapped"]}) {
		t.Fatalf("suggestions: %+v", sugg)
	}
	s := sugg[0]
	if s.Folder != "/src/vega" || s.Project != "vega" || s.MeetingTitle != "Planejamento" || s.MeetingID != "m1" ||
		s.AgeDays != 9 || s.MeetingAt == nil || len(s.People) != 1 {
		t.Errorf("suggestion %+v", s)
	}
	stale, _ := c.Suggestions(ctx, SuggestionQuery{IncludeStale: true})
	if len(stale) != 3 {
		t.Errorf("with stale: %v", suggestionIDs(stale))
	}
	if _, err := NewCodeTasks(c.db, nil, nil).Suggestions(ctx, SuggestionQuery{}); err == nil {
		t.Error("suggestions without owner names: want error")
	}

	// No code task yet: the suggestions are not tasks.
	if list, err := c.List(ctx, CodeTaskQuery{}); err != nil || len(list) != 0 {
		t.Fatalf("empty list: %+v %v", list, err)
	}

	// A promoted action leaves the suggestions.
	p, _ := c.Promote(ctx, ids["mine"])
	c.actions.now = func() time.Time { return time.Date(2025, 3, 12, 12, 0, 0, 0, time.UTC) }
	a, _ := c.Add(ctx, NewCodeTask{Text: "Nova tarefa"})
	sugg, _ = c.Suggestions(ctx, SuggestionQuery{})
	if fmt.Sprint(suggestionIDs(sugg)) != fmt.Sprint([]int64{ids["unmapped"]}) {
		t.Errorf("promoted still suggested: %v", suggestionIDs(sugg))
	}

	list, err := c.List(ctx, CodeTaskQuery{})
	if err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(taskIDs(list)) != fmt.Sprint([]int64{a.ID, p.ID}) || list[1].AgeDays != 1 {
		t.Errorf("newest first: %+v", list)
	}

	// Done tasks only with IncludeDone, after the open ones.
	if _, err := c.SetStatus(ctx, a.ID, StatusDone); err != nil {
		t.Fatal(err)
	}
	list, _ = c.List(ctx, CodeTaskQuery{})
	if fmt.Sprint(taskIDs(list)) != fmt.Sprint([]int64{p.ID}) {
		t.Errorf("open only: %v", taskIDs(list))
	}
	list, _ = c.List(ctx, CodeTaskQuery{IncludeDone: true})
	if fmt.Sprint(taskIDs(list)) != fmt.Sprint([]int64{p.ID, a.ID}) {
		t.Errorf("open first: %v", taskIDs(list))
	}
	list, _ = c.List(ctx, CodeTaskQuery{Status: StatusDone})
	if fmt.Sprint(taskIDs(list)) != fmt.Sprint([]int64{a.ID}) {
		t.Errorf("done: %v", taskIDs(list))
	}
	if _, err := c.List(ctx, CodeTaskQuery{Status: "x"}); err == nil {
		t.Error("bad status: want error")
	}
}

func TestCodeTasksSetStatus(t *testing.T) {
	conn, c, ids := codeTasksFixture(t)
	ctx := context.Background()
	actionStatus := func() (status, source string) {
		t.Helper()
		var src sql.NullString
		if err := conn.QueryRow(`SELECT status, status_source FROM meeting_items WHERE id = ?`, ids["mine"]).Scan(&status, &src); err != nil {
			t.Fatal(err)
		}
		return status, src.String
	}

	p, _ := c.Promote(ctx, ids["mine"])
	for _, status := range []string{StatusDone, StatusOpen, StatusDropped} {
		got, err := c.SetStatus(ctx, p.ID, status)
		if err != nil || got.Status != status {
			t.Fatalf("%s: %+v %v", status, got, err)
		}
		if st, src := actionStatus(); st != status || src != SourceManual {
			t.Errorf("%s: source action %s/%s", status, st, src)
		}
	}

	// Without a source action, only the task changes.
	a, _ := c.Add(ctx, NewCodeTask{Text: "Solta", MeetingID: "m1"})
	if got, err := c.SetStatus(ctx, a.ID, StatusDone); err != nil || got.Status != StatusDone {
		t.Fatalf("no source: %+v %v", got, err)
	}
	var manual int
	_ = conn.QueryRow(`SELECT COUNT(*) FROM meeting_items WHERE status_source = 'manual' AND id <> ?`, ids["mine"]).Scan(&manual)
	if manual != 0 {
		t.Error("a task without source changed an action")
	}

	if _, err := c.SetStatus(ctx, a.ID, "x"); err == nil {
		t.Error("bad status: want error")
	}
	if _, err := c.SetStatus(ctx, 9999, StatusDone); !errors.Is(err, ErrCodeTaskNotFound) {
		t.Errorf("missing task: %v", err)
	}
}

func TestCodeTasksStart(t *testing.T) {
	conn, c, ids := codeTasksFixture(t)
	ctx := context.Background()

	p, _ := c.Promote(ctx, ids["mine"])
	mustExec(t, conn, `UPDATE code_tasks SET context = 'Comece pelo pipeline.' WHERE id = ?`, p.ID)
	st, err := c.Start(ctx, p.ID)
	if err != nil {
		t.Fatal(err)
	}
	if st.Branch != "elan/corrigir-o-deploy-do-vega" || st.Folder != "/src/vega" || st.Project != "Vega" || st.ID != p.ID {
		t.Errorf("start %+v", st)
	}
	for _, want := range []string{
		"Vital vai corrigir o deploy do Vega.", "Instruções do dono:\nComece pelo pipeline.", "Planejamento", "01/03/2025",
		"Planejamento do trimestre do Vega.", "O deploy do Vega passa a ser semanal.",
		"Pessoas envolvidas: Vital", "search", "get_meeting (esta reunião: m1)", "codetasks_list", "codetask_update",
		fmt.Sprintf("id %d", p.ID), "Não feche a tarefa",
	} {
		if !strings.Contains(st.Prompt, want) {
			t.Errorf("prompt without %q:\n%s", want, st.Prompt)
		}
	}
	if strings.Contains(st.Prompt, "Decisão substituída") {
		t.Error("prompt has a superseded decision")
	}
	got, _ := c.Get(ctx, p.ID)
	if got.StartedAt == nil || got.StartedAt.Format(time.RFC3339) != "2025-03-11T12:00:00Z" || got.Branch != st.Branch {
		t.Errorf("stored %+v", got)
	}
	var legacy sql.NullString
	_ = conn.QueryRow(`SELECT branch FROM meeting_items WHERE id = ?`, ids["mine"]).Scan(&legacy)
	if legacy.Valid {
		t.Error("start wrote meeting_items.branch")
	}

	// Starting again keeps the branch and started_at.
	c.actions.now = func() time.Time { return time.Date(2025, 4, 1, 0, 0, 0, 0, time.UTC) }
	again, err := c.Start(ctx, p.ID)
	if err != nil || again.Branch != st.Branch {
		t.Fatalf("restart: %+v %v", again, err)
	}
	if got, _ := c.Get(ctx, p.ID); got.StartedAt.Format(time.RFC3339) != "2025-03-11T12:00:00Z" {
		t.Errorf("restart changed started_at: %v", got.StartedAt)
	}

	// Same slug among code tasks: the id makes it unique. The owner's name
	// is stripped even without a source action.
	dup, _ := c.Add(ctx, NewCodeTask{Text: "Vital vai corrigir o deploy do Vega!", Project: "Vega"})
	st2, err := c.Start(ctx, dup.ID)
	if err != nil {
		t.Fatal(err)
	}
	if st2.Branch != fmt.Sprintf("elan/corrigir-o-deploy-do-vega-%d", dup.ID) {
		t.Errorf("unique branch: %s", st2.Branch)
	}
	// Without a meeting: no meeting section, no get_meeting.
	if strings.Contains(st2.Prompt, "Reunião de origem") || strings.Contains(st2.Prompt, "get_meeting") ||
		strings.Contains(st2.Prompt, "Instruções do dono") || !strings.Contains(st2.Prompt, "codetask_update") {
		t.Errorf("prompt without meeting:\n%s", st2.Prompt)
	}

	// Unmapped project or no project: ErrNoFolder, nothing changes.
	unmapped, _ := c.Promote(ctx, ids["unmapped"])
	_, err = c.Start(ctx, unmapped.ID)
	if !errors.Is(err, ErrNoFolder) || !strings.Contains(err.Error(), "Editor Pro") {
		t.Errorf("unmapped: %v", err)
	}
	none, _ := c.Add(ctx, NewCodeTask{Text: "Sem projeto"})
	if _, err = c.Start(ctx, none.ID); !errors.Is(err, ErrNoFolder) {
		t.Errorf("no project: %v", err)
	}
	var n int
	_ = conn.QueryRow(`SELECT COUNT(*) FROM code_tasks WHERE id IN (?, ?) AND (started_at IS NOT NULL OR branch IS NOT NULL)`,
		unmapped.ID, none.ID).Scan(&n)
	if n != 0 {
		t.Error("failed start marked the task started")
	}
	if _, err := c.Start(ctx, 9999); !errors.Is(err, ErrCodeTaskNotFound) {
		t.Errorf("missing id: %v", err)
	}
}

func TestRedistillRelinksCodeTasks(t *testing.T) {
	conn := openDB(t)
	ctx := context.Background()
	insertMeeting(t, conn, testMeeting{id: "m1", title: "Reunião", folder: "f", started: "2025-01-10T10:00:00-03:00"})
	same := insertAction(t, conn, "m1", "Ana vai migrar o Atlas.", "Ana")
	changed := insertAction(t, conn, "m1", "Ana vai trocar o logger.", "Ana")
	// A later item elsewhere, so the new items get new ids.
	insertMeeting(t, conn, testMeeting{id: "m2", title: "Outra", folder: "g", started: "2025-01-11T10:00:00-03:00", distilled: true})
	insertItem(t, conn, "m2", "fact", "Fato.")
	c := NewCodeTasks(conn, []string{"Ana"}, nil)
	kept, err := c.Promote(ctx, same)
	if err != nil {
		t.Fatal(err)
	}
	lost, err := c.Promote(ctx, changed)
	if err != nil {
		t.Fatal(err)
	}

	r := &fakeRunner{reply: func(int, string) ([]byte, error) {
		return []byte(`{"items":[
			{"kind":"action","text":"ana vai  migrar o Atlas.","people":["Ana"],"importance":2,"supersedes":[],"completes":[],"drops":[]},
			{"kind":"action","text":"Ana vai trocar o logger por slog.","people":["Ana"],"importance":2,"supersedes":[],"completes":[],"drops":[]}
		]}`), nil
	}}
	if _, err := NewDistiller(conn, r).Distill(ctx, 0, nil); err != nil {
		t.Fatal(err)
	}
	var newID int64
	if err := conn.QueryRow(`SELECT id FROM meeting_items WHERE text = 'ana vai  migrar o Atlas.'`).Scan(&newID); err != nil {
		t.Fatal(err)
	}
	k, _ := c.Get(ctx, kept.ID)
	if k.SourceItemID == nil || *k.SourceItemID != newID || newID == same {
		t.Errorf("same text: re-linked to %v, new item %d", k.SourceItemID, newID)
	}
	l, _ := c.Get(ctx, lost.ID)
	if l.SourceItemID != nil || l.Text != "Ana vai trocar o logger." || l.MeetingID != "m1" {
		t.Errorf("changed text: %+v", l)
	}
	// The re-linked task is still not suggested, and closing it closes the new item.
	mustExec(t, conn, `UPDATE meeting_items SET category = 'code'`)
	sugg, _ := c.Suggestions(ctx, SuggestionQuery{IncludeStale: true})
	if len(sugg) != 1 || sugg[0].Text != "Ana vai trocar o logger por slog." {
		t.Errorf("suggestions after re-distill: %+v", sugg)
	}
	if _, err := c.SetStatus(ctx, kept.ID, StatusDone); err != nil {
		t.Fatal(err)
	}
	var status string
	_ = conn.QueryRow(`SELECT status FROM meeting_items WHERE id = ?`, newID).Scan(&status)
	if status != StatusDone {
		t.Errorf("new item status %s", status)
	}
}

func TestBranchSlug(t *testing.T) {
	cases := []struct {
		text   string
		people []string
		want   string
	}{
		{"Vital vai corrigir o deploy do Vega.", []string{"Vital"}, "corrigir-o-deploy-do-vega"},
		{"Carlos Lima vai migrar a API de pagamentos (Boleto) para a v2.", []string{"Carlos Lima", "Ana"}, "migrar-a-api-de-pagamentos-boleto-para-a"},
		{"Ana vai configurar a integração contínua do repositório de autenticação", []string{"Ana"}, "configurar-a-integracao-continua-do"},
		{"Atualizar ação crítica", nil, "atualizar-acao-critica"},
		{"Beatriz Costa e Fernanda Dias vão adaptar o editor de vídeo.", []string{"Beatriz Costa", "Fernanda Dias"}, "adaptar-o-editor-de-video"},
		{"!!!", nil, "tarefa"},
		{strings.Repeat("a", 60), nil, strings.Repeat("a", 40)},
	}
	for _, c := range cases {
		got := branchSlug(c.text, c.people)
		if got != c.want {
			t.Errorf("%q: got %q, want %q", c.text, got, c.want)
		}
		if len(got) > maxSlug {
			t.Errorf("%q: slug longer than %d", got, maxSlug)
		}
	}
}

func TestStartPromptFits(t *testing.T) {
	var decisions []string
	for i := 0; i < 10; i++ {
		decisions = append(decisions, strings.Repeat("decisão longa ", 30))
	}
	p := startPrompt(startInfo{ID: 1, Text: "Tarefa", Project: "P", Folder: "/p", Branch: "elan/t",
		MeetingID: "m", MeetingTitle: "R", Summary: strings.Repeat("resumo ", 300), Decisions: decisions})
	if n := utf8.RuneCountInString(p); n > maxPromptChars {
		t.Errorf("prompt has %d chars", n)
	}
	if !strings.Contains(p, "Não feche a tarefa") || !strings.Contains(p, "Tarefa") {
		t.Errorf("prompt lost its essentials:\n%s", p)
	}
}

func TestMarkdown(t *testing.T) {
	conn, _, ids := codeTasksFixture(t)
	m, err := NewReader(conn).Get(context.Background(), "m1")
	if err != nil {
		t.Fatal(err)
	}
	md := Markdown(m)
	for _, want := range []string{
		"# Planejamento", "01/03/2025", "## Resumo", "Planejamento do trimestre do Vega.",
		"## Decisões", "O deploy do Vega passa a ser semanal.", "## Ações",
		"- [ ] Vital vai corrigir o deploy do Vega.", "- [x] Vital vai subir o hotfix.",
		"https://docs.google.com/document/d/m1/edit",
	} {
		if !strings.Contains(md, want) {
			t.Errorf("markdown without %q:\n%s", want, md)
		}
	}
	if strings.Contains(md, "Decisão substituída") {
		t.Error("markdown has a superseded decision")
	}
	_ = ids
}
