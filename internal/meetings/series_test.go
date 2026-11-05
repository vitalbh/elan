package meetings

import (
	"context"
	"testing"
	"time"
)

func TestSeriesKey(t *testing.T) {
	cases := map[string]string{
		"Pedro / Carlos (recurring)":         "pedro carlos",
		"  pedro -  CARLOS ":                 "pedro carlos",
		"Reunião quinzenal – Growth + Mídia": "reuniao quinzenal growth midia",
		"Dojo 🎖️":                            "dojo",
		"Café com Bits (Recurring)":          "cafe com bits",
	}
	for in, want := range cases {
		if got := SeriesKey(in); got != want {
			t.Errorf("SeriesKey(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestPrevious(t *testing.T) {
	conn := openDB(t)
	ctx := context.Background()
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := conn.Exec(q, args...); err != nil {
			t.Fatal(err)
		}
	}
	add := func(id, title, folder, started string) {
		exec(`INSERT INTO meetings (id, title, folder_id, folder_name, recurring, started_at, source_modified, content_hash, ingested_at)
			VALUES (?, ?, 'f', ?, 0, ?, 'x', 'h', 'now')`, id, title, folder, started)
	}
	// The doc title differs from the calendar title; the series folder matches.
	add("old", "Sync semanal", "Sincronização Pagamentos (recurring)", "2026-10-13T10:02:00-03:00")
	add("last", "Sync semanal", "Sincronização Pagamentos (recurring)", "2026-10-20T10:01:00-03:00")
	add("future", "Sync semanal", "Sincronização Pagamentos (recurring)", "2026-10-27T10:00:00-03:00")
	add("other", "Outra reunião", "Outra reunião - 2026/10/21 10:00 GMT-03:00", "2026-10-21T10:00:00-03:00")
	exec(`INSERT INTO meeting_items (meeting_id, kind, text, created_at) VALUES
		('last', 'summary', 'Resumo da última.', 'now'),
		('last', 'action', 'Ana manda a proposta.', 'now'),
		('last', 'action', 'Ação substituída.', 'now'),
		('last', 'decision', 'Uma decisão.', 'now'),
		('old', 'action', 'Ação antiga.', 'now')`)
	exec(`UPDATE meeting_items SET superseded_by = 1 WHERE text = 'Ação substituída.'`)

	before := time.Date(2026, 10, 27, 9, 0, 0, 0, time.FixedZone("-03", -3*3600))
	p, err := NewReader(conn).Previous(ctx, "sincronizacao  PAGAMENTOS", before)
	if err != nil {
		t.Fatal(err)
	}
	if p == nil || p.ID != "last" || p.Summary != "Resumo da última." {
		t.Fatalf("previous: %+v", p)
	}
	if len(p.OpenActions) != 1 || p.OpenActions[0].Text != "Ana manda a proposta." {
		t.Errorf("open actions: %+v", p.OpenActions)
	}

	// Title match on a one-off meeting.
	if p, err := NewReader(conn).Previous(ctx, "Outra Reunião", before); err != nil || p == nil || p.ID != "other" {
		t.Errorf("by title: %+v %v", p, err)
	}
	// No match.
	if p, err := NewReader(conn).Previous(ctx, "Planejamento", before); err != nil || p != nil {
		t.Errorf("no match: %+v %v", p, err)
	}
	// Nothing before the first meeting.
	if p, err := NewReader(conn).Previous(ctx, "Sincronização Pagamentos", before.AddDate(0, 0, -30)); err != nil || p != nil {
		t.Errorf("before the series: %+v %v", p, err)
	}
}
