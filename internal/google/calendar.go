package google

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"html"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"time"

	"google.golang.org/api/calendar/v3"
	"google.golang.org/api/googleapi"
	"google.golang.org/api/option"
)

// primaryCalendar is the id of the owner's main calendar.
const primaryCalendar = "primary"

// maxEvents caps one listing: the agents get a summary, not an export.
const maxEvents = 250

// maxDescription caps the event description returned: invitations carry
// long boilerplate (dial-in numbers, agendas pasted from docs).
const maxDescription = 500

// Invitation responses, as the Calendar API names them.
const (
	Accepted    = "accepted"
	Declined    = "declined"
	Tentative   = "tentative"
	NeedsAction = "needsAction"
)

var (
	// ErrCalendarScope means the saved token lacks the Calendar scope.
	ErrCalendarScope = errors.New("o login do Google não tem acesso ao Calendar: rode `elan google-login` de novo e aceite o acesso à agenda")
	// ErrCalendarAPIDisabled means the Calendar API is off in the OAuth client's project.
	ErrCalendarAPIDisabled = errors.New("a Google Calendar API não está ativada no projeto do client OAuth")
	// ErrEventNotFound means the event id does not exist on the calendar.
	ErrEventNotFound = errors.New("evento não encontrado")
)

// Opener returns an HTTP client authorized with the profile's token. The
// Calendar client calls it on every request, so a login made after the
// process started (e.g. a long-running MCP server) is picked up.
type Opener func(ctx context.Context) (*http.Client, error)

// CalendarOptions configures a Calendar.
type CalendarOptions struct {
	// Location is the owner's time zone; time.Local when nil.
	Location *time.Location
	// Hours are the working hours used for free slots; DefaultWorkHours
	// when zero.
	Hours WorkHours
	// Endpoint overrides the API root (tests).
	Endpoint string
}

// Calendar reads and writes the owner's primary Google Calendar.
type Calendar struct {
	open     Opener
	loc      *time.Location
	hours    WorkHours
	endpoint string
}

// NewCalendar builds a Calendar client.
func NewCalendar(open Opener, o CalendarOptions) *Calendar {
	if o.Location == nil {
		o.Location = time.Local
	}
	if o.Hours == (WorkHours{}) {
		o.Hours = DefaultWorkHours
	}
	return &Calendar{open: open, loc: o.Location, hours: o.Hours, endpoint: o.Endpoint}
}

// Location is the time zone every returned time is in.
func (c *Calendar) Location() *time.Location { return c.loc }

func (c *Calendar) service(ctx context.Context) (*calendar.Service, error) {
	hc, err := c.open(ctx)
	if err != nil {
		return nil, fmt.Errorf("Google Calendar indisponível: %w", err)
	}
	opts := []option.ClientOption{option.WithHTTPClient(hc)}
	if c.endpoint != "" {
		opts = append(opts, option.WithEndpoint(c.endpoint))
	}
	return calendar.NewService(ctx, opts...)
}

// Attendee is a guest of an event.
type Attendee struct {
	Email     string `json:"email"`
	Name      string `json:"name,omitempty"`
	Response  string `json:"response,omitempty"` // accepted, declined, tentative, needsAction
	Optional  bool   `json:"optional,omitempty"`
	Organizer bool   `json:"organizer,omitempty"`
	Self      bool   `json:"self,omitempty"`
}

// Event is one event, or one occurrence of a recurring event.
type Event struct {
	ID    string    `json:"id"`
	Title string    `json:"title"`
	Start time.Time `json:"start"`
	End   time.Time `json:"end"`
	// AllDay events start at midnight in the owner's zone; End is exclusive.
	AllDay      bool   `json:"all_day,omitempty"`
	Location    string `json:"location,omitempty"`
	Description string `json:"description,omitempty"` // truncated
	MeetLink    string `json:"meet_link,omitempty"`
	Link        string `json:"link,omitempty"` // the event in Google Calendar
	// Organizer is the event's organizer; OrganizerSelf means it is the owner.
	Organizer     Attendee   `json:"organizer"`
	OrganizerSelf bool       `json:"organizer_self,omitempty"`
	Attendees     []Attendee `json:"attendees,omitempty"`
	// MyResponse is the owner's answer: their attendee status, "accepted"
	// when they organize it, empty for an event without guests.
	MyResponse string `json:"my_response,omitempty"`
	// RecurringID is set on occurrences of a recurring series.
	RecurringID string `json:"recurring_id,omitempty"`
	Status      string `json:"status,omitempty"` // confirmed, tentative
	// Transparent events are marked "free" and do not block time.
	Transparent bool   `json:"transparent,omitempty"`
	EventType   string `json:"event_type,omitempty"` // default, outOfOffice, focusTime, workingLocation...
}

