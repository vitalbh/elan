package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/vitalbh/elan/internal/db"
)

// jsonEnv sets up a profile with a database holding one meeting and a few
// actions, and captures stdout. Returns the ids of the actions.
func jsonEnv(t *testing.T) map[string]int64 {
	t.Helper()
	root := t.TempDir()
	t.Setenv("ELAN_HOME", root)
	dir := filepath.Join(root, "work")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	cfg := `{"owner": {"names": ["Vital"]}, "projects": {"Vega": {"path": "/src/vega", "aliases": ["vega.example.com"]}}}`
	if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	conn, err := db.Open(filepath.Join(dir, "elan.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	exec := func(q string, args ...any) int64 {
		res, err := conn.Exec(q, args...)
		if err != nil {
			t.Fatal(err)
		}
		id, _ := res.LastInsertId()
		return id
	}
	exec(`INSERT INTO meetings (id, title, folder_id, folder_name, started_at, source_modified, content_hash, distilled_hash, ingested_at)
		VALUES ('m1', 'Daily Vega', 'f', 'Daily Vega (recurring)', NULL, 'x', 'h', 'h', 'now')`)
	exec(`UPDATE meetings SET started_at = strftime('%Y-%m-%dT%H:%M:%SZ', 'now', '-2 days')`)
	exec(`INSERT INTO meeting_items (meeting_id, kind, text, created_at) VALUES ('m1', 'summary', 'Resumo da daily.', 'now')`)
	ids := map[string]int64{
		"code": exec(`INSERT INTO meeting_items (meeting_id, kind, text, people, category, project, created_at)
			VALUES ('m1', 'action', 'Vital vai corrigir o player do Vega.', '["Vital"]', 'code', 'vega.example.com', 'now')`),
		"unmapped": exec(`INSERT INTO meeting_items (meeting_id, kind, text, people, category, project, created_at)
			VALUES ('m1', 'action', 'Vital vai ajustar o Editor Pro.', '["Vital"]', 'code', 'Editor Pro', 'now')`),
		"other": exec(`INSERT INTO meeting_items (meeting_id, kind, text, people, category, created_at)
			VALUES ('m1', 'action', 'Vital vai ligar para a Ana.', '["Vital"]', 'other', 'now')`),
	}
	return ids
}

func capture(t *testing.T, fn func() int) (string, int) {
	t.Helper()
	var buf bytes.Buffer
	old := stdout
	stdout = &buf
	defer func() { stdout = old }()
	code := fn()
	return buf.String(), code
}

func keysOf(t *testing.T, raw json.RawMessage) string {
	t.Helper()
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("%s: %v", raw, err)
	}
	var keys []string
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return strings.Join(keys, ",")
}

func decodeArray(t *testing.T, out string) []json.RawMessage {
	t.Helper()
	var arr []json.RawMessage
	if err := json.Unmarshal([]byte(out), &arr); err != nil {
		t.Fatalf("not a JSON array: %q: %v", out, err)
	}
	return arr
}

func wantError(t *testing.T, out string, code int, contains string) {
	t.Helper()
	var e map[string]string
	if err := json.Unmarshal([]byte(out), &e); err != nil || code != 1 || !strings.Contains(e["error"], contains) || len(e) != 1 {
		t.Errorf("want {\"error\": ...%s...} and exit 1, got %q (exit %d)", contains, out, code)
	}
}

const codeTaskKeys = "age_days,branch,context,created_at,folder,id,meeting_at,meeting_id,meeting_title,project,source_item_id,started_at,status,text"

