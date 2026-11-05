package search

import (
	"context"
	"fmt"
	"testing"

	"github.com/vitalbh/elan/internal/people"
)

func TestSearchByPerson(t *testing.T) {
	conn, s := setup(t, &fakeEmb{model: "fake:1", dim: 16})
	addMeeting(t, conn, "m1", "Ana / Carlos", "f1", now.AddDate(0, 0, -3), "Resumo\nIntegração do boleto no checkout.", "")
	addMeeting(t, conn, "m2", "Mariana / Carlos", "f2", now.AddDate(0, 0, -2), "Resumo\nPix automático para assinaturas.", "")
	addMeeting(t, conn, "m3", "Backlog", "f3", now.AddDate(0, 0, -1), "Resumo\nPrioridades do boleto e do cartão.", "")
	if _, err := conn.Exec(`UPDATE meetings SET header = 'convidado Pedro Álvares' WHERE id = 'm2'`); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(`INSERT INTO meeting_items (meeting_id, kind, text, people, created_at)
		VALUES ('m3', 'action', 'Júlia vai revisar o fluxo do boleto.', '["Júlia Prado"]', 'now')`); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(`INSERT INTO notes (text, created_at) VALUES ('Ana comentou sobre boleto', '2026-10-29T10:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	mustIndex(t, s, IndexOptions{})
	ctx := context.Background()

	meetingsOf := func(person string) map[string]bool {
		t.Helper()
		res, err := s.Search(ctx, Query{Text: "boleto", Person: person, Limit: 20})
		if err != nil {
			t.Fatalf("%s: %v", person, err)
		}
		got := map[string]bool{}
		for _, r := range res {
			if r.MeetingID == "" {
				t.Fatalf("%s: note leaked into a person search: %+v", person, r)
			}
			got[r.MeetingID] = true
		}
		return got
	}

	if got := meetingsOf("ana"); len(got) != 1 || !got["m1"] {
		t.Errorf("ana: %v, want only m1 (not Mariana)", got)
	}
	if got := meetingsOf("Julia"); len(got) != 1 || !got["m3"] {
		t.Errorf("julia: %v, want m3 through the item people, accent-insensitive", got)
	}
	if got := meetingsOf("pedro alvares"); len(got) != 1 || !got["m2"] {
		t.Errorf("pedro alvares: %v, want m2 through the invitees", got)
	}
	if got := meetingsOf("carlos"); len(got) != 2 || !got["m1"] || !got["m2"] {
		t.Errorf("carlos: %v, want m1 and m2", got)
	}
	if got := meetingsOf("ninguém"); len(got) != 0 {
		t.Errorf("unknown person: %v, want nothing", got)
	}

	s.People = people.NewDirectory([][]string{{"Júlia Prado", "Ju"}})
	if got := meetingsOf("ju"); len(got) != 1 || !got["m3"] {
		t.Errorf("alias ju: %v, want m3", got)
	}
}

func TestMaxPerMeeting(t *testing.T) {
	conn, s := setup(t, &fakeEmb{model: "fake:1", dim: 16})
	addMeeting(t, conn, "big", "Boleto", "f1", now.AddDate(0, 0, -1), "Resumo\nPix boleto boleto.", "")
	addMeeting(t, conn, "small", "Outra", "f2", now.AddDate(0, 0, -30), "Resumo\nFalamos de boleto.", "")
	for i := 0; i < 5; i++ {
		if _, err := conn.Exec(`INSERT INTO meeting_items (meeting_id, kind, text, created_at) VALUES ('big', 'fact', ?, 'now')`,
			fmt.Sprintf("Fato %d sobre o boleto.", i)); err != nil {
			t.Fatal(err)
		}
	}
	mustIndex(t, s, IndexOptions{})
	res, err := s.Search(context.Background(), Query{Text: "boleto", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	n := map[string]int{}
	for _, r := range res {
		n[r.MeetingID]++
	}
	if n["big"] != 2 || n["small"] == 0 {
		t.Fatalf("per meeting %v, want big capped at 2 and small present", n)
	}
	s.Params.MaxPerMeeting = 0
	res, _ = s.Search(context.Background(), Query{Text: "boleto", Limit: 10})
	n = map[string]int{}
	for _, r := range res {
		n[r.MeetingID]++
	}
	if n["big"] < 3 {
		t.Fatalf("without a cap %v, want more than 2 from big", n)
	}
}
