// elan-rex: the elan frontend for the Rex terminal. Like the Herdr mods
// (../herdr), it talks to elan only through its CLI with --json
// (docs/cli-json.md); the git worktree of a code task is made here.
//
//	elan-rex ui [today|search|code|suggest|actions]   the elan window
//	elan-rex code <id>                                start/open a code task in this tab
package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"time"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, errStyle.Render("elan: "+err.Error()))
		if os.Getenv("REX_BLOCK") != "" {
			// running as a tab's program: keep the message on screen
			fmt.Fprint(os.Stderr, dimStyle.Render("enter para fechar"))
			_, _ = fmt.Scanln()
		}
		os.Exit(1)
	}
}

func run(args []string) error {
	c := loadCfg()
	cmd := "ui"
	if len(args) > 0 {
		cmd = args[0]
	}
	switch cmd {
	case "ui":
		tab := ""
		if len(args) > 1 {
			tab = args[1]
		}
		next, err := runUI(c, tab)
		if err != nil || next == nil {
			return err
		}
		return openTask(c, next.id, next.title)
	case "code":
		if len(args) < 2 {
			return fmt.Errorf("uso: elan-rex code <id>")
		}
		id, err := strconv.Atoi(args[1])
		if err != nil {
			return fmt.Errorf("id inválido: %s", args[1])
		}
		return openTask(c, id, "")
	case "help", "-h", "--help":
		fmt.Println("elan-rex ui [today|search|code|suggest|actions] · elan-rex code <id>")
		return nil
	}
	return fmt.Errorf("comando desconhecido: %s", cmd)
}

// openTask turns this tab into the code task: the worktree as the working
// directory and Claude Code with the task's prompt (first time) or the last
// conversation (later).
func openTask(c cfg, id int, title string) error {
	fmt.Println(dimStyle.Render("preparando a worktree…"))
	ws, err := startCodeTask(c, id, title)
	if err != nil {
		return err
	}
	if err := os.Chdir(ws.Path); err != nil {
		return err
	}
	label := ws.Title
	if label == "" {
		label = ws.Branch
	}
	rexRename(clip(label, 40))

	claude, err := exec.LookPath("claude")
	if err != nil {
		return fmt.Errorf("claude não encontrado no PATH")
	}
	if ws.Fresh {
		return syscall.Exec(claude, []string{"claude", ws.Prompt}, os.Environ())
	}
	// Already started before: resume the conversation of this folder; with
	// none (opened elsewhere, or cleared), start again with the prompt.
	sh := `"$0" --continue || exec "$0" "$1"`
	return syscall.Exec("/bin/sh", []string{"sh", "-c", sh, claude, ws.Prompt}, os.Environ())
}

// rexRename labels this tab and its pane (no-op outside Rex). Each rex call
// is capped so a slow server never holds up the window.
func rexRename(label string) {
	block := os.Getenv("REX_BLOCK")
	if block == "" {
		return
	}
	bin := rexBin()
	call := func(args ...string) ([]byte, error) {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		return exec.CommandContext(ctx, bin, args...).Output()
	}
	_, _ = call("block", "rename", block, label)
	// the tab: find the block's window in `rex block inspect`
	out, err := call("block", "inspect", block)
	if err != nil {
		return
	}
	for _, line := range strings.Split(string(out), "\n") {
		if w, ok := strings.CutPrefix(line, "window:"); ok {
			_, _ = call("window", "rename", strings.TrimSpace(w), label)
			return
		}
	}
}

func rexBin() string {
	if p, err := exec.LookPath("rex"); err == nil {
		return p
	}
	for _, p := range []string{
		"/Applications/Rex.app/Contents/Helpers/Rex Server.rex/Contents/MacOS/rex",
		"/Applications/Rex Beta.app/Contents/Helpers/Rex Server.rex/Contents/MacOS/rex",
	} {
		if fileExists(p) {
			return p
		}
	}
	return "rex"
}
