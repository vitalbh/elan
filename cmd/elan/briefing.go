package main

import (
	"context"
	"database/sql"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/vitalbh/elan/internal/agenda"
	"github.com/vitalbh/elan/internal/briefing"
	"github.com/vitalbh/elan/internal/config"
	"github.com/vitalbh/elan/internal/daemon"
	"github.com/vitalbh/elan/internal/db"
	"github.com/vitalbh/elan/internal/google"
	"github.com/vitalbh/elan/internal/meetings"
	"github.com/vitalbh/elan/internal/notify"
)

// actionsAdapter exposes meetings.Actions as briefing.Actions.
type actionsAdapter struct{ a *meetings.Actions }

func (x actionsAdapter) Mine(ctx context.Context) ([]briefing.Action, error) {
	return x.list(ctx, meetings.OwnerMine)
}

func (x actionsAdapter) Waiting(ctx context.Context) ([]briefing.Action, error) {
	return x.list(ctx, meetings.OwnerOthers)
}

func (x actionsAdapter) list(ctx context.Context, owner meetings.ActionOwner) ([]briefing.Action, error) {
	open, err := x.a.OpenActions(ctx, meetings.ActionQuery{Owner: owner, Limit: 100})
	if err != nil {
		return nil, err
	}
	out := make([]briefing.Action, len(open))
	for i, a := range open {
		out[i] = briefing.Action{ID: a.ID, Text: a.Text, People: a.People, MeetingID: a.MeetingID,
			MeetingTitle: a.MeetingTitle, MeetingDate: a.MeetingAt, AgeDays: a.AgeDays}
	}
	return out, nil
}

func newBriefing(cfg config.Config, conn *sql.DB, cal *google.Calendar) (*briefing.Service, *agenda.Service) {
	reader := meetings.NewReader(conn)
	ag := agenda.New(cal, reader)
	return &briefing.Service{
		Agendas: ag,
		Actions: actionsAdapter{meetings.NewActions(conn, cfg.Owner.Names)},
		Items:   reader,
		Owner:   cfg.Owner.Names,
	}, ag
}

func briefingPath(cfg config.Config, day time.Time) string {
	return filepath.Join(cfg.Dir, "briefing", day.Format("2006-01-02")+".html")
}

// writeBriefing builds the day's briefing and saves its page.
func writeBriefing(ctx context.Context, svc *briefing.Service, cfg config.Config, day time.Time) (string, briefing.Briefing, error) {
	b, err := svc.Build(ctx, day)
	if err != nil {
		return "", b, err
	}
	page, err := b.HTML()
	if err != nil {
		return "", b, err
	}
	path := briefingPath(cfg, day)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return "", b, err
	}
	return path, b, os.WriteFile(path, page, 0o600)
}

func briefingSummary(b briefing.Briefing) string {
	return fmt.Sprintf("%d reuniões · %d pendências suas · %d esperando de outros", len(b.Events), len(b.Mine), len(b.Waiting))
}

// briefingJob generates the briefing once per weekday, on the first tick at
// or after cfg.Briefing.At. If the calendar is unreachable (network not up
// right after waking), it retries every 10 minutes for up to 2 hours before
// settling for a briefing without the agenda.
func briefingJob(cfg config.Config, svc *briefing.Service, n notify.MacOS, log *slog.Logger) daemon.Job {
	at, _ := time.Parse("15:04", cfg.Briefing.At)
	var nextTry time.Time
	due := func(now time.Time) time.Time {
		return time.Date(now.Year(), now.Month(), now.Day(), at.Hour(), at.Minute(), 0, 0, now.Location())
	}
	return daemon.Job{
		Name: "briefing",
		Due: func(now time.Time) bool {
			if now.Weekday() == time.Saturday || now.Weekday() == time.Sunday || now.Before(due(now)) || now.Before(nextTry) {
				return false
			}
			_, err := os.Stat(briefingPath(cfg, now))
			return os.IsNotExist(err)
		},
		Run: func(ctx context.Context) error {
			now := time.Now()
			b, err := svc.Build(ctx, now)
			if err != nil {
				return err
			}
			if len(b.Warnings) > 0 && now.Before(due(now).Add(2*time.Hour)) {
				nextTry = now.Add(10 * time.Minute)
				log.Warn("briefing adiado", "warnings", strings.Join(b.Warnings, "; "), "retry", nextTry)
				return nil
			}
			path, b, err := writeBriefing(ctx, svc, cfg, now)
			if err != nil {
				return err
			}
			log.Info("briefing gerado", "path", path)
			// A click-to-open notification depends on terminal-notifier,
			// which macOS 26 does not let notify: open the page in a
			// background tab, ready when the owner sits down.
			if err := exec.CommandContext(ctx, "open", "-g", path).Run(); err != nil {
				log.Warn("abrir o briefing falhou", "err", err)
			}
			if err := n.NotifyOpen(ctx, "Briefing do dia", briefingSummary(b)+" · aberto no navegador", "file://"+path); err != nil {
				log.Warn("notificação falhou", "err", err)
			}
			emitEvent(cfg, log, "briefing", "Briefing do dia", briefingSummary(b), "", "file://"+path)
			return nil
		},
	}
}

