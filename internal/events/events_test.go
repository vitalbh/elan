package events

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func readEvents(t *testing.T, path string) []Event {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var out []Event
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		var e Event
		if err := json.Unmarshal(sc.Bytes(), &e); err != nil {
			t.Fatalf("line %q: %v", sc.Text(), err)
		}
		out = append(out, e)
	}
	return out
}

func TestAppendAndTrim(t *testing.T) {
	path := Path(t.TempDir())
	if err := Append(path, Event{Kind: KindReminder, Title: "Em 10 min: X", Body: "b", MeetingID: "m1", URL: "https://meet"}); err != nil {
		t.Fatal(err)
	}
	evs := readEvents(t, path)
	if len(evs) != 1 || evs[0].ID == "" || evs[0].At.IsZero() || evs[0].MeetingID != "m1" || evs[0].Kind != KindReminder {
		t.Fatalf("events %+v", evs)
	}
	var raw map[string]any
	b, _ := os.ReadFile(path)
	_ = json.Unmarshal(b, &raw)
	for _, k := range []string{"id", "at", "kind", "title", "body", "meeting_id", "url"} {
		if _, ok := raw[k]; !ok {
			t.Errorf("missing key %q in %s", k, b)
		}
	}
	if err := Append(path, Event{Kind: KindTasks, Title: "t", Body: "b"}); err != nil {
		t.Fatal(err)
	}
	lines := readEvents(t, path)
	if len(lines) != 2 || lines[0].ID == lines[1].ID {
		t.Errorf("ids not unique: %+v", lines)
	}

	for i := 0; i < MaxLines+20; i++ {
		if err := Append(path, Event{Kind: KindTasks, Title: fmt.Sprint(i)}); err != nil {
			t.Fatal(err)
		}
	}
	evs = readEvents(t, path)
	if len(evs) != MaxLines || evs[len(evs)-1].Title != fmt.Sprint(MaxLines+19) || evs[0].Title != "20" {
		t.Errorf("trim: %d lines, first %q, last %q", len(evs), evs[0].Title, evs[len(evs)-1].Title)
	}
	if fi, err := os.Stat(filepath.Dir(path)); err != nil || fi.Mode().Perm() != 0o700 {
		t.Errorf("dir mode: %v %v", fi.Mode(), err)
	}
}
