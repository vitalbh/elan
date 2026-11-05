// Package config resolves the active profile and its directories. It is the
// only package that reads the environment.
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// DefaultProfile is used when no profile is given.
const DefaultProfile = "work"

var profileName = regexp.MustCompile(`^[a-z][a-z0-9-]{0,31}$`)

// Config is the resolved configuration of one profile.
type Config struct {
	Profile string
	// Dir holds every file of the profile (credentials, token, database).
	Dir      string
	DBPath   string
	Google   Google
	Meetings Meetings
	Embed    Embed
	Owner    Owner
	Tasks    Tasks
	// People are groups of names of the same person (aliases), e.g.
	// ["Roberto", "Beto"].
	People   [][]string
	Briefing Briefing
	// Projects map project names said in meetings to their folders.
	Projects map[string]Project
}

// Briefing configures the daily briefing.
type Briefing struct {
	// At is the earliest time ("HH:MM", local) the briefing of a weekday is
	// generated; it comes out on the first daemon tick after it.
	At string `json:"at"`
}

// DefaultBriefingAt is used when config.json does not set briefing.at.
const DefaultBriefingAt = "08:50"

// Owner identifies the profile's owner in meetings.
type Owner struct {
	// Names are the exact names (and nicknames) the owner appears with in
	// meeting notes, matched case-insensitively against whole names.
	Names []string `json:"names"`
}

// Tasks configures the export of the owner's action items to Google Tasks.
type Tasks struct {
	// List is the Google Tasks list the actions go to (created if missing).
	List string `json:"list"`
}

// file is the optional per-profile config.json.
type file struct {
	Owner    Owner              `json:"owner"`
	Tasks    Tasks              `json:"tasks"`
	People   [][]string         `json:"people"`
	Briefing Briefing           `json:"briefing"`
	Projects map[string]Project `json:"projects"`
}

// DefaultTasksList is the list used when config.json does not name one.
const DefaultTasksList = "elan · reuniões"

// Embed configures the local embedding model served by Ollama.
type Embed struct {
	// URL of the Ollama server.
	URL string
	// Model is the Ollama embedding model. Changing it requires
	// `elan index --rebuild`.
	Model string
}

// Meetings configures the meeting ingestion.
type Meetings struct {
	// FolderID is the Drive folder Meet saves to. Empty: the "Google Meet"
	// folder at the root of My Drive.
	FolderID string
}

// Google holds the paths of the profile's OAuth files.
type Google struct {
	CredentialsFile string
	TokenFile       string
}

// Load resolves the profile's configuration. ELAN_HOME overrides the root
// directory (default: the user config dir, ~/Library/Application Support on
// macOS).
func Load(profile string) (Config, error) {
	if profile == "" {
		profile = DefaultProfile
	}
	if !profileName.MatchString(profile) {
		return Config{}, fmt.Errorf("perfil inválido %q: use letras minúsculas, dígitos e hífen", profile)
	}
	root := os.Getenv("ELAN_HOME")
	if root == "" {
		base, err := os.UserConfigDir()
		if err != nil {
			return Config{}, fmt.Errorf("diretório de config do usuário: %w", err)
		}
		root = filepath.Join(base, "elan")
	}
	dir := filepath.Join(root, profile)
	f, err := readFile(filepath.Join(dir, "config.json"))
	if err != nil {
		return Config{}, err
	}
	if f.Tasks.List == "" {
		f.Tasks.List = DefaultTasksList
	}
	if f.Briefing.At == "" {
		f.Briefing.At = DefaultBriefingAt
	}
	if _, err := time.Parse("15:04", f.Briefing.At); err != nil {
		return Config{}, fmt.Errorf("briefing.at inválido %q: use HH:MM", f.Briefing.At)
	}
	return Config{
		Profile: profile,
		Dir:     dir,
		DBPath:  filepath.Join(dir, "elan.db"),
		Google: Google{
			CredentialsFile: filepath.Join(dir, "google-credentials.json"),
			TokenFile:       filepath.Join(dir, "google-token.json"),
		},
		Meetings: Meetings{FolderID: os.Getenv("ELAN_MEET_FOLDER_ID")},
		Embed: Embed{
			URL:   envOr("ELAN_OLLAMA_URL", "http://127.0.0.1:11434"),
			Model: envOr("ELAN_EMBED_MODEL", "embeddinggemma"),
		},
		Owner:    f.Owner,
		Tasks:    f.Tasks,
		People:   f.People,
		Briefing: f.Briefing,
		Projects: cleanProjects(f.Projects),
	}, nil
}

func readFile(path string) (file, error) {
	var f file
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return f, nil
	}
	if err != nil {
		return f, err
	}
	if err := json.Unmarshal(b, &f); err != nil {
		return f, fmt.Errorf("%s: %w", path, err)
	}
	names := f.Owner.Names[:0]
	for _, n := range f.Owner.Names {
		if n = strings.TrimSpace(n); n != "" {
			names = append(names, n)
		}
	}
	f.Owner.Names = names
	return f, nil
}

// EnsureDir creates the profile directory with owner-only permissions.
func (c Config) EnsureDir() error {
	if err := os.MkdirAll(c.Dir, 0o700); err != nil {
		return err
	}
	return os.Chmod(c.Dir, 0o700)
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