func TestCodetasksJSON(t *testing.T) {
	ids := jsonEnv(t)

	// No code task yet: the classified actions are only suggestions.
	out, code := capture(t, func() int { return codetasksMain([]string{"--json"}) })
	if code != 0 || strings.TrimSpace(out) != "[]" {
		t.Fatalf("empty list: %q exit %d", out, code)
	}
	out, code = capture(t, func() int { return codetasksSuggestionsMain([]string{"--json", "--n=10"}) })
	if code != 0 {
		t.Fatalf("suggestions exit %d: %s", code, out)
	}
	arr := decodeArray(t, out)
	if len(arr) != 2 {
		t.Fatalf("want 2 suggestions, got %s", out)
	}
	if got := keysOf(t, arr[0]); got != "age_days,folder,item_id,meeting_at,meeting_id,meeting_title,people,project,text" {
		t.Errorf("suggestion keys %s", got)
	}

	// promote: the action's text, project and meeting.
	out, code = capture(t, func() int { return codetasksPromoteMain([]string{fmt.Sprint(ids["code"]), "--json"}) })
	if code != 0 {
		t.Fatalf("promote exit %d: %s", code, out)
	}
	if got := keysOf(t, json.RawMessage(out)); got != codeTaskKeys {
		t.Errorf("promote keys %s", got)
	}
	var promoted struct {
		ID           int64   `json:"id"`
		Folder       string  `json:"folder"`
		Status       string  `json:"status"`
		MeetingID    string  `json:"meeting_id"`
		SourceItemID *int64  `json:"source_item_id"`
		StartedAt    *string `json:"started_at"`
		CreatedAt    string  `json:"created_at"`
	}
	_ = json.Unmarshal([]byte(out), &promoted)
	if promoted.Folder != "/src/vega" || promoted.Status != "open" || promoted.MeetingID != "m1" ||
		promoted.SourceItemID == nil || *promoted.SourceItemID != ids["code"] || promoted.StartedAt != nil || promoted.CreatedAt == "" {
		t.Errorf("promoted %s", out)
	}
	out, _ = capture(t, func() int { return codetasksSuggestionsMain([]string{"--json"}) })
	if len(decodeArray(t, out)) != 1 {
		t.Errorf("promoted action still suggested: %s", out)
	}
	out, code = capture(t, func() int { return codetasksPromoteMain([]string{"999", "--json"}) })
	wantError(t, out, code, "não encontrada")

	// add, with flags before and after the text.
	out, code = capture(t, func() int {
		return codetasksAddMain([]string{"--project=Editor Pro", "Ajustar", "o build", "--context=sem pressa", "--json"})
	})
	if code != 0 {
		t.Fatalf("add exit %d: %s", code, out)
	}
	if got := keysOf(t, json.RawMessage(out)); got != codeTaskKeys {
		t.Errorf("add keys %s", got)
	}
	var added struct {
		ID           int64   `json:"id"`
		Text         string  `json:"text"`
		Folder       string  `json:"folder"`
		Context      string  `json:"context"`
		MeetingAt    *string `json:"meeting_at"`
		SourceItemID *int64  `json:"source_item_id"`
	}
	_ = json.Unmarshal([]byte(out), &added)
	if added.Text != "Ajustar o build" || added.Folder != "" || added.Context != "sem pressa" || added.MeetingAt != nil || added.SourceItemID != nil {
		t.Errorf("added %s", out)
	}
	out, code = capture(t, func() int { return codetasksAddMain([]string{"--json"}) })
	wantError(t, out, code, "uso")
	out, code = capture(t, func() int { return codetasksAddMain([]string{"x", "--meeting=nope", "--json"}) })
	wantError(t, out, code, "reunião não encontrada")

	// start
	out, code = capture(t, func() int { return codetasksStartMain([]string{fmt.Sprint(promoted.ID), "--json"}) })
	if code != 0 {
		t.Fatalf("start exit %d: %s", code, out)
	}
	if got := keysOf(t, json.RawMessage(out)); got != "branch,folder,id,project,prompt" {
		t.Errorf("start keys %s", got)
	}
	var st struct{ Branch, Prompt string }
	_ = json.Unmarshal([]byte(out), &st)
	if st.Branch != "elan/corrigir-o-player-do-vega" || !strings.Contains(st.Prompt, "Daily Vega") {
		t.Errorf("start %s", out)
	}
	out, code = capture(t, func() int { return codetasksStartMain([]string{"--json", fmt.Sprint(added.ID)}) })
	wantError(t, out, code, "Editor Pro")

	// list: open first, newest first; the started one carries its branch.
	out, _ = capture(t, func() int { return codetasksMain([]string{"--json"}) })
	arr = decodeArray(t, out)
	if len(arr) != 2 || keysOf(t, arr[0]) != codeTaskKeys {
		t.Fatalf("list %s", out)
	}
	if !strings.Contains(out, `"branch":"elan/corrigir-o-player-do-vega"`) || strings.Count(out, `"started_at":null`) != 1 {
		t.Errorf("after start: %s", out)
	}

	// update closes the task and its action.
	out, code = capture(t, func() int {
		return codetasksUpdateMain([]string{fmt.Sprint(promoted.ID), "--status=done", "--json"})
	})
	if code != 0 || strings.TrimSpace(out) != fmt.Sprintf(`{"id":%d,"status":"done"}`, promoted.ID) {
		t.Errorf("update: %q exit %d", out, code)
	}
	out, _ = capture(t, func() int { return actionsPendingMain([]string{"--json", "--owner=mine"}) })
	if strings.Contains(out, fmt.Sprintf(`"id":%d,`, ids["code"])) {
		t.Errorf("source action still open: %s", out)
	}
	out, _ = capture(t, func() int { return codetasksMain([]string{"--json"}) })
	if len(decodeArray(t, out)) != 1 {
		t.Errorf("done task listed without --done: %s", out)
	}
	out, _ = capture(t, func() int { return codetasksMain([]string{"--json", "--done"}) })
	if arr := decodeArray(t, out); len(arr) != 2 || !strings.Contains(string(arr[1]), `"status":"done"`) {
		t.Errorf("--done: %s", out)
	}
	out, code = capture(t, func() int { return codetasksUpdateMain([]string{fmt.Sprint(promoted.ID), "--status=x", "--json"}) })
	wantError(t, out, code, "status inválido")
	out, code = capture(t, func() int { return codetasksUpdateMain([]string{"999", "--status=done", "--json"}) })
	wantError(t, out, code, "tarefa de código não encontrada")
}

