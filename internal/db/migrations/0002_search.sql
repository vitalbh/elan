-- Search index: derived from meetings, meeting_items and notes, rebuildable
-- at any time (`elan index --rebuild`). The vec0 table (chunks_vec) depends on
-- the embedder's dimension, so the search package creates it at runtime and
-- records model and dimension in index_meta.
CREATE TABLE chunks (
    id           INTEGER PRIMARY KEY,
    source_type  TEXT NOT NULL CHECK (source_type IN ('meeting_notes', 'meeting_item', 'note', 'transcript')),
    source_id    TEXT NOT NULL,              -- meetings.id, meeting_items.id or notes.id
    seq          INTEGER NOT NULL DEFAULT 0, -- position of the chunk inside its source
    meeting_id   TEXT,                       -- NULL for notes
    title        TEXT NOT NULL DEFAULT '',
    text         TEXT NOT NULL,
    kind         TEXT NOT NULL DEFAULT '',   -- meeting_items.kind
    folder_id    TEXT NOT NULL DEFAULT '',
    superseded   INTEGER NOT NULL DEFAULT 0,
    at           TEXT NOT NULL DEFAULT '',   -- RFC 3339 UTC; '' when unknown
    content_hash TEXT NOT NULL,              -- sha256 of every field above that is embedded or filtered on
    created_at   TEXT NOT NULL
);

CREATE INDEX chunks_source ON chunks (source_type, source_id);

CREATE VIRTUAL TABLE chunks_fts USING fts5 (
    title, text,
    content = 'chunks', content_rowid = 'id',
    tokenize = 'unicode61 remove_diacritics 2'
);

CREATE TRIGGER chunks_ai AFTER INSERT ON chunks BEGIN
    INSERT INTO chunks_fts (rowid, title, text) VALUES (new.id, new.title, new.text);
END;

CREATE TRIGGER chunks_ad AFTER DELETE ON chunks BEGIN
    INSERT INTO chunks_fts (chunks_fts, rowid, title, text) VALUES ('delete', old.id, old.title, old.text);
END;

CREATE TRIGGER chunks_au AFTER UPDATE OF title, text ON chunks BEGIN
    INSERT INTO chunks_fts (chunks_fts, rowid, title, text) VALUES ('delete', old.id, old.title, old.text);
    INSERT INTO chunks_fts (rowid, title, text) VALUES (new.id, new.title, new.text);
END;

-- The embedder that built chunks_vec. One row at most.
CREATE TABLE index_meta (
    id         INTEGER PRIMARY KEY CHECK (id = 1),
    model      TEXT NOT NULL,
    dim        INTEGER NOT NULL,
    created_at TEXT NOT NULL
);
