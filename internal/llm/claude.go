package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"time"
)

// ClaudeCLI runs requests through `claude -p`, one process per call, with no
// tools, no MCP servers, no settings or CLAUDE.md, and no saved session.
type ClaudeCLI struct {
	// Bin is the claude executable. Empty: "claude" from PATH.
	Bin string
	now func() time.Time
}

// args builds the command line. The prompt goes through stdin.
//
//   - --output-format json: one result envelope; with --json-schema the
//     validated answer is in its "structured_output" field.
//   - --tools "": no built-in tools (structured output still works).
//   - --strict-mcp-config without --mcp-config: no MCP servers.
//   - --setting-sources "": no user/project/local settings, hooks or
//     CLAUDE.md, so the call does not depend on the owner's setup. Login
//     still works (it is not a setting).
//   - --no-session-persistence: nothing lands in the session history.
func (c *ClaudeCLI) args(req Request) []string {
	a := []string{
		"-p",
		"--output-format", "json",
		"--tools", "",
		"--strict-mcp-config",
		"--setting-sources", "",
		"--no-session-persistence",
	}
	if req.Model != "" {
		a = append(a, "--model", req.Model)
	}
	if req.System != "" {
		a = append(a, "--system-prompt", req.System)
	}
	if len(req.Schema) > 0 {
		a = append(a, "--json-schema", string(req.Schema))
	}
	return a
}

// JSON implements Runner. Cancel ctx (or give it a deadline) to bound the
// call: the whole process group is killed.
func (c *ClaudeCLI) JSON(ctx context.Context, req Request) ([]byte, error) {
	bin := c.Bin
	if bin == "" {
		bin = "claude"
	}
	// A neutral cwd: no project CLAUDE.md, .mcp.json or settings nearby.
	dir, err := os.MkdirTemp("", "elan-llm-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)

	var stdout, stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, bin, c.args(req)...)
	cmd.Dir = dir
	cmd.Stdin = strings.NewReader(req.Prompt)
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	cmd.WaitDelay = 5 * time.Second

	runErr := cmd.Run()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	now := time.Now
	if c.now != nil {
		now = c.now
	}
	return parseResult(stdout.Bytes(), stderr.Bytes(), runErr, now())
}

type envelope struct {
	Type             string          `json:"type"`
	Subtype          string          `json:"subtype"`
	IsError          bool            `json:"is_error"`
	Result           string          `json:"result"`
	StructuredOutput json.RawMessage `json:"structured_output"`
}

func parseResult(stdout, stderr []byte, runErr error, now time.Time) ([]byte, error) {
	var env envelope
	if err := json.Unmarshal(bytes.TrimSpace(stdout), &env); err != nil {
		text := string(stdout) + "\n" + string(stderr)
		if le := detectLimit(text, now); le != nil {
			return nil, le
		}
		if runErr != nil {
			return nil, fmt.Errorf("claude: %w: %s", runErr, excerpt(text))
		}
		return nil, fmt.Errorf("claude: resposta não é JSON: %s", excerpt(text))
	}
	if env.IsError || runErr != nil {
		if le := detectLimit(env.Result+"\n"+string(stderr), now); le != nil {
			return nil, le
		}
		msg := env.Result
		if msg == "" {
			msg = string(stderr)
		}
		err := fmt.Errorf("claude (%s): %s", env.Subtype, excerpt(msg))
		if runErr != nil {
			err = errors.Join(err, runErr)
		}
		return nil, err
	}
	out := bytes.TrimSpace(env.StructuredOutput)
	if len(out) == 0 || bytes.Equal(out, []byte("null")) {
		return nil, fmt.Errorf("claude (%s): sem structured_output (a chamada tinha --json-schema?)", env.Subtype)
	}
	return out, nil
}

// excerpt trims a message for an error, keeping errors short.
func excerpt(s string) string {
	s = strings.TrimSpace(s)
	r := []rune(s)
	if len(r) > 300 {
		return string(r[:300]) + "…"
	}
	return s
}
