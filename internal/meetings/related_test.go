package meetings

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestOfferedActionsAddsRelated(t *testing.T) {
	conn := openDB(t)
	ctx := context.Background()
	insertMeeting(t, conn, testMeeting{id: "b1", title: "Backlog", folder: "b", recurring: true, started: "2025-01-10T10:00:00-03:00", distilled: true})
	insertMeeting(t, conn, testMeeting{id: "l1", title: "Bia / Carlos", folder: "l", recurring: true, started: "2025-01-05T10:00:00-03:00", distilled: true})
	insertMeeting(t, conn, testMeeting{id: "l2", title: "Bia / Carlos", folder: "l", recurring: true, started: "2025-02-05T10:00:00-03:00", distilled: true})
	insertMeeting(t, conn, testMeeting{id: "late", title: "Depois", folder: "z", started: "2025-03-01T10:00:00-03:00", distilled: true})

	series := insertAction(t, conn, "l1", "Bia vai revisar o player.", "Beatriz")
	backlog := insertAction(t, conn, "b1", "Bia vai criar a issue de comentários.", "Beatriz")
	done := insertAction(t, conn, "b1", "Já feita.", "Beatriz")
	mustExec(t, conn, `UPDATE meeting_items SET status = 'done', status_source = 'tasks' WHERE id = ?`, done)
	manual := insertAction(t, conn, "b1", "Reaberta à mão.", "Beatriz")
	mustExec(t, conn, `UPDATE meeting_items SET status = 'open', status_source = 'manual' WHERE id = ?`, manual)
	later := insertAction(t, conn, "late", "De uma reunião posterior.", "Beatriz")
	own := insertAction(t, conn, "l2", "Da própria reunião.", "Beatriz")
	decision := insertItem(t, conn, "b1", "decision", "Uma decisão.")

	m, err := loadMeeting(ctx, conn, "l2")
	if err != nil {
		t.Fatal(err)
	}
	var gotText string
	var gotBefore time.Time
	related := func(_ context.Context, text string, before time.Time, exclude string) ([]int64, error) {
		gotText, gotBefore = text, before
		if exclude != "l2" {
			t.Errorf("exclude %q", exclude)
		}
		return []int64{series, later, done, manual, own, decision, backlog, 999}, nil
	}
	got, err := offeredActions(ctx, conn, m, related)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].ID != series || got[0].Meeting != "" || got[1].ID != backlog || got[1].Meeting != "Backlog" {
		t.Fatalf("offered %+v, want the series action then the backlog one", got)
	}
	if !strings.Contains(gotText, "notas de Bia / Carlos") || gotBefore.Format("2006-01-02") != "2025-02-05" {
		t.Fatalf("query %q before %v", gotText, gotBefore)
	}
	if p := buildPrompt(m, nil, got, 1000); !strings.Contains(p, fmt.Sprintf("[id %d] (2025-01-10; Beatriz; de Backlog)", backlog)) {
		t.Fatalf("prompt does not name the other meeting:\n%s", p)
	}

	// A failing search falls back to the series.
	got, err = offeredActions(ctx, conn, m, func(context.Context, string, time.Time, string) ([]int64, error) {
		return nil, errors.New("ollama fora")
	})
	if err != nil || len(got) != 1 || got[0].ID != series {
		t.Fatalf("fallback %+v err=%v", got, err)
	}
}

func TestOfferedActionsCapsRelated(t *testing.T) {
	conn := openDB(t)
	ctx := context.Background()
	insertMeeting(t, conn, testMeeting{id: "a", title: "A", folder: "a", started: "2025-01-01T10:00:00-03:00", distilled: true})
	insertMeeting(t, conn, testMeeting{id: "m", title: "M", folder: "m", started: "2025-02-01T10:00:00-03:00", distilled: true})
	var ids []int64
	for i := 0; i < 30; i++ {
		ids = append(ids, insertAction(t, conn, "a", fmt.Sprintf("Ação %d.", i)))
	}
	m, _ := loadMeeting(ctx, conn, "m")
	got, err := offeredActions(ctx, conn, m, func(context.Context, string, time.Time, string) ([]int64, error) { return ids, nil })
	if err != nil || len(got) != maxRelatedActions || got[0].ID != ids[0] {
		t.Fatalf("got %d (err %v), want the first %d", len(got), err, maxRelatedActions)
	}
}

func TestReconcileCandidatesWithRelated(t *testing.T) {
	conn := openDB(t)
	ctx := context.Background()
	insertMeeting(t, conn, testMeeting{id: "r1", title: "R1", folder: "r", recurring: true, started: "2025-01-10T10:00:00-03:00", distilled: true})
	insertMeeting(t, conn, testMeeting{id: "x1", title: "X1", folder: "x", started: "2025-02-10T10:00:00-03:00", distilled: true})
	insertMeeting(t, conn, testMeeting{id: "x2", title: "X2", folder: "y", started: "2025-03-10T10:00:00-03:00", distilled: true})
	mustExec(t, conn, `UPDATE meetings SET actions_checked_hash = content_hash WHERE id = 'x2'`)

	r := NewReconciler(conn, &fakeRunner{})
	ids, _ := r.candidates(ctx)
	if strings.Join(ids, ",") != "r1" {
		t.Fatalf("series only: %v", ids)
	}
	r.Related = func(context.Context, string, time.Time, string) ([]int64, error) { return nil, nil }
	ids, _ = r.candidates(ctx)
	if strings.Join(ids, ",") != "r1,x1" {
		t.Fatalf("with related: %v", ids)
	}
	r.Since, r.Recheck = time.Date(2025, 2, 1, 0, 0, 0, 0, time.UTC), true
	ids, _ = r.candidates(ctx)
	if strings.Join(ids, ",") != "x1,x2" {
		t.Fatalf("since + recheck: %v", ids)
	}
}
