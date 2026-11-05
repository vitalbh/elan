package meetings

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// Categories of an action (meeting_items.category).
const (
	// CategoryCode: the action requires changing code, config, infra, tests,
	// a repo, a deploy or investigating a bug in a system.
	CategoryCode = "code"
	// CategoryOther: everything else (meetings, conversations, docs,
	// decisions, hiring, marketing).
	CategoryOther = "other"
)

// classifyRules tells the model how to classify an action. Shared by the
// distiller and the backfill (`elan actions classify`).
const classifyRules = `Classificação de ação: category "code" quando a ação exige mudar código, configuração, infraestrutura, testes, um repositório, um deploy ou investigar um bug num sistema; "other" para o resto (reuniões, conversas, alinhamentos, documentos, decisões, contratação, marketing, comunicação, design visual e layouts, relatórios e pesquisas que não mexem num sistema). project: o projeto, produto ou sistema da ação como dito nas fontes ("Vega", "Blue Orion", "Nimbus"), sem inventar; "" quando não houver um claro.`

// knownProjectsHint lists the configured projects (name, aliases,
// description), so the model names an action's project with the configured
// name and tells similar projects apart (front-end vs backend).
func knownProjectsHint(projects []Project) string {
	if len(projects) == 0 {
		return ""
	}
	sorted := append([]Project(nil), projects...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Name < sorted[j].Name })
	var b strings.Builder
	b.WriteString("\n## Projetos conhecidos\n")
	b.WriteString("Quando o projeto de uma ação for um destes (pelo nome, por um apelido ou pela descrição), " +
		"devolva em project o nome exatamente como está antes dos dois-pontos. Quando as fontes citam só o produto " +
		"(\"Blue Orion\") e há mais de um projeto dele, escolha pela descrição o que a ação muda. " +
		"Se nenhum servir, use o nome dito nas fontes.\n")
	for _, p := range sorted {
		fmt.Fprintf(&b, "- %s:", p.Name)
		if len(p.Aliases) > 0 {
			fmt.Fprintf(&b, " apelidos %s.", strings.Join(p.Aliases, ", "))
		}
		if d := oneLine(p.Description); d != "" {
			fmt.Fprintf(&b, " %s", d)
		}
		b.WriteString("\n")
	}
	return b.String()
}

// cleanClass validates an item's classification: kept on actions only. An
// action without a category stays unclassified (the backfill picks it up).
func cleanClass(it *Item) error {
	it.Category = strings.ToLower(strings.TrimSpace(it.Category))
	it.Project = oneLine(it.Project)
	if it.Kind != KindAction {
		it.Category, it.Project = "", ""
		return nil
	}
	switch it.Category {
	case "", CategoryCode, CategoryOther:
		return nil
	default:
		return fmt.Errorf("category inválida %q", it.Category)
	}
}

// codeInfo is the classification and start tracking of one of a meeting's
// own actions, kept across a re-distillation like its status.
type codeInfo struct {
	Category  sql.NullString
	Project   string
	StartedAt sql.NullString
	Branch    sql.NullString
}

// ownCodeInfo returns the classification and start tracking of a meeting's
// actions, by textHash of their text.
func ownCodeInfo(ctx context.Context, q dbtx, meetingID string) (map[string]codeInfo, error) {
	rows, err := q.QueryContext(ctx, `
		SELECT text, category, project, started_at, branch FROM meeting_items
		WHERE meeting_id = ? AND kind = 'action' AND (category IS NOT NULL OR started_at IS NOT NULL)`, meetingID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]codeInfo{}
	for rows.Next() {
		var text string
		var c codeInfo
		if err := rows.Scan(&text, &c.Category, &c.Project, &c.StartedAt, &c.Branch); err != nil {
			return nil, err
		}
		out[textHash(text)] = c
	}
	return out, rows.Err()
}

