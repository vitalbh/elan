package main

import (
	"context"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/vitalbh/elan/internal/agenda"
	"github.com/vitalbh/elan/internal/config"
	"github.com/vitalbh/elan/internal/db"
	"github.com/vitalbh/elan/internal/google"
	"github.com/vitalbh/elan/internal/meetings"
)

// newCalendar builds the profile's Calendar client. The token is opened on
// every call: a missing login turns into a clear error from the calendar
// tools only, and a later `elan google-login` works without a restart.
// ctx must live as long as the client (refreshed tokens are saved with it).
func newCalendar(ctx context.Context, cfg config.Config) *google.Calendar {
	return google.NewCalendar(func(context.Context) (*http.Client, error) {
		return google.HTTPClient(ctx, cfg.Google)
	}, google.CalendarOptions{Location: time.Local})
}

// agendaMain runs `elan agenda`: a day of the calendar with the previous
// meeting of each series. Read-only.
func agendaMain(args []string) int {
	fs := flag.NewFlagSet("agenda", flag.ExitOnError)
	profile := fs.String("profile", config.DefaultProfile, "perfil")
	day := fs.String("day", "", "dia (AAAA-MM-DD), padrão hoje")
	fs.Parse(args) //nolint:errcheck // ExitOnError

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := runAgenda(ctx, *profile, *day); err != nil {
		fmt.Fprintln(os.Stderr, "erro:", err)
		return 1
	}
	return 0
}

func runAgenda(ctx context.Context, profile, dayFlag string) error {
	d := time.Now()
	if dayFlag != "" {
		var err error
		if d, err = time.ParseInLocation("2006-01-02", dayFlag, time.Local); err != nil {
			return fmt.Errorf("data inválida %q: use AAAA-MM-DD", dayFlag)
		}
	}
	cfg, err := config.Load(profile)
	if err != nil {
		return err
	}
	conn, err := db.Open(cfg.DBPath)
	if err != nil {
		return err
	}
	defer conn.Close()

	a, err := agenda.New(newCalendar(ctx, cfg), meetings.NewReader(conn)).Agenda(ctx, d)
	if err != nil {
		return err
	}
	fmt.Printf("agenda de %s\n\n", a.Day)
	for _, it := range a.Items {
		when := it.Start.Format("15:04") + "–" + it.End.Format("15:04")
		if it.AllDay {
			when = "dia inteiro"
		}
		var tags []string
		if it.MyResponse != "" && it.MyResponse != google.Accepted {
			tags = append(tags, it.MyResponse)
		}
		if n := len(it.Attendees); n > 0 {
			tags = append(tags, fmt.Sprintf("%d convidados", n))
		}
		if it.MeetLink != "" {
			tags = append(tags, "meet")
		}
		line := when + "  " + it.Title
		if len(tags) > 0 {
			line += "  [" + strings.Join(tags, ", ") + "]"
		}
		fmt.Println(line)
		if p := it.Previous; p != nil {
			fmt.Printf("    anterior: %s (%s)\n", p.StartedAt.Local().Format("02/01/2006"), p.Title)
			if p.Summary != "" {
				fmt.Printf("    resumo: %s\n", p.Summary)
			}
			for _, act := range p.OpenActions {
				fmt.Printf("    - ação: %s\n", act.Text)
			}
		}
	}
	fmt.Printf("\n%d eventos, %d com reunião anterior\n", len(a.Items), a.Matched())
	return nil
}
