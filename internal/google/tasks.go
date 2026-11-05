package google

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"google.golang.org/api/googleapi"
	"google.golang.org/api/option"
	"google.golang.org/api/tasks/v1"
)

// DefaultTaskList is the API alias of the owner's default list ("Minhas
// tarefas" / "My Tasks").
const DefaultTaskList = "@default"

// taskPage is the largest maxResults the Tasks API accepts.
const taskPage = 100

var (
	// ErrTasksScope means the saved token lacks the Tasks scope.
	ErrTasksScope = errors.New("o login do Google não tem acesso ao Google Tasks: rode `elan google-login` de novo e aceite o acesso às tarefas")
	// ErrTasksAPIDisabled means the Tasks API is off in the OAuth client's project.
	ErrTasksAPIDisabled = errors.New("a Google Tasks API não está ativada no projeto do client OAuth")
	// ErrTaskNotFound means the task (or its list) does not exist.
	ErrTaskNotFound = errors.New("tarefa não encontrada")
	// ErrTaskListNotFound means no list has that id or name.
	ErrTaskListNotFound = errors.New("lista de tarefas não encontrada")
)

// TasksOptions configures a Tasks client.
type TasksOptions struct {
	// Location is the owner's time zone for completion and update times;
	// time.Local when nil. Due dates are dates, not instants: see Task.Due.
	Location *time.Location
	// Endpoint overrides the API root (tests).
	Endpoint string
}

// Tasks reads and writes the owner's Google Tasks.
type Tasks struct {
	open     Opener
	loc      *time.Location
	endpoint string
}

// NewTasksClient builds a Tasks client. open runs on every call, so a login
// made after the process started is picked up.
func NewTasksClient(open Opener, o TasksOptions) *Tasks {
	if o.Location == nil {
		o.Location = time.Local
	}
	return &Tasks{open: open, loc: o.Location, endpoint: o.Endpoint}
}

// NewTasks builds a Tasks client on an already authorized HTTP client.
func NewTasks(_ context.Context, hc *http.Client) (*Tasks, error) {
	return NewTasksClient(func(context.Context) (*http.Client, error) { return hc, nil }, TasksOptions{}), nil
}

func (t *Tasks) service(ctx context.Context) (*tasks.Service, error) {
	hc, err := t.open(ctx)
	if err != nil {
		return nil, fmt.Errorf("Google Tasks indisponível: %w", err)
	}
	opts := []option.ClientOption{option.WithHTTPClient(hc)}
	if t.endpoint != "" {
		opts = append(opts, option.WithEndpoint(t.endpoint))
	}
	return tasks.NewService(ctx, opts...)
}

// TaskList is a Google Tasks list.
type TaskList struct {
	ID    string `json:"id"`
	Title string `json:"title"`
	// Default is the owner's default list ("Minhas tarefas").
	Default bool `json:"default,omitempty"`
}

// Task is one Google Task.
type Task struct {
	ID     string `json:"id"`
	ListID string `json:"list_id"`
	// List is the list's title, when the caller knows it.
	List  string `json:"list,omitempty"`
	Title string `json:"title"`
	Notes string `json:"notes,omitempty"`
	// Due is the due date (AAAA-MM-DD). Google Tasks keeps only the date and
	// sends it as midnight UTC: the date is read in UTC, never converted to
	// the local zone (which would show the day before in UTC−3).
	Due  string `json:"due,omitempty"`
	Done bool   `json:"done"`
	// CompletedAt and Updated are in the client's location.
	CompletedAt *time.Time `json:"completed_at,omitempty"`
	Updated     time.Time  `json:"updated"`
	Parent      string     `json:"parent,omitempty"`
	Link        string     `json:"link,omitempty"`
	// position orders siblings (lexicographically).
	position string
}

func (t *Tasks) taskOf(listID string, it *tasks.Task) Task {
	task := Task{ID: it.Id, ListID: listID, Title: it.Title, Notes: it.Notes, Due: dueOf(it.Due),
		Done: it.Status == "completed", Parent: it.Parent, Link: it.WebViewLink, position: it.Position}
	if it.Completed != nil {
		if c, err := time.Parse(time.RFC3339, *it.Completed); err == nil {
			c = c.In(t.loc)
			task.CompletedAt = &c
		}
	}
	if u, err := time.Parse(time.RFC3339, it.Updated); err == nil {
		task.Updated = u.In(t.loc)
	}
	return task
}

// dueOf reads the API's due field (midnight UTC of the due date).
func dueOf(s string) string {
	if s == "" {
		return ""
	}
	d, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return ""
	}
	return d.UTC().Format("2006-01-02")
}

