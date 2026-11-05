// Package events appends the daemon's notifications to a JSON Lines file
// (<profile dir>/events/events.jsonl) that frontends (the herdr mod) tail to
// show toasts. The file keeps only the last MaxLines events.
package events

import (
	"bufio"
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Kinds of event.
const (
	KindBriefing = "briefing"
	KindReminder = "reminder"
	KindTasks    = "tasks"
)

// MaxLines is how many events the file keeps.
const MaxLines = 200

// Event is one notification.
type Event struct {
	ID        string    `json:"id"`
	At        time.Time `json:"at"`
	Kind      string    `json:"kind"`
	Title     string    `json:"title"`
	Body      string    `json:"body"`
	MeetingID string    `json:"meeting_id,omitempty"`
	URL       string    `json:"url,omitempty"`
}

// Path returns the events file of a profile directory.
func Path(profileDir string) string {
	return filepath.Join(profileDir, "events", "events.jsonl")
}

var mu sync.Mutex

// Append writes e as one line at the end of the file at path, filling ID and
// At when empty, and trims the file to the last MaxLines lines. Each line is
// written with a single write on an O_APPEND file, so a reader never sees
// half of it; the trim replaces the file atomically (rename).
func Append(path string, e Event) error {
	if e.ID == "" {
		e.ID = newID()
	}
	if e.At.IsZero() {
		e.At = time.Now()
	}
	line, err := json.Marshal(e)
	if err != nil {
		return err
	}
	line = append(line, '\n')

	mu.Lock()
	defer mu.Unlock()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o600)
	if err != nil {
		return err
	}
	_, werr := f.Write(line)
	if err := errors.Join(werr, f.Close()); err != nil {
		return err
	}
	return trim(path, MaxLines)
}

// trim keeps the last max lines of the file at path.
func trim(path string, max int) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var lines [][]byte
	sc := bufio.NewScanner(bytes.NewReader(b))
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	for sc.Scan() {
		if len(bytes.TrimSpace(sc.Bytes())) > 0 {
			lines = append(lines, append([]byte(nil), sc.Bytes()...))
		}
	}
	if err := sc.Err(); err != nil {
		return err
	}
	if len(lines) <= max {
		return nil
	}
	lines = lines[len(lines)-max:]
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(bytes.Join(lines, []byte("\n")), '\n'), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func newID() string {
	var b [6]byte
	_, _ = rand.Read(b[:])
	return time.Now().UTC().Format("20060102T150405") + "-" + hex.EncodeToString(b[:])
}
