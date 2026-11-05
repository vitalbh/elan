package meetings

import (
	"strings"
	"testing"
	"time"
)

func TestParseName(t *testing.T) {
	cases := []struct {
		name, title string
		start       string // RFC 3339, empty = nil
	}{
		{"Ana / Bia - 2026/10/30 14:32 GMT-03:00 - Anotações do Gemini", "Ana / Bia", "2026-10-30T14:32:00-03:00"},
		{"Alinhamento  - 2026/10/29 14:00 GMT-03:00 - Notes by Gemini", "Alinhamento", "2026-10-29T14:00:00-03:00"},
		{"Weekly - 2026/01/05 09:00 GMT+01:00 - Transcript", "Weekly", "2026-01-05T09:00:00+01:00"},
		{"Reunião iniciada às 2026/10/01 10:51 GMT-03:00 - Anotações do Gemini", "Reunião iniciada às 2026/10/01 10:51 GMT-03:00", "2026-10-01T10:51:00-03:00"},
		{"Documento qualquer", "Documento qualquer", ""},
	}
	for _, c := range cases {
		title, start := ParseName(c.name)
		if title != c.title {
			t.Errorf("%q: title %q, want %q", c.name, title, c.title)
		}
		switch {
		case c.start == "" && start != nil:
			t.Errorf("%q: start %v, want nil", c.name, start)
		case c.start != "":
			want, _ := time.Parse(time.RFC3339, c.start)
			if start == nil || !start.Equal(want) {
				t.Errorf("%q: start %v, want %v", c.name, start, want)
			}
		}
	}
}

const docWithTranscript = "\ufeffout. 29, 2026\n" +
	"Reunião X\n" +
	"convidado Ana Bia\n" +
	"Anexos Reunião X\n" +
	"Registros da reunião Transcrição\n" +
	"\n\n\n" +
	"Resumo\n" +
	"Decidimos usar Y.\n" +
	"\n" +
	"Próximas etapas\n" +
	"Ana vai fazer Z.\n" +
	"\n\n" +
	"Revise as anotações do Gemini.\n" +
	"out. 29, 2026\n" +
	"Reunião X - Transcrição\n" +
	"00:01:26\n" +
	"\n" +
	"Ana: Boa tarde.\n" +
	"Bia: Oi.\n" +
	"\n" +
	"A transcrição foi encerrada após 00:17:06\n"

func TestParseDocWithTranscript(t *testing.T) {
	p := ParseDoc("Reunião X - 2026/10/29 14:00 GMT-03:00 - Anotações do Gemini", docWithTranscript)
	if p.Title != "Reunião X" {
		t.Errorf("title %q", p.Title)
	}
	if !strings.HasPrefix(p.Header, "out. 29, 2026\nReunião X\nconvidado Ana Bia") {
		t.Errorf("header %q", p.Header)
	}
	if !strings.HasPrefix(p.Notes, "Resumo\nDecidimos usar Y.") || !strings.Contains(p.Notes, "Ana vai fazer Z.") {
		t.Errorf("notes %q", p.Notes)
	}
	if strings.Contains(p.Notes, "00:01:26") || strings.Contains(p.Notes, "Boa tarde") {
		t.Errorf("transcript leaked into notes: %q", p.Notes)
	}
	if !strings.HasPrefix(p.Transcript, "out. 29, 2026\nReunião X - Transcrição\n00:01:26") {
		t.Errorf("transcript %q", p.Transcript)
	}
	if !strings.Contains(p.Transcript, "Bia: Oi.") {
		t.Errorf("transcript lost lines: %q", p.Transcript)
	}
}

func TestParseDocWithoutTranscript(t *testing.T) {
	text := "out. 30, 2026\nReunião Y\n\nResumo\nNada.\n\nDetalhes\nMais nada.\n"
	p := ParseDoc("Reunião Y - 2026/10/30 10:00 GMT-03:00 - Anotações do Gemini", text)
	if p.Transcript != "" {
		t.Errorf("transcript %q, want empty", p.Transcript)
	}
	if p.Notes != "Resumo\nNada.\n\nDetalhes\nMais nada." {
		t.Errorf("notes %q", p.Notes)
	}
}

func TestParseDocUnknownLayout(t *testing.T) {
	p := ParseDoc("Solto", "linha 1\nlinha 2\n")
	if p.Header != "" || p.Notes != "linha 1\nlinha 2" {
		t.Errorf("header %q notes %q", p.Header, p.Notes)
	}
}
