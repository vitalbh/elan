package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/vitalbh/elan/internal/config"
	"github.com/vitalbh/elan/internal/db"
	"github.com/vitalbh/elan/internal/llm"
	"github.com/vitalbh/elan/internal/lock"
	"github.com/vitalbh/elan/internal/meetings"
)

// meetingsDistillMain runs "elan meetings distill". It parses its own flags
// so they do not clash with the shared flag set of the other commands.
func meetingsDistillMain(args []string) int {
	fs := flag.NewFlagSet("meetings distill", flag.ExitOnError)
	profile := fs.String("profile", config.DefaultProfile, "perfil")
	limit := fs.Int("limit", 10, "máximo de reuniões nesta execução (0 = todas)")
	model := fs.String("model", "sonnet", "modelo do Claude")
	fs.Parse(args) //nolint:errcheck // ExitOnError

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := meetingsDistill(ctx, *profile, *limit, *model); err != nil {
		fmt.Fprintln(os.Stderr, "erro:", err)
		return 1
	}
	return 0
}

func meetingsDistill(ctx context.Context, profile string, limit int, model string) error {
	cfg, err := config.Load(profile)
	if err != nil {
		return err
	}
	conn, err := db.Open(cfg.DBPath)
	if err != nil {
		return err
	}
	defer conn.Close()

	d := meetings.NewDistiller(conn, &llm.ClaudeCLI{})
	d.Model = model
	d.Related = newRelated(cfg, conn)
	d.KnownProjects = projectsOf(cfg).List()
	n := 0
	var st meetings.DistillStats
	err = withDistillLock(cfg, func() error {
		var err error
		st, err = d.Distill(ctx, limit, func(title string) {
			n++
			fmt.Printf("[%d] %s\n", n, title)
		})
		return err
	})
	if errors.Is(err, lock.ErrHeld) {
		return errors.New("outra destilação já está rodando (o daemon?)")
	}
	fmt.Printf("destiladas: %d, com erro: %d, itens: %d, decisões substituídas: %d, ações concluídas: %d, canceladas: %d, pendentes: %d\n",
		st.Distilled, st.Failed, st.Items, st.Superseded, st.Completed, st.Dropped, st.Pending)
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
