// Package notify shows notifications to the owner. macOS only for now.
package notify

import (
	"context"
	"os/exec"
)

// Notifier shows a notification.
type Notifier interface {
	Notify(ctx context.Context, title, body string) error
}

// MacOS shows notifications through Notification Center: terminal-notifier
// when installed (it can open a URL on click), osascript otherwise.
type MacOS struct{}

// NotifyOpen shows a notification that opens url when clicked. Without
// terminal-notifier the click does nothing.
func (m MacOS) NotifyOpen(ctx context.Context, title, body, url string) error {
	if bin, err := exec.LookPath("terminal-notifier"); err == nil {
		// It fails when macOS has not allowed its notifications yet; the
		// plain notification still reaches the owner.
		if exec.CommandContext(ctx, bin, "-title", title, "-message", body, "-open", url, "-group", "elan").Run() == nil {
			return nil
		}
	}
	return m.Notify(ctx, title, body)
}

// Notify shows a notification. Title and body go as script arguments, so
// their text is never parsed as AppleScript.
func (MacOS) Notify(ctx context.Context, title, body string) error {
	return exec.CommandContext(ctx, "osascript",
		"-e", "on run argv",
		"-e", "display notification (item 2 of argv) with title (item 1 of argv)",
		"-e", "end run",
		title, body,
	).Run()
}
