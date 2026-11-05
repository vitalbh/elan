package main

import (
	"context"
	"database/sql"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"sort"
	"strconv"
	"strings"
	"syscall"

	"github.com/vitalbh/elan/internal/config"
	"github.com/vitalbh/elan/internal/db"
	"github.com/vitalbh/elan/internal/llm"
	"github.com/vitalbh/elan/internal/lock"
	"github.com/vitalbh/elan/internal/meetings"
)

// projectsOf builds the project resolver from config.json "projects".
func projectsOf(cfg config.Config) *meetings.Projects {
	list := make([]meetings.Project, 0, len(cfg.Projects))
	for name, p := range cfg.Projects {
		list = append(list, meetings.Project{Name: name, Folder: p.Path, Aliases: p.Aliases, Description: p.Description})
	}
	sort.Slice(list, func(i, j int) bool { return list[i].Name < list[j].Name })
	return meetings.NewProjects(list)
}

func newCodeTasks(cfg config.Config, conn *sql.DB) *meetings.CodeTasks {
	return meetings.NewCodeTasks(conn, cfg.Owner.Names, projectsOf(cfg))
}

// withDB loads the profile and opens its database for fn.
func withDB(profile string, fn func(cfg config.Config, conn *sql.DB) error) error {
	cfg, err := config.Load(profile)
	if err != nil {
		return err
	}
	conn, err := db.Open(cfg.DBPath)
	if err != nil {
		return err
	}
	defer conn.Close()
	return fn(cfg, conn)
}

func signalContext() (context.Context, context.CancelFunc) {
	return signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
}

// codetasksMain runs `elan codetasks`: the owner's code tasks (open ones;
// with --done, also the done and dropped).
func codetasksMain(args []string) int {
	fs := flag.NewFlagSet("codetasks", flag.ExitOnError)
	profile := fs.String("profile", config.DefaultProfile, "perfil")
	done := fs.Bool("done", false, "inclui as concluídas e canceladas")
	n := fs.Int("n", 100, "quantidade")
	asJSON := fs.Bool("json", false, "saída JSON (docs/cli-json.md)")
	if _, ok, code := parseArgs(fs, args); !ok {
		return code
	}
	ctx, stop := signalContext()
	defer stop()
	var list []meetings.CodeTask
	err := withDB(*profile, func(cfg config.Config, conn *sql.DB) error {
		var err error
		list, err = newCodeTasks(cfg, conn).List(ctx, meetings.CodeTaskQuery{IncludeDone: *done, Limit: *n})
		return err
	})
	return finish(*asJSON, list, err, func() {
		for _, t := range list {
			printCodeTask(t)
		}
		fmt.Printf("%d tarefas de código\n", len(list))
	})
}

// printCodeTask prints one code task on one line, for humans.
func printCodeTask(t meetings.CodeTask) {
	where := t.Project
	if where == "" {
		where = "sem projeto"
	}
	if t.Folder == "" {
		where += ", sem pasta"
	}
	if t.MeetingTitle != "" {
		where += "; " + t.MeetingTitle
	}
	status := ""
	if t.Status != meetings.StatusOpen {
		status = " [" + t.Status + "]"
	} else if t.Branch != "" {
		status = " [começada: " + t.Branch + "]"
	}
	fmt.Printf("#%d (%dd, %s)%s %s\n", t.ID, t.AgeDays, where, status, t.Text)
}

// codetasksSuggestionsMain runs `elan codetasks suggestions`: the owner's
// open actions classified as code that are not code tasks yet.
func codetasksSuggestionsMain(args []string) int {
	fs := flag.NewFlagSet("codetasks suggestions", flag.ExitOnError)
	profile := fs.String("profile", config.DefaultProfile, "perfil")
	stale := fs.Bool("stale", false, "inclui as abertas há mais de 30 dias")
	n := fs.Int("n", 50, "quantidade")
	asJSON := fs.Bool("json", false, "saída JSON (docs/cli-json.md)")
	if _, ok, code := parseArgs(fs, args); !ok {
		return code
	}
	ctx, stop := signalContext()
	defer stop()
	var list []meetings.Suggestion
	err := withDB(*profile, func(cfg config.Config, conn *sql.DB) error {
		var err error
		list, err = newCodeTasks(cfg, conn).Suggestions(ctx, meetings.SuggestionQuery{Limit: *n, IncludeStale: *stale})
		return err
	})
	return finish(*asJSON, list, err, func() {
		for _, s := range list {
			where := s.Project
			if where == "" {
				where = "sem projeto"
			}
			fmt.Printf("#%d (%dd, %s; %s) %s\n", s.ItemID, s.AgeDays, where, s.MeetingTitle, s.Text)
		}
		fmt.Printf("%d sugestões (elan codetasks promote <id> vira tarefa)\n", len(list))
	})
}

