package meetings

import (
	"fmt"
	"strings"
)

// DocURL is the link to a meeting's Google Doc (its id is the Drive id).
func DocURL(id string) string {
	return "https://docs.google.com/document/d/" + id + "/edit"
}

// Markdown renders a meeting for reading: title, date, summary, decisions,
// actions with their status, facts and the link to the Drive doc.
// Superseded decisions and facts are left out.
func Markdown(m Meeting) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# %s\n\n", m.Title)
	if m.StartedAt != nil {
		fmt.Fprintf(&b, "%s", m.StartedAt.Format("02/01/2006 15:04"))
	} else {
		b.WriteString("sem data")
	}
	if m.Recurring {
		fmt.Fprintf(&b, " · série %s", strings.TrimSuffix(m.Folder, " (recurring)"))
	}
	b.WriteString("\n")

	var summary []string
	groups := map[string][]StoredItem{}
	for _, it := range m.Items {
		if it.SupersededBy != nil {
			continue
		}
		if it.Kind == KindSummary {
			summary = append(summary, it.Text)
			continue
		}
		groups[it.Kind] = append(groups[it.Kind], it)
	}
	if len(summary) > 0 {
		fmt.Fprintf(&b, "\n## Resumo\n\n%s\n", strings.Join(summary, "\n\n"))
	} else if !m.Distilled {
		b.WriteString("\n_Reunião ainda não destilada._\n")
	}
	section := func(title, kind string, line func(StoredItem) string) {
		if len(groups[kind]) == 0 {
			return
		}
		fmt.Fprintf(&b, "\n## %s\n\n", title)
		for _, it := range groups[kind] {
			fmt.Fprintf(&b, "- %s\n", line(it))
		}
	}
	plain := func(it StoredItem) string { return oneLine(it.Text) }
	section("Decisões", KindDecision, plain)
	section("Ações", KindAction, func(it StoredItem) string {
		box := "[ ]"
		switch it.Status {
		case StatusDone:
			box = "[x]"
		case StatusDropped:
			box = "[-]"
		}
		s := box + " " + oneLine(it.Text)
		if it.Status == StatusDropped {
			s += " _(cancelada)_"
		}
		return s
	})
	section("Fatos", KindFact, plain)
	fmt.Fprintf(&b, "\n[Abrir no Google Docs](%s)\n", DocURL(m.ID))
	return b.String()
}
