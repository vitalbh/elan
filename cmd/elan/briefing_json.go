package main

import (
	"context"
	"database/sql"
	"time"

	"github.com/vitalbh/elan/internal/briefing"
	"github.com/vitalbh/elan/internal/config"
	"github.com/vitalbh/elan/internal/meetings"
)

// maxBriefingCodeTasks caps the code tasks in `elan briefing --json`.
const maxBriefingCodeTasks = 10

// briefingOut is the output of `elan briefing --json`: the briefing plus the
// owner's open code tasks (not the suggestions). Lists are [] rather than
// null.
type briefingOut struct {
	briefing.Briefing
	CodeTasks []meetings.CodeTask `json:"code_tasks"`
}

// buildBriefingJSON builds the day's briefing without saving its page: the
// daemon generates (and notifies) the page only when it does not exist yet.
func buildBriefingJSON(ctx context.Context, cfg config.Config, conn *sql.DB, svc *briefing.Service, day time.Time) (briefingOut, error) {
	b, err := svc.Build(ctx, day)
	if err != nil {
		return briefingOut{}, err
	}
	out := briefingOut{Briefing: b, CodeTasks: []meetings.CodeTask{}}
	tasks, err := newCodeTasks(cfg, conn).List(ctx, meetings.CodeTaskQuery{Limit: maxBriefingCodeTasks})
	if err != nil {
		out.Warnings = append(out.Warnings, "tarefas de código indisponíveis: "+err.Error())
	} else {
		out.CodeTasks = tasks
	}
	if out.Events == nil {
		out.Events = []briefing.Event{}
	}
	if out.Mine == nil {
		out.Mine = []briefing.Action{}
	}
	if out.Waiting == nil {
		out.Waiting = []briefing.Action{}
	}
	if out.Decisions == nil {
		out.Decisions = []meetings.RecentItem{}
	}
	return out, nil
}
