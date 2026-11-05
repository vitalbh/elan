package mcpserver

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/vitalbh/elan/internal/agenda"
	"github.com/vitalbh/elan/internal/google"
)

// Calendar is the owner's Google Calendar; *google.Calendar implements it.
type Calendar interface {
	Location() *time.Location
	Events(ctx context.Context, from, to time.Time) ([]google.Event, error)
	FreeSlots(ctx context.Context, from, to time.Time, min time.Duration) ([]google.Slot, error)
	Create(ctx context.Context, n google.NewEvent) (google.Event, error)
	Respond(ctx context.Context, id, response, comment string) (google.Event, error)
}

// Agenda builds a day's agenda with meeting context; *agenda.Service
// implements it.
type Agenda interface {
	Agenda(ctx context.Context, day time.Time) (agenda.Agenda, error)
}

// registerCalendar adds the calendar tools when the server has a calendar.
func registerCalendar(s *mcp.Server, h *handlers) {
	if h.d.Agenda != nil {
		mcp.AddTool(s, &mcp.Tool{
			Name: "calendar_agenda",
			Description: "Agenda de um dia (padrão: hoje) com contexto: cada evento traz, quando existe, a reunião anterior da mesma série " +
				"(resumo e ações ainda abertas). Use para \"o que tenho hoje/amanhã\" e para se preparar para uma reunião.",
		}, h.calendarAgenda)
	}
	if h.d.Calendar == nil {
		return
	}
	mcp.AddTool(s, &mcp.Tool{
		Name:        "calendar_events",
		Description: "Eventos da agenda principal do dono num período (recorrências expandidas, cancelados fora), com convidados, respostas e link do Meet.",
	}, h.calendarEvents)
	mcp.AddTool(s, &mcp.Tool{
		Name:        "calendar_free_slots",
		Description: "Horários livres do dono num período, dentro do horário de trabalho (seg a sex). Eventos de dia inteiro, marcados como livres e convites recusados não ocupam tempo.",
	}, h.calendarFreeSlots)
	mcp.AddTool(s, &mcp.Tool{
		Name: "calendar_create_event",
		Description: "CRIA UM EVENTO DE VERDADE na agenda principal do dono. Com convidados, o Google envia o convite por email a eles. " +
			"Só use quando o dono pedir explicitamente; confirme título, data, horário e convidados antes.",
	}, h.calendarCreate)
	mcp.AddTool(s, &mcp.Tool{
		Name: "calendar_respond",
		Description: "RESPONDE DE VERDADE a um convite na agenda do dono (aceitar, recusar, talvez); o organizador é avisado. " +
			"Só use quando o dono pedir explicitamente. Em evento recorrente, o id da ocorrência responde só aquela ocorrência.",
	}, h.calendarRespond)
}

// AgendaIn is the input of calendar_agenda.
type AgendaIn struct {
	Day string `json:"day,omitempty" jsonschema:"dia (AAAA-MM-DD); padrão hoje"`
}

func (h *handlers) calendarAgenda(ctx context.Context, _ *mcp.CallToolRequest, in AgendaIn) (*mcp.CallToolResult, agenda.Agenda, error) {
	day, err := parseDay(in.Day)
	if err != nil {
		return nil, agenda.Agenda{}, err
	}
	if day.IsZero() {
		day = time.Now()
	}
	a, err := h.d.Agenda.Agenda(ctx, day)
	if err != nil {
		return nil, agenda.Agenda{}, err
	}
	if a.Items == nil {
		a.Items = []agenda.Item{}
	}
	return nil, a, nil
}

// RangeIn is a period of the calendar.
type RangeIn struct {
	From string `json:"from,omitempty" jsonschema:"início: AAAA-MM-DD ou AAAA-MM-DDTHH:MM (horário local); padrão hoje 00:00"`
	To   string `json:"to,omitempty" jsonschema:"fim: AAAA-MM-DD (dia incluído) ou AAAA-MM-DDTHH:MM; padrão um dia depois do início"`
}

func (h *handlers) period(in RangeIn) (from, to time.Time, err error) {
	loc := h.d.Calendar.Location()
	from, _, err = parseMoment(in.From, loc)
	if err != nil {
		return
	}
	if from.IsZero() {
		n := time.Now().In(loc)
		from = time.Date(n.Year(), n.Month(), n.Day(), 0, 0, 0, 0, loc)
	}
	var dateOnly bool
	to, dateOnly, err = parseMoment(in.To, loc)
	switch {
	case err != nil:
		return
	case to.IsZero():
		to = from.AddDate(0, 0, 1)
	case dateOnly:
		to = to.AddDate(0, 0, 1) // the day is included
	}
	if !to.After(from) {
		err = fmt.Errorf("o fim (%s) precisa ser depois do início (%s)", to.Format("2006-01-02 15:04"), from.Format("2006-01-02 15:04"))
	}
	return
}

// EventsOut is the output of calendar_events.
type EventsOut struct {
	Events []google.Event `json:"events"`
}

func (h *handlers) calendarEvents(ctx context.Context, _ *mcp.CallToolRequest, in RangeIn) (*mcp.CallToolResult, EventsOut, error) {
	from, to, err := h.period(in)
	if err != nil {
		return nil, EventsOut{}, err
	}
	events, err := h.d.Calendar.Events(ctx, from, to)
	if err != nil {
		return nil, EventsOut{}, err
	}
	if events == nil {
		events = []google.Event{}
	}
	return nil, EventsOut{Events: events}, nil
}

