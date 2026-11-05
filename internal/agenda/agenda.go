// Package agenda joins the owner's calendar with the meeting memory: each
// event of a day comes with the previous meeting of the same series (its
// summary and still-open action items). It backs the calendar_agenda MCP
// tool, `elan agenda`, the daily briefing and the pre-meeting reminder.
package agenda

import (
	"context"
	"time"

	"github.com/vitalbh/elan/internal/google"
	"github.com/vitalbh/elan/internal/meetings"
)

// sameOccurrenceMargin keeps the meeting of the event itself out of
// "previous": Meet docs start a few minutes before or after the scheduled
// time, so only meetings that started at least this long before the event
// count as previous.
const sameOccurrenceMargin = time.Hour

// Events lists calendar events; *google.Calendar implements it.
type Events interface {
	Events(ctx context.Context, from, to time.Time) ([]google.Event, error)
	Location() *time.Location
}

// History finds the previous meeting of a series; *meetings.Reader
// implements it.
type History interface {
	Previous(ctx context.Context, title string, before time.Time) (*meetings.PreviousMeeting, error)
}

// Agenda is one day of the owner's calendar with meeting context.
type Agenda struct {
	// Day is the date, AAAA-MM-DD, in the calendar's time zone.
	Day   string `json:"day"`
	Items []Item `json:"items"`
}

// Item is a calendar event with the previous meeting of its series.
type Item struct {
	google.Event
	// Previous is the latest stored meeting of the same series before the
	// event (title or series folder match, normalized); nil when none, and
	// always nil for all-day events.
	Previous *meetings.PreviousMeeting `json:"previous,omitempty"`
}

// Matched counts the items with a previous meeting.
func (a Agenda) Matched() int {
	n := 0
	for _, it := range a.Items {
		if it.Previous != nil {
			n++
		}
	}
	return n
}

// Service builds agendas.
type Service struct {
	cal     Events
	history History
}

// New builds a Service.
func New(cal Events, history History) *Service {
	return &Service{cal: cal, history: history}
}

// Agenda returns the events of day (any moment of it; the day is taken in
// the calendar's time zone), working-location markers left out, each with
// its previous meeting.
func (s *Service) Agenda(ctx context.Context, day time.Time) (Agenda, error) {
	loc := s.cal.Location()
	d := day.In(loc)
	from := time.Date(d.Year(), d.Month(), d.Day(), 0, 0, 0, 0, loc)
	events, err := s.cal.Events(ctx, from, from.AddDate(0, 0, 1))
	if err != nil {
		return Agenda{}, err
	}
	kept := events[:0]
	for _, e := range events {
		if e.EventType != "workingLocation" {
			kept = append(kept, e)
		}
	}
	items, err := s.Enrich(ctx, kept)
	if err != nil {
		return Agenda{}, err
	}
	return Agenda{Day: from.Format("2006-01-02"), Items: items}, nil
}

// Enrich attaches the previous meeting of each event's series. The
// reminder uses it for a single upcoming event.
func (s *Service) Enrich(ctx context.Context, events []google.Event) ([]Item, error) {
	items := make([]Item, 0, len(events))
	for _, e := range events {
		it := Item{Event: e}
		if !e.AllDay {
			prev, err := s.history.Previous(ctx, e.Title, e.Start.Add(-sameOccurrenceMargin))
			if err != nil {
				return nil, err
			}
			it.Previous = prev
		}
		items = append(items, it)
	}
	return items, nil
}
