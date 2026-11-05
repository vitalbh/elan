package db

import (
	"path/filepath"
	"testing"
)

func TestOpenMigratesAndIsIdempotent(t *testing.T) {
	entries, err := migrations.ReadDir("migrations")
	if err != nil {
		t.Fatal(err)
	}
	want := len(entries)
	path := filepath.Join(t.TempDir(), "elan.db")
	for i := 0; i < 2; i++ {
		db, err := Open(path)
		if err != nil {
			t.Fatalf("open #%d: %v", i, err)
		}
		var n int
		if err := db.QueryRow(`SELECT COUNT(*) FROM schema_migrations`).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n != want {
			t.Fatalf("open #%d: %d migrations, want %d", i, n, want)
		}
		db.Close()
	}
}

func TestForeignKeysOn(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "elan.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	_, err = db.Exec(`INSERT INTO meeting_items (meeting_id, kind, text, created_at) VALUES ('nope', 'fact', 'x', 'now')`)
	if err == nil {
		t.Fatal("insert with a missing meeting should fail")
	}
}

func TestSqliteVecLoaded(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "elan.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var v string
	if err := db.QueryRow(`SELECT vec_version()`).Scan(&v); err != nil {
		t.Fatalf("sqlite-vec not loaded: %v", err)
	}
}

func TestCodeTasksTable(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "elan.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	mustExec := func(q string, args ...any) {
		t.Helper()
		if _, err := db.Exec(q, args...); err != nil {
			t.Fatal(err)
		}
	}
	mustExec(`INSERT INTO meetings (id, title, folder_id, folder_name, source_modified, content_hash, ingested_at)
		VALUES ('m1', 'R', 'f', 'f', 'x', 'h', 'now')`)
	mustExec(`INSERT INTO meeting_items (id, meeting_id, kind, text, created_at) VALUES (5, 'm1', 'action', 'a', 'now')`)
	mustExec(`INSERT INTO code_tasks (text, meeting_id, source_item_id, created_at) VALUES ('a', 'm1', 5, 'now')`)
	mustExec(`INSERT INTO code_tasks (text, created_at) VALUES ('b', 'now'), ('c', 'now')`) // NULL sources don't clash

	if _, err := db.Exec(`INSERT INTO code_tasks (text, source_item_id, created_at) VALUES ('d', 5, 'now')`); err == nil {
		t.Error("a second task from the same action should fail")
	}
	if _, err := db.Exec(`INSERT INTO code_tasks (text, status, created_at) VALUES ('e', 'started', 'now')`); err == nil {
		t.Error("invalid status should fail")
	}
	var status, project, context string
	if err := db.QueryRow(`SELECT status, project, context FROM code_tasks WHERE text = 'b'`).Scan(&status, &project, &context); err != nil {
		t.Fatal(err)
	}
	if status != "open" || project != "" || context != "" {
		t.Errorf("defaults %q %q %q", status, project, context)
	}

	// Deleting the action or the meeting keeps the task, unlinked.
	mustExec(`DELETE FROM meeting_items WHERE id = 5`)
	mustExec(`DELETE FROM meetings WHERE id = 'm1'`)
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM code_tasks WHERE text = 'a' AND source_item_id IS NULL AND meeting_id IS NULL`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Error("task not kept with NULL links")
	}
}