// apiDue writes a due date (AAAA-MM-DD) as the API expects it.
func apiDue(day string) (string, error) {
	d, err := time.Parse("2006-01-02", strings.TrimSpace(day))
	if err != nil {
		return "", fmt.Errorf("data de vencimento inválida %q: use AAAA-MM-DD", day)
	}
	return d.Format("2006-01-02") + "T00:00:00.000Z", nil
}

// Lists returns every task list, the default one flagged.
func (t *Tasks) Lists(ctx context.Context) ([]TaskList, error) {
	svc, err := t.service(ctx)
	if err != nil {
		return nil, err
	}
	var out []TaskList
	err = svc.Tasklists.List().MaxResults(taskPage).Pages(ctx, func(page *tasks.TaskLists) error {
		for _, l := range page.Items {
			out = append(out, TaskList{ID: l.Id, Title: l.Title})
		}
		return nil
	})
	if err != nil {
		return nil, tasksFail("listando as listas de tarefas", err)
	}
	def, err := svc.Tasklists.Get(DefaultTaskList).Context(ctx).Do()
	if err != nil {
		return nil, tasksFail("lendo a lista padrão", err)
	}
	for i := range out {
		out[i].Default = out[i].ID == def.Id
	}
	return out, nil
}

// FindList returns the list with that id or title (case-insensitive). An
// empty ref or "@default" is the default list.
func (t *Tasks) FindList(ctx context.Context, ref string) (TaskList, error) {
	ref = strings.TrimSpace(ref)
	lists, err := t.Lists(ctx)
	if err != nil {
		return TaskList{}, err
	}
	wantDefault := ref == "" || ref == DefaultTaskList
	for _, l := range lists {
		if (wantDefault && l.Default) || l.ID == ref {
			return l, nil
		}
	}
	names := make([]string, 0, len(lists))
	for _, l := range lists {
		if strings.EqualFold(strings.TrimSpace(l.Title), ref) {
			return l, nil
		}
		names = append(names, l.Title)
	}
	return TaskList{}, fmt.Errorf("%w: %q (existem: %s)", ErrTaskListNotFound, ref, strings.Join(names, ", "))
}

// TaskQuery filters ListTasks. Zero values mean no filter.
type TaskQuery struct {
	// IncludeCompleted also returns completed tasks (hidden ones included).
	IncludeCompleted bool
	// DueFrom keeps tasks due on or after this date (AAAA-MM-DD).
	DueFrom string
	// DueBefore keeps tasks due before this date (AAAA-MM-DD, exclusive).
	DueBefore string
}

// ListTasks returns the tasks of a list (open ones unless IncludeCompleted),
// each subtask right after its parent, siblings in the list's order. A due
// filter leaves out tasks without a due date.
func (t *Tasks) ListTasks(ctx context.Context, listID string, q TaskQuery) ([]Task, error) {
	for _, d := range []string{q.DueFrom, q.DueBefore} {
		if d != "" {
			if _, err := apiDue(d); err != nil {
				return nil, err
			}
		}
	}
	svc, err := t.service(ctx)
	if err != nil {
		return nil, err
	}
	call := svc.Tasks.List(listID).MaxResults(taskPage).
		ShowCompleted(q.IncludeCompleted).ShowHidden(q.IncludeCompleted)
	var out []Task
	err = call.Pages(ctx, func(page *tasks.Tasks) error {
		for _, it := range page.Items {
			if it.Deleted {
				continue
			}
			task := t.taskOf(listID, it)
			if !q.IncludeCompleted && task.Done {
				continue
			}
			if (q.DueFrom != "" || q.DueBefore != "") &&
				(task.Due == "" || (q.DueFrom != "" && task.Due < q.DueFrom) || (q.DueBefore != "" && task.Due >= q.DueBefore)) {
				continue
			}
			out = append(out, task)
		}
		return nil
	})
	if err != nil {
		return nil, tasksFail("listando as tarefas", err)
	}
	return orderTasks(out), nil
}

// orderTasks puts each subtask right after its parent; orphans (parent not
// in the slice) stay at the top level.
func orderTasks(list []Task) []Task {
	ids := make(map[string]bool, len(list))
	for _, t := range list {
		ids[t.ID] = true
	}
	children := map[string][]Task{}
	var top []Task
	for _, t := range list {
		if t.Parent != "" && ids[t.Parent] {
			children[t.Parent] = append(children[t.Parent], t)
		} else {
			top = append(top, t)
		}
	}
	byPos := func(s []Task) { sort.SliceStable(s, func(i, j int) bool { return s[i].position < s[j].position }) }
	byPos(top)
	out := make([]Task, 0, len(list))
	for _, t := range top {
		out = append(out, t)
		kids := children[t.ID]
		byPos(kids)
		out = append(out, kids...)
	}
	return out
}

