package mcpserver

import (
	"context"
	"fmt"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/vitalbh/elan/internal/google"
	"github.com/vitalbh/elan/internal/meetings"
)

// GoogleTasks is the owner's Google Tasks; *google.Tasks implements it.
type GoogleTasks interface {
	Lists(ctx context.Context) ([]google.TaskList, error)
	FindList(ctx context.Context, ref string) (google.TaskList, error)
	ListTasks(ctx context.Context, listID string, q google.TaskQuery) ([]google.Task, error)
	FindTask(ctx context.Context, listRef, id string) (google.Task, error)
	Create(ctx context.Context, listID string, n google.NewTask) (google.Task, error)
	Update(ctx context.Context, listID, id string, p google.TaskPatch) (google.Task, error)
}

// addTasksTools registers the Google Tasks tools.
func addTasksTools(s *mcp.Server, h *handlers) {
	mcp.AddTool(s, &mcp.Tool{
		Name:        "tasks_lists",
		Description: "Listas do Google Tasks do dono, com a padrão (\"Minhas tarefas\") marcada.",
	}, h.tasksLists)
	mcp.AddTool(s, &mcp.Tool{
		Name: "tasks_list",
		Description: "Tarefas do Google Tasks do dono. Sem list: as tarefas abertas de todas as listas. Datas de vencimento são dias (AAAA-MM-DD). " +
			"due_before filtra as que vencem antes da data (tarefas sem data ficam de fora).",
	}, h.tasksList)
	mcp.AddTool(s, &mcp.Tool{
		Name: "tasks_create",
		Description: "CRIA UMA TAREFA DE VERDADE no Google Tasks do dono, por padrão na lista padrão (\"Minhas tarefas\"). " +
			"Só use quando o dono pedir explicitamente para criar uma tarefa; confirme título e data antes.",
	}, h.tasksCreate)
	mcp.AddTool(s, &mcp.Tool{
		Name: "tasks_complete",
		Description: "CONCLUI (ou, com reopen, REABRE) DE VERDADE uma tarefa no Google Tasks do dono. Só use quando o dono pedir explicitamente. " +
			"Se a tarefa veio de uma ação de reunião, a ação também é marcada concluída (ou reaberta) no elan.",
	}, h.tasksComplete)
	mcp.AddTool(s, &mcp.Tool{
		Name: "tasks_update",
		Description: "ALTERA DE VERDADE uma tarefa no Google Tasks do dono: título, notas ou vencimento. " +
			"Só use quando o dono pedir explicitamente. Campos vazios ficam como estão; clear_due tira a data.",
	}, h.tasksUpdate)
}

// TaskListsOut is the output of tasks_lists.
type TaskListsOut struct {
	Lists []google.TaskList `json:"lists"`
}

func (h *handlers) tasksLists(ctx context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, TaskListsOut, error) {
	lists, err := h.d.Tasks.Lists(ctx)
	if lists == nil {
		lists = []google.TaskList{}
	}
	return nil, TaskListsOut{Lists: lists}, err
}

// TasksListIn is the input of tasks_list.
type TasksListIn struct {
	List             string `json:"list,omitempty" jsonschema:"nome ou id da lista; vazio = todas as listas"`
	IncludeCompleted bool   `json:"include_completed,omitempty" jsonschema:"inclui as concluídas"`
	DueBefore        string `json:"due_before,omitempty" jsonschema:"só as que vencem antes desta data (AAAA-MM-DD, exclusiva)"`
	Limit            int    `json:"limit,omitempty" jsonschema:"quantidade (padrão 100, máx. 300)"`
}

// TasksListOut is the output of tasks_list.
type TasksListOut struct {
	Tasks []google.Task `json:"tasks"`
	// Truncated: there were more tasks than limit.
	Truncated bool `json:"truncated,omitempty"`
}

func (h *handlers) tasksList(ctx context.Context, _ *mcp.CallToolRequest, in TasksListIn) (*mcp.CallToolResult, TasksListOut, error) {
	limit := clamp(in.Limit, 100, 300)
	q := google.TaskQuery{IncludeCompleted: in.IncludeCompleted, DueBefore: strings.TrimSpace(in.DueBefore)}
	var lists []google.TaskList
	if strings.TrimSpace(in.List) != "" {
		l, err := h.d.Tasks.FindList(ctx, in.List)
		if err != nil {
			return nil, TasksListOut{}, err
		}
		lists = []google.TaskList{l}
	} else {
		var err error
		if lists, err = h.d.Tasks.Lists(ctx); err != nil {
			return nil, TasksListOut{}, err
		}
	}
	out := TasksListOut{Tasks: []google.Task{}}
	for _, l := range lists {
		tasks, err := h.d.Tasks.ListTasks(ctx, l.ID, q)
		if err != nil {
			return nil, TasksListOut{}, fmt.Errorf("lista %q: %w", l.Title, err)
		}
		for _, t := range tasks {
			if len(out.Tasks) == limit {
				out.Truncated = true
				return nil, out, nil
			}
			t.List = l.Title
			out.Tasks = append(out.Tasks, t)
		}
	}
	return nil, out, nil
}

