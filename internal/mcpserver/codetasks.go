package mcpserver

import (
	"context"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/vitalbh/elan/internal/meetings"
)

// addCodeTaskTools registers the code task tools.
func addCodeTaskTools(s *mcp.Server, h *handlers) {
	mcp.AddTool(s, &mcp.Tool{
		Name: "codetask_create",
		Description: "Cria uma tarefa de código do dono (\"tarefa de código\", \"cria uma code task\"): algo para um agente fazer num repositório. " +
			"Use quando o dono pedir para criar uma. text autocontido; project como o dono disse (nome em projects do config ou outro); " +
			"context para instruções extras dele. Se ele citar uma reunião, ache o meeting_id com search ou list_meetings antes. " +
			"Para uma ação que já existe, use codetask_promote.",
	}, h.codetaskCreate)
	mcp.AddTool(s, &mcp.Tool{
		Name:        "codetasks_list",
		Description: "Tarefas de código do dono: abertas primeiro, mais novas primeiro. status: open (padrão), done, dropped ou all.",
	}, h.codetasksList)
	mcp.AddTool(s, &mcp.Tool{
		Name: "codetask_update",
		Description: "Muda o status de uma tarefa de código: done, dropped (cancelada) ou open. Se ela veio de uma ação, a ação muda junto " +
			"(e o Google Tasks acompanha). Só quando o dono pedir.",
	}, h.codetaskUpdate)
	mcp.AddTool(s, &mcp.Tool{
		Name: "codetask_suggestions",
		Description: "Ações do dono que a destilação classificou como código e que ainda não são tarefas de código. São sugestões: " +
			"só vire tarefa (codetask_promote) quando o dono escolher.",
	}, h.codetaskSuggestions)
	mcp.AddTool(s, &mcp.Tool{
		Name:        "codetask_promote",
		Description: "Transforma uma ação (item_id de codetask_suggestions, actions_pending ou get_meeting) em tarefa de código, com o texto, o projeto e a reunião dela. Promover de novo devolve a mesma tarefa.",
	}, h.codetaskPromote)
}

// CodeTaskCreateIn is the input of codetask_create.
type CodeTaskCreateIn struct {
	Text      string `json:"text" jsonschema:"o que fazer, autocontido"`
	Project   string `json:"project,omitempty" jsonschema:"projeto ou repositório, como o dono disse"`
	MeetingID string `json:"meeting_id,omitempty" jsonschema:"reunião de origem (meeting_id de search ou list_meetings)"`
	Context   string `json:"context,omitempty" jsonschema:"instruções ou notas do dono para quem for fazer"`
}

func (h *handlers) codetaskCreate(ctx context.Context, _ *mcp.CallToolRequest, in CodeTaskCreateIn) (*mcp.CallToolResult, meetings.CodeTask, error) {
	t, err := h.d.CodeTasks.Add(ctx, meetings.NewCodeTask{Text: in.Text, Project: in.Project, MeetingID: in.MeetingID, Context: in.Context})
	return nil, t, err
}

// CodeTasksListIn is the input of codetasks_list.
type CodeTasksListIn struct {
	Status string `json:"status,omitempty" jsonschema:"open (padrão), done, dropped ou all"`
	Limit  int    `json:"limit,omitempty" jsonschema:"quantidade (padrão 50, máx. 200)"`
}

// CodeTasksOut is the output of codetasks_list.
type CodeTasksOut struct {
	CodeTasks []meetings.CodeTask `json:"code_tasks"`
}

func (h *handlers) codetasksList(ctx context.Context, _ *mcp.CallToolRequest, in CodeTasksListIn) (*mcp.CallToolResult, CodeTasksOut, error) {
	q := meetings.CodeTaskQuery{Status: in.Status, Limit: clamp(in.Limit, 50, 200)}
	if in.Status == "all" {
		q = meetings.CodeTaskQuery{IncludeDone: true, Limit: q.Limit}
	}
	list, err := h.d.CodeTasks.List(ctx, q)
	if list == nil {
		list = []meetings.CodeTask{}
	}
	return nil, CodeTasksOut{CodeTasks: list}, err
}

// CodeTaskUpdateIn is the input of codetask_update.
type CodeTaskUpdateIn struct {
	ID     int64  `json:"id" jsonschema:"id da tarefa de código"`
	Status string `json:"status" jsonschema:"done, dropped ou open"`
}

func (h *handlers) codetaskUpdate(ctx context.Context, _ *mcp.CallToolRequest, in CodeTaskUpdateIn) (*mcp.CallToolResult, meetings.CodeTask, error) {
	t, err := h.d.CodeTasks.SetStatus(ctx, in.ID, in.Status)
	return nil, t, err
}

// SuggestionsIn is the input of codetask_suggestions.
type SuggestionsIn struct {
	Limit        int  `json:"limit,omitempty" jsonschema:"quantidade (padrão 30, máx. 100)"`
	IncludeStale bool `json:"include_stale,omitempty" jsonschema:"inclui ações abertas há mais de 30 dias"`
}

// SuggestionsOut is the output of codetask_suggestions.
type SuggestionsOut struct {
	Suggestions []meetings.Suggestion `json:"suggestions"`
}

func (h *handlers) codetaskSuggestions(ctx context.Context, _ *mcp.CallToolRequest, in SuggestionsIn) (*mcp.CallToolResult, SuggestionsOut, error) {
	list, err := h.d.CodeTasks.Suggestions(ctx, meetings.SuggestionQuery{Limit: clamp(in.Limit, 30, 100), IncludeStale: in.IncludeStale})
	if list == nil {
		list = []meetings.Suggestion{}
	}
	return nil, SuggestionsOut{Suggestions: list}, err
}

// PromoteIn is the input of codetask_promote.
type PromoteIn struct {
	ItemID int64 `json:"item_id" jsonschema:"id da ação"`
}

func (h *handlers) codetaskPromote(ctx context.Context, _ *mcp.CallToolRequest, in PromoteIn) (*mcp.CallToolResult, meetings.CodeTask, error) {
	t, err := h.d.CodeTasks.Promote(ctx, in.ItemID)
	return nil, t, err
}
