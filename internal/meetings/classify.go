package meetings

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/vitalbh/elan/internal/llm"
)

// DefaultClassifyBatch is how many actions go in one classify call.
const DefaultClassifyBatch = 50

var classifySchema = json.RawMessage(`{
  "type": "object",
  "properties": {
    "actions": {
      "type": "array",
      "items": {
        "type": "object",
        "properties": {
          "id": {"type": "integer"},
          "category": {"type": "string", "enum": ["code", "other"]},
          "project": {"type": "string"}
        },
        "required": ["id", "category", "project"],
        "additionalProperties": false
      }
    }
  },
  "required": ["actions"],
  "additionalProperties": false
}`)

const classifySystem = `Você classifica ações (tarefas com dono) combinadas em reuniões de trabalho. Para cada ação da lista, devolva o id, a category e o project.

` + classifyRules + `

O título e a pasta da reunião ajudam a achar o projeto quando a ação não o diz. Responda todas as ações da lista, uma vez cada.`

// Classifier backfills the category and project of actions distilled before
// the distiller classified them: batches of actions, one call per batch.
type Classifier struct {
	db     *sql.DB
	runner llm.Runner
	// Model is passed to the runner ("sonnet" when empty).
	Model string
	// Batch is how many actions go in one call (DefaultClassifyBatch when 0).
	Batch int
	// Timeout bounds one call (default 5 minutes).
	Timeout time.Duration
	// KnownProjects are offered to the model, as in Distiller.
	KnownProjects []Project
}

// NewClassifier builds a Classifier.
func NewClassifier(db *sql.DB, runner llm.Runner) *Classifier {
	return &Classifier{db: db, runner: runner}
}

// ClassifyStats summarises a classify run.
type ClassifyStats struct {
	Calls, Code, Other int
	// Missing: actions of a successful call the answer left out.
	Missing int
	// Failed calls (bad answer); their actions stay unclassified.
	Failed int
	// Pending: actions still unclassified after the run.
	Pending int
	// Projects counts the projects found among the classified code tasks.
	Projects map[string]int
}

type classifyAction struct {
	ID                  int64
	Text, Title, Folder string
	People              []string
}

// Classify classifies unclassified actions, open ones first and newest
// first, making at most limit calls (0 = no limit). progress, when set, is
// called before each call with its number and size. A usage limit or a
// cancelled ctx stops the run.
func (c *Classifier) Classify(ctx context.Context, limit int, progress func(call, size int)) (ClassifyStats, error) {
	st := ClassifyStats{Projects: map[string]int{}}
	todo, err := c.candidates(ctx)
	if err != nil {
		return st, err
	}
	batch := c.Batch
	if batch <= 0 {
		batch = DefaultClassifyBatch
	}
	var errs []error
	for len(todo) > 0 && (limit <= 0 || st.Calls < limit) {
		if err := ctx.Err(); err != nil {
			errs = append(errs, err)
			break
		}
		n := min(batch, len(todo))
		chunk := todo[:n]
		todo = todo[n:]
		st.Calls++
		if progress != nil {
			progress(st.Calls, len(chunk))
		}
		if err := c.classifyBatch(ctx, chunk, &st); err != nil {
			if _, ok := llm.IsLimit(err); ok || ctx.Err() != nil {
				errs = append(errs, err)
				break
			}
			st.Failed++
			errs = append(errs, fmt.Errorf("lote %d: %w", st.Calls, err))
		}
	}
	if err := c.db.QueryRowContext(context.WithoutCancel(ctx),
		`SELECT COUNT(*) FROM meeting_items WHERE kind = 'action' AND category IS NULL`).Scan(&st.Pending); err != nil {
		errs = append(errs, err)
	}
	return st, errors.Join(errs...)
}