// TaskCreateIn is the input of tasks_create.
type TaskCreateIn struct {
	Title string `json:"title" jsonschema:"título da tarefa"`
	Notes string `json:"notes,omitempty" jsonschema:"notas"`
	Due   string `json:"due,omitempty" jsonschema:"vencimento (AAAA-MM-DD)"`
	List  string `json:"list,omitempty" jsonschema:"nome ou id da lista; padrão: a lista padrão do dono"`
}

func (h *handlers) tasksCreate(ctx context.Context, _ *mcp.CallToolRequest, in TaskCreateIn) (*mcp.CallToolResult, google.Task, error) {
	l, err := h.d.Tasks.FindList(ctx, in.List)
	if err != nil {
		return nil, google.Task{}, err
	}
	t, err := h.d.Tasks.Create(ctx, l.ID, google.NewTask{Title: in.Title, Notes: in.Notes, Due: in.Due})
	t.List = l.Title
	return nil, t, err
}

// TaskCompleteIn is the input of tasks_complete.
type TaskCompleteIn struct {
	ID     string `json:"id" jsonschema:"id da tarefa (de tasks_list ou o task_id de actions_pending)"`
	List   string `json:"list,omitempty" jsonschema:"nome ou id da lista; vazio = procura em todas"`
	Reopen bool   `json:"reopen,omitempty" jsonschema:"reabre em vez de concluir"`
}

// TaskCompleteOut is the output of tasks_complete.
type TaskCompleteOut struct {
	Task google.Task `json:"task"`
	// Action is the meeting action the task came from, with its status
	// after the change.
	Action *meetings.ActionState `json:"action,omitempty"`
	// Warning: the task changed but its action could not be updated.
	Warning string `json:"warning,omitempty"`
}

func (h *handlers) tasksComplete(ctx context.Context, _ *mcp.CallToolRequest, in TaskCompleteIn) (*mcp.CallToolResult, TaskCompleteOut, error) {
	found, err := h.d.Tasks.FindTask(ctx, in.List, in.ID)
	if err != nil {
		return nil, TaskCompleteOut{}, err
	}
	done := !in.Reopen
	t, err := h.d.Tasks.Update(ctx, found.ListID, found.ID, google.TaskPatch{Done: &done})
	if err != nil {
		return nil, TaskCompleteOut{}, err
	}
	t.List = found.List
	out := TaskCompleteOut{Task: t}
	if h.d.Actions != nil {
		state := meetings.TaskState{ID: t.ID, Completed: t.Done, At: t.Updated, WebLink: t.Link}
		if t.CompletedAt != nil {
			state.At = *t.CompletedAt
		}
		if out.Action, err = h.d.Actions.ApplyTask(ctx, t.ListID, state); err != nil {
			out.Warning = "tarefa alterada, mas a ação no elan não: " + err.Error()
		}
	}
	return nil, out, nil
}

// TaskUpdateIn is the input of tasks_update.
type TaskUpdateIn struct {
	ID       string `json:"id" jsonschema:"id da tarefa"`
	List     string `json:"list,omitempty" jsonschema:"nome ou id da lista; vazio = procura em todas"`
	Title    string `json:"title,omitempty" jsonschema:"novo título"`
	Notes    string `json:"notes,omitempty" jsonschema:"novas notas (substituem as atuais)"`
	Due      string `json:"due,omitempty" jsonschema:"novo vencimento (AAAA-MM-DD)"`
	ClearDue bool   `json:"clear_due,omitempty" jsonschema:"tira a data de vencimento"`
}

func (h *handlers) tasksUpdate(ctx context.Context, _ *mcp.CallToolRequest, in TaskUpdateIn) (*mcp.CallToolResult, google.Task, error) {
	var p google.TaskPatch
	if s := strings.TrimSpace(in.Title); s != "" {
		p.Title = &s
	}
	if s := strings.TrimSpace(in.Notes); s != "" {
		p.Notes = &s
	}
	if s := strings.TrimSpace(in.Due); s != "" {
		p.Due = &s
	}
	if in.ClearDue {
		if p.Due != nil {
			return nil, google.Task{}, fmt.Errorf("use due ou clear_due, não os dois")
		}
		empty := ""
		p.Due = &empty
	}
	if p == (google.TaskPatch{}) {
		return nil, google.Task{}, fmt.Errorf("nada para mudar: passe title, notes, due ou clear_due")
	}
	found, err := h.d.Tasks.FindTask(ctx, in.List, in.ID)
	if err != nil {
		return nil, google.Task{}, err
	}
	t, err := h.d.Tasks.Update(ctx, found.ListID, found.ID, p)
	t.List = found.List
	return nil, t, err
}
