-- Code tasks: an action that requires changing code, config, infra, tests, a
-- repo, a deploy or investigating a bug in a system. The distiller classifies
-- new actions; `elan actions classify` backfills the older ones. Like the
-- status, the classification and the start tracking are carried over to the
-- new item with the same normalized text when a meeting is re-distilled.
--
-- category: 'code' or 'other' on actions; NULL = not classified yet (and on
--   the other kinds).
-- project: the project as named in the meeting ("Vega", "Blue Orion"); ''
--   when none. Matched against config.json "projects" to find its folder.
-- started_at, branch: set by `elan codetasks start` (RFC 3339; elan/<slug>).
ALTER TABLE meeting_items ADD COLUMN category TEXT CHECK (category IN ('code', 'other'));
ALTER TABLE meeting_items ADD COLUMN project TEXT NOT NULL DEFAULT '';
ALTER TABLE meeting_items ADD COLUMN started_at TEXT;
ALTER TABLE meeting_items ADD COLUMN branch TEXT;

CREATE INDEX meeting_items_category ON meeting_items (kind, category, status);
