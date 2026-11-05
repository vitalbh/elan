package meetings

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"testing"
)

func TestDistillSupersedesFacts(t *testing.T) {
	conn := openDB(t)
	insertMeeting(t, conn, testMeeting{id: "m1", title: "Boleto 1", folder: "p", recurring: true, started: "2025-01-10T10:00:00-03:00", distilled: true})
	insertMeeting(t, conn, testMeeting{id: "m2", title: "Boleto 2", folder: "p", recurring: true, started: "2025-02-10T10:00:00-03:00"})

	date := insertItem(t, conn, "m1", "fact", "O Boleto automático sai em novembro.")
	minor := insertItem(t, conn, "m1", "fact", "A reunião atrasou dez minutos.")
	mustExec(t, conn, `UPDATE meeting_items SET importance = 1 WHERE id = ?`, minor)
	plan := insertItem(t, conn, "m1", "decision", "Lançar o Boleto só no Vega.")

	r := &fakeRunner{reply: func(int, string) ([]byte, error) {
		return summaryReply("Boleto 2",
			fmt.Sprintf(`{"kind":"fact","text":"O Boleto automático ficou para janeiro.","people":[],"importance":3,"supersedes":[%d,%d]}`, date, minor),
			fmt.Sprintf(`{"kind":"decision","text":"Lançar o Boleto no Vega e no Blue Orion juntos.","people":[],"importance":3,"supersedes":[%d]}`, plan),
		), nil
	}}
	st, err := NewDistiller(conn, r).Distill(context.Background(), 10, nil)
	if err != nil {
		t.Fatal(err)
	}
	if st.Superseded != 2 {
		t.Fatalf("superseded %d, want the fact and the decision", st.Superseded)
	}
	p := r.prompts[0]
	if !strings.Contains(p, fmt.Sprintf("[id %d] (2025-01-10, fato)", date)) || !strings.Contains(p, fmt.Sprintf("[id %d] (2025-01-10, decisão)", plan)) {
		t.Fatalf("prompt should list the fact and the decision:\n%s", p)
	}
	if strings.Contains(p, fmt.Sprintf("[id %d]", minor)) {
		t.Fatal("a minor fact (importance 1) should not be offered")
	}
	for id, want := range map[int64]bool{date: true, minor: false, plan: true} {
		var by sql.NullInt64
		if err := conn.QueryRow(`SELECT superseded_by FROM meeting_items WHERE id = ?`, id).Scan(&by); err != nil {
			t.Fatal(err)
		}
		if by.Valid != want {
			t.Errorf("item %d superseded=%v, want %v", id, by.Valid, want)
		}
	}
}
