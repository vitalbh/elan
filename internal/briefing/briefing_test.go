package briefing

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/vitalbh/elan/internal/agenda"
	"github.com/vitalbh/elan/internal/google"
	"github.com/vitalbh/elan/internal/meetings"
)

var day = time.Date(2026, 11, 2, 8, 50, 0, 0, time.Local) // a Monday

type fakeAgendas struct {
	a   agenda.Agenda
	err error
}

func (f fakeAgendas) Agenda(context.Context, time.Time) (agenda.Agenda, error) { return f.a, f.err }

type fakeActions struct{ mine, waiting []Action }

func (f fakeActions) Mine(context.Context) ([]Action, error)    { return f.mine, nil }
func (f fakeActions) Waiting(context.Context) ([]Action, error) { return f.waiting, nil }

type fakeItems struct {
	since, until time.Time
	items        []meetings.RecentItem
	changes      []meetings.Change
}

func (f *fakeItems) ChangesSince(_ context.Context, since, until time.Time) ([]meetings.Change, error) {
	return f.changes, nil
}

func (f *fakeItems) ItemsSince(_ context.Context, _ []string, _ int, since, until time.Time) ([]meetings.RecentItem, error) {
	f.since, f.until = since, until
	return f.items, nil
}

func TestBuild(t *testing.T) {
	prev := &meetings.PreviousMeeting{
		ID: "m1", Title: "Bia / Carlos", StartedAt: day.AddDate(0, 0, -28),
		Summary: "Alinhamento <b>de</b> conteúdo.",
		OpenActions: []meetings.StoredItem{
			{ID: 1, Kind: "action", Text: "Carlos Lima vai revisar o roadmap.", People: []string{"Carlos Lima"}},
			{ID: 2, Kind: "action", Text: "Beatriz vai falar com a Mari.", People: []string{"Beatriz Costa"}},
			{ID: 3, Kind: "action", Text: "Carlos Eduardo (outro) vai ver o layout.", People: []string{"Carlos Eduardo Rocha"}},
		},
	}
	items := &fakeItems{changes: []meetings.Change{{Old: "Boleto sai em novembro.", New: "Boleto ficou para janeiro.", MeetingID: "m9", Meeting: "Roberto / Carlos", At: day.AddDate(0, 0, -3)}}, items: []meetings.RecentItem{{StoredItem: meetings.StoredItem{Text: "Adotar Flutter no Vega."}, MeetingTitle: "Paulo / Carlos", StartedAt: day.AddDate(0, 0, -3)}}}
	waiting := make([]Action, 20)
	for i := range waiting {
		waiting[i] = Action{Text: "x", AgeDays: 20 - i}
	}
	s := &Service{
		Agendas: fakeAgendas{a: agenda.Agenda{Items: []agenda.Item{
			{Event: google.Event{Title: "Bia / Carlos", Start: day.Add(time.Hour), End: day.Add(2 * time.Hour), MyResponse: "needsAction"}, Previous: prev},
			{Event: google.Event{Title: "Sem série", Start: day.Add(3 * time.Hour), End: day.Add(4 * time.Hour)}},
		}}},
		Actions: fakeActions{mine: []Action{{Text: "Mandar a proposta.", MeetingTitle: "Boleto", AgeDays: 2}}, waiting: waiting},
		Items:   items,
		Owner:   []string{"Carlos Lima", "Vital", "Carlos"},
		now:     func() time.Time { return day },
	}
	b, err := s.Build(context.Background(), day)
	if err != nil {
		t.Fatal(err)
	}
	if len(b.Events) != 2 || len(b.Events[0].MyActions) != 1 || b.Events[0].OtherActions != 2 {
		t.Fatalf("events: %+v", b.Events)
	}
	if b.WaitingTotal != 20 || !strings.Contains(b.Markdown(), "+5 mais antiga(s)") {
		t.Fatalf("waiting total %d", b.WaitingTotal)
	}
	if len(b.Waiting) != 15 || b.Waiting[0].AgeDays != 1 {
		t.Fatalf("waiting capped to the 15 most recent: len=%d first=%d", len(b.Waiting), b.Waiting[0].AgeDays)
	}
	if wantSince := time.Date(2026, 10, 30, 0, 0, 0, 0, time.Local); !items.since.Equal(wantSince) {
		t.Fatalf("decisions since %v, want the previous Friday %v", items.since, wantSince)
	}

	md := b.Markdown()
	for _, want := range []string{"Briefing de segunda, 2 nov", "09:50–10:50", "_(sem resposta)_", "Com você: Carlos Lima vai revisar", "+2 pendência(s)", "Mandar a proposta.", "Adotar Flutter", "## O que mudou", "~~Boleto sai em novembro.~~ → Boleto ficou para janeiro."} {
		if !strings.Contains(md, want) {
			t.Errorf("markdown missing %q:\n%s", want, md)
		}
	}
	html, err := b.HTML()
	if err != nil {
		t.Fatal(err)
	}
	h := string(html)
	if !strings.Contains(h, "Alinhamento &lt;b&gt;de&lt;/b&gt;") || strings.Contains(h, "<b>de</b>") {
		t.Error("html must escape meeting text")
	}
	if !strings.Contains(h, `<p class="old">Boleto sai em novembro.</p>`) {
		t.Errorf("html should show what changed")
	}
	if !strings.Contains(h, "<li>Revisar o roadmap.</li>") {
		t.Errorf("html should drop the owner's name from their actions")
	}
	if !strings.Contains(h, "sem resposta") || !strings.Contains(h, "segunda, 2 nov") {
		t.Errorf("html missing parts")
	}
}

func TestBuildDegradesToWarnings(t *testing.T) {
	s := &Service{Agendas: fakeAgendas{err: errors.New("sem login")}, now: func() time.Time { return day }}
	b, err := s.Build(context.Background(), day)
	if err != nil {
		t.Fatal(err)
	}
	if len(b.Warnings) != 1 || !strings.Contains(b.Markdown(), "agenda indisponível") {
		t.Fatalf("warnings %v", b.Warnings)
	}
	if _, err := b.HTML(); err != nil {
		t.Fatal(err)
	}
}
