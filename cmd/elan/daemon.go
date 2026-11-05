package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/vitalbh/elan/internal/config"
	"github.com/vitalbh/elan/internal/daemon"
	"github.com/vitalbh/elan/internal/db"
	"github.com/vitalbh/elan/internal/embed"
	"github.com/vitalbh/elan/internal/google"
	"github.com/vitalbh/elan/internal/llm"
	"github.com/vitalbh/elan/internal/lock"
	"github.com/vitalbh/elan/internal/meetings"
	"github.com/vitalbh/elan/internal/notify"
	"github.com/vitalbh/elan/internal/search"
	"github.com/vitalbh/elan/internal/service"
)

const (
	distillPerCycle = 10
	tasksWindow     = 7 * 24 * time.Hour
)

// daemonMain runs `elan daemon`: sync → distill → index → tasks export →
// tasks status back-sync → tasks status push in a loop. It is what the LaunchAgent runs.
func daemonMain(args []string) int {
	fs := flag.NewFlagSet("daemon", flag.ExitOnError)
	profile := fs.String("profile", config.DefaultProfile, "perfil")
	interval := fs.Duration("interval", 30*time.Minute, "intervalo entre ciclos")
	once := fs.Bool("once", false, "roda um ciclo e sai")
	fs.Parse(args) //nolint:errcheck // ExitOnError

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := runDaemon(ctx, *profile, *interval, *once); err != nil {
		fmt.Fprintln(os.Stderr, "erro:", err)
		return 1
	}
	return 0
}

func runDaemon(ctx context.Context, profile string, interval time.Duration, once bool) error {
	cfg, err := config.Load(profile)
	if err != nil {
		return err
	}
	if err := cfg.EnsureDir(); err != nil {
		return err
	}
	l, err := lock.Try(filepath.Join(cfg.Dir, "daemon.lock"))
	if errors.Is(err, lock.ErrHeld) {
		return errors.New("o daemon deste perfil já está rodando")
	}
	if err != nil {
		return err
	}
	defer l.Release()

	log := slog.New(slog.NewJSONHandler(os.Stderr, nil)).With("profile", cfg.Profile)
	conn, err := db.Open(cfg.DBPath)
	if err != nil {
		return err
	}
	defer conn.Close()
	hc, err := google.HTTPClient(ctx, cfg.Google)
	if err != nil {
		return err
	}
	drive, err := google.NewDrive(ctx, hc)
	if err != nil {
		return err
	}
	tasksClient, err := google.NewTasks(ctx, hc)
	if err != nil {
		return err
	}

	syncer := meetings.NewService(conn, &meetSource{drive: drive, folderID: cfg.Meetings.FolderID})
	distiller := meetings.NewDistiller(conn, &llm.ClaudeCLI{})
	distiller.Related = newRelated(cfg, conn)
	distiller.Model = "sonnet"
	distiller.KnownProjects = projectsOf(cfg).List()
	searcher := search.NewService(conn, embed.NewOllama(cfg.Embed))
	exporter := meetings.NewTaskExporter(conn, tasksClient, cfg.Owner.Names)
	taskStatus := meetings.NewTaskStatusSync(conn, taskStatusSource{tasks: tasksClient})
	taskPush := meetings.NewTaskPush(conn, taskWriter{tasks: tasksClient})
	var notifier notify.Notifier = notify.MacOS{}

	d := &daemon.Daemon{
		Interval: interval,
		Log:      log,
		Steps: []daemon.Step{
			{Name: "sync", Run: func(ctx context.Context) error {
				st, err := syncer.Sync(ctx)
				if st.New+st.Updated+st.Failed > 0 {
					log.Info("reuniões sincronizadas", "new", st.New, "updated", st.Updated, "failed", st.Failed)
				}
				return err
			}},
			{Name: "distill", Run: func(ctx context.Context) error {
				return withDistillLock(cfg, func() error {
					st, err := distiller.Distill(ctx, distillPerCycle, func(string) {})
					if st.Distilled+st.Failed > 0 {
						log.Info("reuniões destiladas", "distilled", st.Distilled, "failed", st.Failed, "pending", st.Pending,
							"actions_done", st.Completed, "actions_dropped", st.Dropped)
					}
					return err
				})
			}},
			{Name: "index", Run: func(ctx context.Context) error {
				_, err := searcher.Index(ctx, search.IndexOptions{})
				return err
			}},
			{Name: "tasks", Run: func(ctx context.Context) error {
				if len(cfg.Owner.Names) == 0 {
					return nil
				}
				pending, err := exporter.Pending(ctx, time.Now().Add(-tasksWindow))
				if err != nil {
					return err
				}
				n, err := exporter.Export(ctx, cfg.Tasks.List, pending)
				if n > 0 {
					log.Info("tarefas criadas", "count", n, "list", cfg.Tasks.List)
					body := fmt.Sprintf("%d tarefa(s) nova(s) das reuniões em %q", n, cfg.Tasks.List)
					if err := notifier.Notify(ctx, "elan", body); err != nil {
						log.Warn("notificação falhou", "err", err)
					}
					emitEvent(cfg, log, "tasks", "Tarefas novas", body, "", "")
				}
				return err
			}},
			{Name: "taskstatus", Run: func(ctx context.Context) error {
				st, err := taskStatus.Sync(ctx)
				if st.Done+st.Dropped+st.Reopened > 0 {
					log.Info("status das tarefas", "done", st.Done, "dropped", st.Dropped, "reopened", st.Reopened)
				}
				return err
			}},
			{Name: "taskpush", Run: func(ctx context.Context) error {
				st, err := taskPush.Push(ctx, false)
				if st.Pushed+st.Gone+st.Failed > 0 {
					log.Info("status enviado às tarefas", "pushed", st.Pushed, "gone", st.Gone, "failed", st.Failed)
				}
				return err
			}},
		},
	}
	cal := newCalendar(ctx, cfg)
	brief, ag := newBriefing(cfg, conn, cal)
	mac := notify.MacOS{}
	d.Jobs = []daemon.Job{
		briefingJob(cfg, brief, mac, log),
		reminderJob(cfg, cal, brief, ag, mac, log),
	}
	log.Info("daemon iniciado", "interval", interval.String(), "briefing", cfg.Briefing.At)
	if once {
		d.Cycle(ctx)
		return nil
	}
	return d.Run(ctx)
}