// reminderJob notifies ~10 minutes before each meeting with guests: the
// previous meeting of the series and what the owner owes from it. The
// calendar is read at most every 10 minutes.
func reminderJob(cfg config.Config, cal *google.Calendar, svc *briefing.Service, ag *agenda.Service, n notify.MacOS, log *slog.Logger) daemon.Job {
	var events []google.Event
	var fetched time.Time
	reminded := map[string]bool{}
	return daemon.Job{
		Name: "reminder",
		Due:  func(time.Time) bool { return true },
		Run: func(ctx context.Context) error {
			now := time.Now()
			if now.Sub(fetched) >= 10*time.Minute {
				evs, err := cal.Events(ctx, now, now.Add(2*time.Hour))
				if err != nil {
					return err
				}
				events, fetched = evs, now
			}
			for _, e := range events {
				key := e.ID + e.Start.String()
				left := e.Start.Sub(now)
				if reminded[key] || left <= 8*time.Minute || left > 11*time.Minute ||
					e.AllDay || e.Transparent || e.MyResponse == "declined" || len(e.Attendees) == 0 ||
					(e.EventType != "" && e.EventType != "default") {
					continue
				}
				reminded[key] = true
				items, err := ag.Enrich(ctx, []google.Event{e})
				if err != nil || len(items) == 0 {
					return err
				}
				ev := svc.Event(items[0])
				body := "Sem reunião anterior registrada."
				previous := ""
				if p := ev.Previous; p != nil {
					previous = p.ID
					body = fmt.Sprintf("Última (%s): %s", p.StartedAt.Format("02/01"), clip(p.Summary, 140))
					if len(ev.MyActions) > 0 {
						body += fmt.Sprintf(" · você ficou de %d coisa(s)", len(ev.MyActions))
					}
				}
				link := e.MeetLink
				if link == "" {
					link = e.Link
				}
				if err := n.NotifyOpen(ctx, "Em 10 min: "+e.Title, body, link); err != nil {
					log.Warn("notificação falhou", "err", err)
				}
				emitEvent(cfg, log, "reminder", "Em 10 min: "+e.Title, body, previous, link)
			}
			return nil
		},
	}
}

func clip(s string, max int) string {
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > max {
		return strings.TrimSpace(string(r[:max-1])) + "…"
	}
	return s
}

// briefingMain runs `elan briefing`: builds the day's briefing, saves the
// page, prints the Markdown and optionally opens the page.
func briefingMain(args []string) int {
	fs := flag.NewFlagSet("briefing", flag.ExitOnError)
	profile := fs.String("profile", config.DefaultProfile, "perfil")
	day := fs.String("day", "", "dia (AAAA-MM-DD), padrão hoje")
	open := fs.Bool("open", false, "abre a página no navegador")
	asJSON := fs.Bool("json", false, "saída JSON sem salvar a página (docs/cli-json.md)")
	if _, ok, code := parseArgs(fs, args); !ok {
		return code
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	err := func() error {
		cfg, err := config.Load(*profile)
		if err != nil {
			return err
		}
		d := time.Now()
		if *day != "" {
			if d, err = time.ParseInLocation("2006-01-02", *day, time.Local); err != nil {
				return fmt.Errorf("data inválida %q: use AAAA-MM-DD", *day)
			}
		}
		conn, err := db.Open(cfg.DBPath)
		if err != nil {
			return err
		}
		defer conn.Close()
		svc, _ := newBriefing(cfg, conn, newCalendar(ctx, cfg))
		if *asJSON {
			out, err := buildBriefingJSON(ctx, cfg, conn, svc, d)
			if err == nil {
				err = printJSON(out)
			}
			return err
		}
		path, b, err := writeBriefing(ctx, svc, cfg, d)
		if err != nil {
			return err
		}
		fmt.Print(b.Markdown())
		fmt.Printf("\nPágina: %s\n", path)
		if *open {
			return exec.Command("open", path).Run()
		}
		return nil
	}()
	if err != nil && *asJSON {
		return jsonError(err)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "erro:", err)
		return 1
	}
	return 0
}