// Get returns one task; a deleted task is ErrTaskNotFound.
func (t *Tasks) Get(ctx context.Context, listID, id string) (Task, error) {
	svc, err := t.service(ctx)
	if err != nil {
		return Task{}, err
	}
	it, err := svc.Tasks.Get(listID, id).Context(ctx).Do()
	if err != nil {
		return Task{}, tasksFail("lendo a tarefa", err)
	}
	if it.Deleted {
		return Task{}, fmt.Errorf("lendo a tarefa: %w", ErrTaskNotFound)
	}
	return t.taskOf(listID, it), nil
}

// FindTask returns the task with that id in the list listRef (id or title),
// or, with an empty listRef, in whichever list holds it (default list first).
func (t *Tasks) FindTask(ctx context.Context, listRef, id string) (Task, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return Task{}, errors.New("o id da tarefa é obrigatório")
	}
	if strings.TrimSpace(listRef) != "" {
		l, err := t.FindList(ctx, listRef)
		if err != nil {
			return Task{}, err
		}
		task, err := t.Get(ctx, l.ID, id)
		task.List = l.Title
		return task, err
	}
	lists, err := t.Lists(ctx)
	if err != nil {
		return Task{}, err
	}
	sort.SliceStable(lists, func(i, j int) bool { return lists[i].Default && !lists[j].Default })
	for _, l := range lists {
		task, err := t.Get(ctx, l.ID, id)
		if errors.Is(err, ErrTaskNotFound) {
			continue
		}
		task.List = l.Title
		return task, err
	}
	return Task{}, fmt.Errorf("%w: %s (em nenhuma lista)", ErrTaskNotFound, id)
}

// NewTask is a task to create.
type NewTask struct {
	Title string
	Notes string
	// Due is a date (AAAA-MM-DD); empty means no due date.
	Due string
}

// Create adds a task at the top of a list.
func (t *Tasks) Create(ctx context.Context, listID string, n NewTask) (Task, error) {
	n.Title = strings.TrimSpace(n.Title)
	if n.Title == "" {
		return Task{}, errors.New("o título da tarefa é obrigatório")
	}
	body := &tasks.Task{Title: n.Title, Notes: strings.TrimSpace(n.Notes)}
	if strings.TrimSpace(n.Due) != "" {
		due, err := apiDue(n.Due)
		if err != nil {
			return Task{}, err
		}
		body.Due = due
	}
	svc, err := t.service(ctx)
	if err != nil {
		return Task{}, err
	}
	it, err := svc.Tasks.Insert(listID, body).Context(ctx).Do()
	if err != nil {
		return Task{}, tasksFail("criando a tarefa", err)
	}
	return t.taskOf(listID, it), nil
}

// TaskPatch changes fields of a task; nil fields stay as they are.
type TaskPatch struct {
	Title *string
	// Notes replaces the notes; "" clears them.
	Notes *string
	// Due sets the due date (AAAA-MM-DD); "" clears it.
	Due *string
	// Done completes (true) or reopens (false) the task.
	Done *bool
}

// Update applies p to a task.
func (t *Tasks) Update(ctx context.Context, listID, id string, p TaskPatch) (Task, error) {
	if p.Title == nil && p.Notes == nil && p.Due == nil && p.Done == nil {
		return Task{}, errors.New("nada para mudar: passe título, notas, vencimento ou status")
	}
	body := &tasks.Task{}
	if p.Title != nil {
		if body.Title = strings.TrimSpace(*p.Title); body.Title == "" {
			return Task{}, errors.New("o título da tarefa não pode ficar vazio")
		}
	}
	if p.Notes != nil {
		if body.Notes = strings.TrimSpace(*p.Notes); body.Notes == "" {
			body.NullFields = append(body.NullFields, "Notes")
		}
	}
	if p.Due != nil {
		if strings.TrimSpace(*p.Due) == "" {
			body.NullFields = append(body.NullFields, "Due")
		} else {
			due, err := apiDue(*p.Due)
			if err != nil {
				return Task{}, err
			}
			body.Due = due
		}
	}
	if p.Done != nil {
		if *p.Done {
			body.Status = "completed"
		} else {
			body.Status = "needsAction"
			body.NullFields = append(body.NullFields, "Completed")
		}
	}
	svc, err := t.service(ctx)
	if err != nil {
		return Task{}, err
	}
	it, err := svc.Tasks.Patch(listID, id, body).Context(ctx).Do()
	if err != nil {
		return Task{}, tasksFail("alterando a tarefa", err)
	}
	return t.taskOf(listID, it), nil
}

