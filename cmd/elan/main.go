// Command elan is the CLI and (later) the daemon of the personal brain.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"syscall"
	"text/tabwriter"
	"time"

	"github.com/vitalbh/elan/internal/config"
	"github.com/vitalbh/elan/internal/db"
	"github.com/vitalbh/elan/internal/google"
	"github.com/vitalbh/elan/internal/meetings"
)

const usage = `uso: elan <comando> [--profile=work]

comandos:
  google-login   autoriza o Google (Drive, Calendar, Tasks) para o perfil
  drive-recent   lista os arquivos mais recentes do Drive
  meetings sync  ingere as reuniões da pasta do Meet no Drive
  meetings distill  destila as reuniões via claude -p [--limit=10] [--model=sonnet]
  index          atualiza o índice de busca [--rebuild]
  search         busca híbrida: elan search "<consulta>" [--since=AAAA-MM-DD] [--person=nome] [--transcripts] [--n=10]
  note           salva uma nota: elan note "<texto>"
  notes          as últimas notas: elan notes [--n=20] [--json]
  tasks export   suas ações das reuniões recentes → Google Tasks [--days=7] [--dry-run]
  agenda         agenda do dia com a reunião anterior de cada série [--day=AAAA-MM-DD]
  timeline       linha do tempo de um assunto: elan timeline "pix" [--person=] [--since=] [--json]
  briefing       briefing do dia (Markdown + página HTML) [--day=AAAA-MM-DD] [--open]
  tasks status   traz das tarefas exportadas o status (concluída, excluída) para as ações
  tasks push     leva às tarefas exportadas o status das ações fechadas/reabertas no elan [--dry-run]
  actions pending   ações em aberto [--owner=mine|others|all] [--person=Nome] [--stale] [--n=30]
  actions reconcile marca ações antigas concluídas/canceladas pelas reuniões seguintes [--limit=10] [--model=sonnet]
  actions classify  classifica ações antigas em código/outras e projeto [--limit=10] [--batch=50] [--model=sonnet]
  action update  muda o status de uma ação: elan action update <id> --status=done|dropped|open
  codetasks      suas tarefas de código [--done]; elan codetasks add "<texto>" [--project=X] [--meeting=<id>] [--context="..."]
                 | promote <item_id> | start <id> | update <id> --status=done|dropped|open
                 | suggestions [--stale] [--n=50] (ações classificadas como código, ainda não tarefas)
  meeting        uma reunião em Markdown: elan meeting <id> [--json|--markdown]
  (--json em briefing, search, actions pending, action update, codetasks e meeting: docs/cli-json.md)
  mcp            servidor MCP via stdio (para o Claude)
  daemon         ciclo sync → distill → index → tasks → taskstatus → taskpush [--interval=30m] [--once]
  service        LaunchAgent do daemon: install | uninstall | status
`

