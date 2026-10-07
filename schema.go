package main

import (
	"database/sql"
	"fmt"
	"log"
	"strings"
	"time"
)

// Timestamps are stored as UTC RFC3339 text ("2006-01-02T15:04:05Z"), which
// sorts correctly as a string. Calendar dates are stored as YYYY-MM-DD.
func dbTime(t time.Time) string { return t.UTC().Format(time.RFC3339) }

const sqlNow = `strftime('%Y-%m-%dT%H:%M:%SZ','now')`

// schemaVersion is the migration number the tables below correspond to.
const schemaVersion = 9

type tableDef struct{ name, columns string }

// schemaTables is the current database format. New databases are created
// from it directly.
var schemaTables = []tableDef{
	{"projects", `id INTEGER PRIMARY KEY,
		name TEXT NOT NULL,
		position INTEGER NOT NULL DEFAULT 0,
		archived INTEGER NOT NULL DEFAULT 0 CHECK(archived IN (0,1)),
		completed INTEGER NOT NULL DEFAULT 0 CHECK(completed IN (0,1)),
        revision INTEGER NOT NULL DEFAULT 1`},
	// Headings split a project's list into parts. They hold no state of their
	// own: deleting one moves its tasks to the top of the project.
	{"headings", `id INTEGER PRIMARY KEY,
		project_id INTEGER NOT NULL REFERENCES projects(id),
		name TEXT NOT NULL,
		position INTEGER NOT NULL DEFAULT 0`},
	// AUTOINCREMENT keeps ids of deleted tasks and notes from being reused, so
	// an old link or undo can never reach a different row.
	{"todos", `id INTEGER PRIMARY KEY AUTOINCREMENT,
		category TEXT NOT NULL CHECK(category IN ('groceries','todo','shopping')),
		text TEXT NOT NULL,
		due_date TEXT NOT NULL DEFAULT '',
		done INTEGER NOT NULL DEFAULT 0 CHECK(done IN (0,1)),
		archived INTEGER NOT NULL DEFAULT 0 CHECK(archived IN (0,1)),
		project_id INTEGER REFERENCES projects(id),
		heading_id INTEGER REFERENCES headings(id),
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
	// VAPID keys and delivery markers. Calendar and reminder configuration
	// comes from the deployment, not this table.
	{"settings", `key TEXT PRIMARY KEY,
		value TEXT NOT NULL`},
	{"undo_operations", `token TEXT PRIMARY KEY,
        payload TEXT NOT NULL,
        expires_at TEXT NOT NULL,
        consumed INTEGER NOT NULL DEFAULT 0 CHECK(consumed IN (0,1))`},
	{"schema_migrations", `version INTEGER PRIMARY KEY,
		applied_at TEXT NOT NULL`},
}

var schemaIndexes = []string{
	`CREATE INDEX idx_todos_active_project ON todos(project_id,category,done,due_date) WHERE archived=0`,
	`CREATE INDEX idx_todos_progress ON todos(project_id,done) WHERE category='todo' AND (archived=0 OR done=1)`,
	`CREATE INDEX idx_undo_expires ON undo_operations(expires_at)`,
	`CREATE INDEX idx_todos_project_heading ON todos(project_id,heading_id,archived)`,
	`CREATE INDEX idx_todos_due ON todos(category,archived,done,due_date)`,
	`CREATE INDEX idx_todos_completed ON todos(completed_at)`,
	`CREATE INDEX idx_todos_archive_after ON todos(archive_after)`,
	`CREATE INDEX idx_todos_repeated_from ON todos(repeated_from)`,
	`CREATE INDEX idx_headings_project ON headings(project_id,position)`,
	`CREATE INDEX idx_deliveries_pending ON push_deliveries(status,next_attempt)`,
}

// migrations bring an existing database from the previous version to the
// keyed one, inside a transaction. Never edit an applied step.
var migrations = map[int][]string{
	9: {`CREATE INDEX idx_todos_active_project ON todos(project_id,category,done,due_date) WHERE archived=0`, `ALTER TABLE projects ADD COLUMN revision INTEGER NOT NULL DEFAULT 1`,
		`CREATE TABLE undo_operations(token TEXT PRIMARY KEY,payload TEXT NOT NULL,expires_at TEXT NOT NULL,consumed INTEGER NOT NULL DEFAULT 0 CHECK(consumed IN (0,1)))`,
		`CREATE INDEX idx_undo_expires ON undo_operations(expires_at)`,
		`CREATE INDEX idx_todos_progress ON todos(project_id,done) WHERE category='todo' AND (archived=0 OR done=1)`},
	// Stages become headings: no archive of their own. Tasks under an archived
	// stage were hidden, so they are archived themselves (restorable from the
	// archive, at the top of their project) before the stage goes.
	6: {
		`UPDATE todos SET archived=1,archived_at=? WHERE archived=0 AND stage_id IN (SELECT id FROM stages WHERE archived=1)`,
		`UPDATE todos SET stage_id=NULL WHERE stage_id IN (SELECT id FROM stages WHERE archived=1)`,
		`DELETE FROM stages WHERE archived=1`,
		`DROP INDEX idx_stages_project`,
		`DROP INDEX idx_todos_project_stage`,
		`ALTER TABLE stages DROP COLUMN archived`,
		`ALTER TABLE stages RENAME TO headings`,
		`ALTER TABLE todos RENAME COLUMN stage_id TO heading_id`,
		`CREATE INDEX idx_todos_project_heading ON todos(project_id,heading_id,archived)`,
		`CREATE INDEX idx_headings_project ON headings(project_id,position)`,
	},
	// Linking notes to projects was retired; nothing links them any more.
	7: {`DROP TABLE project_notes`},
	// The reminder time and calendar zone now come only from the deployment
	// (TASK_REMINDER_TIME, TZ); drop the copies the settings screen kept.
	8: {`DELETE FROM settings WHERE key IN ('task_enabled','task_time','timezone')`},
}

// migrate creates a new database from the schema, or brings an existing one
// to the current format. Databases from before the migration-5 rebuild
// (September 2026) are refused: run a release from that time first.
//
// To change the schema later, edit schemaTables/schemaIndexes, bump
// schemaVersion, and add a numbered step to migrations.
func migrate(conn *sql.DB, now time.Time) error {
	var todos int
	if err := conn.QueryRow(`SELECT count(*) FROM sqlite_master WHERE type='table' AND name='todos'`).Scan(&todos); err != nil {
		return err
	}
	if todos == 0 {
		return createSchema(conn, now)
	}
	var version int
	if err := conn.QueryRow(`SELECT COALESCE(max(version),0) FROM schema_migrations`).Scan(&version); err != nil || version < 5 {
		return fmt.Errorf("database format %d is older than 5; upgrade with a 2026-09-27 release (commit 02c622b) first, then this one", version)
	}
	if version < schemaVersion {
		if err := snapshotBeforeMigration(conn, version, now); err != nil {
			return err
		}
	}
	for v := version + 1; v <= schemaVersion; v++ {
		if err := applyMigration(conn, v, now); err != nil {
			return fmt.Errorf("migration %d: %w", v, err)
		}
	}
	return nil
}

func applyMigration(conn *sql.DB, v int, now time.Time) error {
	tx, err := conn.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, statement := range migrations[v] {
		var args []any
		if strings.Contains(statement, "?") {
			args = append(args, dbTime(now))
		}
		if _, err = tx.Exec(statement, args...); err != nil {
			return fmt.Errorf("%s: %w", statement, err)
		}
	}
	if _, err = tx.Exec(`INSERT INTO schema_migrations VALUES(?,?)`, v, dbTime(now)); err != nil {
		return err
	}
	return tx.Commit()
}

// snapshotBeforeMigration copies a file database next to itself before its
// format changes, so an upgrade can always be undone by hand.
func snapshotBeforeMigration(conn *sql.DB, version int, now time.Time) error {
	var seq int
	var name, file string
	if err := conn.QueryRow(`PRAGMA database_list`).Scan(&seq, &name, &file); err != nil || file == "" {
		return err
	}
	target := fmt.Sprintf("%s.v%d-%s", file, version, now.UTC().Format("20060102T150405Z"))
	if _, err := conn.Exec(`VACUUM INTO ?`, target); err != nil {
		return fmt.Errorf("snapshot before migrating: %w", err)
	}
	log.Printf("Saved a copy of the version %d database to %s", version, target)
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