func TestActionsJSON(t *testing.T) {
	ids := jsonEnv(t)
	out, code := capture(t, func() int { return actionsPendingMain([]string{"--json", "--owner=mine", "--n=10"}) })
	if code != 0 {
		t.Fatalf("exit %d: %s", code, out)
	}
	arr := decodeArray(t, out)
	if len(arr) != 3 {
		t.Fatalf("want 3 actions: %s", out)
	}
	for _, k := range []string{"id", "text", "people", "meeting_id", "meeting_title", "meeting_at", "age_days", "stale", "mine"} {
		if !strings.Contains(keysOf(t, arr[0]), k) {
			t.Errorf("pending without %q: %s", k, arr[0])
		}
	}
	out, code = capture(t, func() int { return actionsPendingMain([]string{"--json", "--owner=bogus"}) })
	wantError(t, out, code, "owner inválido")

	out, code = capture(t, func() int {
		return actionUpdateMain([]string{fmt.Sprint(ids["other"]), "--status=done", "--json"})
	})
	if code != 0 || strings.TrimSpace(out) != fmt.Sprintf(`{"id":%d,"status":"done"}`, ids["other"]) {
		t.Errorf("update: %q exit %d", out, code)
	}
	out, code = capture(t, func() int { return actionUpdateMain([]string{"999", "--status=done", "--json"}) })
	wantError(t, out, code, "não encontrada")
	out, code = capture(t, func() int { return actionUpdateMain([]string{fmt.Sprint(ids["other"]), "--status=x", "--json"}) })
	wantError(t, out, code, "status inválido")
}

func TestMeetingAndSearchJSON(t *testing.T) {
	jsonEnv(t)
	out, code := capture(t, func() int { return meetingMain([]string{"m1", "--json"}) })
	if code != 0 {
		t.Fatalf("exit %d: %s", code, out)
	}
	for _, k := range []string{"id", "title", "started_at", "folder", "items"} {
		if !strings.Contains(keysOf(t, json.RawMessage(out)), k) {
			t.Errorf("meeting without %q: %s", k, out)
		}
	}
	out, code = capture(t, func() int { return meetingMain([]string{"nope", "--json"}) })
	wantError(t, out, code, "reunião não encontrada")

	// No index yet: the error comes as JSON.
	out, code = capture(t, func() int { return searchCmd("search", []string{"player", "--json"}) })
	wantError(t, out, code, "índice")
}

func TestBriefingJSON(t *testing.T) {
	jsonEnv(t)
	out, code := capture(t, func() int { return briefingMain([]string{"--json"}) })
	if code != 0 {
		t.Fatalf("exit %d: %s", code, out)
	}
	keys := keysOf(t, json.RawMessage(out))
	for _, k := range []string{"day", "generated", "events", "mine", "waiting", "decisions", "mine_total", "waiting_total", "code_tasks"} {
		if !strings.Contains(","+keys+",", ","+k+",") {
			t.Errorf("briefing without %q: %s", k, keys)
		}
	}
	var b struct {
		Events    []any             `json:"events"`
		CodeTasks []json.RawMessage `json:"code_tasks"`
	}
	_ = json.Unmarshal([]byte(out), &b)
	// The classified actions are suggestions, not code tasks.
	if b.Events == nil || b.CodeTasks == nil || len(b.CodeTasks) != 0 {
		t.Errorf("briefing lists: %s", out)
	}
	capture(t, func() int { return codetasksAddMain([]string{"Trocar o logger", "--json"}) })
	out, _ = capture(t, func() int { return briefingMain([]string{"--json"}) })
	b.CodeTasks = nil
	_ = json.Unmarshal([]byte(out), &b)
	if len(b.CodeTasks) != 1 || keysOf(t, b.CodeTasks[0]) != codeTaskKeys {
		t.Errorf("briefing code_tasks: %s", out)
	}
	// --json does not save the page: the daemon decides by its existence.
	if _, err := os.Stat(filepath.Join(os.Getenv("ELAN_HOME"), "work", "briefing")); !os.IsNotExist(err) {
		t.Error("briefing --json saved the page")
	}
}

func TestNotesJSON(t *testing.T) {
	jsonEnv(t)
	out, code := capture(t, func() int { return notesMain([]string{"--json"}) })
	if code != 0 || strings.TrimSpace(out) != "[]" {
		t.Fatalf("empty: %q exit %d", out, code)
	}
	for _, text := range []string{"primeira", "segunda"} {
		if c := noteMain([]string{text}); c != 0 {
			t.Fatalf("note %q: exit %d", text, c)
		}
	}
	out, code = capture(t, func() int { return notesMain([]string{"--json", "--n=1"}) })
	arr := decodeArray(t, out)
	if code != 0 || len(arr) != 1 {
		t.Fatalf("want 1 note: %s", out)
	}
	for _, k := range []string{"id", "text", "source", "created_at"} {
		if !strings.Contains(keysOf(t, arr[0]), k) {
			t.Errorf("note without %q: %s", k, arr[0])
		}
	}
	if !strings.Contains(out, `"segunda"`) {
		t.Errorf("newest first: %s", out)
	}
}
