package search

import (
	"regexp"
	"strings"
	"unicode/utf8"
)

// maxChunkRunes bounds a chunk: ~300-400 tokens of Portuguese, well inside
// EmbeddingGemma's 2048-token context and small enough to keep one topic.
const maxChunkRunes = 1200

var blankLines = regexp.MustCompile(`\n[ \t]*\n+`)

type unit struct {
	text    string
	newPara bool // starts a paragraph (joined with a blank line)
}

// splitText packs the paragraphs of text into chunks of at most max runes.
// Paragraphs are never merged across that bound; a paragraph longer than max
// is split by lines, and a line longer than max by words.
func splitText(text string, max int) []string {
	text = strings.ReplaceAll(text, "\r\n", "\n")
	var units []unit
	for _, p := range blankLines.Split(text, -1) {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		if utf8.RuneCountInString(p) <= max {
			units = append(units, unit{p, true})
			continue
		}
		first := true
		for _, line := range strings.Split(p, "\n") {
			line = strings.TrimSpace(line)
			if line == "" {
				continue
			}
			for _, piece := range splitWords(line, max) {
				units = append(units, unit{piece, first})
				first = false
			}
		}
	}

	var chunks []string
	var b strings.Builder
	size := 0
	for _, u := range units {
		n := utf8.RuneCountInString(u.text)
		if size > 0 && size+2+n > max {
			chunks = append(chunks, b.String())
			b.Reset()
			size = 0
		}
		if size > 0 {
			if u.newPara {
				b.WriteString("\n\n")
				size += 2
			} else {
				b.WriteString("\n")
				size++
			}
		}
		b.WriteString(u.text)
		size += n
	}
	if size > 0 {
		chunks = append(chunks, b.String())
	}
	return chunks
}

// splitWords cuts a line longer than max runes at word boundaries.
func splitWords(line string, max int) []string {
	if utf8.RuneCountInString(line) <= max {
		return []string{line}
	}
	var out []string
	var b strings.Builder
	size := 0
	for _, w := range strings.Fields(line) {
		n := utf8.RuneCountInString(w)
		if size > 0 && size+1+n > max {
			out = append(out, b.String())
			b.Reset()
			size = 0
		}
		if n > max { // a single huge "word": hard cut
			r := []rune(w)
			for len(r) > max {
				out = append(out, string(r[:max]))
				r = r[max:]
			}
			w, n = string(r), len(r)
		}
		if size > 0 {
			b.WriteByte(' ')
			size++
		}
		b.WriteString(w)
		size += n
	}
	if size > 0 {
		out = append(out, b.String())
	}
	return out
}
