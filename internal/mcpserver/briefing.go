package mcpserver

import (
	"context"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/vitalbh/elan/internal/briefing"
)

// Briefer builds a day's briefing.
type Briefer interface {
	Build(ctx context.Context, day time.Time) (briefing.Briefing, error)
}

func addBriefingTool(s *mcp.Server, h *handlers) {
	mcp.AddTool(s, &mcp.Tool{
		Name:        "briefing_today",
		Description: "Briefing do dia: agenda com a reunião anterior de cada série, pendências do dono, o que ele espera dos outros e decisões recentes. Em Markdown, pronto para mostrar.",
	}, h.briefing)
}

// BriefingIn is the input of briefing_today.
type BriefingIn struct {
	Day string `json:"day,omitempty" jsonschema:"dia (AAAA-MM-DD), padrão hoje"`
}

// BriefingOut is the briefing as Markdown.
type BriefingOut struct {
	Markdown string `json:"markdown"`
}

func (h *handlers) briefing(ctx context.Context, _ *mcp.CallToolRequest, in BriefingIn) (*mcp.CallToolResult, BriefingOut, error) {
	day := time.Now()
	if in.Day != "" {
		d, err := parseDay(in.Day)
		if err != nil {
			return nil, BriefingOut{}, err
		}
		day = d
	}
	b, err := h.d.Briefing.Build(ctx, day)
	if err != nil {
		return nil, BriefingOut{}, err
	}
	md := b.Markdown()
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: md}}}, BriefingOut{Markdown: md}, nil
}
