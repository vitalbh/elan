package search

import (
	"context"
	"encoding/json"

	"github.com/vitalbh/elan/internal/people"
)

// meetingsWith returns the ids of the meetings a person took part in: named
// in the meeting title ("Ana / Bia"), in the header (invitees) or in the
// people of a distilled item, under the name or any alias of s.People.
// Names match as whole word sequences, case- and accent-insensitively: "ana"
// matches "Ana Souza" but not "Mariana".
func (s *Service) meetingsWith(ctx context.Context, person string) ([]any, error) {
	var wants [][]string
	for _, n := range s.People.Expand(person) {
		wants = append(wants, people.Tokens(n))
	}
	if len(wants) == 0 {
		return nil, nil
	}
	matches := func(have []string) bool {
		for _, w := range wants {
			if people.Contains(have, w) {
				return true
			}
		}
		return false
	}
	found := map[string]bool{}

	rows, err := s.db.QueryContext(ctx, `SELECT id, title, header FROM meetings`)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var id, title, header string
		if err := rows.Scan(&id, &title, &header); err != nil {
			rows.Close()
			return nil, err
		}
		if matches(people.Tokens(title + " " + header)) {
			found[id] = true
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	rows, err = s.db.QueryContext(ctx, `SELECT meeting_id, people FROM meeting_items WHERE people <> '[]'`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id, list string
		if err := rows.Scan(&id, &list); err != nil {
			return nil, err
		}
		if found[id] {
			continue
		}
		var names []string
		if json.Unmarshal([]byte(list), &names) != nil {
			continue
		}
		for _, n := range names {
			if matches(people.Tokens(n)) {
				found[id] = true
				break
			}
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	ids := make([]any, 0, len(found))
	for id := range found {
		ids = append(ids, id)
	}
	return ids, nil
}
