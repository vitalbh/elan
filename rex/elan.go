package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// cfg: which elan and which profile. ELAN_BIN / ELAN_PROFILE override the
// defaults (elan from PATH, profile "work").
type cfg struct {
	bin, profile string
}

func loadCfg() cfg {
	c := cfg{bin: "elan", profile: "work"}
	if v := os.Getenv("ELAN_BIN"); v != "" {
		c.bin = v
	} else if _, err := exec.LookPath("elan"); err != nil {
		if home, _ := os.UserHomeDir(); home != "" {
			if p := filepath.Join(home, ".local", "bin", "elan"); fileExists(p) {
				c.bin = p
			}
		}
	}
	if v := os.Getenv("ELAN_PROFILE"); v != "" {
		c.profile = v
	}
	return c
}

func (c cfg) profileDir() string {
	if root := os.Getenv("ELAN_HOME"); root != "" {
		return filepath.Join(root, c.profile)
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, "Library", "Application Support", "elan", c.profile)
}

// run calls `elan <args> --json --profile=<p>` and decodes stdout into out.
// elan's {"error": "..."} becomes the returned error (docs/cli-json.md).
func (c cfg) run(out any, args ...string) error {
	args = append(args, "--json", "--profile="+c.profile)
	cmd := exec.Command(c.bin, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	runErr := cmd.Run()
	var e struct {
		Error string `json:"error"`
	}
	if json.Unmarshal(stdout.Bytes(), &e) == nil && e.Error != "" {
		return errors.New(e.Error)
	}
	if runErr != nil {
		msg := firstLine(stderr.String())
		if msg == "" {
			msg = firstLine(stdout.String())
		}
		if msg == "" {
			msg = runErr.Error()
		}
		return errors.New(msg)
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(stdout.Bytes(), out); err != nil {
		return fmt.Errorf("resposta do elan não é JSON: %v", err)
	}
	return nil
}

// text runs elan without --json (meeting --markdown). --profile goes right
// after the command: some commands stop reading flags at the first argument.
func (c cfg) text(command string, args ...string) (string, error) {
	cmd := exec.Command(c.bin, append([]string{command, "--profile=" + c.profile}, args...)...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		if msg := firstLine(stderr.String()); msg != "" {
			return "", errors.New(msg)
		}
		return "", err
	}
	return stdout.String(), nil
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	return strings.TrimPrefix(s, "erro: ")
}

func fileExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

// ── contract types (docs/cli-json.md) ─────────────────────────────────

type previous struct {
	ID          string `json:"id"`
	Title       string `json:"title"`
	StartedAt   string `json:"started_at"`
	Summary     string `json:"summary"`
	OpenActions int    `json:"open_actions"`
}

type event struct {
	ID           string    `json:"id"`
	Title        string    `json:"title"`
	Start        time.Time `json:"start"`
	End          time.Time `json:"end"`
	AllDay       bool      `json:"all_day"`
	MeetLink     string    `json:"meet_link"`
	Link         string    `json:"link"`
	MyResponse   string    `json:"my_response"`
	Previous     *previous `json:"previous"`
	MyActions    []action  `json:"my_actions"`
	OtherActions int       `json:"other_actions"`
}

type action struct {
	ID           int      `json:"id"`
	Text         string   `json:"text"`
	People       []string `json:"people"`
	MeetingID    string   `json:"meeting_id"`
	MeetingTitle string   `json:"meeting_title"`
	MeetingAt    string   `json:"meeting_at"`
	MeetingDate  string   `json:"meeting_date"`
	AgeDays      int      `json:"age_days"`
	Mine         bool     `json:"mine"`
	TaskLink     string   `json:"task_link"`
}

type codeTask struct {
	ID           int    `json:"id"`
	Text         string `json:"text"`
	Project      string `json:"project"`
	Folder       string `json:"folder"`
	Context      string `json:"context"`
	Status       string `json:"status"`
	MeetingID    string `json:"meeting_id"`
	MeetingTitle string `json:"meeting_title"`
	AgeDays      int    `json:"age_days"`
	StartedAt    string `json:"started_at"`
	Branch       string `json:"branch"`
	ItemID       int    `json:"item_id"` // suggestions: the action to promote
}

type briefing struct {
	Events       []event    `json:"events"`
	Mine         []action   `json:"mine"`
	Waiting      []action   `json:"waiting"`
	MineTotal    int        `json:"mine_total"`
	WaitingTotal int        `json:"waiting_total"`
	CodeTasks    []codeTask `json:"code_tasks"`
	Warnings     []string   `json:"warnings"`
}

type searchHit struct {
	MeetingID string `json:"meeting_id"`
	Title     string `json:"title"`
	Date      string `json:"date"`
	Type      string `json:"type"`
	Kind      string `json:"kind"`
	Text      string `json:"text"`
}

type note struct {
	ID        int       `json:"id"`
	Text      string    `json:"text"`
	Source    string    `json:"source"`
	CreatedAt time.Time `json:"created_at"`
}

type started struct {
	ID      int    `json:"id"`
	Branch  string `json:"branch"`
	Folder  string `json:"folder"`
	Project string `json:"project"`
	Prompt  string `json:"prompt"`
}

// ownerNames: config.json owner.names, to turn "Vital vai X" into "X".
func (c cfg) ownerNames() []string {
	var v struct {
		Owner struct {
			Names []string `json:"names"`
		} `json:"owner"`
	}
	data, err := os.ReadFile(filepath.Join(c.profileDir(), "config.json"))
	if err == nil {
		_ = json.Unmarshal(data, &v)
	}
	return v.Owner.Names
}
