-- Code tasks the owner asked for: created by hand (CLI, MCP) or promoted from
-- an action. Actions classified 'code' (migration 0006) are only suggestions
-- until promoted; meeting_items.started_at/branch are no longer written.
--
-- text: what to do, self-contained (copied from the action when promoted).
-- project: as the owner or the meeting named it; '' when none. Resolved to a
--   folder through config.json "projects" at read time.
-- context: extra instructions or notes from the owner, for the start prompt.
-- meeting_id: the meeting the task came from, if any.
-- source_item_id: the action it was promoted from. Unique, so promoting twice
--   returns the same task. Re-distilling the meeting recreates its item ids:
--   the distiller re-links the task to the new item with the same normalized
--   text (like the action status); when the text changed it becomes NULL and
--   the task keeps its own text and meeting_id.
-- status: open/done/dropped; closing or reopening the task does the same to
--   its source action (manual status, pushed to Google Tasks by the daemon).
-- started_at, branch: set by `elan codetasks start` (RFC 3339; elan/<slug>,
--   unique among code tasks).
CREATE TABLE code_tasks (
    id             INTEGER PRIMARY KEY,
    text           TEXT NOT NULL,
    project        TEXT NOT NULL DEFAULT '',
    context        TEXT NOT NULL DEFAULT '',
    meeting_id     TEXT REFERENCES meetings (id) ON DELETE SET NULL,
    source_item_id INTEGER REFERENCES meeting_items (id) ON DELETE SET NULL,
    status         TEXT NOT NULL DEFAULT 'open' CHECK (status IN ('open', 'done', 'dropped')),
    status_at      TEXT,
    started_at     TEXT,
    branch         TEXT,
    created_at     TEXT NOT NULL
);

CREATE UNIQUE INDEX code_tasks_source ON code_tasks (source_item_id) WHERE source_item_id IS NOT NULL;
CREATE INDEX code_tasks_status ON code_tasks (status, created_at);
CREATE INDEX code_tasks_branch ON code_tasks (branch) WHERE branch IS NOT NULL;