// codetasksAddMain runs `elan codetasks add "<texto>"`: creates a code task.
func codetasksAddMain(args []string) int {
	fs := flag.NewFlagSet("codetasks add", flag.ExitOnError)
	profile := fs.String("profile", config.DefaultProfile, "perfil")
	project := fs.String("project", "", "projeto (nome em \"projects\" do config.json ou outro)")
	meeting := fs.String("meeting", "", "id da reunião de origem")
	taskContext := fs.String("context", "", "instruções ou notas para o prompt")
	asJSON := fs.Bool("json", false, "saída JSON (docs/cli-json.md)")
	pos, ok, code := parseArgs(fs, args)
	if !ok {
		return code
	}
	ctx, stop := signalContext()
	defer stop()
	var t meetings.CodeTask
	err := func() error {
		text := strings.TrimSpace(strings.Join(pos, " "))
		if text == "" {
			return errors.New(`uso: elan codetasks add "<texto>" [--project=X] [--meeting=<id>] [--context="..."]`)
		}
		return withDB(*profile, func(cfg config.Config, conn *sql.DB) error {
			var err error
			t, err = newCodeTasks(cfg, conn).Add(ctx, meetings.NewCodeTask{
				Text: text, Project: *project, MeetingID: *meeting, Context: *taskContext,
			})
			return err
		})
	}()
	return finish(*asJSON, t, err, func() { fmt.Print("criada: "); printCodeTask(t) })
}

// codetasksPromoteMain runs `elan codetasks promote <item_id>`: makes an
// action a code task (the same one when promoted before).
func codetasksPromoteMain(args []string) int {
	fs := flag.NewFlagSet("codetasks promote", flag.ExitOnError)
	profile := fs.String("profile", config.DefaultProfile, "perfil")
	asJSON := fs.Bool("json", false, "saída JSON (docs/cli-json.md)")
	pos, ok, code := parseArgs(fs, args)
	if !ok {
		return code
	}
	ctx, stop := signalContext()
	defer stop()
	var t meetings.CodeTask
	err := func() error {
		id, err := idArg(pos, "elan codetasks promote <item_id>")
		if err != nil {
			return err
		}
		return withDB(*profile, func(cfg config.Config, conn *sql.DB) error {
			t, err = newCodeTasks(cfg, conn).Promote(ctx, id)
			return err
		})
	}()
	return finish(*asJSON, t, err, func() { printCodeTask(t) })
}

// codetasksUpdateMain runs `elan codetasks update <id> --status=...`: closes
// or reopens a code task (and the action it came from).
func codetasksUpdateMain(args []string) int {
	fs := flag.NewFlagSet("codetasks update", flag.ExitOnError)
	profile := fs.String("profile", config.DefaultProfile, "perfil")
	status := fs.String("status", "", "done, dropped ou open")
	asJSON := fs.Bool("json", false, "saída JSON (docs/cli-json.md)")
	pos, ok, code := parseArgs(fs, args)
	if !ok {
		return code
	}
	ctx, stop := signalContext()
	defer stop()
	var t meetings.CodeTask
	err := func() error {
		id, err := idArg(pos, "elan codetasks update <id> --status=done|dropped|open")
		if err != nil {
			return err
		}
		return withDB(*profile, func(cfg config.Config, conn *sql.DB) error {
			t, err = newCodeTasks(cfg, conn).SetStatus(ctx, id, *status)
			return err
		})
	}()
	out := struct {
		ID     int64  `json:"id"`
		Status string `json:"status"`
	}{t.ID, t.Status}
	return finish(*asJSON, out, err, func() {
		fmt.Printf("#%d → %s: %s\n", t.ID, t.Status, t.Text)
	})
}

// codetasksStartMain runs `elan codetasks start <id>`: marks the task
// started and prints the branch, folder and the prompt for Claude Code.
func codetasksStartMain(args []string) int {
	fs := flag.NewFlagSet("codetasks start", flag.ExitOnError)
	profile := fs.String("profile", config.DefaultProfile, "perfil")
	asJSON := fs.Bool("json", false, "saída JSON (docs/cli-json.md)")
	pos, ok, code := parseArgs(fs, args)
	if !ok {
		return code
	}
	ctx, stop := signalContext()
	defer stop()
	var st meetings.StartedTask
	err := func() error {
		id, err := idArg(pos, "elan codetasks start <id>")
		if err != nil {
			return err
		}
		return withDB(*profile, func(cfg config.Config, conn *sql.DB) error {
			st, err = newCodeTasks(cfg, conn).Start(ctx, id)
			return err
		})
	}()
	return finish(*asJSON, st, err, func() {
		fmt.Printf("Branch: %s\nPasta: %s\n\n%s", st.Branch, st.Folder, st.Prompt)
	})
}

func idArg(pos []string, use string) (int64, error) {
	if len(pos) != 1 {
		return 0, errors.New("uso: " + use)
	}
	id, err := strconv.ParseInt(pos[0], 10, 64)
	if err != nil || id <= 0 {
		return 0, fmt.Errorf("id inválido %q", pos[0])
	}
	return id, nil
}

