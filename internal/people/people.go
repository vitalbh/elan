// Package people matches person names the way they appear in meetings:
// accent- and case-insensitive whole-word sequences, plus aliases.
package people

import (
	"strings"
	"unicode"

	"golang.org/x/text/unicode/norm"
)

// Directory holds groups of names that refer to the same person, e.g.
// {"Roberto", "Beto"}.
type Directory struct {
	groups [][]string
}

// NewDirectory builds a Directory from alias groups (from config.json).
func NewDirectory(groups [][]string) *Directory {
	d := &Directory{}
	for _, g := range groups {
		var clean []string
		for _, n := range g {
			if len(Tokens(n)) > 0 {
				clean = append(clean, strings.TrimSpace(n))
			}
		}
		if len(clean) > 0 {
			d.groups = append(d.groups, clean)
		}
	}
	return d
}

// Expand returns every name to look for when searching for name: the name
// itself plus the names of each group with an entry containing it ("Paulo"
// expands to the group of "Paulo Ramos"). A nil Directory only returns
// the name.
func (d *Directory) Expand(name string) []string {
	want := Tokens(name)
	if len(want) == 0 {
		return nil
	}
	out := []string{strings.TrimSpace(name)}
	if d == nil {
		return out
	}
	seen := map[string]bool{strings.Join(want, " "): true}
	for _, g := range d.groups {
		hit := false
		for _, n := range g {
			if Contains(Tokens(n), want) {
				hit = true
				break
			}
		}
		if !hit {
			continue
		}
		for _, n := range g {
			k := strings.Join(Tokens(n), " ")
			if !seen[k] {
				seen[k] = true
				out = append(out, n)
			}
		}
	}
	return out
}

// Tokens lowercases, strips accents and splits on anything that is not a
// letter or digit.
func Tokens(s string) []string {
	var b strings.Builder
	for _, r := range norm.NFD.String(strings.ToLower(s)) {
		switch {
		case unicode.Is(unicode.Mn, r):
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			b.WriteRune(r)
		default:
			b.WriteRune(' ')
		}
	}
	return strings.Fields(b.String())
}

// Contains reports whether want appears in have as a contiguous run.
func Contains(have, want []string) bool {
	if len(want) == 0 {
		return false
	}
	for i := 0; i+len(want) <= len(have); i++ {
		match := true
		for j, w := range want {
			if have[i+j] != w {
				match = false
				break
			}
		}
		if match {
			return true
		}
	}
	return false
}