// FreeSlotsIn is the input of calendar_free_slots.
type FreeSlotsIn struct {
	RangeIn
	MinMinutes int `json:"min_minutes,omitempty" jsonschema:"duração mínima de cada janela em minutos (padrão 30)"`
}

// FreeSlot is a free window.
type FreeSlot struct {
	Start   time.Time `json:"start"`
	End     time.Time `json:"end"`
	Minutes int       `json:"minutes"`
}

// FreeSlotsOut is the output of calendar_free_slots.
type FreeSlotsOut struct {
	Slots []FreeSlot `json:"slots"`
}

func (h *handlers) calendarFreeSlots(ctx context.Context, _ *mcp.CallToolRequest, in FreeSlotsIn) (*mcp.CallToolResult, FreeSlotsOut, error) {
	from, to, err := h.period(in.RangeIn)
	if err != nil {
		return nil, FreeSlotsOut{}, err
	}
	min := clamp(in.MinMinutes, 30, 8*60)
	slots, err := h.d.Calendar.FreeSlots(ctx, from, to, time.Duration(min)*time.Minute)
	if err != nil {
		return nil, FreeSlotsOut{}, err
	}
	out := FreeSlotsOut{Slots: make([]FreeSlot, 0, len(slots))}
	for _, s := range slots {
		out.Slots = append(out.Slots, FreeSlot{Start: s.Start, End: s.End, Minutes: s.Minutes()})
	}
	return nil, out, nil
}

// CreateEventIn is the input of calendar_create_event.
type CreateEventIn struct {
	Title           string   `json:"title" jsonschema:"título do evento"`
	Start           string   `json:"start" jsonschema:"início: AAAA-MM-DDTHH:MM (horário local)"`
	End             string   `json:"end,omitempty" jsonschema:"fim: AAAA-MM-DDTHH:MM; ou use duration_minutes"`
	DurationMinutes int      `json:"duration_minutes,omitempty" jsonschema:"duração em minutos quando end não vier (padrão 30)"`
	Attendees       []string `json:"attendees,omitempty" jsonschema:"emails dos convidados (recebem o convite)"`
	Description     string   `json:"description,omitempty" jsonschema:"descrição do evento"`
	Meet            bool     `json:"meet,omitempty" jsonschema:"adiciona link do Google Meet"`
}

func (h *handlers) calendarCreate(ctx context.Context, _ *mcp.CallToolRequest, in CreateEventIn) (*mcp.CallToolResult, google.Event, error) {
	loc := h.d.Calendar.Location()
	start, dateOnly, err := parseMoment(in.Start, loc)
	if err != nil {
		return nil, google.Event{}, err
	}
	if start.IsZero() || dateOnly {
		return nil, google.Event{}, fmt.Errorf("start precisa de data e hora (AAAA-MM-DDTHH:MM)")
	}
	end, endDateOnly, err := parseMoment(in.End, loc)
	if err != nil {
		return nil, google.Event{}, err
	}
	if endDateOnly {
		return nil, google.Event{}, fmt.Errorf("end precisa de data e hora (AAAA-MM-DDTHH:MM)")
	}
	if end.IsZero() {
		end = start.Add(time.Duration(clamp(in.DurationMinutes, 30, 24*60)) * time.Minute)
	}
	ev, err := h.d.Calendar.Create(ctx, google.NewEvent{
		Title: in.Title, Start: start, End: end, Description: in.Description, Attendees: in.Attendees, Meet: in.Meet,
	})
	return nil, ev, err
}

// RespondIn is the input of calendar_respond.
type RespondIn struct {
	EventID  string `json:"event_id" jsonschema:"id do evento (de calendar_events ou calendar_agenda)"`
	Response string `json:"response" jsonschema:"accepted, declined ou tentative"`
	Comment  string `json:"comment,omitempty" jsonschema:"comentário opcional para o organizador"`
}

func (h *handlers) calendarRespond(ctx context.Context, _ *mcp.CallToolRequest, in RespondIn) (*mcp.CallToolResult, google.Event, error) {
	if strings.TrimSpace(in.EventID) == "" {
		return nil, google.Event{}, fmt.Errorf("event_id é obrigatório")
	}
	ev, err := h.d.Calendar.Respond(ctx, strings.TrimSpace(in.EventID), strings.TrimSpace(in.Response), in.Comment)
	return nil, ev, err
}

// parseMoment reads AAAA-MM-DD (dateOnly), AAAA-MM-DDTHH:MM[:SS] or RFC 3339
// in loc. Empty is the zero time.
func parseMoment(s string, loc *time.Location) (t time.Time, dateOnly bool, err error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}, false, nil
	}
	if t, err := time.ParseInLocation("2006-01-02", s, loc); err == nil {
		return t, true, nil
	}
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t.In(loc), false, nil
	}
	for _, layout := range []string{"2006-01-02T15:04:05", "2006-01-02T15:04", "2006-01-02 15:04"} {
		if t, err := time.ParseInLocation(layout, s, loc); err == nil {
			return t, false, nil
		}
	}
	return time.Time{}, false, fmt.Errorf("data/hora inválida %q: use AAAA-MM-DD ou AAAA-MM-DDTHH:MM", s)
}
