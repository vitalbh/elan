package meetings

import (
	"context"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/vitalbh/elan/internal/llm"
)

var idInPrompt = regexp.MustCompile(`\[id (\d+)\]`)

func TestClassify(t *testing.T) {
	conn := openDB(t)
	ctx := context.Background()
	insertMeeting(t, conn, testMeeting{id: "m1", title: "Daily Vega", folder: "f", started: "2025-03-01T10:00:00-03:00"})
	var ids []int64
	for i := 0; i < 5; i++ {
		ids = append(ids, insertAction(t, conn, "m1", fmt.Sprintf("Ação %d", i), "Ana"))
	}
	done := insertAction(t, conn, "m1", "Ação fechada", "Ana")
	mustExec(t, conn, `UPDATE meeting_items SET status = 'done' WHERE id = ?`, done)
	already := insertAction(t, conn, "m1", "Já classificada", "Ana")
	setClass(t, conn, already, CategoryOther, "")

	r := &fakeRunner{reply: func(n int, prompt string) ([]byte, error) {
		var parts []string
		for k, m := range idInPrompt.FindAllStringSubmatch(prompt, -1) {
			id, _ := strconv.ParseInt(m[1], 10, 64)
			if n == 1 && k == 1 {
				continue // left out of the answer
			}
			cat := "other"
			if id%2 == 0 {
				cat = "code"
			}
			parts = append(parts, fmt.Sprintf(`{"id":%d,"category":%q,"project":"Vega"}`, id, cat))
		}
		// An id outside the batch and a repeated one are ignored.
		parts = append(parts, fmt.Sprintf(`{"id":%d,"category":"code","project":"X"}`, already))
		return []byte(`{"actions":[` + strings.Join(parts, ",") + `]}`), nil
	}}
	c := NewClassifier(conn, r)
	c.Batch = 2
	c.KnownProjects = []Project{{Name: "Vega"}}
	st, err := c.Classify(ctx, 2, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(r.prompts) != 2 || st.Calls != 2 || st.Missing != 1 || st.Code+st.Other != 3 || st.Pending != 3 {
		t.Errorf("stats %+v, calls %d", st, len(r.prompts))
	}
	if !strings.Contains(r.prompts[0], "reunião: Daily Vega") || !strings.Contains(r.prompts[0], "- Vega:") {
		t.Errorf("prompt:\n%s", r.prompts[0])
	}
	if strings.Contains(r.prompts[0], "Ação fechada") || strings.Contains(r.prompts[1], "Ação fechada") {
		t.Error("open actions go first")
	}
	var cat string
	_ = conn.QueryRow(`SELECT category FROM meeting_items WHERE id = ?`, already).Scan(&cat)
	if cat != CategoryOther {
		t.Error("classified action was overwritten")
	}

	// The rest, no limit.
	st, err = c.Classify(ctx, 0, nil)
	if err != nil || st.Pending != 0 || st.Calls != 2 {
		t.Errorf("second run %+v %v", st, err)
	}
	if st.Projects["Vega"] == 0 {
		t.Errorf("projects %v", st.Projects)
	}
}

func TestClassifyBadAnswerAndLimit(t *testing.T) {
	conn := openDB(t)
	insertMeeting(t, conn, testMeeting{id: "m1", title: "R", folder: "f", started: "2025-03-01T10:00:00-03:00"})
	a := insertAction(t, conn, "m1", "A", "Ana")
	insertAction(t, conn, "m1", "B", "Ana")
	r := &fakeRunner{reply: func(n int, _ string) ([]byte, error) {
		if n == 1 {
			return []byte(fmt.Sprintf(`{"actions":[{"id":%d,"category":"infra","project":""}]}`, a)), nil
		}
		return nil, &llm.LimitError{Message: "limite"}
	}}
	c := NewClassifier(conn, r)
	c.Batch = 1
	st, err := c.Classify(context.Background(), 0, nil)
	if _, ok := llm.IsLimit(err); !ok {
		t.Fatalf("want limit error, got %v", err)
	}
	if st.Calls != 2 || st.Failed != 1 || st.Pending != 2 {
		t.Errorf("stats %+v", st)
	}
}
