package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"

	"golang.org/x/text/unicode/norm"
)

// Port of startCodeTask in ../herdr/lib.js: elan marks the task started and
// returns branch, project folder and prompt; the git worktree is made here
// (elan never touches git). Same place as the Herdr mod
// (~/.herdr/worktrees/<repo>/<branch>), so a task started in one frontend
// opens in the same worktree from the other.

type taskWorkspace struct {
	Path   string // the worktree
	Branch string
	Prompt string
	Title  string
	Fresh  bool // the worktree was created now (first start)
}

func startCodeTask(c cfg, id int, title string) (taskWorkspace, error) {
	var s started
	if err := c.run(&s, "codetasks", "start", fmt.Sprint(id)); err != nil {
		return taskWorkspace{}, err
	}
	if s.Folder == "" {
		return taskWorkspace{}, fmt.Errorf("projeto %q sem pasta no config.json do elan", s.Project)
	}
	top, err := git(s.Folder, "rev-parse", "--show-toplevel")
	if err != nil {
		return taskWorkspace{}, fmt.Errorf("%s não é um repositório git", s.Folder)
	}
	home, _ := os.UserHomeDir()
	path := filepath.Join(home, ".herdr", "worktrees", filepath.Base(top), branchSlug(s.Branch))
	ws := taskWorkspace{Path: path, Branch: s.Branch, Prompt: s.Prompt, Title: title}

	if fileExists(filepath.Join(path, ".git")) {
		return ws, nil
	}
	_, refErr := git(top, "show-ref", "--verify", "--quiet", "refs/heads/"+s.Branch)
	exists := refErr == nil
	args := []string{"worktree", "add", path, s.Branch}
	if !exists {
		// Where the branch starts: the project's "base" in config.json (e.g.
		// "vega/master" when origin is a stale fork), else the org remote's
		// default branch, else HEAD.
		base := "HEAD"
		if entry := projectEntry(c, s.Project); entry.Base != "" {
			_, _ = git(top, "fetch", "--quiet", strings.SplitN(entry.Base, "/", 2)[0])
			base = entry.Base
		} else if d := remoteDefault(c, top); d != "" {
			base = d
		}
		args = []string{"worktree", "add", "-b", s.Branch, path, base}
	}
	if _, err := git(top, args...); err != nil {
		return taskWorkspace{}, fmt.Errorf("git worktree: %v", err)
	}
	ws.Fresh = true
	return ws, nil
}

func git(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		msg := firstLine(string(out))
		if msg == "" {
			msg = err.Error()
		}
		return "", errors.New(msg)
	}
	return strings.TrimSpace(string(out)), nil
}

var slugBad = regexp.MustCompile(`[^A-Za-z0-9._-]+`)

func branchSlug(b string) string { return strings.Trim(slugBad.ReplaceAllString(b, "-"), "-") }

type project struct {
	Path    string   `json:"path"`
	Base    string   `json:"base"`
	Aliases []string `json:"aliases"`
}

func fold(s string) string {
	var b strings.Builder
	for _, r := range norm.NFD.String(strings.ToLower(strings.TrimSpace(s))) {
		if r < 0x300 || r > 0x36f {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// projectEntry finds the project in config.json like elan does (case and
// accents ignored, aliases included).
func projectEntry(c cfg, name string) project {
	var conf struct {
		Projects map[string]json.RawMessage `json:"projects"`
	}
	data, err := os.ReadFile(filepath.Join(c.profileDir(), "config.json"))
	if err != nil || json.Unmarshal(data, &conf) != nil {
		return project{}
	}
	for k, raw := range conf.Projects {
		var p project
		if json.Unmarshal(raw, &p) != nil {
			var path string
			if json.Unmarshal(raw, &path) != nil {
				continue
			}
			p = project{Path: path}
		}
		for _, n := range append([]string{k}, p.Aliases...) {
			if fold(n) == fold(name) {
				return p
			}
		}
	}
	return project{}
}

// remoteDefault: the default branch of the org remote — the first of
// config.json "base_remotes" (default ["upstream"]) that exists, else
// origin — after fetching it. "" when nothing answers.
func remoteDefault(c cfg, repo string) string {
	prefer := []string{"upstream"}
	var conf struct {
		BaseRemotes []string `json:"base_remotes"`
	}
	if data, err := os.ReadFile(filepath.Join(c.profileDir(), "config.json")); err == nil {
		if json.Unmarshal(data, &conf) == nil && len(conf.BaseRemotes) > 0 {
			prefer = conf.BaseRemotes
		}
	}
	out, _ := git(repo, "remote")
	remotes := strings.Fields(out)
	has := func(r string) bool {
		for _, x := range remotes {
			if x == r {
				return true
			}
		}
		return false
	}
	remote := ""
	for _, r := range prefer {
		if has(r) {
			remote = r
			break
		}
	}
	if remote == "" && has("origin") {
		remote = "origin"
	}
	if remote == "" {
		return ""
	}
	if _, err := git(repo, "fetch", "--quiet", remote); err != nil {
		return ""
	}
	head, err := git(repo, "ls-remote", "--symref", remote, "HEAD")
	if err != nil {
		return ""
	}
	m := regexp.MustCompile(`(?m)^ref: refs/heads/(\S+)\s+HEAD`).FindStringSubmatch(head)
	if m == nil {
		return ""
	}
	return remote + "/" + m[1]
}
