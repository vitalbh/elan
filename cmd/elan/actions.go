package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/vitalbh/elan/internal/config"
	"github.com/vitalbh/elan/internal/db"
	"github.com/vitalbh/elan/internal/llm"
	"github.com/vitalbh/elan/internal/lock"
	"github.com/vitalbh/elan/internal/meetings"
)

// actionsPendingMain runs `elan actions pending`: open action items.
func actionsPendingMain(args []string) int {
	fs := flag.NewFlagSet("actions pending", flag.ExitOnError)
	profile := fs.String("profile", config.DefaultProfile, "perfil")
	owner := fs.String("owner", "mine", "mine, others ou all")
	person := fs.String("person", "", "só ações desta pessoa")
	stale := fs.Bool("stale", false, "inclui ações abertas há mais de 30 dias")
	n := fs.Int("n", 30, "quantidade")
	asJSON := fs.Bool("json", false, "saída JSON (docs/cli-json.md)")
	if _, ok, code := parseArgs(fs, args); !ok {
		return code
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	list := []meetings.OpenAction{}
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
		found, err := meetings.NewActions(conn, cfg.Owner.Names).OpenActions(ctx, meetings.ActionQuery{
			Owner: meetings.ActionOwner(*owner), Person: *person, IncludeStale: *stale, Limit: *n,
		})
		if err != nil {
			return err
		}
		list = append(list, found...)
		return nil
	}()
	return finish(*asJSON, list, err, func() {
		for _, a := range list {
			flags := ""
			if a.Stale {
				flags += " [antiga]"
			}
			if a.TaskID != "" {
				flags += " [tarefa]"
			}
			fmt.Printf("#%d (%dd, %s; %s)%s %s\n", a.ID, a.AgeDays, a.MeetingTitle, strings.Join(a.People, ", "), flags, a.Text)
		}
		fmt.Printf("%d ações em aberto\n", len(list))
	})
}

// actionsReconcileMain runs `elan actions reconcile`: backfills the status of
// earlier actions from meetings distilled before the distiller reported them.
func actionsReconcileMain(args []string) int {
	fs := flag.NewFlagSet("actions reconcile", flag.ExitOnError)
	profile := fs.String("profile", config.DefaultProfile, "perfil")
	limit := fs.Int("limit", 10, "máximo de chamadas ao Claude nesta execução (0 = sem limite)")
	model := fs.String("model", "sonnet", "modelo do Claude")
	since := fs.String("since", "", "só reuniões a partir desta data (AAAA-MM-DD)")
	recheck := fs.Bool("recheck", false, "verifica de novo reuniões já verificadas (use com --since)")
	fs.Parse(args) //nolint:errcheck // ExitOnError

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	sinceDay, err := parseDay(*since)
	if err != nil {
		fmt.Fprintln(os.Stderr, "erro:", err)
		return 1
	}
	if err := actionsReconcile(ctx, *profile, *limit, *model, sinceDay, *recheck); err != nil {
		fmt.Fprintln(os.Stderr, "erro:", err)
		return 1
	}
	return 0
}

func actionsReconcile(ctx context.Context, profile string, limit int, model string, since time.Time, recheck bool) error {
	cfg, err := config.Load(profile)
	if err != nil {
		return err
	}
	conn, err := db.Open(cfg.DBPath)
	if err != nil {
		return err
	}
	defer conn.Close()

	r := meetings.NewReconciler(conn, &llm.ClaudeCLI{})
	r.Model = model
	r.Related = newRelated(cfg, conn)
	r.Since, r.Recheck = since, recheck
	var st meetings.ReconcileStats
	// Shares the distill lock: both spend Claude usage and write statuses.
	err = withDistillLock(cfg, func() error {
		var err error
		n := 0
		st, err = r.Reconcile(ctx, limit, func(title string) {
			n++
			fmt.Printf("[%d] %s\n", n, title)
		})
		return err
	})
	if errors.Is(err, lock.ErrHeld) {
		return errors.New("uma destilação está rodando (o daemon?); tente de novo depois")
	}
	fmt.Printf("verificadas: %d, chamadas: %d, concluídas: %d, canceladas: %d, com erro: %d, pendentes: %d\n",
		st.Checked, st.Calls, st.Completed, st.Dropped, st.Failed, st.Pending)
	if le, ok := llm.IsLimit(err); ok {
		if le.ResetAt.IsZero() {
			fmt.Println("Limite de uso do Claude atingido; tente de novo mais tarde.")
		} else {
			fmt.Printf("Limite de uso do Claude atingido; libera em %s.\n", le.ResetAt.Local().Format("02/01 15:04"))
		}
		return nil
	}
	return err
}
