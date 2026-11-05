// Package service installs elan's daemon as a macOS LaunchAgent.
package service

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"text/template"
	"time"
)

// Agent describes the LaunchAgent of one profile.
type Agent struct {
	Profile string
	Bin     string // absolute path of the elan binary
	Home    string // user home
}

// Label is the launchd label: one agent per profile.
func (a Agent) Label() string { return "elan." + a.Profile }

// PlistPath is where the agent definition lives.
func (a Agent) PlistPath() string {
	return filepath.Join(a.Home, "Library", "LaunchAgents", a.Label()+".plist")
}

// LogPath receives the daemon's stdout and stderr.
func (a Agent) LogPath() string {
	return filepath.Join(a.Home, "Library", "Logs", "elan", a.Profile+".log")
}

// PATH for the daemon: claude lives in ~/.local/bin, ollama and osascript
// helpers in the Homebrew and system dirs.
func (a Agent) path() string {
	return strings.Join([]string{
		filepath.Join(a.Home, ".local", "bin"),
		"/opt/homebrew/bin", "/usr/local/bin", "/usr/bin", "/bin", "/usr/sbin", "/sbin",
	}, ":")
}

var plist = template.Must(template.New("plist").Parse(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>Label</key><string>{{.Label}}</string>
	<key>ProgramArguments</key>
	<array>
		<string>{{.Bin}}</string>
		<string>daemon</string>
		<string>--profile={{.Profile}}</string>
	</array>
	<key>EnvironmentVariables</key>
	<dict>
		<key>PATH</key><string>{{.Path}}</string>
		<key>HOME</key><string>{{.Home}}</string>
	</dict>
	<key>RunAtLoad</key><true/>
	<key>KeepAlive</key><true/>
	<key>ThrottleInterval</key><integer>60</integer>
	<key>ProcessType</key><string>Background</string>
	<key>StandardOutPath</key><string>{{.Log}}</string>
	<key>StandardErrorPath</key><string>{{.Log}}</string>
</dict>
</plist>
`))

// Render returns the plist.
func (a Agent) Render() ([]byte, error) {
	if !filepath.IsAbs(a.Bin) {
		return nil, fmt.Errorf("caminho do binário precisa ser absoluto: %s", a.Bin)
	}
	var b bytes.Buffer
	err := plist.Execute(&b, map[string]string{
		"Label": a.Label(), "Bin": a.Bin, "Profile": a.Profile,
		"Path": a.path(), "Home": a.Home, "Log": a.LogPath(),
	})
	return b.Bytes(), err
}

func (a Agent) target() string { return fmt.Sprintf("gui/%d/%s", os.Getuid(), a.Label()) }

// Install writes the plist and (re)loads the agent, which starts it.
func (a Agent) Install() error {
	if _, err := os.Stat(a.Bin); err != nil {
		return fmt.Errorf("binário não encontrado (rode `make install`): %w", err)
	}
	body, err := a.Render()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(a.PlistPath()), 0o755); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(a.LogPath()), 0o700); err != nil {
		return err
	}
	if err := os.WriteFile(a.PlistPath(), body, 0o644); err != nil {
		return err
	}
	a.stop()
	// bootstrap can still fail with EIO while launchd tears the old job
	// down; retry for a few seconds.
	var out []byte
	for i := 0; i < 10; i++ {
		if out, err = exec.Command("launchctl", "bootstrap", fmt.Sprintf("gui/%d", os.Getuid()), a.PlistPath()).CombinedOutput(); err == nil {
			return nil
		}
		time.Sleep(time.Second)
	}
	return fmt.Errorf("launchctl bootstrap: %v: %s", err, bytes.TrimSpace(out))
}

// stop unloads the agent and waits (up to 10 s) until launchd forgets it:
// bootout returns before the job is gone.
func (a Agent) stop() {
	exec.Command("launchctl", "bootout", a.target()).Run() //nolint:errcheck // not loaded is fine
	for i := 0; i < 20; i++ {
		if exec.Command("launchctl", "print", a.target()).Run() != nil {
			return
		}
		time.Sleep(500 * time.Millisecond)
	}
}

// Uninstall stops the agent and removes the plist.
func (a Agent) Uninstall() error {
	a.stop()
	if err := os.Remove(a.PlistPath()); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

// Status reports whether the agent is loaded and its state and pid.
func (a Agent) Status() (string, error) {
	out, err := exec.Command("launchctl", "print", a.target()).CombinedOutput()
	if err != nil {
		return "não instalado", nil
	}
	var state, pid string
	for _, l := range strings.Split(string(out), "\n") {
		l = strings.TrimSpace(l)
		if v, ok := strings.CutPrefix(l, "state = "); ok && state == "" {
			state = v
		}
		if v, ok := strings.CutPrefix(l, "pid = "); ok && pid == "" {
			pid = v
		}
	}
	s := "carregado, estado: " + state
	if pid != "" {
		s += ", pid " + pid
	}
	return s, nil
}