// saveCodeInfo writes a new action item's classification: the carried one
// when the action was classified before (same text), else the model's.
func saveCodeInfo(ctx context.Context, q dbtx, id int64, it Item, carried codeInfo) error {
	if it.Kind != KindAction {
		return nil
	}
	category := sql.NullString{String: it.Category, Valid: it.Category != ""}
	project := it.Project
	if carried.Category.Valid {
		category, project = carried.Category, carried.Project
	}
	if !category.Valid && project == "" && !carried.StartedAt.Valid {
		return nil
	}
	_, err := q.ExecContext(ctx, `
		UPDATE meeting_items SET category = ?, project = ?, started_at = ?, branch = ? WHERE id = ?`,
		category, project, carried.StartedAt, carried.Branch, id)
	return err
}

// linkedCodeTasks returns the code tasks promoted from a meeting's actions,
// by textHash of the action's text, to re-link them after a
// re-distillation.
func linkedCodeTasks(ctx context.Context, q dbtx, meetingID string) (map[string][]int64, error) {
	rows, err := q.QueryContext(ctx, `
		SELECT c.id, i.text FROM code_tasks c JOIN meeting_items i ON i.id = c.source_item_id
		WHERE i.meeting_id = ? ORDER BY c.id`, meetingID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string][]int64{}
	for rows.Next() {
		var id int64
		var text string
		if err := rows.Scan(&id, &text); err != nil {
			return nil, err
		}
		h := textHash(text)
		out[h] = append(out[h], id)
	}
	return out, rows.Err()
}

// relinkCodeTask points a code task promoted from the old item with the
// same text to the new action item; each task is re-linked once.
func relinkCodeTask(ctx context.Context, q dbtx, linked map[string][]int64, newID int64, it Item) error {
	if it.Kind != KindAction {
		return nil
	}
	h := textHash(it.Text)
	ids := linked[h]
	if len(ids) == 0 {
		return nil
	}
	linked[h] = ids[1:]
	_, err := q.ExecContext(ctx, `UPDATE code_tasks SET source_item_id = ? WHERE id = ?`, newID, ids[0])
	return err
}

// Project maps a project name, as said in meetings, to its folder.
type Project struct {
	Name        string
	Folder      string
	Aliases     []string
	Description string
}

// Projects resolves an action's project to a configured project: the name
// or an alias, ignoring case, accents, spaces and punctuation ("blueorion"
// matches "Blue Orion").
type Projects struct {
	byKey map[string]Project
	list  []Project
}

// NewProjects builds a Projects.
func NewProjects(list []Project) *Projects {
	p := &Projects{byKey: map[string]Project{}}
	for _, pr := range list {
		p.list = append(p.list, pr)
		for _, n := range append([]string{pr.Name}, pr.Aliases...) {
			if k := projectKey(n); k != "" {
				if _, dup := p.byKey[k]; !dup {
					p.byKey[k] = pr
				}
			}
		}
	}
	sort.Slice(p.list, func(i, j int) bool { return p.list[i].Name < p.list[j].Name })
	return p
}

// List returns the configured projects, sorted by name.
func (p *Projects) List() []Project {
	if p == nil {
		return nil
	}
	return p.list
}

// Resolve returns the configured project an action's project refers to.
func (p *Projects) Resolve(name string) (Project, bool) {
	if p == nil {
		return Project{}, false
	}
	k := projectKey(name)
	if k == "" {
		return Project{}, false
	}
	pr, ok := p.byKey[k]
	return pr, ok
}

// folder returns the folder of an action's project, "" when unmapped.
func (p *Projects) folder(name string) string {
	pr, _ := p.Resolve(name)
	return pr.Folder
}

