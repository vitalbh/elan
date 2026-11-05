// Package briefing builds the owner's daily briefing: today's agenda with
// the previous meeting of each series, the owner's open actions, what the
// owner is waiting on from others and the recent important decisions.
package briefing

import (
	"context"
	"sort"
	"time"

	"github.com/vitalbh/elan/internal/agenda"
	"github.com/vitalbh/elan/internal/meetings"
	"github.com/vitalbh/elan/internal/people"
)

// Action is an open action item as the briefing shows it.
type Action struct {
	ID           int64      `json:"id"`
	Text         string     `json:"text"`
	People       []string   `json:"people,omitempty"`
	MeetingID    string     `json:"meeting_id"`
	MeetingTitle string     `json:"meeting_title"`
	MeetingDate  *time.Time `json:"meeting_date,omitempty"`
	AgeDays      int        `json:"age_days"`
}

// Actions provides the open actions (the action-status layer).
type Actions interface {
	// Mine returns the owner's open, non-stale actions.
	Mine(ctx context.Context) ([]Action, error)
	// Waiting returns other people's open, non-stale actions.
	Waiting(ctx context.Context) ([]Action, error)
}

// Agendas returns a day's agenda.
type Agendas interface {
	Agenda(ctx context.Context, day time.Time) (agenda.Agenda, error)
}

// Items returns recent distilled items and what they replaced.
type Items interface {
	ItemsSince(ctx context.Context, kinds []string, minImportance int, since, until time.Time) ([]meetings.RecentItem, error)
	ChangesSince(ctx context.Context, since, until time.Time) ([]meetings.Change, error)
}

// Event is an agenda event with the owner's part of the previous meeting.
type Event struct {
	agenda.Item
	// MyActions are the open actions of the previous meeting the owner is
	// part of (as owner or named in them).
	MyActions []meetings.StoredItem `json:"my_actions,omitempty"`
	// OtherActions counts the other open actions of the previous meeting.
	OtherActions int `json:"other_actions,omitempty"`
}

// Briefing is one day's briefing.
type Briefing struct {
	Day       time.Time `json:"day"`
	Generated time.Time `json:"generated"`
	Events    []Event   `json:"events"`
	Mine      []Action  `json:"mine"`
	Waiting   []Action  `json:"waiting"`
	// MineTotal and WaitingTotal count every open action before the cap.
	MineTotal    int                   `json:"mine_total"`
	WaitingTotal int                   `json:"waiting_total"`
	Decisions    []meetings.RecentItem `json:"decisions"`
	// Changes are the decisions and facts replaced since the last workday.
	Changes []meetings.Change `json:"changes"`
	// Warnings name the sections that could not be built (e.g. calendar
	// offline); the rest of the briefing is still useful.
	Warnings []string `json:"warnings,omitempty"`
	// Owner are the owner's names, used to shorten the owner's actions.
	Owner []string `json:"-"`
}

// Service builds briefings.
type Service struct {
	Agendas Agendas
	Actions Actions
	Items   Items
	// Owner are the owner's names, to pick the owner's actions out of a
	// previous meeting.
	Owner []string
	// MaxMine and MaxWaiting cap the two lists (defaults 12 and 15).
	MaxMine    int
	MaxWaiting int
	now        func() time.Time
}

// Build assembles the briefing of day. A failing section becomes a warning
// instead of failing the whole briefing.
func (s *Service) Build(ctx context.Context, day time.Time) (Briefing, error) {
	now := time.Now
	if s.now != nil {
		now = s.now
	}
	b := Briefing{Day: startOfDay(day), Generated: now(), Owner: s.Owner}

	if s.Agendas != nil {
		if a, err := s.Agendas.Agenda(ctx, day); err != nil {
			b.Warnings = append(b.Warnings, "agenda indisponível: "+err.Error())
		} else {
			for _, it := range a.Items {
				b.Events = append(b.Events, s.Event(it))
			}
		}
	}
	if s.Actions != nil {
		if mine, err := s.Actions.Mine(ctx); err != nil {
			b.Warnings = append(b.Warnings, "suas pendências indisponíveis: "+err.Error())
		} else {
			b.MineTotal = len(mine)
			b.Mine = capRecent(mine, s.maxMine())
		}
		if waiting, err := s.Actions.Waiting(ctx); err != nil {
			b.Warnings = append(b.Warnings, "pendências de outros indisponíveis: "+err.Error())
		} else {
			b.WaitingTotal = len(waiting)
			b.Waiting = capRecent(waiting, s.maxWaiting())
		}
	}
	if s.Items != nil {
		since := previousWorkday(b.Day)
		if d, err := s.Items.ItemsSince(ctx, []string{"decision"}, 2, since, b.Day); err != nil {
			b.Warnings = append(b.Warnings, "decisões recentes indisponíveis: "+err.Error())
		} else {
			b.Decisions = d
		}
		if c, err := s.Items.ChangesSince(ctx, since, b.Day); err != nil {
			b.Warnings = append(b.Warnings, "mudanças recentes indisponíveis: "+err.Error())
		} else {
			b.Changes = c
		}
	}
	return b, nil
}

// Event wraps an agenda item with the owner's part of its previous meeting.
func (s *Service) Event(it agenda.Item) Event {
	e := Event{Item: it}
	if it.Previous == nil {
		return e
	}
	var owner [][]string
	for _, n := range s.Owner {
		owner = append(owner, people.Tokens(n))
	}
	for _, a := range it.Previous.OpenActions {
		if isOwner(a.People, owner) {
			e.MyActions = append(e.MyActions, a)
		} else {
			e.OtherActions++
		}
	}
	return e
}

func isOwner(names []string, owner [][]string) bool {
	for _, n := range names {
		t := people.Tokens(n)
		for _, o := range owner {
			if len(t) == len(o) && people.Contains(t, o) {
				return true
			}
		}
	}
	return false
}

func (s *Service) maxMine() int {
	if s.MaxMine > 0 {
		return s.MaxMine
	}
	return 12
}

func (s *Service) maxWaiting() int {
	if s.MaxWaiting > 0 {
		return s.MaxWaiting
	}
	return 15
}

// capRecent keeps the n most recent actions.
func capRecent(a []Action, n int) []Action {
	sort.SliceStable(a, func(i, j int) bool { return a[i].AgeDays < a[j].AgeDays })
	if len(a) > n {
		a = a[:n]
	}
	return a
}

func startOfDay(t time.Time) time.Time {
	y, m, d := t.Date()
	return time.Date(y, m, d, 0, 0, 0, 0, t.Location())
}

// previousWorkday is the start of the last weekday before day (Friday for a
// Monday).
func previousWorkday(day time.Time) time.Time {
	d := startOfDay(day).AddDate(0, 0, -1)
	for d.Weekday() == time.Saturday || d.Weekday() == time.Sunday {
		d = d.AddDate(0, 0, -1)
	}
	return d
}
