package main

import (
	"context"
	"database/sql"
	"strconv"
	"time"

	"github.com/vitalbh/elan/internal/config"
	"github.com/vitalbh/elan/internal/embed"
	"github.com/vitalbh/elan/internal/meetings"
	"github.com/vitalbh/elan/internal/search"
)

// newRelated finds, with the hybrid search over action items, the earlier
// actions a meeting's notes may report on. It ignores the per-meeting cap
// of the regular search: several actions of one meeting may be related.
func newRelated(cfg config.Config, conn *sql.DB) meetings.RelatedActions {
	svc := search.NewService(conn, embed.NewOllama(cfg.Embed))
	svc.Params.MaxPerMeeting = 0
	return func(ctx context.Context, text string, before time.Time, exclude string) ([]int64, error) {
		res, err := svc.Search(ctx, search.Query{
			Text:        text,
			Until:       before,
			SourceTypes: []string{search.SourceMeetingItem},
			Limit:       60,
		})
		if err != nil {
			return nil, err
		}
		var ids []int64
		for _, r := range res {
			if r.Kind != "action" || r.MeetingID == exclude {
				continue
			}
			if id, err := strconv.ParseInt(r.SourceID, 10, 64); err == nil {
				ids = append(ids, id)
			}
		}
		return ids, nil
	}
}