// projectKey folds a project name to lowercase letters and digits.
func projectKey(s string) string {
	var b strings.Builder
	for _, r := range foldName(s) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// CodeTask is a task in a code base the owner asked for: created by hand or
// promoted from an action (code_tasks).
type CodeTask struct {
	ID      int64  `json:"id"`
	Text    string `json:"text"`
	Project string `json:"project"`
	// Folder is the project's folder from config.json, "" when unmapped.
	Folder string `json:"folder"`
	// Context holds the owner's extra instructions or notes.
	Context string `json:"context"`
	Status  string `json:"status"`
	// MeetingID, MeetingTitle, MeetingAt: the meeting it came from, if any.
	MeetingID    string     `json:"meeting_id"`
	MeetingTitle string     `json:"meeting_title"`
	MeetingAt    *time.Time `json:"meeting_at"`
	// AgeDays counts from the task's creation.
	AgeDays int `json:"age_days"`
	// SourceItemID is the action it was promoted from, if any.
	SourceItemID *int64     `json:"source_item_id"`
	StartedAt    *time.Time `json:"started_at"`
	Branch       string     `json:"branch"`
	CreatedAt    time.Time  `json:"created_at"`
}

// CodeTaskQuery filters CodeTasks.List.
type CodeTaskQuery struct {
	// Status keeps the tasks with this status (default open).
	Status string
	// IncludeDone returns tasks of every status; Status is ignored.
	IncludeDone bool
	// Limit caps the result (default 100).
	Limit int
}

// NewCodeTask is the input of CodeTasks.Add.
type NewCodeTask struct {
	Text string
	// Project is any text; resolved to a folder through config.json.
	Project   string
	MeetingID string
	Context   string
}

// Suggestion is one of the owner's open actions classified as code that is
// not a code task yet.
type Suggestion struct {
	ItemID       int64      `json:"item_id"`
	Text         string     `json:"text"`
	Project      string     `json:"project"`
	Folder       string     `json:"folder"`
	People       []string   `json:"people"`
	MeetingID    string     `json:"meeting_id"`
	MeetingTitle string     `json:"meeting_title"`
	MeetingAt    *time.Time `json:"meeting_at"`
	// AgeDays counts from the meeting (from its ingestion when undated).
	AgeDays int `json:"age_days"`
}

// SuggestionQuery filters CodeTasks.Suggestions.
type SuggestionQuery struct {
	// Limit caps the result (default 50).
	Limit int
	// IncludeStale also returns actions open for longer than
	// DefaultStaleAfter.
	IncludeStale bool
}

// ErrCodeTaskNotFound means there is no code task with that id.
var ErrCodeTaskNotFound = errors.New("tarefa de código não encontrada")

// CodeTasks manages the owner's code tasks and suggests actions to promote.
type CodeTasks struct {
	db         *sql.DB
	actions    *Actions
	ownerNames []string
	projects   *Projects
}

// NewCodeTasks builds a CodeTasks. ownerNames identify the owner as in
// NewActions (for the suggestions and the branch slug).
func NewCodeTasks(db *sql.DB, ownerNames []string, projects *Projects) *CodeTasks {
	return &CodeTasks{db: db, actions: NewActions(db, ownerNames), ownerNames: ownerNames, projects: projects}
}

func (c *CodeTasks) nowString() string { return c.actions.now().UTC().Format(time.RFC3339) }

const codeTaskSelect = `
	SELECT c.id, c.text, c.project, c.context, c.status, COALESCE(c.meeting_id, ''),
		COALESCE(m.title, ''), m.started_at, c.source_item_id, c.started_at,
		COALESCE(c.branch, ''), c.created_at
	FROM code_tasks c LEFT JOIN meetings m ON m.id = c.meeting_id`

type rowScanner interface{ Scan(dest ...any) error }

func (c *CodeTasks) scanTask(r rowScanner) (CodeTask, error) {
	var t CodeTask
	var meetingAt, started sql.NullString
	var source sql.NullInt64
	var created string
	if err := r.Scan(&t.ID, &t.Text, &t.Project, &t.Context, &t.Status, &t.MeetingID,
		&t.MeetingTitle, &meetingAt, &source, &started, &t.Branch, &created); err != nil {
		return CodeTask{}, err
	}
	t.MeetingAt = parseStarted(meetingAt)
	t.StartedAt = parseStarted(started)
	if source.Valid {
		t.SourceItemID = &source.Int64
	}
	if ts, err := time.Parse(time.RFC3339, created); err == nil {
		t.CreatedAt = ts
		t.AgeDays = int(c.actions.now().Sub(ts).Hours() / 24)
	}
	t.Folder = c.projects.folder(t.Project)
	return t, nil
}

// List returns code tasks: open first, then newest first.
func (c *CodeTasks) List(ctx context.Context, q CodeTaskQuery) ([]CodeTask, error) {
	if q.Limit <= 0 {
		q.Limit = 100
	}
	where, args := "", []any{}
	if !q.IncludeDone {
		status := q.Status
		if status == "" {
			status = StatusOpen
		}
		if err := validStatus(status); err != nil {
			return nil, err
		}
		where, args = " WHERE c.status = ?", append(args, status)
	}
	rows, err := c.db.QueryContext(ctx, codeTaskSelect+where+`
		ORDER BY c.status <> 'open', julianday(c.created_at) DESC, c.id DESC
		LIMIT ?`, append(args, q.Limit)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []CodeTask{}
	for rows.Next() {
		t, err := c.scanTask(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// Get returns one code task.
func (c *CodeTasks) Get(ctx context.Context, id int64) (CodeTask, error) {
	t, err := c.scanTask(c.db.QueryRowContext(ctx, codeTaskSelect+` WHERE c.id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return CodeTask{}, fmt.Errorf("%w: %d", ErrCodeTaskNotFound, id)
	}
	return t, err
}

// projectName returns the configured name of a project when it resolves to
// one ("vega.example.com" → "Vega"), else the text as given.
func (c *CodeTasks) projectName(name string) string {
	name = oneLine(name)
	if pr, ok := c.projects.Resolve(name); ok {
		return pr.Name
	}
	return name
}

// Add creates a code task. The project may be any text (the folder stays
// empty when it is not in config.json); the meeting, when given, must exist.
func (c *CodeTasks) Add(ctx context.Context, n NewCodeTask) (CodeTask, error) {
	text := oneLine(n.Text)
	if text == "" {
		return CodeTask{}, errors.New("texto da tarefa vazio")
	}
	meeting := sql.NullString{String: strings.TrimSpace(n.MeetingID), Valid: strings.TrimSpace(n.MeetingID) != ""}
	if meeting.Valid {
		var found int
		if err := c.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM meetings WHERE id = ?`, meeting.String).Scan(&found); err != nil {
			return CodeTask{}, err
		}
		if found == 0 {
			return CodeTask{}, fmt.Errorf("%w: %s", ErrNotFound, meeting.String)
		}
	}
	res, err := c.db.ExecContext(ctx, `
		INSERT INTO code_tasks (text, project, context, meeting_id, created_at) VALUES (?, ?, ?, ?, ?)`,
		text, c.projectName(n.Project), strings.TrimSpace(n.Context), meeting, c.nowString())
	if err != nil {
		return CodeTask{}, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return CodeTask{}, err
	}
	return c.Get(ctx, id)
}

// Promote makes an action a code task, copying its text, project and
// meeting. Promoting the same action again returns the existing task.
func (c *CodeTasks) Promote(ctx context.Context, itemID int64) (CodeTask, error) {
	id, err := func() (int64, error) {
		tx, err := c.db.BeginTx(ctx, nil)
		if err != nil {
			return 0, err
		}
		defer tx.Rollback() //nolint:errcheck // no-op after commit
		var id int64
		err = tx.QueryRowContext(ctx, `SELECT id FROM code_tasks WHERE source_item_id = ?`, itemID).Scan(&id)
		if err == nil {
			return id, nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return 0, err
		}
		var text, project, meetingID string
		err = tx.QueryRowContext(ctx, `SELECT text, project, meeting_id FROM meeting_items WHERE id = ? AND kind = 'action'`,
			itemID).Scan(&text, &project, &meetingID)
		if errors.Is(err, sql.ErrNoRows) {
			return 0, fmt.Errorf("%w: %d", ErrActionNotFound, itemID)
		}
		if err != nil {
			return 0, err
		}
		res, err := tx.ExecContext(ctx, `
			INSERT INTO code_tasks (text, project, meeting_id, source_item_id, created_at) VALUES (?, ?, ?, ?, ?)`,
			text, c.projectName(project), meetingID, itemID, c.nowString())
		if err != nil {
			return 0, err
		}
		if id, err = res.LastInsertId(); err != nil {
			return 0, err
		}
		return id, tx.Commit()
	}()
	if err != nil {
		return CodeTask{}, err
	}
	return c.Get(ctx, id)
}

// SetStatus sets a code task's status (open, done or dropped). When the task
// came from an action, the action gets the same status by hand (and the
// daemon pushes it to Google Tasks), in the same transaction.
func (c *CodeTasks) SetStatus(ctx context.Context, id int64, status string) (CodeTask, error) {
	if err := validStatus(status); err != nil {
		return CodeTask{}, err
	}
	err := func() error {
		tx, err := c.db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		defer tx.Rollback() //nolint:errcheck // no-op after commit
		now := c.nowString()
		res, err := tx.ExecContext(ctx, `UPDATE code_tasks SET status = ?, status_at = ? WHERE id = ?`, status, now, id)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return fmt.Errorf("%w: %d", ErrCodeTaskNotFound, id)
		}
		var source sql.NullInt64
		if err := tx.QueryRowContext(ctx, `SELECT source_item_id FROM code_tasks WHERE id = ?`, id).Scan(&source); err != nil {
			return err
		}
		if source.Valid {
			if _, err := setActionStatus(ctx, tx, source.Int64, status, now); err != nil {
				return err
			}
		}
		return tx.Commit()
	}()
	if err != nil {
		return CodeTask{}, err
	}
	return c.Get(ctx, id)
}

// Suggestions returns the owner's open actions classified as code that are
// not code tasks yet, newest meeting first.
func (c *CodeTasks) Suggestions(ctx context.Context, q SuggestionQuery) ([]Suggestion, error) {
	a := c.actions
	if len(a.names) == 0 {
		return nil, errors.New("sem nomes do dono: preencha owner.names no config.json do perfil")
	}
	if q.Limit <= 0 {
		q.Limit = 50
	}
	rows, err := c.db.QueryContext(ctx, `
		SELECT i.id, i.text, i.people, i.project, m.id, m.title, m.started_at, COALESCE(m.started_at, m.ingested_at)
		FROM meeting_items i JOIN meetings m ON m.id = i.meeting_id
		WHERE i.kind = 'action' AND i.status = 'open' AND i.category = 'code'
			AND NOT EXISTS (SELECT 1 FROM code_tasks c WHERE c.source_item_id = i.id)
		ORDER BY julianday(COALESCE(m.started_at, m.ingested_at)) DESC, i.id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	now := a.now()
	out := []Suggestion{}
	for rows.Next() && len(out) < q.Limit {
		var s Suggestion
		var people, at string
		var meetingAt sql.NullString
		if err := rows.Scan(&s.ItemID, &s.Text, &people, &s.Project, &s.MeetingID, &s.MeetingTitle, &meetingAt, &at); err != nil {
			return nil, err
		}
		_ = json.Unmarshal([]byte(people), &s.People)
		if s.People == nil {
			s.People = []string{}
		}
		if !a.isOwner(s.People) {
			continue
		}
		s.MeetingAt = parseStarted(meetingAt)
		if ts, err := time.Parse(time.RFC3339, at); err == nil {
			age := now.Sub(ts)
			s.AgeDays = int(age.Hours() / 24)
			if age > DefaultStaleAfter && !q.IncludeStale {
				continue
			}
		}
		s.Folder = c.projects.folder(s.Project)
		out = append(out, s)
	}
	return out, rows.Err()
}

// ErrNoFolder means the task's project has no folder in config.json.
var ErrNoFolder = errors.New("projeto sem pasta")

// StartedTask is a code task ready to work on.
type StartedTask struct {
	ID      int64  `json:"id"`
	Branch  string `json:"branch"`
	Folder  string `json:"folder"`
	Project string `json:"project"`
	// Prompt is the first message for Claude Code, in Portuguese.
	Prompt string `json:"prompt"`
}

// maxPromptChars bounds the start prompt.
const maxPromptChars = 2500

// Start marks a code task started (started_at and a branch elan/<slug>
// unique among code tasks, kept on later starts) and returns the prompt for
// Claude Code. It fails with ErrNoFolder, changing nothing, when the task's
// project has no folder.
func (c *CodeTasks) Start(ctx context.Context, id int64) (StartedTask, error) {
	t, err := c.Get(ctx, id)
	if err != nil {
		return StartedTask{}, err
	}
	if t.Folder == "" {
		if t.Project == "" {
			return StartedTask{}, fmt.Errorf("%w: a tarefa %d não tem projeto", ErrNoFolder, id)
		}
		return StartedTask{}, fmt.Errorf("%w: %q não está em \"projects\" no config.json", ErrNoFolder, t.Project)
	}
	var people []string
	if t.SourceItemID != nil {
		var peopleJSON string
		if err := c.db.QueryRowContext(ctx, `SELECT people FROM meeting_items WHERE id = ?`, *t.SourceItemID).
			Scan(&peopleJSON); err != nil && !errors.Is(err, sql.ErrNoRows) {
			return StartedTask{}, err
		}
		_ = json.Unmarshal([]byte(peopleJSON), &people)
	}

	st := StartedTask{ID: id, Folder: t.Folder, Project: t.Project, Branch: t.Branch}
	if st.Branch == "" {
		tx, err := c.db.BeginTx(ctx, nil)
		if err != nil {
			return StartedTask{}, err
		}
		defer tx.Rollback() //nolint:errcheck // no-op after commit
		st.Branch = "elan/" + branchSlug(t.Text, append(append([]string(nil), people...), c.ownerNames...))
		var taken int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM code_tasks WHERE branch = ? AND id <> ?`,
			st.Branch, id).Scan(&taken); err != nil {
			return StartedTask{}, err
		}
		if taken > 0 {
			st.Branch = fmt.Sprintf("%s-%d", st.Branch, id)
		}
		if _, err := tx.ExecContext(ctx, `UPDATE code_tasks SET started_at = COALESCE(started_at, ?), branch = ? WHERE id = ?`,
			c.nowString(), st.Branch, id); err != nil {
			return StartedTask{}, err
		}
		if err := tx.Commit(); err != nil {
			return StartedTask{}, err
		}
	}

	info := startInfo{
		ID: id, Text: t.Text, Context: t.Context, Project: t.Project, Folder: t.Folder, Branch: st.Branch,
		People: people, MeetingID: t.MeetingID, MeetingTitle: t.MeetingTitle, MeetingAt: t.MeetingAt,
	}
	if t.MeetingID != "" {
		if info.Summary, info.Decisions, err = meetingContext(ctx, c.db, t.MeetingID); err != nil {
			return StartedTask{}, err
		}
	}
	st.Prompt = startPrompt(info)
	return st, nil
}

// meetingContext returns a meeting's summary and its still valid decisions.
func meetingContext(ctx context.Context, db *sql.DB, meetingID string) (string, []string, error) {
	rows, err := db.QueryContext(ctx, `
		SELECT kind, text FROM meeting_items
		WHERE meeting_id = ? AND kind IN ('summary', 'decision') AND superseded_by IS NULL
		ORDER BY kind = 'decision', importance DESC, id`, meetingID)
	if err != nil {
		return "", nil, err
	}
	defer rows.Close()
	var summary string
	var decisions []string
	for rows.Next() {
		var kind, text string
		if err := rows.Scan(&kind, &text); err != nil {
			return "", nil, err
		}
		if kind == KindSummary {
			summary = text
		} else {
			decisions = append(decisions, text)
		}
	}
	return summary, decisions, rows.Err()
}

type startInfo struct {
	ID              int64
	Text, Context   string
	Project, Folder string
	Branch          string
	People          []string
	MeetingID       string
	MeetingTitle    string
	MeetingAt       *time.Time
	Summary         string
	Decisions       []string
}

// startPrompt renders the first message for Claude Code, within
// maxPromptChars: decisions are dropped from the end, then the summary and
// the owner's context shortened, until it fits.
func startPrompt(s startInfo) string {
	render := func(contextMax, summaryMax, decisions int) string {
		var b strings.Builder
		fmt.Fprintf(&b, "Tarefa de código (registrada no elan, id %d): %s\n", s.ID, oneLine(s.Text))
		if ctx := strings.TrimSpace(s.Context); ctx != "" {
			fmt.Fprintf(&b, "\nInstruções do dono:\n%s\n", clipRunes(ctx, contextMax))
		}
		fmt.Fprintf(&b, "\nProjeto: %s (%s). Branch: %s.\n", s.Project, s.Folder, s.Branch)
		if s.MeetingID != "" {
			date := ""
			if s.MeetingAt != nil {
				date = " — " + s.MeetingAt.Format("02/01/2006")
			}
			fmt.Fprintf(&b, "\nReunião de origem: %s%s\n", s.MeetingTitle, date)
			if sum := oneLine(s.Summary); sum != "" {
				fmt.Fprintf(&b, "Resumo: %s\n", clipRunes(sum, summaryMax))
			}
			if decisions > 0 {
				b.WriteString("Decisões dessa reunião:\n")
				for _, d := range s.Decisions[:decisions] {
					fmt.Fprintf(&b, "- %s\n", clipRunes(oneLine(d), 220))
				}
			}
			if len(s.People) > 0 {
				fmt.Fprintf(&b, "Pessoas envolvidas: %s\n", strings.Join(s.People, ", "))
			}
		}
		b.WriteString("\nPara mais contexto, use as tools MCP do elan: search (reuniões e notas)")
		if s.MeetingID != "" {
			fmt.Fprintf(&b, ", get_meeting (esta reunião: %s)", s.MeetingID)
		}
		fmt.Fprintf(&b, ", codetasks_list e codetask_update (esta tarefa: id %d).\n", s.ID)
		b.WriteString("Quando terminar, avise o dono com um resumo do que mudou. Não feche a tarefa você mesmo: quem marca como concluída é ele.\n")
		return b.String()
	}
	decisions := min(len(s.Decisions), 6)
	contextMax, summaryMax := 1000, 700
	for {
		p := render(contextMax, summaryMax, decisions)
		if utf8.RuneCountInString(p) <= maxPromptChars {
			return p
		}
		switch {
		case decisions > 0:
			decisions--
		case summaryMax > 100:
			summaryMax -= 200
		case contextMax > 200:
			contextMax -= 200
		default:
			return clipRunes(p, maxPromptChars)
		}
	}
}

func clipRunes(s string, max int) string {
	if r := []rune(s); len(r) > max {
		return strings.TrimSpace(string(r[:max-1])) + "…"
	}
	return s
}

// maxSlug bounds the slug of a branch name.
const maxSlug = 40

// branchSlug turns an action's text into an ASCII slug of at most maxSlug
// characters, without the owner's name and "vai" it usually starts with
// ("Vital vai migrar o Atlas" → "migrar-o-atlas").
func branchSlug(text string, people []string) string {
	words := strings.Fields(foldName(text))
	// Drop the leading people ("Ana e Bruno vão ...") and the verb.
	for stripped := true; stripped && len(words) > 1; {
		stripped = false
		for _, p := range people {
			pw := strings.Fields(foldName(p))
			if len(pw) > 0 && len(words) > len(pw) && strings.Join(words[:len(pw)], " ") == strings.Join(pw, " ") {
				words, stripped = words[len(pw):], true
				break
			}
		}
		if stripped && len(words) > 1 && (words[0] == "e" || words[0] == ",") {
			words = words[1:]
		}
	}
	if len(words) > 1 {
		switch words[0] {
		case "vai", "vao", "deve", "devem", "precisa", "precisam", "ira", "irao":
			words = words[1:]
		}
	}
	var parts []string
	for _, w := range words {
		var b strings.Builder
		for _, r := range w {
			if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
				b.WriteRune(r)
			} else if b.Len() > 0 {
				parts = append(parts, b.String())
				b.Reset()
			}
		}
		if b.Len() > 0 {
			parts = append(parts, b.String())
		}
	}
	slug := ""
	for _, p := range parts {
		next := p
		if slug != "" {
			next = slug + "-" + p
		}
		if len(next) > maxSlug {
			if slug == "" {
				slug = p[:maxSlug]
			}
			break
		}
		slug = next
	}
	if slug == "" {
		return "tarefa"
	}
	return slug
}
