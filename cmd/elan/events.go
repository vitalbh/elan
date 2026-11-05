package main

import (
	"log/slog"

	"github.com/vitalbh/elan/internal/config"
	"github.com/vitalbh/elan/internal/events"
)

// emitEvent appends a daemon notification to the profile's events file, for
// frontends that show toasts (kind: events.KindBriefing, KindReminder or
// KindTasks). A failure is only logged.
func emitEvent(cfg config.Config, log *slog.Logger, kind, title, body, meetingID, url string) {
	e := events.Event{Kind: kind, Title: title, Body: body, MeetingID: meetingID, URL: url}
	if err := events.Append(events.Path(cfg.Dir), e); err != nil {
		log.Warn("evento não registrado", "kind", kind, "err", err)
	}
}
