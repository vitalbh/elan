// Package db opens the profile's SQLite database (with sqlite-vec loaded) and
// applies the embedded migrations on open.
package db

import (
	"database/sql"
	"embed"
	"fmt"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	sqlitevec "github.com/asg017/sqlite-vec-go-bindings/cgo"
	_ "github.com/mattn/go-sqlite3" // driver "sqlite3"
)

// Every connection opened by this process gets sqlite-vec (vec0, vec_*).
// The extension is compiled into the binary and linked against the SQLite
// amalgamation of mattn/go-sqlite3.
func init() { sqlitevec.Auto() }

//go:embed migrations/*.sql
var migrations embed.FS

// Open opens (or creates) the database at path and migrates it.
func Open(path string) (*sql.DB, error) {
	q := url.Values{}
	q.Set("_journal_mode", "WAL")
	q.Set("_busy_timeout", "5000")
	q.Set("_foreign_keys", "on")
	q.Set("_txlock", "immediate")
	db, err := sql.Open("sqlite3", "file:"+path+"?"+q.Encode())
	if err != nil {
		return nil, err
	}
	if err := migrate(db); err != nil {
		db.Close()
		return nil, err
	}
	return db, nil
}

func migrate(db *sql.DB) error {
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS schema_migrations (
		version    INTEGER PRIMARY KEY,
		applied_at TEXT NOT NULL
	)`); err != nil {
		return err
	}
	entries, err := migrations.ReadDir("migrations")
	if err != nil {
		return err
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	for _, e := range entries {
		version, err := strconv.Atoi(strings.SplitN(e.Name(), "_", 2)[0])
		if err != nil {
			return fmt.Errorf("migration sem número: %s", e.Name())
		}
		var applied int
		if err := db.QueryRow(`SELECT COUNT(*) FROM schema_migrations WHERE version = ?`, version).Scan(&applied); err != nil {
			return err
		}
		if applied > 0 {
			continue
		}
		body, err := migrations.ReadFile("migrations/" + e.Name())
		if err != nil {
			return err
		}
		tx, err := db.Begin()
		if err != nil {
			return err
		}
		if _, err := tx.Exec(string(body)); err != nil {
			tx.Rollback()
			return fmt.Errorf("migration %s: %w", e.Name(), err)
		}
		if _, err := tx.Exec(`INSERT INTO schema_migrations (version, applied_at) VALUES (?, ?)`,
			version, time.Now().UTC().Format(time.RFC3339)); err != nil {
			tx.Rollback()
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
	}
	return nil
}
