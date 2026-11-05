package meetings

import (
	"context"
	"database/sql"
	"encoding/json"
	"strings"
	"time"
)

// RecentItem is a distilled item with the meeting it came from.
type RecentItem struct {
	StoredItem
	MeetingID    string    `json:"meeting_id"`
	MeetingTitle string    `json:"meeting_title"`
	StartedAt    time.Time `json:"started_at"`
}

// ItemsSince returns the still-valid items of the given kinds, with
// importance at least minImportance, from meetings started in [since,
// until), oldest meeting first.
func (r *Reader) ItemsSince(ctx context.Context, kinds []string, minImportance int, since, until time.Time) ([]RecentItem, error) {
	if len(kinds) == 0 {
		return nil, nil
	}
	args := []any{since.Format(time.RFC3339), until.Format(time.RFC3339), minImportance}
	for _, k := range kinds {
		args = append(args, k)
	}
	rows, err := r.db.QueryContext(ctx, `
		SELECT i.id, i.kind, i.text, i.people, i.importance, m.id, m.title, m.started_at
		FROM meeting_items i JOIN meetings m ON m.id = i.meeting_id
		WHERE julianday(m.started_at) >= julianday(?) AND julianday(m.started_at) < julianday(?)
			AND i.importance >= ? AND i.superseded_by IS NULL
			AND i.kind IN (?`+strings.Repeat(", ?", len(kinds)-1)+`)
		ORDER BY julianday(m.started_at), i.id`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []RecentItem
	for rows.Next() {
		var it RecentItem
		var people string
		var started sql.NullString
		if err := rows.Scan(&it.ID, &it.Kind, &it.Text, &people, &it.Importance, &it.MeetingID, &it.MeetingTitle, &started); err != nil {
			return nil, err
		}
		_ = json.Unmarshal([]byte(people), &it.People)
		if t := parseStarted(started); t != nil {
			it.StartedAt = *t
		}
		out = append(out, it)
	}
	return out, rows.Err()
}

// Change is a decision or fact replaced by a newer item.
type Change struct {
	Old        string    `json:"old"`
	OldMeeting string    `json:"old_meeting"`
	OldAt      time.Time `json:"old_at"`
	New        string    `json:"new"`
	MeetingID  string    `json:"meeting_id"`
	Meeting    string    `json:"meeting"`
	At         time.Time `json:"at"`
}

// ChangesSince returns the decisions and facts replaced by items of
// meetings started in [since, until), oldest replacement first.
func (r *Reader) ChangesSince(ctx context.Context, since, until time.Time) ([]Change, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT o.text, om.title, COALESCE(om.started_at, ''), n.text, nm.id, nm.title, nm.started_at
		FROM meeting_items o
		JOIN meetings om ON om.id = o.meeting_id
		JOIN meeting_items n ON n.id = o.superseded_by
		JOIN meetings nm ON nm.id = n.meeting_id
		WHERE o.kind IN ('decision', 'fact')
			AND julianday(nm.started_at) >= julianday(?) AND julianday(nm.started_at) < julianday(?)
		ORDER BY julianday(nm.started_at), n.id`,
		since.Format(time.RFC3339), until.Format(time.RFC3339))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Change
	for rows.Next() {
		var c Change
		var oldAt, at string
		if err := rows.Scan(&c.Old, &c.OldMeeting, &oldAt, &c.New, &c.MeetingID, &c.Meeting, &at); err != nil {
			return nil, err
		}
		c.OldAt, _ = time.Parse(time.RFC3339, oldAt)
		c.At, _ = time.Parse(time.RFC3339, at)
		out = append(out, c)
	}
	return out, rows.Err()
}
