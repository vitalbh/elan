package mcpserver

import (
	"context"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/vitalbh/elan/internal/meetings"
)

// addActionTools registers the action status tools.
func addActionTools(s *mcp.Server, h *handlers) {
	mcp.AddTool(s, &mcp.Tool{
		Name: "actions_pending",
		Description: "Ações das reuniões ainda em aberto, mais recentes primeiro. owner=mine: as do dono; others: as de outras pessoas " +
			"(o que o dono espera dos outros); all: todas. Ações abertas há mais de 30 dias ficam de fora, a não ser com include_stale.",
	}, h.actionsPending)
	mcp.AddTool(s, &mcp.Tool{
		Name:        "action_update",
		Description: "Muda o status de uma ação (id de actions_pending ou get_meeting): done, dropped (cancelada) ou open. Só quando o dono pedir.",
	}, h.actionUpdate)
}

// ActionsIn is the input of actions_pending.
type ActionsIn struct {
	Owner        string `json:"owner,omitempty" jsonschema:"mine (do dono), others (dos outros) ou all (padrão)"`
	Person       string `json:"person,omitempty" jsonschema:"só ações em que esta pessoa está envolvida"`
	Since        string `json:"since,omitempty" jsonschema:"só de reuniões a partir desta data (AAAA-MM-DD)"`
	IncludeStale bool   `json:"include_stale,omitempty" jsonschema:"inclui ações abertas há mais de 30 dias"`
	Limit        int    `json:"limit,omitempty" jsonschema:"quantidade (padrão 30, máx. 100)"`
}

// ActionsOut is the output of actions_pending.
type ActionsOut struct {
	Actions []meetings.OpenAction `json:"actions"`
}

func (h *handlers) actionsPending(ctx context.Context, _ *mcp.CallToolRequest, in ActionsIn) (*mcp.CallToolResult, ActionsOut, error) {
	q := meetings.ActionQuery{
		Owner:        meetings.ActionOwner(in.Owner),
		Person:       in.Person,
		IncludeStale: in.IncludeStale,
		Limit:        clamp(in.Limit, 30, 100),
	}
	var err error
	if q.Since, err = parseDay(in.Since); err != nil {
		return nil, ActionsOut{}, err
	}
	list, err := h.d.Actions.OpenActions(ctx, q)
	if list == nil {
		list = []meetings.OpenAction{}
	}
	return nil, ActionsOut{Actions: list}, err
}

// ActionUpdateIn is the input of action_update.
type ActionUpdateIn struct {
	ID     int64  `json:"id" jsonschema:"id da ação"`
	Status string `json:"status" jsonschema:"done, dropped ou open"`
}

func (h *handlers) actionUpdate(ctx context.Context, _ *mcp.CallToolRequest, in ActionUpdateIn) (*mcp.CallToolResult, meetings.ActionState, error) {
	st, err := h.d.Actions.SetStatus(ctx, in.ID, in.Status)
	return nil, st, err
}
