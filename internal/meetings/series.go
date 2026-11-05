package meetings

import (
	"context"
	"database/sql"
	"encoding/json"
	"strings"
	"time"
	"unicode"
)

// PreviousMeeting is the latest stored meeting of a series before a given
// moment: what a calendar event needs to show "last time on this meeting".
type PreviousMeeting struct {
	ID        string    `json:"id"`
	Title     string    `json:"title"`
	StartedAt time.Time `json:"started_at"`
	// Summary is the distilled summary item, empty before distillation.
	Summary string `json:"summary,omitempty"`
	// OpenActions are the action items of that meeting still open.
	OpenActions []StoredItem `json:"open_actions,omitempty"`
}

// recurringSuffix marks the Drive folder of a series.
const recurringSuffix = "(recurring)"

// SeriesKey normalizes a meeting or event title for series matching:
// lowercase, no accents, punctuation as spaces, collapsed whitespace, and
// the "(recurring)" suffix of series folders dropped. "Pedro / Carlos
// (recurring)" and "pedro - carlos" give the same key.
func SeriesKey(s string) string {
	s = strings.TrimSpace(s)
	if len(s) >= len(recurringSuffix) && strings.EqualFold(s[len(s)-len(recurringSuffix):], recurringSuffix) {
		s = s[:len(s)-len(recurringSuffix)]
	}
	folded := foldName(s)
	var b strings.Builder
	for _, r := range folded {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
		} else {
			b.WriteRune(' ')
		}
	}
	return strings.Join(strings.Fields(b.String()), " ")
}

// Previous returns the latest meeting started before `before` whose title,
// or whose folder name without the "(recurring)" suffix, has the same
// SeriesKey as title. It returns nil when there is none.
func (r *Reader) Previous(ctx context.Context, title string, before time.Time) (*PreviousMeeting, error) {
	key := SeriesKey(title)
	if key == "" {
		return nil, nil
	}
	rows, err := r.db.QueryContext(ctx, `
		SELECT id, title, folder_name, started_at FROM meetings
		WHERE started_at IS NOT NULL AND julianday(started_at) < julianday(?)
		ORDER BY julianday(started_at) DESC`, before.Format(time.RFC3339))
	if err != nil {
		return nil, err
	}
	var p *PreviousMeeting
	for rows.Next() {
		var id, mtitle, folder string
		var started sql.NullString
		if err := rows.Scan(&id, &mtitle, &folder, &started); err != nil {
			rows.Close()
			return nil, err
		}
		if SeriesKey(mtitle) != key && SeriesKey(folder) != key {
			continue
		}
		at := parseStarted(started)
		if at == nil {
			continue
		}
		p = &PreviousMeeting{ID: id, Title: mtitle, StartedAt: *at}
		break
	}
	// Close before the next queries: the pool may have a single connection.
	rows.Close()
	if err := rows.Err(); err != nil || p == nil {
		return nil, err
	}
	err = r.db.QueryRowContext(ctx, `
		SELECT COALESCE((SELECT text FROM meeting_items WHERE meeting_id = ? AND kind = 'summary' ORDER BY id LIMIT 1), '')`,
		p.ID).Scan(&p.Summary)
	if err != nil {
		return nil, err
	}
	if p.OpenActions, err = r.openActions(ctx, p.ID); err != nil {
		return nil, err
	}
	return p, nil
}

// openActions returns the action items of a meeting that are still open.
// It is the single place that decides what "open" means.
func (r *Reader) openActions(ctx context.Context, meetingID string) ([]StoredItem, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT id, kind, text, people, importance
		FROM meeting_items
		WHERE meeting_id = ? AND kind = 'action' AND superseded_by IS NULL AND status = 'open'
		ORDER BY id`, meetingID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []StoredItem
	for rows.Next() {
		var it StoredItem
		var people string
		if err := rows.Scan(&it.ID, &it.Kind, &it.Text, &people, &it.Importance); err != nil {
			return nil, err
		}
		_ = json.Unmarshal([]byte(people), &it.People)
		out = append(out, it)
	}
	return out, rows.Err()
}
