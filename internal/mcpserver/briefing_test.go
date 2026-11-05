package mcpserver

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/vitalbh/elan/internal/briefing"
)

type fakeBriefer struct{ day time.Time }

func (f *fakeBriefer) Build(_ context.Context, day time.Time) (briefing.Briefing, error) {
	f.day = day
	return briefing.Briefing{Day: day, Mine: []briefing.Action{{Text: "Mandar a proposta.", MeetingTitle: "Pix"}}}, nil
}

func TestBriefingTool(t *testing.T) {
	ctx := context.Background()
	fb := &fakeBriefer{}
	srv := New(Deps{Briefing: fb, Version: "test"})
	ct, st := mcp.NewInMemoryTransports()
	if _, err := srv.Connect(ctx, st, nil); err != nil {
		t.Fatal(err)
	}
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "test"}, nil).Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer cs.Close()

	out, res := call[BriefingOut](t, cs, "briefing_today", map[string]any{"day": "2026-11-02"})
	if res.IsError || !strings.Contains(out.Markdown, "Mandar a proposta.") || fb.day.Format("2006-01-02") != "2026-11-02" {
		t.Fatalf("briefing_today: %+v day=%v", out, fb.day)
	}
	if text, ok := res.Content[0].(*mcp.TextContent); !ok || !strings.Contains(text.Text, "# Briefing de segunda") {
		t.Fatalf("content should be the markdown: %+v", res.Content)
	}
}
