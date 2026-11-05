package main

import (
	"context"
	"database/sql"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/vitalbh/elan/internal/config"
	"github.com/vitalbh/elan/internal/db"
	"github.com/vitalbh/elan/internal/embed"
	"github.com/vitalbh/elan/internal/people"
	"github.com/vitalbh/elan/internal/search"
	"github.com/vitalbh/elan/internal/timeline"
)

// newTimeline builds the timeline service on a search without the
// per-meeting cap: one meeting may hold several items about the topic.
func newTimeline(cfg config.Config, conn *sql.DB) *timeline.Service {
	svc := search.NewService(conn, embed.NewOllama(cfg.Embed))
	svc.People = people.NewDirectory(cfg.People)
	svc.Params.MaxPerMeeting = 0
	return timeline.New(conn, svc)
}

// timelineMain runs `elan timeline "<assunto>"`.
func timelineMain(args []string) int {
	fs := flag.NewFlagSet("timeline", flag.ContinueOnError)
	profile := fs.String("profile", config.DefaultProfile, "perfil")
	person := fs.String("person", "", "só reuniões de que essa pessoa participou")
	since := fs.String("since", "", "a partir desta data (AAAA-MM-DD)")
	until := fs.String("until", "", "antes desta data (AAAA-MM-DD)")
	n := fs.Int("n", 40, "máximo de itens")
	asJSON := fs.Bool("json", false, "saída JSON")
	topic, ok, code := parseArgs(fs, args)
	if !ok {
		return code
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	t, err := func() (timeline.Timeline, error) {
		cfg, err := config.Load(*profile)
		if err != nil {
			return timeline.Timeline{}, err
		}
		q := timeline.Query{Topic: strings.Join(topic, " "), Person: *person, Limit: *n}
		if q.Since, err = parseDay(*since); err != nil {
			return timeline.Timeline{}, err
		}
		if q.Until, err = parseDay(*until); err != nil {
			return timeline.Timeline{}, err
		}
		conn, err := db.Open(cfg.DBPath)
		if err != nil {
			return timeline.Timeline{}, err
		}
		defer conn.Close()
		return newTimeline(cfg, conn).Build(ctx, q)
	}()
	return finish(*asJSON, t, err, func() { fmt.Print(t.Markdown()) })
}
