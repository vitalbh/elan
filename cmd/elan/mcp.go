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

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/vitalbh/elan/internal/config"
	"github.com/vitalbh/elan/internal/db"
	"github.com/vitalbh/elan/internal/embed"
	"github.com/vitalbh/elan/internal/mcpserver"
	"github.com/vitalbh/elan/internal/meetings"
	"github.com/vitalbh/elan/internal/notes"
	"github.com/vitalbh/elan/internal/people"
	"github.com/vitalbh/elan/internal/search"
)

const version = "0.1.0"

// mcpMain runs `elan mcp`: the MCP server over stdio. Nothing but the
// protocol may go to stdout.
func mcpMain(args []string) int {
	fs := flag.NewFlagSet("mcp", flag.ExitOnError)
	profile := fs.String("profile", config.DefaultProfile, "perfil")
	fs.Parse(args) //nolint:errcheck // ExitOnError

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := runMCP(ctx, *profile); err != nil {
		fmt.Fprintln(os.Stderr, "erro:", err)
		return 1
	}
	return 0
}

func runMCP(ctx context.Context, profile string) error {
	cfg, err := config.Load(profile)
	if err != nil {
		return err
	}
	if err := cfg.EnsureDir(); err != nil {
		return err
	}
	conn, err := db.Open(cfg.DBPath)
	if err != nil {
		return err
	}
	defer conn.Close()
	svc := search.NewService(conn, embed.NewOllama(cfg.Embed))
	svc.People = people.NewDirectory(cfg.People)
	reader := meetings.NewReader(conn)
	cal := newCalendar(ctx, cfg)
	brief, ag := newBriefing(cfg, conn, cal)
	srv := mcpserver.New(mcpserver.Deps{
		Search:    svc,
		Meetings:  reader,
		Notes:     notes.NewService(conn),
		Indexer:   svc,
		Actions:   meetings.NewActions(conn, cfg.Owner.Names),
		Version:   version,
		Calendar:  cal,
		Agenda:    ag,
		Briefing:  brief,
		Tasks:     newTasks(ctx, cfg),
		CodeTasks: newCodeTasks(cfg, conn),
		Timeline:  newTimeline(cfg, conn),
	})
	return srv.Run(ctx, &mcp.StdioTransport{})
}

// noteMain runs `elan note "<texto>"`: saves a note and indexes it.
func noteMain(args []string) int {
	fs := flag.NewFlagSet("note", flag.ExitOnError)
	profile := fs.String("profile", config.DefaultProfile, "perfil")
	fs.Parse(args) //nolint:errcheck // ExitOnError

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := runNote(ctx, *profile, strings.Join(fs.Args(), " ")); err != nil {
		fmt.Fprintln(os.Stderr, "erro:", err)
		return 1
	}
	return 0
}

// notesMain: the latest notes, newest first.
func notesMain(args []string) int {
	fs := flag.NewFlagSet("notes", flag.ExitOnError)
	profile := fs.String("profile", config.DefaultProfile, "perfil")
	n := fs.Int("n", 20, "quantidade")
	asJSON := fs.Bool("json", false, "saída JSON (docs/cli-json.md)")
	if _, ok, code := parseArgs(fs, args); !ok {
		return code
	}
	ctx, stop := signalContext()
	defer stop()
	var list []notes.Note
	err := withDB(*profile, func(_ config.Config, conn *sql.DB) error {
		var err error
		list, err = notes.NewService(conn).Recent(ctx, *n)
		return err
	})
	return finish(*asJSON, list, err, func() {
		for _, nt := range list {
			fmt.Printf("%s  %s\n", nt.CreatedAt.Local().Format("02/01 15:04"), nt.Text)
		}
	})
}

func runNote(ctx context.Context, profile, text string) error {
	cfg, err := config.Load(profile)
	if err != nil {
		return err
	}
	if err := cfg.EnsureDir(); err != nil {
		return err
	}
	conn, err := db.Open(cfg.DBPath)
	if err != nil {
		return err
	}
	defer conn.Close()
	n, err := notes.NewService(conn).Save(ctx, text, "cli")
	if err != nil {
		return err
	}
	if _, err := search.NewService(conn, embed.NewOllama(cfg.Embed)).Index(ctx, search.IndexOptions{}); err != nil {
		fmt.Printf("nota %d salva, mas não indexada: %v\n", n.ID, err)
		return nil
	}
	fmt.Printf("nota %d salva\n", n.ID)
	return nil
}