// Blocks reports whether the event takes the owner's time: timed, not
// marked free, not declined, not a working-location marker.
func (e Event) Blocks() bool {
	return !e.AllDay && !e.Transparent && e.MyResponse != Declined && e.EventType != "workingLocation"
}

func (c *Calendar) eventOf(e *calendar.Event) Event {
	ev := Event{
		ID: e.Id, Title: strings.TrimSpace(e.Summary), Location: e.Location,
		Description: truncate(plainText(e.Description), maxDescription),
		MeetLink:    e.HangoutLink, Link: e.HtmlLink, RecurringID: e.RecurringEventId,
		Status: e.Status, Transparent: e.Transparency == "transparent", EventType: e.EventType,
	}
	if ev.Title == "" {
		ev.Title = "(sem título)"
	}
	ev.Start, ev.AllDay = c.timeOf(e.Start)
	ev.End, _ = c.timeOf(e.End)
	if ev.MeetLink == "" && e.ConferenceData != nil {
		for _, ep := range e.ConferenceData.EntryPoints {
			if ep.EntryPointType == "video" {
				ev.MeetLink = ep.Uri
				break
			}
		}
	}
	if e.Organizer != nil {
		ev.Organizer = Attendee{Email: e.Organizer.Email, Name: e.Organizer.DisplayName, Organizer: true, Self: e.Organizer.Self}
		ev.OrganizerSelf = e.Organizer.Self
	}
	for _, a := range e.Attendees {
		at := Attendee{Email: a.Email, Name: a.DisplayName, Response: a.ResponseStatus,
			Optional: a.Optional, Organizer: a.Organizer, Self: a.Self}
		ev.Attendees = append(ev.Attendees, at)
		if at.Self {
			ev.MyResponse = at.Response
		}
	}
	if ev.MyResponse == "" && ev.OrganizerSelf {
		ev.MyResponse = Accepted
	}
	return ev
}

// timeOf reads an API date-time; all-day dates are midnight in the owner's zone.
func (c *Calendar) timeOf(d *calendar.EventDateTime) (time.Time, bool) {
	if d == nil {
		return time.Time{}, false
	}
	if d.DateTime != "" {
		t, err := time.Parse(time.RFC3339, d.DateTime)
		if err != nil {
			return time.Time{}, false
		}
		return t.In(c.loc), false
	}
	t, err := time.ParseInLocation("2006-01-02", d.Date, c.loc)
	if err != nil {
		return time.Time{}, true
	}
	return t, true
}

// Events returns the events of the primary calendar overlapping [from, to),
// recurring series expanded into occurrences, cancelled ones left out,
// sorted by start.
func (c *Calendar) Events(ctx context.Context, from, to time.Time) ([]Event, error) {
	if !to.After(from) {
		return nil, errors.New("o fim do período precisa ser depois do início")
	}
	svc, err := c.service(ctx)
	if err != nil {
		return nil, err
	}
	call := svc.Events.List(primaryCalendar).SingleEvents(true).OrderBy("startTime").MaxResults(250).
		TimeMin(from.Format(time.RFC3339)).TimeMax(to.Format(time.RFC3339)).TimeZone(c.loc.String())
	var out []Event
	errFull := errors.New("full")
	err = call.Pages(ctx, func(p *calendar.Events) error {
		for _, e := range p.Items {
			if e.Status == "cancelled" {
				continue
			}
			out = append(out, c.eventOf(e))
		}
		if len(out) >= maxEvents {
			return errFull
		}
		return nil
	})
	if err != nil && !errors.Is(err, errFull) {
		return nil, fail("listar os eventos", err)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Start.Before(out[j].Start) })
	if len(out) > maxEvents {
		out = out[:maxEvents]
	}
	return out, nil
}

// NewEvent is an event to create on the primary calendar.
type NewEvent struct {
	Title       string
	Start, End  time.Time
	Description string
	// Attendees are guest emails; when present, Google emails them the
	// invitation.
	Attendees []string
	// Meet adds a Google Meet link.
	Meet bool
}