// withDistillLock keeps the daemon and `elan meetings distill` from
// distilling (and spending Claude usage on) the same meetings at once.
func withDistillLock(cfg config.Config, fn func() error) error {
	l, err := lock.Try(filepath.Join(cfg.Dir, "distill.lock"))
	if err != nil {
		return err
	}
	defer l.Release()
	return fn()
}

// serviceMain runs `elan service install|uninstall|status`.
func serviceMain(action string, args []string) int {
	fs := flag.NewFlagSet("service", flag.ExitOnError)
	profile := fs.String("profile", config.DefaultProfile, "perfil")
	home, _ := os.UserHomeDir()
	bin := fs.String("bin", filepath.Join(home, ".local", "bin", "elan"), "binário que o LaunchAgent roda")
	fs.Parse(args) //nolint:errcheck // ExitOnError

	if _, err := config.Load(*profile); err != nil {
		fmt.Fprintln(os.Stderr, "erro:", err)
		return 1
	}
	a := service.Agent{Profile: *profile, Bin: *bin, Home: home}
	var err error
	switch action {
	case "install":
		if err = a.Install(); err == nil {
			fmt.Printf("LaunchAgent %s instalado e iniciado.\nLog: %s\n", a.Label(), a.LogPath())
		}
	case "uninstall":
		if err = a.Uninstall(); err == nil {
			fmt.Printf("LaunchAgent %s removido.\n", a.Label())
		}
	case "status":
		var s string
		if s, err = a.Status(); err == nil {
			fmt.Printf("%s: %s\nLog: %s\n", a.Label(), s, a.LogPath())
		}
	default:
		err = fmt.Errorf("uso: elan service install|uninstall|status")
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "erro:", err)
		return 1
	}
	return 0
}