func (c *Classifier) candidates(ctx context.Context) ([]classifyAction, error) {
	rows, err := c.db.QueryContext(ctx, `
		SELECT i.id, i.text, i.people, m.title, m.folder_name
		FROM meeting_items i JOIN meetings m ON m.id = i.meeting_id
		WHERE i.kind = 'action' AND i.category IS NULL
		ORDER BY i.status <> 'open', julianday(COALESCE(m.started_at, m.ingested_at)) DESC, i.id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []classifyAction
	for rows.Next() {
		var a classifyAction
		var people string
		if err := rows.Scan(&a.ID, &a.Text, &people, &a.Title, &a.Folder); err != nil {
			return nil, err
		}
		_ = json.Unmarshal([]byte(people), &a.People)
		out = append(out, a)
	}
	return out, rows.Err()
}

type classified struct {
	ID       int64  `json:"id"`
	Category string `json:"category"`
	Project  string `json:"project"`
}

func (c *Classifier) classifyBatch(ctx context.Context, chunk []classifyAction, st *ClassifyStats) error {
	model := c.Model
	if model == "" {
		model = "sonnet"
	}
	timeout := c.Timeout
	if timeout <= 0 {
		timeout = 5 * time.Minute
	}
	callCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	raw, err := c.runner.JSON(callCtx, llm.Request{
		System: classifySystem,
		Prompt: buildClassifyPrompt(chunk) + knownProjectsHint(c.KnownProjects),
		Model:  model,
		Schema: classifySchema,
	})
	if err != nil {
		return err
	}
	answers, err := parseClassified(raw, chunk)
	if err != nil {
		return err
	}
	tx, err := c.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck // no-op after commit
	code, other := 0, 0
	projects := map[string]int{}
	for _, a := range answers {
		res, err := tx.ExecContext(ctx, `
			UPDATE meeting_items SET category = ?, project = ?
			WHERE id = ? AND kind = 'action' AND category IS NULL`, a.Category, a.Project, a.ID)
		if err != nil {
			return err
		}
		if k, _ := res.RowsAffected(); k == 0 {
			continue // re-distilled or classified meanwhile
		}
		if a.Category == CategoryCode {
			code++
			if a.Project != "" {
				projects[a.Project]++
			}
		} else {
			other++
		}
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	st.Code += code
	st.Other += other
	st.Missing += len(chunk) - len(answers)
	for p, n := range projects {
		st.Projects[p] += n
	}
	return nil
}

// parseClassified decodes and validates a classify answer: only ids of the
// batch, once each, with a valid category.
func parseClassified(raw []byte, chunk []classifyAction) ([]classified, error) {
	var out struct {
		Actions []classified `json:"actions"`
	}
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&out); err != nil {
		return nil, fmt.Errorf("JSON da classificação: %w", err)
	}
	if out.Actions == nil {
		return nil, errors.New("JSON da classificação sem actions")
	}
	want := make(map[int64]bool, len(chunk))
	for _, a := range chunk {
		want[a.ID] = true
	}
	seen := map[int64]bool{}
	var valid []classified
	for _, a := range out.Actions {
		a.Category = strings.ToLower(strings.TrimSpace(a.Category))
		a.Project = oneLine(a.Project)
		if !want[a.ID] || seen[a.ID] {
			continue
		}
		if a.Category != CategoryCode && a.Category != CategoryOther {
			return nil, fmt.Errorf("ação %d: category inválida %q", a.ID, a.Category)
		}
		seen[a.ID] = true
		valid = append(valid, a)
	}
	return valid, nil
}

func buildClassifyPrompt(chunk []classifyAction) string {
	var b strings.Builder
	b.WriteString("# Ações para classificar\n")
	for _, a := range chunk {
		people := strings.Join(a.People, ", ")
		if people == "" {
			people = "sem dono"
		}
		fmt.Fprintf(&b, "- [id %d] (reunião: %s; pasta: %s; %s) %s\n",
			a.ID, oneLine(a.Title), oneLine(a.Folder), people, oneLine(a.Text))
	}
	return b.String()
}
