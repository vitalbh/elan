-- Owner's action items already sent to Google Tasks. Keyed by meeting and
-- text hash, not by item id: re-distilling a meeting recreates its items.
CREATE TABLE task_exports (
    id         INTEGER PRIMARY KEY,
    meeting_id TEXT NOT NULL REFERENCES meetings (id) ON DELETE CASCADE,
    text_hash  TEXT NOT NULL,
    list_id    TEXT NOT NULL,
    task_id    TEXT NOT NULL,
    created_at TEXT NOT NULL,
    UNIQUE (meeting_id, text_hash)
);
