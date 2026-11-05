package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/vitalbh/elan/internal/config"
	"github.com/vitalbh/elan/internal/db"
	"github.com/vitalbh/elan/internal/embed"
	"github.com/vitalbh/elan/internal/people"
	"github.com/vitalbh/elan/internal/search"
)

// searchCmd runs `elan index` and `elan search`, which have their own flags
// and a positional query. Returns the exit code.
func searchCmd(cmd string, args []string) int {
	fs := flag.NewFlagSet(cmd, flag.ExitOnError)
	profile := fs.String("profile", config.DefaultProfile, "perfil")
	rebuild := fs.Bool("rebuild", false, "apaga e recria o índice inteiro (index)")
	since := fs.String("since", "", "só resultados a partir desta data, AAAA-MM-DD (search)")
	until := fs.String("until", "", "só resultados antes desta data, AAAA-MM-DD (search)")
	transcripts := fs.Bool("transcripts", false, "inclui as transcrições (search)")
	person := fs.String("person", "", "só reuniões de que essa pessoa participou (search)")
	n := fs.Int("n", 10, "quantidade de resultados (search)")
	asJSON := fs.Bool("json", false, "saída JSON, docs/cli-json.md (search)")
	// Flags may come before or after the query.
	positional, ok, code := parseArgs(fs, args)
	if !ok {
		return code
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	cfg, err := config.Load(*profile)
	if err == nil {
		switch cmd {
		case "index":
			err = runIndex(ctx, cfg, *rebuild)
		case "search":
			q := search.Query{Text: strings.Join(positional, " "), IncludeTranscripts: *transcripts, Person: *person, Limit: *n}
			if q.Since, err = parseDay(*since); err == nil {
				if q.Until, err = parseDay(*until); err == nil {
					if *asJSON {
						var hits []searchHit
						if hits, err = searchJSON(ctx, cfg, q); err == nil {
							return finish(true, hits, nil, nil)
						}
					} else {
						err = runSearch(ctx, cfg, q)
					}
				}
			}
		}
	}
	if err != nil && *asJSON && cmd == "search" {
		return jsonError(err)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "erro:", err)
		return 1
	}
	return 0
}

func parseDay(s string) (time.Time, error) {
	if s == "" {
		return time.Time{}, nil
	}
	t, err := time.ParseInLocation("2006-01-02", s, time.Local)
	if err != nil {
		return time.Time{}, fmt.Errorf("data inválida %q: use AAAA-MM-DD", s)
	}
	return t, nil
}

func openSearch(cfg config.Config) (*search.Service, func(), error) {
	if err := cfg.EnsureDir(); err != nil {
		return nil, nil, err
	}
	conn, err := db.Open(cfg.DBPath)
	if err != nil {
		return nil, nil, err
	}
	svc := search.NewService(conn, embed.NewOllama(cfg.Embed))
	svc.People = people.NewDirectory(cfg.People)
	return svc, func() { conn.Close() }, nil
}

func runIndex(ctx context.Context, cfg config.Config, rebuild bool) error {
	svc, closeDB, err := openSearch(cfg)
	if err != nil {
		return err
	}
	defer closeDB()
	progress := func(done, total int) {
		fmt.Fprintf(os.Stderr, "\rembeddings: %d/%d", done, total)
		if done == total {
			fmt.Fprintln(os.Stderr)
		}
	}
	st, err := svc.Index(ctx, search.IndexOptions{Rebuild: rebuild, Progress: progress})
	if err != nil {
		return err
	}
	fmt.Printf("índice: %s (dim %d) em %s\n", st.Model, st.Dim, st.Duration.Round(time.Millisecond))
	types := make([]string, 0, len(st.Sources))
	for t := range st.Sources {
		types = append(types, t)
	}
	sort.Strings(types)
	for _, t := range types {
		s := st.Sources[t]
		fmt.Printf("  %-14s %5d chunks, %d novos, %d removidos\n", t, s.Chunks, s.Embedded, s.Deleted)
	}
	return nil
}

// searchHit is one result of `elan search --json`.
type searchHit struct {
	MeetingID string `json:"meeting_id"`
	Title     string `json:"title"`
	Date      string `json:"date"`
	Type      string `json:"type"`
	Kind      string `json:"kind"`
	Text      string `json:"text"`
}

func searchJSON(ctx context.Context, cfg config.Config, q search.Query) ([]searchHit, error) {
	svc, closeDB, err := openSearch(cfg)
	if err != nil {
		return nil, err
	}
	defer closeDB()
	results, err := svc.Search(ctx, q)
	if err != nil {
		return nil, err
	}
	hits := make([]searchHit, len(results))
	for i, r := range results {
		hits[i] = searchHit{MeetingID: r.MeetingID, Title: r.Title, Type: r.SourceType, Kind: r.Kind, Text: r.Text}
		if !r.At.IsZero() {
			hits[i].Date = r.At.Format(time.RFC3339)
		}
	}
	return hits, nil
}

func runSearch(ctx context.Context, cfg config.Config, q search.Query) error {
	svc, closeDB, err := openSearch(cfg)
	if err != nil {
		return err
	}
	defer closeDB()
	results, err := svc.Search(ctx, q)
	if err != nil {
		return err
	}
	if len(results) == 0 {
		fmt.Println("nada encontrado")
		return nil
	}
	for i, r := range results {
		date := "sem data  "
		if !r.At.IsZero() {
			date = r.At.Local().Format("2006-01-02")
		}
		title := r.Title
		if title == "" {
			title = "(nota)"
		}
		typ := r.SourceType
		if r.Kind != "" {
			typ += "/" + r.Kind
		}
		fmt.Printf("%2d. %s  %s  [%s] %.4f %s\n", i+1, date, title, typ, r.Score, strings.Join(r.Retrievers, "+"))
		fmt.Printf("    %s\n", r.Snippet)
	}
	return nil
}
