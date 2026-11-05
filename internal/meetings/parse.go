// Package meetings ingests meeting notes and transcripts into the database.
package meetings

import (
	"regexp"
	"strings"
	"time"
)

// Parsed is the structure extracted from a meeting doc.
type Parsed struct {
	Title     string
	StartedAt *time.Time
	// Header holds the lines before the notes (date, title, invitees,
	// attachments), kept for the distiller.
	Header     string
	Notes      string
	Transcript string
}

// docDate matches the start time Meet puts in doc names:
// "<title> - 2026/10/30 14:32 GMT-03:00 - Anotações do Gemini", or, for a
// meeting without a calendar event, "Reunião iniciada às 2026/10/01 10:51
// GMT-03:00 - Anotações do Gemini".
var docDate = regexp.MustCompile(`(\d{4}/\d{2}/\d{2} \d{2}:\d{2}) GMT([+-]\d{2}):?(\d{2})`)

var titleSep = regexp.MustCompile(`\s+-\s*$`)

// ParseName extracts the title and start time from a Meet doc name. The
// title is the text before " - <date>"; without that separator (meetings
// with no calendar event) it is the name up to the date. Names without a
// date keep the whole name as title and a nil time.
func ParseName(name string) (string, *time.Time) {
	loc := docDate.FindStringSubmatchIndex(name)
	if loc == nil {
		return strings.TrimSpace(name), nil
	}
	m := func(i int) string { return name[loc[2*i]:loc[2*i+1]] }
	title := strings.TrimSpace(name[:loc[1]])
	if prefix := name[:loc[0]]; titleSep.MatchString(prefix) {
		title = strings.TrimSpace(titleSep.ReplaceAllString(prefix, ""))
	}
	t, err := time.Parse("2006/01/02 15:04 -0700", m(1)+" "+m(2)+m(3))
	if err != nil {
		return title, nil
	}
	return title, &t
}

// ParseDoc splits the plain-text export of a Meet doc.
//
// The export is: a header (date, title, invitees, attachments), the notes
// starting at the first section title, and, when transcription was on, the
// transcript: the date line again, a title line, then "HH:MM:SS" markers
// and "Speaker: text" lines.
func ParseDoc(name, text string) Parsed {
	title, started := ParseName(name)
	p := Parsed{Title: title, StartedAt: started}

	text = strings.TrimPrefix(text, "\ufeff")
	text = strings.ReplaceAll(text, "\r\n", "\n")
	lines := strings.Split(text, "\n")

	body, transcript := splitTranscript(lines)
	p.Transcript = clean(transcript)

	notesAt := firstSection(body)
	p.Header = clean(body[:notesAt])
	p.Notes = clean(body[notesAt:])
	return p
}

func isTimestamp(s string) bool {
	s = strings.TrimSpace(s)
	if len(s) != 8 || s[2] != ':' || s[5] != ':' {
		return false
	}
	for i, c := range s {
		if i == 2 || i == 5 {
			continue
		}
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

// splitTranscript cuts at the first timestamp, moving back to the repeated
// date line that opens the transcript when it is right above.
func splitTranscript(lines []string) (body, transcript []string) {
	first := -1
	for i, l := range lines {
		if isTimestamp(l) {
			first = i
			break
		}
	}
	if first < 0 {
		return lines, nil
	}
	cut := first
	date := strings.TrimSpace(lines[0])
	for i := first - 1; i >= 0 && i >= first-3; i-- {
		if date != "" && strings.TrimSpace(lines[i]) == date {
			cut = i
			break
		}
	}
	if cut == 0 { // the date match was the header itself
		cut = first
	}
	return lines[:cut], lines[cut:]
}

// sectionTitles open the notes, in the languages Gemini writes them.
var sectionTitles = []string{"resumo", "summary", "resumen", "próximas etapas", "next steps", "detalhes", "details"}

func firstSection(lines []string) int {
	for i, l := range lines {
		t := strings.ToLower(strings.TrimSpace(l))
		for _, s := range sectionTitles {
			if t == s {
				return i
			}
		}
	}
	// Unknown layout: everything is notes.
	return 0
}

// clean joins lines, trims them and collapses runs of blank lines.
func clean(lines []string) string {
	var b strings.Builder
	blank := 0
	for _, l := range lines {
		l = strings.TrimRight(l, " \t")
		if strings.TrimSpace(l) == "" {
			blank++
			continue
		}
		if b.Len() > 0 {
			if blank > 0 {
				b.WriteString("\n\n")
			} else {
				b.WriteString("\n")
			}
		}
		blank = 0
		b.WriteString(l)
	}
	return b.String()
}
