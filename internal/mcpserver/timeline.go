package mcpserver

import (
	"context"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/vitalbh/elan/internal/timeline"
)

// Timeliner builds a topic's timeline.
type Timeliner interface {
	Build(ctx context.Context, q timeline.Query) (timeline.Timeline, error)
}

func addTimelineTool(s *mcp.Server, h *handlers) {
	mcp.AddTool(s, &mcp.Tool{
		Name:        "timeline",
		Description: "Linha do tempo de um assunto: decisões, fatos e ações de todas as reuniões sobre ele, das mais recentes para as mais antigas, mostrando o que foi substituído por quê. Use para \"como evoluiu X\", \"histórico de Y\", \"o que mudou sobre Z\".",
	}, h.timeline)
}

// TimelineIn is the input of timeline.
type TimelineIn struct {
	Topic  string `json:"topic" jsonschema:"o assunto (projeto, sistema, tema)"`
	Person string `json:"person,omitempty" jsonschema:"só reuniões de que essa pessoa participou"`
	Since  string `json:"since,omitempty" jsonschema:"a partir desta data (AAAA-MM-DD)"`
	Until  string `json:"until,omitempty" jsonschema:"antes desta data (AAAA-MM-DD)"`
	Limit  int    `json:"limit,omitempty" jsonschema:"máximo de itens (padrão 40, máx. 100)"`
}

// TimelineOut is the timeline as Markdown.
type TimelineOut struct {
	Markdown string `json:"markdown"`
}

func (h *handlers) timeline(ctx context.Context, _ *mcp.CallToolRequest, in TimelineIn) (*mcp.CallToolResult, TimelineOut, error) {
	q := timeline.Query{Topic: in.Topic, Person: in.Person, Limit: clamp(in.Limit, 40, 100)}
	var err error
	if q.Since, err = parseDay(in.Since); err != nil {
		return nil, TimelineOut{}, err
	}
	if q.Until, err = parseDay(in.Until); err != nil {
		return nil, TimelineOut{}, err
	}
	t, err := h.d.Timeline.Build(ctx, q)
	if err != nil {
		return nil, TimelineOut{}, err
	}
	md := t.Markdown()
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: md}}}, TimelineOut{Markdown: md}, nil
}
