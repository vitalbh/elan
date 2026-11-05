package people

import (
	"reflect"
	"testing"
)

func TestExpand(t *testing.T) {
	d := NewDirectory([][]string{
		{"Roberto", "Beto"},
		{"Paulo Ramos", "Ramos"},
		{"Fernanda Dias", "Fê"},
		{"", "  "},
	})
	cases := []struct {
		in   string
		want []string
	}{
		{"beto", []string{"beto", "Roberto"}},
		{"Roberto", []string{"Roberto", "Beto"}},
		{"Paulo", []string{"Paulo", "Paulo Ramos", "Ramos"}},
		{"fe", []string{"fe", "Fernanda Dias"}},
		{"Fernanda Dias", []string{"Fernanda Dias", "Fê"}},
		{"Ana", []string{"Ana"}},
		{"  ", nil},
	}
	for _, c := range cases {
		if got := d.Expand(c.in); !reflect.DeepEqual(got, c.want) {
			t.Errorf("Expand(%q) = %q, want %q", c.in, got, c.want)
		}
	}
	var nilDir *Directory
	if got := nilDir.Expand("Ana"); !reflect.DeepEqual(got, []string{"Ana"}) {
		t.Errorf("nil directory: %q", got)
	}
}

func TestContains(t *testing.T) {
	have := Tokens("convidado Ana Souza, Mariana Lima")
	for _, c := range []struct {
		want string
		ok   bool
	}{{"ana", true}, {"Ana Souza", true}, {"souza mariana", true}, {"mari", false}, {"lima ana", false}, {"", false}} {
		if got := Contains(have, Tokens(c.want)); got != c.ok {
			t.Errorf("%q: %v, want %v", c.want, got, c.ok)
		}
	}
}
