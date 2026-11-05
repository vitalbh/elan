package meetings

import (
	"context"
	"database/sql"
	"encoding/json"
	"strings"
	"time"
)

// RelatedActions returns ids of meeting items (best first) that text may
// report on: in production, a hybrid search over the action items of
// meetings started before `before`, excluding meeting `exclude`. The ids
// are filtered again here, so the finder may return anything.
type RelatedActions func(ctx context.Context, text string, before time.Time, exclude string) ([]int64, error)

// maxRelatedActions caps the open actions of other meetings offered to the
// model next to the series' own.
const maxRelatedActions = 20

// relatedQueryRunes is how much of the meeting's notes (Gemini's summary
// and next steps come first) drives the search for related actions.
const relatedQueryRunes = 1500

// offeredActions are the earlier open actions the model may report as done
// or dropped: those of m's recurring series plus, when related is set, the
// open actions of any other earlier meeting that look related to m. An
// action is often agreed in one meeting and reported in another (a team
// backlog item closed in a 1:1), so the series alone misses most of them.
func offeredActions(ctx context.Context, q dbtx, m distillMeeting, related RelatedActions) ([]seriesAction, error) {
	actions, err := seriesActions(ctx, q, m)
	if err != nil || related == nil || !m.StartedAt.Valid {
		return actions, err
	}
	started, err := time.Parse(time.RFC3339, m.StartedAt.String)
	if err != nil {
		return actions, nil
	}
	text := []rune(strings.TrimSpace(m.Notes))
	if len(text) > relatedQueryRunes {
		text = text[:relatedQueryRunes]
	}
	if len(text) == 0 {
		return actions, nil
	}
	ids, err := related(ctx, string(text), started, m.ID)
	if err != nil {
		// The search is a bonus (Ollama may be down): fall back to the
		// series alone rather than failing the meeting.
		return actions, nil
	}
	seen := actionIDs(actions)
	more, err := loadRelatedActions(ctx, q, m, ids, seen)
	if err != nil {
		return nil, err
	}
	return append(actions, more...), nil
}

// loadRelatedActions keeps, in the finder's order, the ids that are open
// actions (not reopened by hand) of an earlier meeting other than m and not
// already offered, up to maxRelatedActions.
func loadRelatedActions(ctx context.Context, q dbtx, m distillMeeting, ids []int64, seen map[int64]bool) ([]seriesAction, error) {
	var out []seriesAction
	for _, id := range ids {
		if len(out) == maxRelatedActions {
			break
		}
		if seen[id] {
			continue
		}
		var a seriesAction
		var people string
		err := q.QueryRowContext(ctx, `
			SELECT i.id, COALESCE(mt.started_at, ''), i.text, i.people, mt.title
			FROM meeting_items i JOIN meetings mt ON mt.id = i.meeting_id
			WHERE i.id = ? AND i.kind = 'action' AND mt.id <> ?
				AND ((i.status = 'open' AND i.status_source IS NOT 'manual')
					OR (i.status_source = 'distill' AND i.done_in = ?))
				AND mt.started_at IS NOT NULL
				AND julianday(mt.started_at) < julianday(?)`,
			id, m.ID, m.ID, m.StartedAt.String,
		).Scan(&a.ID, &a.Date, &a.Text, &people, &a.Meeting)
		if err == sql.ErrNoRows {
			continue
		}
		if err != nil {
			return nil, err
		}
		_ = json.Unmarshal([]byte(people), &a.People)
		seen[id] = true
		out = append(out, a)
	}
	return out, nil
}
