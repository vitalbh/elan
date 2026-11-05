// Package daemon runs elan's steps (sync, distill, index, export) in a loop.
package daemon

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/vitalbh/elan/internal/llm"
	"github.com/vitalbh/elan/internal/lock"
)

// Step is one unit of work of a cycle.
type Step struct {
	Name string
	Run  func(ctx context.Context) error
}

// Job runs at given moments rather than every cycle: it is checked every
// tick and run when Due says so (e.g. the daily briefing, reminders).
type Job struct {
	Name string
	Due  func(now time.Time) bool
	Run  func(ctx context.Context) error
}

// Daemon runs its steps in order every Interval and its jobs when due. A
// step that hits the Claude usage limit is paused until the reset; the
// others keep running. A step that fails is logged and retried on the next
// cycle.
type Daemon struct {
	Steps    []Step
	Jobs     []Job
	Interval time.Duration
	// Tick is how often the daemon wakes up to check (default 1 minute).
	Tick time.Duration
	Log  *slog.Logger
	// LimitFallback is the pause when the limit error has no reset time.
	LimitFallback time.Duration

	now         func() time.Time
	pausedUntil map[string]time.Time
}

// Run starts with a cycle right away (catching up on what was missed while
// the machine was off or asleep) and then wakes up every Tick: a cycle runs
// when Interval of wall-clock time has passed since the last one, and the
// jobs run when due. Wall-clock time matters because Go's monotonic clock
// stops while macOS sleeps: a plain 30-minute ticker could fire up to 30
// minutes after the machine wakes up.
func (d *Daemon) Run(ctx context.Context) error {
	if d.now == nil {
		d.now = time.Now
	}
	tick := d.Tick
	if tick <= 0 {
		tick = time.Minute
	}
	t := time.NewTicker(tick)
	defer t.Stop()
	last := time.Time{}
	for {
		now := d.now().Round(0) // wall clock only
		if last.IsZero() || now.Sub(last) >= d.Interval || now.Before(last) {
			d.Cycle(ctx)
			last = now
		}
		d.RunJobs(ctx)
		select {
		case <-ctx.Done():
			return nil
		case <-t.C:
		}
	}
}

// RunJobs runs the jobs that are due now.
func (d *Daemon) RunJobs(ctx context.Context) {
	if d.now == nil {
		d.now = time.Now
	}
	for _, j := range d.Jobs {
		if ctx.Err() != nil {
			return
		}
		if !j.Due(d.now()) {
			continue
		}
		if err := j.Run(ctx); err != nil && ctx.Err() == nil {
			d.Log.Error("tarefa agendada falhou", "job", j.Name, "err", err)
		}
	}
}

// Cycle runs every step once, skipping paused ones.
func (d *Daemon) Cycle(ctx context.Context) {
	if d.now == nil {
		d.now = time.Now
	}
	if d.pausedUntil == nil {
		d.pausedUntil = map[string]time.Time{}
	}
	if d.LimitFallback == 0 {
		d.LimitFallback = time.Hour
	}
	for _, s := range d.Steps {
		if ctx.Err() != nil {
			return
		}
		if until, ok := d.pausedUntil[s.Name]; ok {
			if d.now().Before(until) {
				d.Log.Info("passo pausado pelo limite do Claude", "step", s.Name, "until", until)
				continue
			}
			delete(d.pausedUntil, s.Name)
		}
		start := d.now()
		err := s.Run(ctx)
		if le, ok := llm.IsLimit(err); ok {
			until := le.ResetAt
			if until.IsZero() || until.Before(d.now()) {
				until = d.now().Add(d.LimitFallback)
			}
			d.pausedUntil[s.Name] = until
			d.Log.Warn("limite do Claude, passo pausado", "step", s.Name, "until", until)
			continue
		}
		switch {
		case errors.Is(err, lock.ErrHeld):
			d.Log.Info("passo pulado: outro processo está rodando", "step", s.Name)
		case err != nil && ctx.Err() == nil:
			d.Log.Error("passo falhou", "step", s.Name, "err", err)
		case err == nil:
			d.Log.Debug("passo ok", "step", s.Name, "took", d.now().Sub(start).Round(time.Millisecond))
		}
	}
}
