-- Status of action items. Kept on meeting_items: it is an attribute of the
-- item, and the queries that list open actions (briefing, MCP, distill prompt)
-- stay a single SQL statement. Re-distilling a meeting recreates its item ids,
-- so the distiller carries each action's status over to the new item with the
-- same normalized text (the key task_exports uses).
--
-- status_source: who set the status. NULL = never set (open by default).
--   distill: a later meeting of the series reported it (done_by / done_in);
--   tasks:   the exported Google Task was completed or deleted;
--   manual:  the owner set it (MCP action_update). Manual wins over the others.
-- done_by: the later meeting's item that reported it (distill only).
-- done_in: the later meeting that reported it (distill and reconcile); lets a
--   re-distillation of that meeting reopen what it had closed and judge again.
ALTER TABLE meeting_items ADD COLUMN status TEXT NOT NULL DEFAULT 'open'
    CHECK (status IN ('open', 'done', 'dropped'));
ALTER TABLE meeting_items ADD COLUMN status_at TEXT;
ALTER TABLE meeting_items ADD COLUMN status_source TEXT
    CHECK (status_source IN ('distill', 'tasks', 'manual'));
ALTER TABLE meeting_items ADD COLUMN done_by INTEGER REFERENCES meeting_items (id) ON DELETE SET NULL;
ALTER TABLE meeting_items ADD COLUMN done_in TEXT REFERENCES meetings (id) ON DELETE SET NULL;

CREATE INDEX meeting_items_kind_status ON meeting_items (kind, status);

-- content_hash of the meeting when its notes were last checked for earlier
-- actions reported done (by the distiller or by `elan actions reconcile`).
ALTER TABLE meetings ADD COLUMN actions_checked_hash TEXT NOT NULL DEFAULT '';

-- Link to the task in the Google Tasks web UI, filled by the status back-sync.
ALTER TABLE task_exports ADD COLUMN web_link TEXT NOT NULL DEFAULT '';

-- Cursor of the status back-sync per Google Tasks list (updatedMin).
CREATE TABLE task_list_sync (
    list_id   TEXT PRIMARY KEY,
    synced_at TEXT NOT NULL
);