func main() {
	if len(os.Args) < 2 {
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
	cmd, args := os.Args[1], os.Args[2:]
	if (cmd == "meetings" || cmd == "tasks" || cmd == "actions" || cmd == "service" || cmd == "action" || cmd == "codetasks") && len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		cmd, args = cmd+" "+args[0], args[1:]
	}
	switch cmd {
	case "meetings distill":
		os.Exit(meetingsDistillMain(args))
	case "index", "search":
		os.Exit(searchCmd(cmd, args))
	case "tasks export":
		os.Exit(tasksExportMain(args))
	case "tasks status":
		os.Exit(tasksStatusMain(args))
	case "tasks push":
		os.Exit(tasksPushMain(args))
	case "actions pending":
		os.Exit(actionsPendingMain(args))
	case "actions reconcile":
		os.Exit(actionsReconcileMain(args))
	case "actions classify":
		os.Exit(actionsClassifyMain(args))
	case "action update":
		os.Exit(actionUpdateMain(args))
	case "codetasks":
		os.Exit(codetasksMain(args))
	case "codetasks start":
		os.Exit(codetasksStartMain(args))
	case "codetasks suggestions":
		os.Exit(codetasksSuggestionsMain(args))
	case "codetasks add":
		os.Exit(codetasksAddMain(args))
	case "codetasks promote":
		os.Exit(codetasksPromoteMain(args))
	case "codetasks update":
		os.Exit(codetasksUpdateMain(args))
	case "meeting":
		os.Exit(meetingMain(args))
	case "note":
		os.Exit(noteMain(args))
	case "notes":
		os.Exit(notesMain(args))
	case "agenda":
		os.Exit(agendaMain(args))
	case "timeline":
		os.Exit(timelineMain(args))
	case "briefing":
		os.Exit(briefingMain(args))
	case "mcp":
		os.Exit(mcpMain(args))
	case "daemon":
		os.Exit(daemonMain(args))
	case "service install", "service uninstall", "service status":
		os.Exit(serviceMain(strings.TrimPrefix(cmd, "service "), args))
	}
	fs := flag.NewFlagSet(cmd, flag.ExitOnError)
	profile := fs.String("profile", config.DefaultProfile, "perfil")
	n := fs.Int64("n", 5, "quantidade de arquivos (drive-recent)")
	q := fs.String("q", "", "filtro de busca do Drive (drive-recent)")
	fs.Parse(args) //nolint:errcheck // ExitOnError

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := run(ctx, cmd, *profile, *n, *q); err != nil {
		fmt.Fprintln(os.Stderr, "erro:", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, cmd, profile string, n int64, q string) error {
	cfg, err := config.Load(profile)
	if err != nil {
		return err
	}
	switch cmd {
	case "google-login":
		return googleLogin(ctx, cfg)
	case "drive-recent":
		return driveRecent(ctx, cfg, n, q)
	case "meetings sync":
		return meetingsSync(ctx, cfg)
	default:
		fmt.Fprint(os.Stderr, usage)
		return errors.New("comando desconhecido: " + cmd)
	}
}

func googleLogin(ctx context.Context, cfg config.Config) error {
	if err := cfg.EnsureDir(); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	open := func(u string) error {
		fmt.Println("Abrindo o navegador para o login do Google. Se não abrir, acesse:")
		fmt.Println(u)
		return exec.Command("open", u).Run()
	}
	if err := google.Login(ctx, cfg.Google, open); err != nil {
		return err
	}
	fmt.Printf("Login salvo no perfil %q.\n", cfg.Profile)
	return nil
}

func driveRecent(ctx context.Context, cfg config.Config, n int64, q string) error {
	hc, err := google.HTTPClient(ctx, cfg.Google)
	if err != nil {
		return err
	}
	d, err := google.NewDrive(ctx, hc)
	if err != nil {
		return err
	}
	files, err := d.Recent(ctx, n, q)
	if err != nil {
		return err
	}
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "MODIFICADO\tTIPO\tNOME")
	for _, f := range files {
		fmt.Fprintf(w, "%s\t%s\t%s\n", f.Modified.Local().Format("2006-01-02 15:04"), f.MimeType, f.Name)
	}
	return w.Flush()
}

func meetingsSync(ctx context.Context, cfg config.Config) error {
	if err := cfg.EnsureDir(); err != nil {
		return err
	}
	hc, err := google.HTTPClient(ctx, cfg.Google)
	if err != nil {
		return err
	}
	d, err := google.NewDrive(ctx, hc)
	if err != nil {
		return err
	}
	conn, err := db.Open(cfg.DBPath)
	if err != nil {
		return err
	}
	defer conn.Close()

	src := &meetSource{drive: d, folderID: cfg.Meetings.FolderID}
	st, err := meetings.NewService(conn, src).Sync(ctx)
	fmt.Printf("reuniões: %d vistas, %d novas, %d atualizadas, %d sem mudança, %d com erro\n",
		st.Seen, st.New, st.Updated, st.Unchanged, st.Failed)
	return err
}

// meetSource adapts the Drive client to meetings.Source.
type meetSource struct {
	drive    *google.Drive
	folderID string
}

func (s *meetSource) List(ctx context.Context) ([]meetings.Doc, error) {
	folder := s.folderID
	if folder == "" {
		var err error
		if folder, err = s.drive.MeetFolderID(ctx); err != nil {
			return nil, err
		}
	}
	found, err := s.drive.MeetDocs(ctx, folder)
	if err != nil {
		return nil, err
	}
	docs := make([]meetings.Doc, len(found))
	for i, f := range found {
		docs[i] = meetings.Doc{ID: f.ID, Name: f.Name, FolderID: f.FolderID, FolderName: f.FolderName, Modified: f.Modified}
	}
	return docs, nil
}

func (s *meetSource) Export(ctx context.Context, id string) (string, error) {
	return s.drive.ExportText(ctx, id)
}
