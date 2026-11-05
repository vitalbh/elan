package llm

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestParseResult(t *testing.T) {
	now := time.Date(2026, 10, 30, 10, 0, 0, 0, time.UTC)
	ok := `{"type":"result","subtype":"success","is_error":false,"result":"{\"a\":1}","structured_output":{"a":1}}`
	out, err := parseResult([]byte(ok), nil, nil, now)
	if err != nil || string(out) != `{"a":1}` {
		t.Fatalf("ok: %s %v", out, err)
	}

	noStructured := `{"type":"result","subtype":"success","is_error":false,"result":"hi"}`
	if _, err := parseResult([]byte(noStructured), nil, nil, now); err == nil {
		t.Fatal("missing structured_output should fail")
	}

	isErr := `{"type":"result","subtype":"error_during_execution","is_error":true,"result":"boom"}`
	if _, err := parseResult([]byte(isErr), nil, errors.New("exit status 1"), now); err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("is_error: %v", err)
	}

	limit := `{"type":"result","subtype":"success","is_error":true,"result":"You've hit your session limit · resets 2pm (America/Sao_Paulo)"}`
	_, err = parseResult([]byte(limit), nil, errors.New("exit status 1"), now)
	le, isLimit := IsLimit(err)
	if !isLimit {
		t.Fatalf("want LimitError, got %v", err)
	}
	sp, _ := time.LoadLocation("America/Sao_Paulo")
	if want := time.Date(2026, 10, 30, 14, 0, 0, 0, sp); !le.ResetAt.Equal(want) {
		t.Fatalf("reset %v, want %v", le.ResetAt, want)
	}

	// Plain-text limit on stderr, no envelope.
	_, err = parseResult(nil, []byte("Error: You've hit your weekly limit\n"), errors.New("exit status 1"), now)
	if le, ok := IsLimit(err); !ok || !le.ResetAt.IsZero() {
		t.Fatalf("stderr limit: %v", err)
	}

	if _, err := parseResult([]byte("garbage"), nil, nil, now); err == nil {
		t.Fatal("non-JSON should fail")
	}
}

func TestParseReset(t *testing.T) {
	sp, _ := time.LoadLocation("America/Sao_Paulo")
	now := time.Date(2026, 10, 30, 15, 0, 0, 0, sp) // 3pm in São Paulo
	cases := []struct {
		text string
		want time.Time
	}{
		{"resets 2pm (America/Sao_Paulo)", time.Date(2026, 10, 31, 14, 0, 0, 0, sp)},
		{"resets 4:30pm (America/Sao_Paulo)", time.Date(2026, 10, 30, 16, 30, 0, 0, sp)},
		{"resets 12am (America/Sao_Paulo)", time.Date(2026, 10, 31, 0, 0, 0, 0, sp)},
		{"resets at 18:00", time.Date(2026, 10, 30, 18, 0, 0, 0, sp)},
		{"resets Nov 3, 9am (America/Sao_Paulo)", time.Date(2026, 11, 3, 9, 0, 0, 0, sp)},
		{"resets Nov 3 at 9am", time.Date(2026, 11, 3, 9, 0, 0, 0, sp)},
		{"Claude AI usage limit reached|1793462400", time.Unix(1793462400, 0)},
		{"resets soon", time.Time{}},
		{"resets 25:00", time.Time{}},
	}
	for _, c := range cases {
		if got := parseReset(c.text, now); !got.Equal(c.want) {
			t.Errorf("%q: got %v, want %v", c.text, got, c.want)
		}
	}
	if got := parseReset("resets Jan 2, 9am (UTC)", time.Date(2026, 12, 30, 0, 0, 0, 0, time.UTC)); got.Year() != 2027 {
		t.Errorf("year rollover: %v", got)
	}
}

func TestDetectLimit(t *testing.T) {
	now := time.Now()
	for _, s := range []string{
		"You've hit your session limit · resets 2pm (America/Sao_Paulo)",
		"You've hit your usage limit",
		"You've hit your weekly limit · resets Nov 3, 9am",
		"5-hour limit reached ∙ resets 3am",
		"Claude AI usage limit reached|1793462400",
	} {
		if detectLimit(s, now) == nil {
			t.Errorf("not detected: %q", s)
		}
	}
	if detectLimit("rate of change limited to items", now) != nil {
		t.Error("false positive")
	}
}

// fakeClaude writes an executable that records its args, cwd and stdin and
// prints body.
func fakeClaude(t *testing.T, body string) (bin, logDir string) {
	t.Helper()
	dir := t.TempDir()
	bin = filepath.Join(dir, "claude")
	script := "#!/bin/sh\n" +
		"for a in \"$@\"; do printf '%s\\n' \"$a\"; done > " + dir + "/args\n" +
		"pwd > " + dir + "/cwd\n" +
		"cat > " + dir + "/stdin\n" +
		body + "\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return bin, dir
}

func TestClaudeCLIInvocation(t *testing.T) {
	bin, dir := fakeClaude(t, `echo '{"type":"result","subtype":"success","is_error":false,"structured_output":{"ok":true}}'`)
	c := &ClaudeCLI{Bin: bin}
	schema := json.RawMessage(`{"type":"object"}`)
	out, err := c.JSON(context.Background(), Request{System: "sys", Prompt: "olá\nmundo", Model: "sonnet", Schema: schema})
	if err != nil || string(out) != `{"ok":true}` {
		t.Fatalf("%s %v", out, err)
	}
	read := func(name string) string {
		b, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	want := strings.Join([]string{"-p", "--output-format", "json", "--tools", "", "--strict-mcp-config",
		"--setting-sources", "", "--no-session-persistence", "--model", "sonnet",
		"--system-prompt", "sys", "--json-schema", `{"type":"object"}`}, "\n") + "\n"
	if got := read("args"); got != want {
		t.Fatalf("args:\n%q\nwant\n%q", got, want)
	}
	if got := read("stdin"); got != "olá\nmundo" {
		t.Fatalf("stdin %q", got)
	}
	cwd := strings.TrimSpace(read("cwd"))
	if !strings.Contains(filepath.Base(cwd), "elan-llm-") {
		t.Fatalf("cwd %q is not the temp dir", cwd)
	}
	if _, err := os.Stat(cwd); !os.IsNotExist(err) {
		t.Fatalf("temp cwd not removed: %v", err)
	}
}

func TestClaudeCLILimitExit(t *testing.T) {
	bin, _ := fakeClaude(t, `echo '{"type":"result","subtype":"success","is_error":true,"result":"You've hit your session limit · resets 2pm (America/Sao_Paulo)"}'; exit 1`)
	_, err := (&ClaudeCLI{Bin: bin}).JSON(context.Background(), Request{Prompt: "x"})
	if le, ok := IsLimit(err); !ok || le.ResetAt.IsZero() {
		t.Fatalf("want LimitError with reset, got %v", err)
	}
}

func TestClaudeCLICancelKillsGroup(t *testing.T) {
	// The child sleep keeps stdout open; only killing the group ends it fast.
	bin, _ := fakeClaude(t, `sleep 30 & wait`)
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := (&ClaudeCLI{Bin: bin}).JSON(ctx, Request{Prompt: "x"})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("want deadline, got %v", err)
	}
	if d := time.Since(start); d > 3*time.Second {
		t.Fatalf("took %v: process group not killed", d)
	}
}