// SetCompleted completes (done) or reopens a task.
func (t *Tasks) SetCompleted(ctx context.Context, listID, id string, done bool) (Task, error) {
	return t.Update(ctx, listID, id, TaskPatch{Done: &done})
}

// Delete deletes a task.
func (t *Tasks) Delete(ctx context.Context, listID, id string) error {
	svc, err := t.service(ctx)
	if err != nil {
		return err
	}
	if err := svc.Tasks.Delete(listID, id).Context(ctx).Do(); err != nil {
		return tasksFail("apagando a tarefa", err)
	}
	return nil
}

// EnsureList returns the id of the task list with that title, creating it
// when missing.
func (t *Tasks) EnsureList(ctx context.Context, title string) (string, error) {
	svc, err := t.service(ctx)
	if err != nil {
		return "", err
	}
	var id string
	err = svc.Tasklists.List().MaxResults(taskPage).Pages(ctx, func(page *tasks.TaskLists) error {
		for _, l := range page.Items {
			if l.Title == title && id == "" {
				id = l.Id
			}
		}
		return nil
	})
	if err != nil {
		return "", tasksFail("listando as listas de tarefas", err)
	}
	if id != "" {
		return id, nil
	}
	l, err := svc.Tasklists.Insert(&tasks.TaskList{Title: title}).Context(ctx).Do()
	if err != nil {
		return "", tasksFail("criando a lista de tarefas", err)
	}
	return l.Id, nil
}

// Insert adds a task to a list and returns its id.
func (t *Tasks) Insert(ctx context.Context, listID, title, notes string) (string, error) {
	task, err := t.Create(ctx, listID, NewTask{Title: title, Notes: notes})
	return task.ID, err
}

// TaskInfo is the status of a task.
type TaskInfo struct {
	ID string
	// Status is "needsAction" or "completed".
	Status string
	// Completed and Updated are RFC 3339; Completed is empty when open.
	Completed, Updated string
	Deleted            bool
	WebLink            string
}

// ListStatus returns the tasks of a list updated at or after updatedMin
// (every task when zero), completed, hidden and deleted ones included.
func (t *Tasks) ListStatus(ctx context.Context, listID string, updatedMin time.Time) ([]TaskInfo, error) {
	svc, err := t.service(ctx)
	if err != nil {
		return nil, err
	}
	call := svc.Tasks.List(listID).
		ShowCompleted(true).ShowHidden(true).ShowDeleted(true).MaxResults(taskPage).
		Fields("nextPageToken", "items(id,status,completed,deleted,updated,webViewLink)")
	if !updatedMin.IsZero() {
		call = call.UpdatedMin(updatedMin.UTC().Format(time.RFC3339))
	}
	var out []TaskInfo
	err = call.Pages(ctx, func(page *tasks.Tasks) error {
		for _, it := range page.Items {
			info := TaskInfo{ID: it.Id, Status: it.Status, Updated: it.Updated, Deleted: it.Deleted, WebLink: it.WebViewLink}
			if it.Completed != nil {
				info.Completed = *it.Completed
			}
			out = append(out, info)
		}
		return nil
	})
	if err != nil {
		return nil, tasksFail("lendo o status das tarefas", err)
	}
	return out, nil
}

// tasksFail wraps err for op, turning the errors only a new login or a
// console change fixes into clear sentinels.
func tasksFail(op string, err error) error {
	var ge *googleapi.Error
	if errors.As(err, &ge) {
		text := strings.ToLower(ge.Message + " " + ge.Body)
		for _, e := range ge.Errors {
			text += " " + strings.ToLower(e.Reason+" "+e.Message)
		}
		switch {
		case ge.Code == http.StatusNotFound || ge.Code == http.StatusGone:
			err = ErrTaskNotFound
		case ge.Code == http.StatusForbidden && (strings.Contains(text, "insufficientpermissions") ||
			strings.Contains(text, "insufficient authentication scopes") || strings.Contains(text, "access_token_scope_insufficient")):
			err = ErrTasksScope
		case ge.Code == http.StatusForbidden && (strings.Contains(text, "accessnotconfigured") ||
			strings.Contains(text, "service_disabled") || strings.Contains(text, "is disabled")):
			err = ErrTasksAPIDisabled
		}
	}
	return fmt.Errorf("%s: %w", op, err)
}
