package main

import "testing"

func TestDropName(t *testing.T) {
	names := []string{"Ana Souza", "Ana"}
	cases := map[string]string{
		"Ana Souza vai enviar o link.": "Enviar o link.",
		"ana vai revisar o PR.":        "Revisar o PR.",
		"Bruno vai testar.":            "Bruno vai testar.",
	}
	for in, want := range cases {
		if got := dropName(in, names); got != want {
			t.Errorf("dropName(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestBranchSlugAndFold(t *testing.T) {
	if got := branchSlug("elan/ajustar player!"); got != "elan-ajustar-player" {
		t.Errorf("branchSlug = %q", got)
	}
	if fold(" Blue Orión ") != fold("blue orion") {
		t.Error("fold should ignore case, accents and outer spaces")
	}
}

func TestClipAndAgo(t *testing.T) {
	if got := clip("abc   def ghi", 7); got != "abc de…" {
		t.Errorf("clip = %q", got)
	}
	if ago(0) != "hoje" || ago(1) != "ontem" || ago(5) != "há 5 dias" {
		t.Error(ago(0), ago(1), ago(5))
	}
}
