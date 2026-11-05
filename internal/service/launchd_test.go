package service

import (
	"strings"
	"testing"
)

func TestRender(t *testing.T) {
	a := Agent{Profile: "work", Bin: "/Users/x/.local/bin/elan", Home: "/Users/x"}
	b, err := a.Render()
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	for _, want := range []string{
		"<string>elan.work</string>",
		"<string>/Users/x/.local/bin/elan</string>",
		"<string>daemon</string>",
		"<string>--profile=work</string>",
		"/Users/x/.local/bin:/opt/homebrew/bin",
		"/Users/x/Library/Logs/elan/work.log",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("plist missing %q", want)
		}
	}
	if a.PlistPath() != "/Users/x/Library/LaunchAgents/elan.work.plist" {
		t.Errorf("plist path %s", a.PlistPath())
	}
	if _, err := (Agent{Profile: "work", Bin: "bin/elan", Home: "/Users/x"}).Render(); err == nil {
		t.Error("relative binary path should fail")
	}
}
