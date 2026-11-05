package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadProjects(t *testing.T) {
	root := t.TempDir()
	home := t.TempDir()
	t.Setenv("ELAN_HOME", root)
	t.Setenv("HOME", home)
	dir := filepath.Join(root, "work")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	cfg := `{"projects": {
		"Vega": "~/go/src/vega",
		"Blue Orion Web": {"path": "~/Dev/BlueOrionWeb", "aliases": ["ccweb", " "], "description": " front-end web ", "base": "main"},
		"Nimbus": {"path": "/abs/nimbus"}
	}}`
	if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := Load("work")
	if err != nil {
		t.Fatal(err)
	}
	if got := c.Projects["Vega"]; got.Path != filepath.Join(home, "go/src/vega") || len(got.Aliases) != 0 {
		t.Errorf("string form: %+v", got)
	}
	web := c.Projects["Blue Orion Web"]
	if web.Path != filepath.Join(home, "Dev/BlueOrionWeb") || len(web.Aliases) != 1 || web.Aliases[0] != "ccweb" || web.Description != "front-end web" {
		t.Errorf("object form: %+v", web)
	}
	if c.Projects["Nimbus"].Path != "/abs/nimbus" {
		t.Errorf("absolute path: %+v", c.Projects["Nimbus"])
	}

	if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte(`{"projects": {"X": 3}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load("work"); err == nil {
		t.Error("invalid project entry: want error")
	}
}