// Create creates an event on the owner's primary calendar.
func (c *Calendar) Create(ctx context.Context, n NewEvent) (Event, error) {
	n.Title = strings.TrimSpace(n.Title)
	if n.Title == "" {
		return Event{}, errors.New("o título do evento é obrigatório")
	}
	if !n.End.After(n.Start) {
		return Event{}, errors.New("o fim do evento precisa ser depois do início")
	}
	svc, err := c.service(ctx)
	if err != nil {
		return Event{}, err
	}
	e := &calendar.Event{
		Summary:     n.Title,
		Description: strings.TrimSpace(n.Description),
		Start:       &calendar.EventDateTime{DateTime: n.Start.In(c.loc).Format(time.RFC3339), TimeZone: c.loc.String()},
		End:         &calendar.EventDateTime{DateTime: n.End.In(c.loc).Format(time.RFC3339), TimeZone: c.loc.String()},
	}
	for _, a := range n.Attendees {
		if a = strings.TrimSpace(a); a != "" {
			e.Attendees = append(e.Attendees, &calendar.EventAttendee{Email: a})
		}
	}
	updates := "none"
	if len(e.Attendees) > 0 {
		updates = "all"
	}
	call := svc.Events.Insert(primaryCalendar, e).SendUpdates(updates).Context(ctx)
	if n.Meet {
		b := make([]byte, 8)
		_, _ = rand.Read(b)
		e.ConferenceData = &calendar.ConferenceData{CreateRequest: &calendar.CreateConferenceRequest{
			RequestId:             "elan-" + hex.EncodeToString(b),
			ConferenceSolutionKey: &calendar.ConferenceSolutionKey{Type: "hangoutsMeet"},
		}}
		call = call.ConferenceDataVersion(1)
	}
	got, err := call.Do()
	if err != nil {
		return Event{}, fail("criar o evento", err)
	}
	return c.eventOf(got), nil
}

// Respond sets the owner's answer (accepted, declined or tentative) to the
// invitation of event id, with an optional comment. For a recurring event,
// an occurrence id answers that occurrence only. The organizer is notified.
func (c *Calendar) Respond(ctx context.Context, id, response, comment string) (Event, error) {
	switch response {
	case Accepted, Declined, Tentative:
	default:
		return Event{}, fmt.Errorf("resposta inválida %q: use accepted, declined ou tentative", response)
	}
	svc, err := c.service(ctx)
	if err != nil {
		return Event{}, err
	}
	cur, err := svc.Events.Get(primaryCalendar, id).Context(ctx).Do()
	if err != nil {
		return Event{}, fail("buscar o evento", err)
	}
	patch := &calendar.Event{}
	found := false
	for _, a := range cur.Attendees {
		at := *a
		if a.Self {
			found = true
			at.ResponseStatus = response
			at.Comment = strings.TrimSpace(comment)
		}
		patch.Attendees = append(patch.Attendees, &at)
	}
	if !found {
		if cur.Organizer != nil && cur.Organizer.Self {
			return Event{}, errors.New("o dono organiza este evento: não há convite para responder")
		}
		return Event{}, errors.New("o dono não está entre os convidados deste evento")
	}
	got, err := svc.Events.Patch(primaryCalendar, id, patch).SendUpdates("all").Context(ctx).Do()
	if err != nil {
		return Event{}, fail("responder ao convite", err)
	}
	return c.eventOf(got), nil
}

// FreeSlots returns the free windows of at least min inside the working
// hours of each weekday in [from, to). All-day events, events marked free
// and declined invitations do not take time.
func (c *Calendar) FreeSlots(ctx context.Context, from, to time.Time, min time.Duration) ([]Slot, error) {
	events, err := c.Events(ctx, from, to)
	if err != nil {
		return nil, err
	}
	return FreeSlots(events, from.In(c.loc), to.In(c.loc), min, c.hours), nil
}

// fail wraps err for op, turning the errors only a new login or a console
// change fixes into clear sentinels.
func fail(op string, err error) error {
	var ge *googleapi.Error
	if errors.As(err, &ge) {
		text := strings.ToLower(ge.Message + " " + ge.Body)
		for _, e := range ge.Errors {
			text += " " + strings.ToLower(e.Reason+" "+e.Message)
		}
		switch {
		case ge.Code == http.StatusNotFound || ge.Code == http.StatusGone:
			err = ErrEventNotFound
		case ge.Code == http.StatusForbidden && (strings.Contains(text, "insufficientpermissions") ||
			strings.Contains(text, "insufficient authentication scopes") || strings.Contains(text, "access_token_scope_insufficient")):
			err = ErrCalendarScope
		case ge.Code == http.StatusForbidden && (strings.Contains(text, "accessnotconfigured") ||
			strings.Contains(text, "service_disabled") || strings.Contains(text, "is disabled")):
			err = ErrCalendarAPIDisabled
		}
	}
	return fmt.Errorf("%s: %w", op, err)
}

var htmlTag = regexp.MustCompile(`(?i)<br\s*/?>|</p>|<[^>]+>`)

// plainText turns an HTML description (the Calendar UI writes HTML) into
// plain text with collapsed whitespace.
func plainText(s string) string {
	s = html.UnescapeString(htmlTag.ReplaceAllString(s, " "))
	return strings.Join(strings.Fields(s), " ")
}

func truncate(s string, max int) string {
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return strings.TrimSpace(string(r[:max])) + "…"
}
