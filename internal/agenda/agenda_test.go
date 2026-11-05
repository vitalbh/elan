package agenda

import (
	"context"
	"testing"
	"time"

	"github.com/vitalbh/elan/internal/google"
	"github.com/vitalbh/elan/internal/meetings"
)

type fakeEvents struct {
	events   []google.Event
	from, to time.Time
}

func (f *fakeEvents) Location() *time.Location { return loc }

func (f *fakeEvents) Events(_ context.Context, from, to time.Time) ([]google.Event, error) {
	f.from, f.to = from, to
	return f.events, nil
}

type fakeHistory struct{ asked map[string]time.Time }

func (f *fakeHistory) Previous(_ context.Context, title string, before time.Time) (*meetings.PreviousMeeting, error) {
	f.asked[title] = before
	if title == "Série" {
		return &meetings.PreviousMeeting{ID: "m1", Title: title}, nil
	}
	return nil, nil
}

var loc = time.FixedZone("-03", -3*3600)

func TestAgenda(t *testing.T) {
	start := time.Date(2026, 11, 2, 10, 0, 0, 0, loc)
	ev := &fakeEvents{events: []google.Event{
		{ID: "a", Title: "Feriado", AllDay: true, Start: time.Date(2026, 11, 2, 0, 0, 0, 0, loc)},
		{ID: "w", Title: "Home", EventType: "workingLocation", Start: start},
		{ID: "s", Title: "Série", Start: start, End: start.Add(time.Hour)},
		{ID: "n", Title: "Avulsa", Start: start.Add(2 * time.Hour)},
	}}
	h := &fakeHistory{asked: map[string]time.Time{}}
	a, err := New(ev, h).Agenda(context.Background(), time.Date(2026, 11, 2, 15, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if a.Day != "2026-11-02" || !ev.from.Equal(time.Date(2026, 11, 2, 0, 0, 0, 0, loc)) || ev.to.Sub(ev.from) != 24*time.Hour {
		t.Fatalf("day %s, range %v–%v", a.Day, ev.from, ev.to)
	}
	if len(a.Items) != 3 {
		t.Fatalf("%d items, want 3 (working location out)", len(a.Items))
	}
	if a.Items[1].Previous == nil || a.Items[1].Previous.ID != "m1" || a.Items[2].Previous != nil || a.Matched() != 1 {
		t.Errorf("previous: %+v", a.Items)
	}
	if _, asked := h.asked["Feriado"]; asked {
		t.Error("all-day events must not be matched")
	}
	if got := h.asked["Série"]; !got.Equal(start.Add(-sameOccurrenceMargin)) {
		t.Errorf("asked before %v", got)
	}
}
