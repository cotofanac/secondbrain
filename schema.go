package main

import (
	"database/sql"
	"fmt"
	"time"
)

// Timestamps are stored as UTC RFC3339 text ("2006-01-02T15:04:05Z"), which
// sorts correctly as a string. Calendar dates are stored as YYYY-MM-DD.
func dbTime(t time.Time) string { return t.UTC().Format(time.RFC3339) }

const sqlNow = `strftime('%Y-%m-%dT%H:%M:%SZ','now')`

// schemaVersion is the migration number the tables below correspond to.
const schemaVersion = 5

type tableDef struct{ name, columns string }

// schemaTables is the current database format. New databases are created
// from it directly.
var schemaTables = []tableDef{
	{"projects", `id INTEGER PRIMARY KEY,
		name TEXT NOT NULL,
		position INTEGER NOT NULL DEFAULT 0,
		archived INTEGER NOT NULL DEFAULT 0 CHECK(archived IN (0,1)),
		completed INTEGER NOT NULL DEFAULT 0 CHECK(completed IN (0,1))`},
	{"stages", `id INTEGER PRIMARY KEY,
		project_id INTEGER NOT NULL REFERENCES projects(id),
		name TEXT NOT NULL,
		position INTEGER NOT NULL DEFAULT 0,
		archived INTEGER NOT NULL DEFAULT 0 CHECK(archived IN (0,1))`},
	// AUTOINCREMENT keeps ids of deleted tasks and notes from being reused, so
	// an old link or undo can never reach a different row.
	{"todos", `id INTEGER PRIMARY KEY AUTOINCREMENT,
		category TEXT NOT NULL CHECK(category IN ('groceries','todo','shopping')),
		text TEXT NOT NULL,
		due_date TEXT NOT NULL DEFAULT '',
		done INTEGER NOT NULL DEFAULT 0 CHECK(done IN (0,1)),
		archived INTEGER NOT NULL DEFAULT 0 CHECK(archived IN (0,1)),
		project_id INTEGER REFERENCES projects(id),
		stage_id INTEGER REFERENCES stages(id),
		repeat TEXT NOT NULL DEFAULT '' CHECK(repeat IN ('','daily','weekly','monthly','yearly')),
		repeated_from INTEGER REFERENCES todos(id) ON DELETE SET NULL,
		revision INTEGER NOT NULL DEFAULT 1,
		created_at TEXT NOT NULL DEFAULT (` + sqlNow + `),
		completed_at TEXT,
		archived_at TEXT,
		archive_after TEXT`},
	{"notes", `id INTEGER PRIMARY KEY AUTOINCREMENT,
		title TEXT NOT NULL UNIQUE,
		content TEXT NOT NULL DEFAULT '',
		archived INTEGER NOT NULL DEFAULT 0 CHECK(archived IN (0,1)),
		revision INTEGER NOT NULL DEFAULT 1,
		created_at TEXT NOT NULL DEFAULT (` + sqlNow + `),
		updated_at TEXT NOT NULL DEFAULT (` + sqlNow + `)`},
	{"project_notes", `project_id INTEGER NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
		note_id INTEGER NOT NULL REFERENCES notes(id) ON DELETE CASCADE,
		PRIMARY KEY(project_id,note_id)`},
	{"sessions", `token TEXT PRIMARY KEY,
		expires_at TEXT NOT NULL,
		last_seen TEXT NOT NULL`},
	// One row per installed device or browser; the endpoint is the push
	// service URL and is unique per subscription.
	{"push_subscriptions", `endpoint TEXT PRIMARY KEY,
		p256dh TEXT NOT NULL,
		auth TEXT NOT NULL,
		created_at TEXT NOT NULL DEFAULT (` + sqlNow + `)`},
	{"push_deliveries", `endpoint TEXT NOT NULL REFERENCES push_subscriptions(endpoint) ON DELETE CASCADE,
		event_key TEXT NOT NULL,
		payload TEXT NOT NULL,
		attempts INTEGER NOT NULL DEFAULT 0,
		next_attempt TEXT NOT NULL,
		expires_at TEXT NOT NULL,
		status TEXT NOT NULL DEFAULT 'pending',
		result TEXT NOT NULL DEFAULT '',
		PRIMARY KEY(endpoint,event_key)`},
	// Key/value store: VAPID keys, reminder schedule and time zone.
	{"settings", `key TEXT PRIMARY KEY,
		value TEXT NOT NULL`},
	{"schema_migrations", `version INTEGER PRIMARY KEY,
		applied_at TEXT NOT NULL`},
}

var schemaIndexes = []string{
	`CREATE INDEX idx_todos_project_stage ON todos(project_id,stage_id,archived)`,
	`CREATE INDEX idx_todos_due ON todos(category,archived,done,due_date)`,
	`CREATE INDEX idx_todos_completed ON todos(completed_at)`,
	`CREATE INDEX idx_todos_archive_after ON todos(archive_after)`,
	`CREATE INDEX idx_todos_repeated_from ON todos(repeated_from)`,
	`CREATE INDEX idx_stages_project ON stages(project_id,position)`,
	`CREATE INDEX idx_deliveries_pending ON push_deliveries(status,next_attempt)`,
}

// migrate creates a new database from the schema, or checks that an existing
// one is already in the current format. Databases from before the migration-5
// rebuild (September 2026) are refused: run a release from that time first.
//
// To change the schema later, edit schemaTables/schemaIndexes, bump
// schemaVersion, and add a numbered transactional step here that brings an
// existing database from the previous version.
func migrate(conn *sql.DB, now time.Time) error {
	var todos int
	if err := conn.QueryRow(`SELECT count(*) FROM sqlite_master WHERE type='table' AND name='todos'`).Scan(&todos); err != nil {
		return err
	}
	if todos == 0 {
		return createSchema(conn, now)
	}
	var version int
	if err := conn.QueryRow(`SELECT COALESCE(max(version),0) FROM schema_migrations`).Scan(&version); err != nil || version < schemaVersion {
		return fmt.Errorf("database format %d is older than %d; upgrade with a 2026-09-27 release (commit 02c622b) first, then this one", version, schemaVersion)
	}
	return nil
}

func createSchema(conn *sql.DB, now time.Time) error {
	tx, err := conn.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, t := range schemaTables {
		if _, err = tx.Exec("CREATE TABLE " + t.name + " (" + t.columns + ")"); err != nil {
			return fmt.Errorf("create %s: %w", t.name, err)
		}
	}
	for _, statement := range schemaIndexes {
		if _, err = tx.Exec(statement); err != nil {
			return err
		}
	}
	for v := 1; v <= schemaVersion; v++ {
		if _, err = tx.Exec(`INSERT INTO schema_migrations VALUES(?,?)`, v, dbTime(now)); err != nil {
			return err
		}
	}
	return tx.Commit()
}
