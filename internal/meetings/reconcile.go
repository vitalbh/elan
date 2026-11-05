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

// reconcileNotesBudget caps the notes in a reconcile prompt: the Gemini
// notes are enough to tell what was reported done.
const reconcileNotesBudget = 20000

var reconcileSchema = json.RawMessage(`{
  "type": "object",
  "properties": {
    "completes": {"type": "array", "items": {"type": "integer"}},
    "drops": {"type": "array", "items": {"type": "integer"}}
  },
  "required": ["completes", "drops"],
  "additionalProperties": false
}`)

const reconcileSystem = `Você recebe as anotações de uma reunião de trabalho e uma lista de ações ainda em aberto, combinadas em reuniões anteriores (da mesma série recorrente ou de outras reuniões sobre temas parecidos).

Devolva em completes os ids das ações que as anotações desta reunião dizem que foram concluídas (entregue, feito, resolvido) e em drops os ids das que foram canceladas ou abandonadas explicitamente.

Conta como concluída quando as anotações dizem que foi feita ou mostram o resultado dela (a ação era "validar a promoção de X com S" e as anotações dizem "promoção de X aprovada"). Nunca invente: ação só citada, em andamento, atrasada, replanejada ou sem menção fica de fora. Na dúvida, deixe de fora. Listas vazias são a resposta mais comum.`

// Reconciler backfills action statuses for meetings distilled before the
// distiller reported completed actions: per recurring series, oldest first,
// it asks the model which earlier open actions each meeting reports as done
// or dropped. One call per meeting that has earlier open actions.
type Reconciler struct {
	db     *sql.DB
	runner llm.Runner
	// Model is passed to the runner ("sonnet" when empty).
	Model string
	// Timeout bounds one call (default 5 minutes).
	Timeout time.Duration
	// Related, when set, adds open actions of other related meetings to the
	// series' own, and makes meetings outside a series candidates too.
	Related RelatedActions
	// Since, when set, limits the run to meetings started at or after it.
	Since time.Time
	// Recheck also takes meetings already checked (with Since, to re-run
	// recent meetings after the offered actions changed).
	Recheck bool
	now     func() time.Time
}

// NewReconciler builds a Reconciler.
func NewReconciler(db *sql.DB, runner llm.Runner) *Reconciler {
	return &Reconciler{db: db, runner: runner, now: time.Now}
}

// ReconcileStats summarises a reconcile run.
type ReconcileStats struct {
	// Checked meetings, Calls made to the model among them (meetings without
	// earlier open actions are checked without a call).
	Checked, Calls, Completed, Dropped, Failed int
	// Pending is how many meetings are still unchecked after the run.
	Pending int
}

// Reconcile checks distilled meetings of recurring series not checked yet,
// oldest first, making at most limit model calls (0 = no limit). progress,
// when set, is called with each meeting's title before its call. A failed
// meeting is counted and stays unchecked; a usage limit or a cancelled ctx
// stops the run.
func (r *Reconciler) Reconcile(ctx context.Context, limit int, progress func(title string)) (ReconcileStats, error) {
	var st ReconcileStats
	ids, err := r.candidates(ctx)
	if err != nil {
		return st, err
	}
	var errs []error
	for i, id := range ids {
		if err := ctx.Err(); err != nil {
			st.Pending = len(ids) - i
			return st, err
		}
		m, err := loadMeeting(ctx, r.db, id)
		if err != nil {
			return st, err
		}
		actions, err := offeredActions(ctx, r.db, m, r.Related)
		if err != nil {
			return st, err
		}
		if len(actions) == 0 {
			if err := r.markChecked(ctx, r.db, m); err != nil {
				return st, err
			}
			st.Checked++
			continue
		}
		if limit > 0 && st.Calls >= limit {
			st.Pending = len(ids) - i
			break
		}
		if progress != nil {
			progress(m.Title)
		}
		st.Calls++
		done, dropped, err := r.reconcileOne(ctx, m, actions)
		if err != nil {
			if _, ok := llm.IsLimit(err); ok || ctx.Err() != nil {
				st.Pending = len(ids) - i
				return st, err
			}
			st.Failed++
			errs = append(errs, fmt.Errorf("%s: %w", m.Title, err))
			continue
		}
		st.Checked++
		st.Completed += done
		st.Dropped += dropped
	}
	return st, errors.Join(errs...)
}

func (r *Reconciler) candidates(ctx context.Context) ([]string, error) {
	where := []string{"started_at IS NOT NULL", "distilled_hash = content_hash"}
	var args []any
	if r.Related == nil {
		where = append(where, "recurring = 1")
	}
	if !r.Recheck {
		where = append(where, "actions_checked_hash <> content_hash")
	}
	if !r.Since.IsZero() {
		where = append(where, "julianday(started_at) >= julianday(?)")
		args = append(args, r.Since.Format(time.RFC3339))
	}
	rows, err := r.db.QueryContext(ctx, `SELECT id FROM meetings WHERE `+strings.Join(where, " AND ")+`
		ORDER BY julianday(started_at), id`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

func (r *Reconciler) reconcileOne(ctx context.Context, m distillMeeting, actions []seriesAction) (int, int, error) {
	model := r.Model
	if model == "" {
		model = "sonnet"
	}
	timeout := r.Timeout
	if timeout <= 0 {
		timeout = 5 * time.Minute
	}
	callCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	raw, err := r.runner.JSON(callCtx, llm.Request{
		System: reconcileSystem,
		Prompt: buildReconcilePrompt(m, actions),
		Model:  model,
		Schema: reconcileSchema,
	})
	if err != nil {
		return 0, 0, err
	}
	var out struct {
		Completes []int64 `json:"completes"`
		Drops     []int64 `json:"drops"`
	}
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&out); err != nil {
		return 0, 0, fmt.Errorf("JSON da reconciliação: %w", err)
	}

	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, 0, err
	}
	defer tx.Rollback() //nolint:errcheck // no-op after commit
	if err := reopenClosedBy(ctx, tx, m.ID); err != nil {
		return 0, 0, err
	}
	now := r.now().UTC().Format(time.RFC3339)
	done, dropped, err := closeActions(ctx, tx, m.ID, sql.NullInt64{}, out.Completes, out.Drops, actionIDs(actions), now)
	if err != nil {
		return 0, 0, err
	}
	if err := r.markChecked(ctx, tx, m); err != nil {
		return 0, 0, err
	}
	return done, dropped, tx.Commit()
}

func (r *Reconciler) markChecked(ctx context.Context, q dbtx, m distillMeeting) error {
	_, err := q.ExecContext(ctx, `UPDATE meetings SET actions_checked_hash = ? WHERE id = ?`, m.ContentHash, m.ID)
	return err
}

func buildReconcilePrompt(m distillMeeting, actions []seriesAction) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# Reunião: %s\n", m.Title)
	if m.StartedAt.Valid {
		fmt.Fprintf(&b, "Data: %s\n", m.StartedAt.String)
	}
	fmt.Fprintf(&b, "Série recorrente: %s\n", m.FolderName)
	notes := strings.TrimSpace(m.Notes)
	if notes == "" {
		notes = "(sem anotações)"
	}
	fmt.Fprintf(&b, "\n## Anotações do Gemini\n%s\n", truncateMiddle(notes, reconcileNotesBudget))
	writeSeriesActions(&b, actions)
	return b.String()
}
