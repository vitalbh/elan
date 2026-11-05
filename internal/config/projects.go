package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Project maps a project, as named in meetings, to its folder on disk.
type Project struct {
	// Path is the folder of the project's repository ("~" expanded).
	Path string `json:"path"`
	// Aliases are other names the project goes by in meetings.
	Aliases []string `json:"aliases,omitempty"`
	// Description says what the project is (front-end, backend, ...), to
	// help the model tell similar projects apart. Other fields of the entry
	// (e.g. "base", used by frontends) are ignored here.
	Description string `json:"description,omitempty"`
}

// UnmarshalJSON accepts both forms of an entry of "projects": a string (the
// path) or an object {"path": "...", "aliases": [...]}.
func (p *Project) UnmarshalJSON(b []byte) error {
	var path string
	if err := json.Unmarshal(b, &path); err == nil {
		*p = Project{Path: path}
		return nil
	}
	type plain Project
	var v plain
	if err := json.Unmarshal(b, &v); err != nil {
		return fmt.Errorf("projeto: use \"caminho\" ou {\"path\": ..., \"aliases\": [...]}: %w", err)
	}
	*p = Project(v)
	return nil
}

// cleanProjects expands "~" in the paths and drops empty aliases.
func cleanProjects(in map[string]Project) map[string]Project {
	if len(in) == 0 {
		return nil
	}
	home, _ := os.UserHomeDir()
	out := make(map[string]Project, len(in))
	for name, p := range in {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		p.Path = expandHome(strings.TrimSpace(p.Path), home)
		p.Description = strings.TrimSpace(p.Description)
		aliases := make([]string, 0, len(p.Aliases))
		for _, a := range p.Aliases {
			if a = strings.TrimSpace(a); a != "" {
				aliases = append(aliases, a)
			}
		}
		p.Aliases = aliases
		out[name] = p
	}
	return out
}

func expandHome(path, home string) string {
	if home == "" {
		return path
	}
	if path == "~" {
		return home
	}
	if strings.HasPrefix(path, "~/") {
		return filepath.Join(home, path[2:])
	}
	return path
}
