package briefing

import (
	"fmt"
	"strings"
	"time"
)

var weekdays = [...]string{"domingo", "segunda", "terça", "quarta", "quinta", "sexta", "sábado"}

var months = [...]string{"", "jan", "fev", "mar", "abr", "mai", "jun", "jul", "ago", "set", "out", "nov", "dez"}

func dayLabel(t time.Time) string {
	return fmt.Sprintf("%s, %d %s", weekdays[t.Weekday()], t.Day(), months[t.Month()])
}

func shortDate(t time.Time) string { return t.Format("02/01") }

func timeRange(e Event) string {
	if e.AllDay {
		return "dia todo"
	}
	return e.Start.Format("15:04") + "–" + e.End.Format("15:04")
}

func ago(days int) string {
	switch {
	case days <= 0:
		return "hoje"
	case days == 1:
		return "ontem"
	default:
		return fmt.Sprintf("há %d dias", days)
	}
}

// Markdown renders the briefing for the MCP tool (and the terminal).
func (b Briefing) Markdown() string {
	var w strings.Builder
	fmt.Fprintf(&w, "# Briefing de %s\n", dayLabel(b.Day))
	for _, warn := range b.Warnings {
		fmt.Fprintf(&w, "\n> ⚠️ %s\n", warn)
	}

	w.WriteString("\n## Agenda\n")
	if len(b.Events) == 0 {
		w.WriteString("\nNenhum evento.\n")
	}
	for _, e := range b.Events {
		fmt.Fprintf(&w, "\n- **%s** %s", timeRange(e), e.Title)
		if e.MyResponse == "needsAction" {
			w.WriteString(" _(sem resposta)_")
		}
		w.WriteString("\n")
		if p := e.Previous; p != nil {
			fmt.Fprintf(&w, "  - Última (%s): %s\n", shortDate(p.StartedAt), oneLine(p.Summary, 280))
			for _, a := range e.MyActions {
				fmt.Fprintf(&w, "  - Com você: %s\n", a.Text)
			}
			if e.OtherActions > 0 {
				fmt.Fprintf(&w, "  - +%d pendência(s) de outras pessoas dessa reunião\n", e.OtherActions)
			}
		}
	}

	w.WriteString("\n## Suas pendências\n")
	if len(b.Mine) == 0 {
		w.WriteString("\nNada em aberto.\n")
	}
	for _, a := range b.Mine {
		fmt.Fprintf(&w, "\n- %s _(%s, %s)_", a.Text, a.MeetingTitle, ago(a.AgeDays))
	}
	if more := b.MineTotal - len(b.Mine); more > 0 {
		fmt.Fprintf(&w, "\n- _+%d mais antiga(s)_", more)
	}
	if len(b.Mine) > 0 {
		w.WriteString("\n")
	}

	w.WriteString("\n## Esperando de outros\n")
	if len(b.Waiting) == 0 {
		w.WriteString("\nNada em aberto.\n")
	}
	for _, a := range b.Waiting {
		fmt.Fprintf(&w, "\n- %s _(%s, %s)_", a.Text, a.MeetingTitle, ago(a.AgeDays))
	}
	if more := b.WaitingTotal - len(b.Waiting); more > 0 {
		fmt.Fprintf(&w, "\n- _+%d mais antiga(s)_", more)
	}
	if len(b.Waiting) > 0 {
		w.WriteString("\n")
	}

	if len(b.Changes) > 0 {
		w.WriteString("\n## O que mudou\n")
		for _, c := range b.Changes {
			fmt.Fprintf(&w, "\n- ~~%s~~ → %s _(%s, %s)_", c.Old, c.New, c.Meeting, shortDate(c.At))
		}
		w.WriteString("\n")
	}

	if len(b.Decisions) > 0 {
		w.WriteString("\n## Decisões recentes\n")
		for _, d := range b.Decisions {
			fmt.Fprintf(&w, "\n- %s _(%s, %s)_", d.Text, d.MeetingTitle, shortDate(d.StartedAt))
		}
		w.WriteString("\n")
	}
	return w.String()
}

func oneLine(s string, max int) string {
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > max {
		s = strings.TrimSpace(string(r[:max-1])) + "…"
	}
	return s
}
