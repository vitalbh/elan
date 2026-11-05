package daemon

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/vitalbh/elan/internal/llm"
)

func TestCyclePausesOnlyTheLimitedStep(t *testing.T) {
	now := time.Date(2026, 10, 31, 10, 0, 0, 0, time.UTC)
	var calls []string
	limited := true
	d := &Daemon{
		Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
		Steps: []Step{
			{Name: "sync", Run: func(context.Context) error { calls = append(calls, "sync"); return nil }},
			{Name: "distill", Run: func(context.Context) error {
				calls = append(calls, "distill")
				if limited {
					return &llm.LimitError{ResetAt: now.Add(2 * time.Hour)}
				}
				return nil
			}},
			{Name: "index", Run: func(context.Context) error { calls = append(calls, "index"); return errors.New("ollama fora") }},
		},
		now: func() time.Time { return now },
	}
	ctx := context.Background()

	d.Cycle(ctx)
	if got := len(calls); got != 3 {
		t.Fatalf("first cycle %v", calls)
	}

	calls = nil
	now = now.Add(time.Hour) // still before the reset
	d.Cycle(ctx)
	if len(calls) != 2 || calls[0] != "sync" || calls[1] != "index" {
		t.Fatalf("paused cycle %v, want distill skipped and failed index retried", calls)
	}

	calls = nil
	limited = false
	now = now.Add(90 * time.Minute) // after the reset
	d.Cycle(ctx)
	if len(calls) != 3 {
		t.Fatalf("resumed cycle %v", calls)
	}
}

func TestLimitWithoutResetUsesFallback(t *testing.T) {
	now := time.Date(2026, 10, 31, 10, 0, 0, 0, time.UTC)
	n := 0
	d := &Daemon{
		Log:           slog.New(slog.NewTextHandler(io.Discard, nil)),
		LimitFallback: 30 * time.Minute,
		Steps:         []Step{{Name: "distill", Run: func(context.Context) error { n++; return &llm.LimitError{} }}},
		now:           func() time.Time { return now },
	}
	d.Cycle(context.Background())
	now = now.Add(20 * time.Minute)
	d.Cycle(context.Background())
	now = now.Add(15 * time.Minute)
	d.Cycle(context.Background())
	if n != 2 {
		t.Fatalf("runs %d, want 2 (paused in between)", n)
	}
}

func TestRunJobsOnlyWhenDue(t *testing.T) {
	now := time.Date(2026, 11, 2, 8, 49, 0, 0, time.UTC)
	runs := 0
	d := &Daemon{
		Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
		Jobs: []Job{{
			Name: "briefing",
			Due:  func(n time.Time) bool { return !n.Before(time.Date(2026, 11, 2, 8, 50, 0, 0, time.UTC)) && runs == 0 },
			Run:  func(context.Context) error { runs++; return nil },
		}},
		now: func() time.Time { return now },
	}
	d.RunJobs(context.Background())
	if runs != 0 {
		t.Fatal("ran before due")
	}
	now = now.Add(2 * time.Minute)
	d.RunJobs(context.Background())
	d.RunJobs(context.Background())
	if runs != 1 {
		t.Fatalf("runs %d, want 1", runs)
	}
}