// actionUpdateMain runs `elan action update <id> --status=...`: sets an
// action's status by hand.
func actionUpdateMain(args []string) int {
	fs := flag.NewFlagSet("action update", flag.ExitOnError)
	profile := fs.String("profile", config.DefaultProfile, "perfil")
	status := fs.String("status", "", "done, dropped ou open")
	asJSON := fs.Bool("json", false, "saída JSON (docs/cli-json.md)")
	pos, ok, code := parseArgs(fs, args)
	if !ok {
		return code
	}
	ctx, stop := signalContext()
	defer stop()
	var st meetings.ActionState
	err := func() error {
		id, err := idArg(pos, "elan action update <id> --status=done|dropped|open")
		if err != nil {
			return err
		}
		return withDB(*profile, func(cfg config.Config, conn *sql.DB) error {
			st, err = meetings.NewActions(conn, cfg.Owner.Names).SetStatus(ctx, id, *status)
			return err
		})
	}()
	out := struct {
		ID     int64  `json:"id"`
		Status string `json:"status"`
	}{st.ID, st.Status}
	return finish(*asJSON, out, err, func() {
		fmt.Printf("#%d → %s: %s\n", st.ID, st.Status, st.Text)
	})
}

// meetingMain runs `elan meeting <id>`: one meeting as Markdown (default),
// --json (meetings.Meeting) or --markdown.
func meetingMain(args []string) int {
	fs := flag.NewFlagSet("meeting", flag.ExitOnError)
	profile := fs.String("profile", config.DefaultProfile, "perfil")
	asJSON := fs.Bool("json", false, "saída JSON (docs/cli-json.md)")
	fs.Bool("markdown", false, "saída Markdown (o padrão)")
	pos, ok, code := parseArgs(fs, args)
	if !ok {
		return code
	}
	ctx, stop := signalContext()
	defer stop()
	var m meetings.Meeting
	err := func() error {
		if len(pos) != 1 {
			return errors.New("uso: elan meeting <id> [--json|--markdown]")
		}
		return withDB(*profile, func(_ config.Config, conn *sql.DB) error {
			var err error
			m, err = meetings.NewReader(conn).Get(ctx, pos[0])
			return err
		})
	}()
	return finish(*asJSON, m, err, func() { fmt.Print(meetings.Markdown(m)) })
}

// actionsClassifyMain runs `elan actions classify`: backfills the category
// (code/other) and project of actions distilled before the distiller
// classified them, one claude -p call per batch.
func actionsClassifyMain(args []string) int {
	fs := flag.NewFlagSet("actions classify", flag.ExitOnError)
	profile := fs.String("profile", config.DefaultProfile, "perfil")
	limit := fs.Int("limit", 10, "máximo de chamadas ao Claude nesta execução (0 = sem limite)")
	batch := fs.Int("batch", meetings.DefaultClassifyBatch, "ações por chamada")
	model := fs.String("model", "sonnet", "modelo do Claude")
	if _, ok, code := parseArgs(fs, args); !ok {
		return code
	}
	ctx, stop := signalContext()
	defer stop()
	err := withDB(*profile, func(cfg config.Config, conn *sql.DB) error {
		c := meetings.NewClassifier(conn, &llm.ClaudeCLI{})
		c.Model, c.Batch, c.KnownProjects = *model, *batch, projectsOf(cfg).List()
		var st meetings.ClassifyStats
		// Shares the distill lock: both spend Claude usage and write actions.
		err := withDistillLock(cfg, func() error {
			var err error
			st, err = c.Classify(ctx, *limit, func(call, size int) {
				fmt.Printf("[%d] %d ações\n", call, size)
			})
			return err
		})
		if errors.Is(err, lock.ErrHeld) {
			return errors.New("uma destilação está rodando (o daemon?); tente de novo depois")
		}
		fmt.Printf("chamadas: %d, código: %d, outras: %d, sem resposta: %d, lotes com erro: %d, pendentes: %d\n",
			st.Calls, st.Code, st.Other, st.Missing, st.Failed, st.Pending)
		if len(st.Projects) > 0 {
			type pc struct {
				name string
				n    int
			}
			var ps []pc
			for p, n := range st.Projects {
				ps = append(ps, pc{p, n})
			}
			sort.Slice(ps, func(i, j int) bool { return ps[i].n > ps[j].n || (ps[i].n == ps[j].n && ps[i].name < ps[j].name) })
			var parts []string
			for _, p := range ps {
				parts = append(parts, fmt.Sprintf("%s (%d)", p.name, p.n))
			}
			fmt.Println("projetos das tarefas de código:", strings.Join(parts, ", "))
		}
		if le, ok := llm.IsLimit(err); ok {
			if le.ResetAt.IsZero() {
				fmt.Println("Limite de uso do Claude atingido; tente de novo mais tarde.")
			} else {
				fmt.Printf("Limite de uso do Claude atingido; libera em %s.\n", le.ResetAt.Local().Format("02/01 15:04"))
			}
			return nil
		}
		return err
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "erro:", err)
		return 1
	}
	return 0
}
