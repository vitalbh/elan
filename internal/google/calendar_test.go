package google

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

var testLoc = func() *time.Location {
	loc, err := time.LoadLocation("America/Sao_Paulo")
	if err != nil {
		panic(err)
	}
	return loc
}()

func at(day, hour, minute int) time.Time {
	return time.Date(2026, 11, day, hour, minute, 0, 0, testLoc)
}

// fakeCalendar answers the Calendar API endpoints the client uses with
// canned JSON and records the requests.
type fakeCalendar struct {
	t      *testing.T
	events []map[string]any
	status int    // when set, every request fails with it
	body   string // error body

	mu       sync.Mutex
	requests []string
	patched  map[string]any
	inserted map[string]any
}

func (f *fakeCalendar) client(t *testing.T) *Calendar {
	srv := httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(srv.Close)
	open := func(context.Context) (*http.Client, error) { return srv.Client(), nil }
	return NewCalendar(open, CalendarOptions{Location: testLoc, Endpoint: srv.URL + "/calendar/v3/"})
}

func (f *fakeCalendar) serve(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.requests = append(f.requests, r.Method+" "+r.URL.Path+"?"+r.URL.RawQuery)
	w.Header().Set("Content-Type", "application/json")
	if f.status != 0 {
		w.WriteHeader(f.status)
		io.WriteString(w, f.body)
		return
	}
	var body map[string]any
	if b, _ := io.ReadAll(r.Body); len(b) > 0 {
		if err := json.Unmarshal(b, &body); err != nil {
			f.t.Errorf("bad body %s", b)
		}
	}
	path := strings.TrimPrefix(r.URL.Path, "/calendar/v3/calendars/primary/events")
	switch {
	case r.Method == http.MethodGet && path == "":
		json.NewEncoder(w).Encode(map[string]any{"items": f.events})
	case r.Method == http.MethodPost && path == "":
		f.inserted = body
		body["id"] = "new1"
		body["hangoutLink"] = "https://meet.google.com/abc"
		body["organizer"] = map[string]any{"email": "dono@example.com", "self": true}
		json.NewEncoder(w).Encode(body)
	case r.Method == http.MethodGet:
		for _, e := range f.events {
			if "/"+e["id"].(string) == path {
				json.NewEncoder(w).Encode(e)
				return
			}
		}
		w.WriteHeader(http.StatusNotFound)
		io.WriteString(w, `{"error":{"code":404,"message":"Not Found"}}`)
	case r.Method == http.MethodPatch:
		f.patched = body
		for _, e := range f.events {
			if "/"+e["id"].(string) == path {
				out := map[string]any{}
				for k, v := range e {
					out[k] = v
				}
				out["attendees"] = body["attendees"]
				json.NewEncoder(w).Encode(out)
				return
			}
		}
		w.WriteHeader(http.StatusNotFound)
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

func timed(id, title string, start, end time.Time, extra map[string]any) map[string]any {
	e := map[string]any{
		"id": id, "summary": title, "status": "confirmed",
		"start": map[string]any{"dateTime": start.Format(time.RFC3339)},
		"end":   map[string]any{"dateTime": end.Format(time.RFC3339)},
	}
	for k, v := range extra {
		e[k] = v
	}
	return e
}

func invite(response string) map[string]any {
	return map[string]any{
		"organizer": map[string]any{"email": "ana@example.com", "displayName": "Ana"},
		"attendees": []any{
			map[string]any{"email": "ana@example.com", "displayName": "Ana", "responseStatus": "accepted", "organizer": true},
			map[string]any{"email": "dono@example.com", "responseStatus": response, "self": true},
		},
	}
}

func TestEvents(t *testing.T) {
	inv := invite(NeedsAction)
	inv["hangoutLink"] = "https://meet.google.com/xyz"
	inv["description"] = "<p>Pauta:<br>item&nbsp;1</p>" + strings.Repeat("x", 600)
	inv["recurringEventId"] = "series1"
	f := &fakeCalendar{t: t, events: []map[string]any{
		timed("e1", "Pagamentos", at(2, 10, 0), at(2, 11, 0), inv),
		{"id": "e2", "summary": "Feriado", "status": "confirmed", "start": map[string]any{"date": "2026-11-02"}, "end": map[string]any{"date": "2026-11-03"}},
		timed("e3", "Cancelada", at(2, 12, 0), at(2, 13, 0), map[string]any{"status": "cancelled"}),
		timed("e4", "Foco", at(2, 14, 0), at(2, 15, 0), map[string]any{"organizer": map[string]any{"email": "dono@example.com", "self": true}}),
	}}
	c := f.client(t)
	got, err := c.Events(context.Background(), at(2, 0, 0), at(3, 0, 0))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("%d events, want 3 (cancelled out): %+v", len(got), got)
	}
	e := got[1] // sorted by start: the all-day event first
	if got[0].ID != "e2" || !got[0].AllDay || !got[0].Start.Equal(at(2, 0, 0)) {
		t.Errorf("all-day: %+v", got[0])
	}
	if e.ID != "e1" || e.MyResponse != NeedsAction || e.Organizer.Name != "Ana" || e.OrganizerSelf ||
		len(e.Attendees) != 2 || e.MeetLink != "https://meet.google.com/xyz" || e.RecurringID != "series1" {
		t.Errorf("invite: %+v", e)
	}
	if !strings.HasPrefix(e.Description, "Pauta: item 1 xxx") || len([]rune(e.Description)) > maxDescription+1 {
		t.Errorf("description: %q", e.Description)
	}
	if e.Start.Location() != testLoc {
		t.Errorf("start not in the calendar zone: %v", e.Start)
	}
	if got[2].MyResponse != Accepted {
		t.Errorf("own event: response %q, want accepted", got[2].MyResponse)
	}
	req := f.requests[0]
	for _, want := range []string{"singleEvents=true", "orderBy=startTime", "timeZone=America%2FSao_Paulo"} {
		if !strings.Contains(req, want) {
			t.Errorf("request %s lacks %s", req, want)
		}
	}
}

func TestFreeSlots(t *testing.T) {
	declined := invite(Declined)
	f := &fakeCalendar{t: t, events: []map[string]any{
		timed("a", "A", at(2, 9, 0), at(2, 11, 0), nil),
		timed("b", "B", at(2, 10, 30), at(2, 11, 7), nil), // overlaps A; end rounds up to 11:10
		timed("c", "Recusada", at(2, 13, 0), at(2, 14, 0), declined),
		timed("d", "Livre", at(2, 15, 0), at(2, 16, 0), map[string]any{"transparency": "transparent"}),
		timed("e", "C", at(2, 16, 0), at(2, 18, 40), nil),
		{"id": "f", "summary": "Dia todo", "status": "confirmed", "start": map[string]any{"date": "2026-11-02"}, "end": map[string]any{"date": "2026-11-03"}},
	}}
	c := f.client(t)
	// Monday 2 to Sunday 8 is the range; only Monday is checked here.
	slots, err := c.FreeSlots(context.Background(), at(2, 0, 0), at(3, 0, 0), 30*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, s := range slots {
		got = append(got, s.Start.Format("15:04")+"-"+s.End.Format("15:04"))
	}
	want := []string{"11:10-16:00"}
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Errorf("slots = %v, want %v (18:40-19:00 is shorter than 30 min)", got, want)
	}

	// Weekends have no working hours; a range starting mid-day starts there.
	slots = FreeSlots(nil, at(6, 17, 2), at(9, 10, 0), 30*time.Minute, DefaultWorkHours)
	got = nil
	for _, s := range slots {
		got = append(got, s.Start.Format("02 15:04")+"-"+s.End.Format("15:04"))
	}
	if want := []string{"06 17:05-19:00", "09 09:00-10:00"}; strings.Join(got, " ") != strings.Join(want, " ") {
		t.Errorf("slots = %v, want %v", got, want)
	}
}

func TestCreate(t *testing.T) {
	f := &fakeCalendar{t: t}
	c := f.client(t)
	ev, err := c.Create(context.Background(), NewEvent{
		Title: " Revisão ", Start: at(4, 10, 0), End: at(4, 10, 30), Attendees: []string{"ana@example.com", " "}, Meet: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if ev.ID != "new1" || ev.Title != "Revisão" || ev.MeetLink == "" || !ev.Start.Equal(at(4, 10, 0)) {
		t.Errorf("created: %+v", ev)
	}
	if att := f.inserted["attendees"].([]any); len(att) != 1 {
		t.Errorf("attendees sent: %v", att)
	}
	if f.inserted["conferenceData"] == nil {
		t.Error("meet requested without conferenceData")
	}
	req := f.requests[0]
	if !strings.Contains(req, "sendUpdates=all") || !strings.Contains(req, "conferenceDataVersion=1") {
		t.Errorf("request %s", req)
	}
	if _, err := c.Create(context.Background(), NewEvent{Title: "x", Start: at(4, 10, 0), End: at(4, 10, 0)}); err == nil {
		t.Error("end before start should fail")
	}
}

func TestRespond(t *testing.T) {
	own := map[string]any{"organizer": map[string]any{"email": "dono@example.com", "self": true}}
	f := &fakeCalendar{t: t, events: []map[string]any{
		timed("inv", "Convite", at(2, 10, 0), at(2, 11, 0), invite(NeedsAction)),
		timed("own", "Minha", at(2, 12, 0), at(2, 13, 0), own),
	}}
	c := f.client(t)
	ev, err := c.Respond(context.Background(), "inv", Declined, "conflito")
	if err != nil {
		t.Fatal(err)
	}
	if ev.MyResponse != Declined {
		t.Errorf("response %q", ev.MyResponse)
	}
	att := f.patched["attendees"].([]any)
	self := att[1].(map[string]any)
	if self["responseStatus"] != Declined || self["comment"] != "conflito" || att[0].(map[string]any)["responseStatus"] != Accepted {
		t.Errorf("patched attendees: %v", att)
	}
	if _, err := c.Respond(context.Background(), "own", Accepted, ""); err == nil {
		t.Error("responding to own event should fail")
	}
	if _, err := c.Respond(context.Background(), "inv", "maybe", ""); err == nil {
		t.Error("invalid response should fail")
	}
	if _, err := c.Respond(context.Background(), "nope", Accepted, ""); !errors.Is(err, ErrEventNotFound) {
		t.Errorf("unknown event: %v", err)
	}
}

func TestErrors(t *testing.T) {
	f := &fakeCalendar{t: t, status: http.StatusForbidden,
		body: `{"error":{"code":403,"message":"Request had insufficient authentication scopes.","errors":[{"reason":"insufficientPermissions"}]}}`}
	if _, err := f.client(t).Events(context.Background(), at(2, 0, 0), at(3, 0, 0)); !errors.Is(err, ErrCalendarScope) {
		t.Errorf("scope error: %v", err)
	}
	missing := NewCalendar(func(context.Context) (*http.Client, error) { return nil, ErrNotLoggedIn }, CalendarOptions{})
	if _, err := missing.Events(context.Background(), at(2, 0, 0), at(3, 0, 0)); !errors.Is(err, ErrNotLoggedIn) ||
		!strings.Contains(err.Error(), "google-login") {
		t.Errorf("no login: %v", err)
	}
}
