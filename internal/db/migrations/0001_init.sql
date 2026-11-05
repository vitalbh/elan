CREATE TABLE meetings (
    id              TEXT PRIMARY KEY,           -- Drive id of the real doc (shortcuts resolved)
    title           TEXT NOT NULL,
    folder_id       TEXT NOT NULL,              -- Drive folder of the meeting or of the series
    folder_name     TEXT NOT NULL,
    recurring       INTEGER NOT NULL DEFAULT 0, -- folder is a "(recurring)" series
    started_at      TEXT,                       -- RFC 3339; NULL when the doc name has no date
    source_modified TEXT NOT NULL,              -- Drive modifiedTime of the doc
    content_hash    TEXT NOT NULL,              -- sha256 of the exported text
    header          TEXT NOT NULL DEFAULT '',   -- invitees, attachments: lines before the notes
    notes           TEXT NOT NULL DEFAULT '',   -- Gemini notes (summary, next steps, details)
    transcript      TEXT NOT NULL DEFAULT '',
    distilled_hash  TEXT NOT NULL DEFAULT '',   -- content_hash at the last distillation
    ingested_at     TEXT NOT NULL
);

CREATE INDEX meetings_started ON meetings (started_at);
CREATE INDEX meetings_folder ON meetings (folder_id, started_at);

-- Distilled facts of a meeting: what the search indexes by default.
CREATE TABLE meeting_items (
    id            INTEGER PRIMARY KEY,
    meeting_id    TEXT NOT NULL REFERENCES meetings (id) ON DELETE CASCADE,
    kind          TEXT NOT NULL CHECK (kind IN ('summary', 'decision', 'action', 'fact')),
    text          TEXT NOT NULL,
    people        TEXT NOT NULL DEFAULT '[]',   -- JSON array of names
    importance    INTEGER NOT NULL DEFAULT 2 CHECK (importance BETWEEN 1 AND 3),
    superseded_by INTEGER REFERENCES meeting_items (id) ON DELETE SET NULL,
    created_at    TEXT NOT NULL
);

CREATE INDEX meeting_items_meeting ON meeting_items (meeting_id);

CREATE TABLE notes (
    id         INTEGER PRIMARY KEY,
    text       TEXT NOT NULL,
    source     TEXT NOT NULL DEFAULT 'cli',
    created_at TEXT NOT NULL
);
