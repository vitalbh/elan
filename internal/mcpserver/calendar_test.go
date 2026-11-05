package mcpserver

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/vitalbh/elan/internal/agenda"
	"github.com/vitalbh/elan/internal/db"
	"github.com/vitalbh/elan/internal/google"
	"github.com/vitalbh/elan/internal/meetings"
	"github.com/vitalbh/elan/internal/notes"
)

var calLoc = time.FixedZone("-03", -3*3600)

// fakeCalendar records the calls and answers from a fixed list of events.
type fakeCalendar struct {
	events   []google.Event
	from, to time.Time
	min      time.Duration
	created  *google.NewEvent
	respond  [3]string
	err      error
}

func (f *fakeCalendar) Location() *time.Location { return calLoc }

func (f *fakeCalendar) Events(_ context.Context, from, to time.Time) ([]google.Event, error) {
	f.from, f.to = from, to
	return f.events, f.err
}

func (f *fakeCalendar) FreeSlots(_ context.Context, from, to time.Time, min time.Duration) ([]google.Slot, error) {
	f.from, f.to, f.min = from, to, min
	return []google.Slot{{Start: from.Add(9 * time.Hour), End: from.Add(10 * time.Hour)}}, f.err
}

func (f *fakeCalendar) Create(_ context.Context, n google.NewEvent) (google.Event, error) {
	f.created = &n
	return google.Event{ID: "new", Title: n.Title, Start: n.Start, End: n.End}, f.err
}

func (f *fakeCalendar) Respond(_ context.Context, id, response, comment string) (google.Event, error) {
	f.respond = [3]string{id, response, comment}
	return google.Event{ID: id, MyResponse: response}, f.err
}

func setupCalendar(t *testing.T, cal *fakeCalendar) *mcp.ClientSession {
	t.Helper()
	ctx := context.Background()
	conn, err := db.Open(filepath.Join(t.TempDir(), "elan.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	for _, q := range []string{
		`INSERT INTO meetings (id, title, folder_id, folder_name, recurring, started_at, source_modified, content_hash, ingested_at)
			VALUES ('m1', 'Pagamentos', 'f1', 'Pagamentos (recurring)', 1, '2026-10-26T10:00:00-03:00', 'x', 'h', 'now')`,
		`INSERT INTO meeting_items (meeting_id, kind, text, created_at) VALUES
			('m1', 'summary', 'Boleto no Vega Pay.', 'now'), ('m1', 'action', 'Ana manda a proposta.', 'now')`,
	} {
		if _, err := conn.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	reader := meetings.NewReader(conn)
	srv := New(Deps{Meetings: reader, Notes: notes.NewService(conn), Version: "test",
		Calendar: cal, Agenda: agenda.New(cal, reader)})
	ct, st := mcp.NewInMemoryTransports()
	if _, err := srv.Connect(ctx, st, nil); err != nil {
		t.Fatal(err)
	}
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "test"}, nil).Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cs.Close() })
	return cs
}

func TestCalendarTools(t *testing.T) {
	start := time.Date(2026, 11, 2, 10, 0, 0, 0, calLoc)
	cal := &fakeCalendar{events: []google.Event{
		{ID: "e1", Title: "pagamentos", Start: start, End: start.Add(time.Hour), MyResponse: google.NeedsAction},
		{ID: "e2", Title: "Avulsa", Start: start.Add(3 * time.Hour), End: start.Add(4 * time.Hour)},
	}}
	cs := setupCalendar(t, cal)

	tools, err := cs.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	names := map[string]string{}
	for _, tl := range tools.Tools {
		names[tl.Name] = tl.Description
	}
	for _, n := range []string{"calendar_agenda", "calendar_events", "calendar_free_slots", "calendar_create_event", "calendar_respond"} {
		if _, ok := names[n]; !ok {
			t.Errorf("tool %s missing", n)
		}
	}
	if !strings.Contains(names["calendar_create_event"], "DE VERDADE") || !strings.Contains(names["calendar_respond"], "DE VERDADE") {
		t.Error("write tools must say they act on the real calendar")
	}

	a, _ := call[agenda.Agenda](t, cs, "calendar_agenda", map[string]any{"day": "2026-11-02"})
	if a.Day != "2026-11-02" || len(a.Items) != 2 {
		t.Fatalf("agenda: %+v", a)
	}
	p := a.Items[0].Previous
	if p == nil || p.ID != "m1" || p.Summary != "Boleto no Vega Pay." || len(p.OpenActions) != 1 || a.Items[1].Previous != nil {
		t.Errorf("agenda previous: %+v", a.Items)
	}
	if a.Items[0].ID != "e1" || a.Items[0].MyResponse != google.NeedsAction {
		t.Errorf("agenda event fields: %+v", a.Items[0])
	}

	ev, _ := call[EventsOut](t, cs, "calendar_events", map[string]any{"from": "2026-11-02", "to": "2026-11-03"})
	if len(ev.Events) != 2 || !cal.from.Equal(time.Date(2026, 11, 2, 0, 0, 0, 0, calLoc)) || !cal.to.Equal(time.Date(2026, 11, 4, 0, 0, 0, 0, calLoc)) {
		t.Errorf("events: %d, range %v–%v (to day is included)", len(ev.Events), cal.from, cal.to)
	}
	if _, res := call[EventsOut](t, cs, "calendar_events", map[string]any{"from": "amanhã"}); !res.IsError {
		t.Error("bad date should be a tool error")
	}
	if _, res := call[EventsOut](t, cs, "calendar_events", map[string]any{"from": "2026-11-03", "to": "2026-11-02T10:00"}); !res.IsError {
		t.Error("end before start should be a tool error")
	}

	slots, _ := call[FreeSlotsOut](t, cs, "calendar_free_slots", map[string]any{"from": "2026-11-02T08:00", "to": "2026-11-02T18:00", "min_minutes": 45})
	if len(slots.Slots) != 1 || slots.Slots[0].Minutes != 60 || cal.min != 45*time.Minute || cal.from.Hour() != 8 {
		t.Errorf("free slots: %+v, min %v, from %v", slots, cal.min, cal.from)
	}

	created, _ := call[google.Event](t, cs, "calendar_create_event", map[string]any{
		"title": "Revisão", "start": "2026-11-04T15:00", "attendees": []string{"ana@example.com"}, "meet": true,
	})
	if created.ID != "new" || cal.created.End.Sub(cal.created.Start) != 30*time.Minute || !cal.created.Meet || len(cal.created.Attendees) != 1 {
		t.Errorf("create: %+v %+v", created, cal.created)
	}
	if _, res := call[google.Event](t, cs, "calendar_create_event", map[string]any{"title": "x", "start": "2026-11-04"}); !res.IsError {
		t.Error("start without time should be a tool error")
	}

	r, _ := call[google.Event](t, cs, "calendar_respond", map[string]any{"event_id": "e1", "response": "tentative", "comment": "talvez"})
	if r.MyResponse != "tentative" || cal.respond != [3]string{"e1", "tentative", "talvez"} {
		t.Errorf("respond: %+v %v", r, cal.respond)
	}

	// Without a Google login the calendar tools fail clearly.
	cal.err = errors.New("Google Calendar indisponível: " + google.ErrNotLoggedIn.Error())
	_, res := call[EventsOut](t, cs, "calendar_events", nil)
	if !res.IsError || !strings.Contains(res.Content[0].(*mcp.TextContent).Text, "google-login") {
		t.Errorf("no login: %+v", res)
	}
}
