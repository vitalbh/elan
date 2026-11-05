// Package timeline answers "how did this topic evolve?": the distilled items
// of every meeting about it, in date order, with what replaced what.
package timeline

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/vitalbh/elan/internal/search"
)

// Searcher is the hybrid search over distilled items.
type Searcher interface {
	Search(ctx context.Context, q search.Query) ([]search.Result, error)
}

// Query asks for a topic's timeline.
type Query struct {
	Topic        string
	Person       string
	Since, Until time.Time
	// Limit caps the items (default 40).
	Limit int
}

// Item is a distilled item on the timeline.
type Item struct {
	ID     int64    `json:"id"`
	Kind   string   `json:"kind"`
	Text   string   `json:"text"`
	People []string `json:"people,omitempty"`
	// Status of an action (open, done, dropped).
	Status string `json:"status,omitempty"`
	// ReplacedBy is the text of the newer item that replaced this one, and
	// ReplacedOn its meeting's date.
	ReplacedBy string     `json:"replaced_by,omitempty"`
	ReplacedOn *time.Time `json:"replaced_on,omitempty"`
}

// Entry is one meeting on the timeline with its items about the topic.
type Entry struct {
	MeetingID string    `json:"meeting_id"`
	Title     string    `json:"title"`
	Date      time.Time `json:"date"`
	Items     []Item    `json:"items"`
}

// Timeline is a topic's history, newest meeting first.
type Timeline struct {
	Topic   string  `json:"topic"`
	Entries []Entry `json:"entries"`
}

// Service builds timelines.
type Service struct {
	db     *sql.DB
	search Searcher
}

// New builds a Service. search should not cap results per meeting.
func New(db *sql.DB, s Searcher) *Service { return &Service{db: db, search: s} }

// maxVecOnlyRank keeps a hit found only by the vector search when it ranks
// this high: the vector search always returns its k nearest, relevant or
// not, and a timeline full of loosely related items is worse than a short
// one.
const maxVecOnlyRank = 5

// Build searches the topic in the distilled items (replaced ones included)
// and lays the hits out by meeting date, newest first.
func (s *Service) Build(ctx context.Context, q Query) (Timeline, error) {
	if strings.TrimSpace(q.Topic) == "" {
		return Timeline{}, fmt.Errorf("assunto vazio")
	}
	if q.Limit <= 0 {
		q.Limit = 40
	}
	res, err := s.search.Search(ctx, search.Query{
		Text:              q.Topic,
		Person:            q.Person,
		Since:             q.Since,
		Until:             q.Until,
		SourceTypes:       []string{search.SourceMeetingItem},
		IncludeSuperseded: true,
		Limit:             q.Limit * 2,
	})
	if err != nil {
		return Timeline{}, err
	}
	var ids []int64
	for _, r := range res {
		if r.BM25Rank == 0 && r.VecRank > maxVecOnlyRank {
			continue
		}
		if id, err := strconv.ParseInt(r.SourceID, 10, 64); err == nil {
			ids = append(ids, id)
		}
		if len(ids) == q.Limit {
			break
		}
	}

	byMeeting := map[string]*Entry{}
	for _, id := range ids {
		it, e, err := s.load(ctx, id)
		if err == sql.ErrNoRows {
			continue
		}
		if err != nil {
			return Timeline{}, err
		}
		if byMeeting[e.MeetingID] == nil {
			byMeeting[e.MeetingID] = &e
		}
		byMeeting[e.MeetingID].Items = append(byMeeting[e.MeetingID].Items, it)
	}
	t := Timeline{Topic: q.Topic}
	for _, e := range byMeeting {
		sort.SliceStable(e.Items, func(i, j int) bool { return kindOrder(e.Items[i].Kind) < kindOrder(e.Items[j].Kind) })
		t.Entries = append(t.Entries, *e)
	}
	// Newest first: where the topic stands now, then how it got there.
	sort.Slice(t.Entries, func(i, j int) bool { return t.Entries[i].Date.After(t.Entries[j].Date) })
	return t, nil
}

func (s *Service) load(ctx context.Context, id int64) (Item, Entry, error) {
	var it Item
	var e Entry
	var people, started, status string
	var replacedBy sql.NullString
	var replacedOn sql.NullString
	err := s.db.QueryRowContext(ctx, `
		SELECT i.id, i.kind, i.text, i.people, COALESCE(i.status, ''), m.id, m.title, COALESCE(m.started_at, ''),
			r.text, rm.started_at
		FROM meeting_items i
		JOIN meetings m ON m.id = i.meeting_id
		LEFT JOIN meeting_items r ON r.id = i.superseded_by
		LEFT JOIN meetings rm ON rm.id = r.meeting_id
		WHERE i.id = ?`, id,
	).Scan(&it.ID, &it.Kind, &it.Text, &people, &status, &e.MeetingID, &e.Title, &started, &replacedBy, &replacedOn)
	if err != nil {
		return it, e, err
	}
	_ = json.Unmarshal([]byte(people), &it.People)
	if it.Kind == "action" {
		it.Status = status
	}
	e.Date, _ = time.Parse(time.RFC3339, started)
	if replacedBy.Valid {
		it.ReplacedBy = replacedBy.String
		if t, err := time.Parse(time.RFC3339, replacedOn.String); err == nil {
			it.ReplacedOn = &t
		}
	}
	return it, e, nil
}

func kindOrder(k string) int {
	switch k {
	case "decision":
		return 0
	case "fact":
		return 1
	case "action":
		return 2
	default:
		return 3
	}
}

var kindLabel = map[string]string{"decision": "Decisão", "fact": "Fato", "action": "Ação", "summary": "Resumo"}

var statusLabel = map[string]string{"done": " (feita)", "dropped": " (cancelada)"}

// Markdown renders the timeline for the MCP tool and the terminal.
func (t Timeline) Markdown() string {
	var b strings.Builder
	fmt.Fprintf(&b, "# Linha do tempo: %s\n", t.Topic)
	if len(t.Entries) == 0 {
		b.WriteString("\nNada encontrado nas reuniões destiladas.\n")
		return b.String()
	}
	for _, e := range t.Entries {
		date := "sem data"
		if !e.Date.IsZero() {
			date = e.Date.Local().Format("02/01/2006")
		}
		fmt.Fprintf(&b, "\n## %s — %s\n", date, e.Title)
		for _, it := range e.Items {
			fmt.Fprintf(&b, "- **%s:** %s%s", kindLabel[it.Kind], it.Text, statusLabel[it.Status])
			if it.ReplacedBy != "" {
				when := ""
				if it.ReplacedOn != nil {
					when = " em " + it.ReplacedOn.Local().Format("02/01")
				}
				fmt.Fprintf(&b, " — ~~substituída%s por:~~ %s", when, it.ReplacedBy)
			}
			b.WriteString("\n")
		}
	}
	return b.String()
}
