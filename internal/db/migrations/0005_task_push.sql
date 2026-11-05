-- Push of action status changes made in elan (manual, distill, reconcile) to
-- the exported Google Tasks (daemon step taskpush).
--
-- pushed_status: the action status Google Tasks is known to reflect, either
--   because taskpush wrote it or because it came from Tasks (back-sync).
--   '' = unknown (never pushed).
-- pushed_at: status_at of that status. A change in elan is pushed when its
--   status differs from pushed_status or its status_at is newer.
ALTER TABLE task_exports ADD COLUMN pushed_status TEXT NOT NULL DEFAULT '';
ALTER TABLE task_exports ADD COLUMN pushed_at TEXT NOT NULL DEFAULT '';

-- Baseline: tasks exported before this migration are not pushed for status
-- changes made before it (the backlog stays as it is in Google Tasks); only
-- changes from now on are.
UPDATE task_exports SET pushed_at = strftime('%Y-%m-%dT%H:%M:%SZ', 'now');
