package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/vitalbh/elan/internal/config"
	"github.com/vitalbh/elan/internal/db"
	"github.com/vitalbh/elan/internal/google"
	"github.com/vitalbh/elan/internal/meetings"
)

// tasksExportMain runs `elan tasks export`: the owner's action items of
// recent meetings go to a Google Tasks list.
func tasksExportMain(args []string) int {
	fs := flag.NewFlagSet("tasks export", flag.ExitOnError)
	profile := fs.String("profile", config.DefaultProfile, "perfil")
	days := fs.Int("days", 7, "só reuniões dos últimos N dias")
	dryRun := fs.Bool("dry-run", false, "só lista o que seria criado")
	fs.Parse(args) //nolint:errcheck // ExitOnError

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := runTasksExport(ctx, *profile, *days, *dryRun); err != nil {
		fmt.Fprintln(os.Stderr, "erro:", err)
		return 1
	}
	return 0
}

func runTasksExport(ctx context.Context, profile string, days int, dryRun bool) error {
	cfg, err := config.Load(profile)
	if err != nil {
		return err
	}
	conn, err := db.Open(cfg.DBPath)
	if err != nil {
		return err
	}
	defer conn.Close()

	var sink meetings.TaskSink
	if !dryRun {
		hc, err := google.HTTPClient(ctx, cfg.Google)
		if err != nil {
			return err
		}
		if sink, err = google.NewTasks(ctx, hc); err != nil {
			return err
		}
	}
	e := meetings.NewTaskExporter(conn, sink, cfg.Owner.Names)
	since := time.Now().AddDate(0, 0, -days)
	pending, err := e.Pending(ctx, since)
	if err != nil {
		return err
	}
	for _, a := range pending {
		date := ""
		if a.StartedAt != nil {
			date = a.StartedAt.Format("02/01")
		}
		fmt.Printf("- [%s %s] %s\n", date, a.MeetingTitle, a.Text)
	}
	if dryRun {
		fmt.Printf("%d tarefas seriam criadas em %q\n", len(pending), cfg.Tasks.List)
		return nil
	}
	n, err := e.Export(ctx, cfg.Tasks.List, pending)
	fmt.Printf("%d tarefas criadas em %q\n", n, cfg.Tasks.List)
	return err
}

// taskStatusSource adapts the Google Tasks client to meetings.TaskStatusSource.
type taskStatusSource struct{ tasks *google.Tasks }

func (s taskStatusSource) ListTasks(ctx context.Context, listID string, updatedMin time.Time) ([]meetings.TaskState, error) {
	infos, err := s.tasks.ListStatus(ctx, listID, updatedMin)
	if err != nil {
		return nil, err
	}
	out := make([]meetings.TaskState, len(infos))
	for i, t := range infos {
		st := meetings.TaskState{ID: t.ID, Completed: t.Status == "completed", Deleted: t.Deleted, WebLink: t.WebLink}
		at := t.Updated
		if st.Completed && t.Completed != "" {
			at = t.Completed
		}
		st.At, _ = time.Parse(time.RFC3339, at) // zero when missing: the sync uses now
		out[i] = st
	}
	return out, nil
}

// tasksStatusMain runs `elan tasks status`: the daemon's back-sync of the
// exported tasks' status (completed, deleted) to their action items.
func tasksStatusMain(args []string) int {
	fs := flag.NewFlagSet("tasks status", flag.ExitOnError)
	profile := fs.String("profile", config.DefaultProfile, "perfil")
	fs.Parse(args) //nolint:errcheck // ExitOnError

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	err := func() error {
		cfg, err := config.Load(*profile)
		if err != nil {
			return err
		}
		conn, err := db.Open(cfg.DBPath)
		if err != nil {
			return err
		}
		defer conn.Close()
		hc, err := google.HTTPClient(ctx, cfg.Google)
		if err != nil {
			return err
		}
		t, err := google.NewTasks(ctx, hc)
		if err != nil {
			return err
		}
		st, err := meetings.NewTaskStatusSync(conn, taskStatusSource{tasks: t}).Sync(ctx)
		fmt.Printf("listas: %d, tarefas vistas: %d, concluídas: %d, excluídas: %d, reabertas: %d\n",
			st.Lists, st.Seen, st.Done, st.Dropped, st.Reopened)
		return err
	}()
	if err != nil {
		fmt.Fprintln(os.Stderr, "erro:", err)
		return 1
	}
	return 0
}

// newTasks builds the profile's Google Tasks client for the MCP server. The
// token is opened on every call: a missing login turns into a clear error
// from the tasks tools only, and a later `elan google-login` works without a
// restart. ctx must live as long as the client.
func newTasks(ctx context.Context, cfg config.Config) *google.Tasks {
	return google.NewTasksClient(func(context.Context) (*http.Client, error) {
		return google.HTTPClient(ctx, cfg.Google)
	}, google.TasksOptions{Location: time.Local})
}

// taskWriter adapts the Google Tasks client to meetings.TaskWriter.
type taskWriter struct{ tasks *google.Tasks }

func (w taskWriter) GetTask(ctx context.Context, listID, taskID string) (meetings.RemoteTask, error) {
	t, err := w.tasks.Get(ctx, listID, taskID)
	if errors.Is(err, google.ErrTaskNotFound) {
		return meetings.RemoteTask{}, meetings.ErrTaskGone
	}
	return meetings.RemoteTask{Completed: t.Done, Notes: t.Notes}, err
}

func (w taskWriter) SetTask(ctx context.Context, listID, taskID string, rt meetings.RemoteTask) error {
	done, notes := rt.Completed, rt.Notes
	_, err := w.tasks.Update(ctx, listID, taskID, google.TaskPatch{Done: &done, Notes: &notes})
	if errors.Is(err, google.ErrTaskNotFound) {
		return meetings.ErrTaskGone
	}
	return err
}

// tasksPushMain runs `elan tasks push`: the daemon's push of action status
// changes made in elan to their exported tasks.
func tasksPushMain(args []string) int {
	fs := flag.NewFlagSet("tasks push", flag.ExitOnError)
	profile := fs.String("profile", config.DefaultProfile, "perfil")
	dryRun := fs.Bool("dry-run", false, "só lista o que seria enviado, sem chamar o Google")
	fs.Parse(args) //nolint:errcheck // ExitOnError

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	err := func() error {
		cfg, err := config.Load(*profile)
		if err != nil {
			return err
		}
		conn, err := db.Open(cfg.DBPath)
		if err != nil {
			return err
		}
		defer conn.Close()
		var w meetings.TaskWriter
		if !*dryRun {
			w = taskWriter{tasks: newTasks(ctx, cfg)}
		}
		st, err := meetings.NewTaskPush(conn, w).Push(ctx, *dryRun)
		for _, c := range st.Changes {
			fmt.Printf("- [%s] ação %d → %s: %s\n", c.Result, c.ActionID, c.Status, c.Text)
		}
		if *dryRun {
			fmt.Printf("%d tarefas seriam alteradas no Google Tasks\n", len(st.Changes))
		} else {
			fmt.Printf("enviadas: %d, já estavam assim: %d, excluídas no Tasks: %d, falharam: %d\n",
				st.Pushed, st.Unchanged, st.Gone, st.Failed)
		}
		return err
	}()
	if err != nil {
		fmt.Fprintln(os.Stderr, "erro:", err)
		return 1
	}
	return 0
}
