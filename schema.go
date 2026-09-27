package main

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
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
// from it directly; older ones reach it through migrate.
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

// migrate brings any database to the current format: an empty one is
// created from the schema, an older one is upgraded and then rebuilt.
func migrate(conn *sql.DB, now time.Time) error {
	var todos, current int
	if err := conn.QueryRow(`SELECT count(*) FROM sqlite_master WHERE type='table' AND name='todos'`).Scan(&todos); err != nil {
		return err
	}
	if todos == 0 {
		return createSchema(conn, now)
	}
	if err := conn.QueryRow(`SELECT count(*) FROM sqlite_master WHERE type='table' AND name='schema_migrations'`).Scan(&current); err != nil {
		return err
	}
	if current > 0 {
		if err := conn.QueryRow(`SELECT count(*) FROM schema_migrations WHERE version>=?`, schemaVersion).Scan(&current); err != nil {
			return err
		}
		if current > 0 {
			return nil
		}
	}
	upgradeLegacyTables(conn)
	if err := migrateWorkspace(conn, now); err != nil {
		return err
	}
	return rebuildSchema(conn, now)
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

// upgradeLegacyTables brings a database from before the numbered migrations
// to the shape migration 1 expects. Its ALTERs fail harmlessly once applied.
func upgradeLegacyTables(conn *sql.DB) {
	for _, statement := range []string{
		`CREATE TABLE IF NOT EXISTS todos (id INTEGER PRIMARY KEY AUTOINCREMENT,
			category TEXT NOT NULL CHECK(category IN ('groceries','todo','shopping')), text TEXT NOT NULL,
			due_date TEXT DEFAULT '', done INTEGER DEFAULT 0, position INTEGER DEFAULT 0,
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP, archived INTEGER DEFAULT 0)`,
		`CREATE TABLE IF NOT EXISTS notes (id INTEGER PRIMARY KEY AUTOINCREMENT, title TEXT NOT NULL UNIQUE,
			content TEXT DEFAULT '', updated_at DATETIME DEFAULT CURRENT_TIMESTAMP,
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP, archived INTEGER DEFAULT 0)`,
		`CREATE TABLE IF NOT EXISTS sessions (token TEXT PRIMARY KEY, expires_at TEXT NOT NULL)`,
		`CREATE TABLE IF NOT EXISTS push_subscriptions (endpoint TEXT PRIMARY KEY, p256dh TEXT NOT NULL,
			auth TEXT NOT NULL, created_at DATETIME DEFAULT CURRENT_TIMESTAMP)`,
		`CREATE TABLE IF NOT EXISTS settings (key TEXT PRIMARY KEY, value TEXT NOT NULL)`,
		`ALTER TABLE sessions ADD COLUMN last_seen TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE todos ADD COLUMN archived_at DATETIME DEFAULT NULL`,
	} {
		conn.Exec(statement)
	}
}

// Legacy settings nothing reads any more.
const retiredSettings = `'task_reminder_sent','review_enabled','review_enabled_at','review_time','review_day'`

// ts converts a stored timestamp in either SQLite's "YYYY-MM-DD HH:MM:SS" or
// RFC3339 (with or without fractional seconds) to UTC RFC3339.
func ts(column string) string { return `strftime('%Y-%m-%dT%H:%M:%SZ',` + column + `)` }

// rebuildSchema is migration 5. Columns added over time were appended to the
// original tables with loose types, so every table is recreated in its final
// shape and the rows are copied across, normalizing timestamps and flags. It
// also drops weekly reviews and todos.position, which nothing uses. A copy of
// the database is written to the backup folder first.
func rebuildSchema(conn *sql.DB, now time.Time) error {
	ctx := context.Background()
	c, err := conn.Conn(ctx)
	if err != nil {
		return err
	}
	defer c.Close()
	if backupDir != "" {
		path := filepath.Join(backupDir, "secondbrain-before-v5.db")
		if _, statErr := os.Stat(path); os.IsNotExist(statErr) {
			if err = os.MkdirAll(backupDir, 0750); err != nil {
				return err
			}
			if _, err = c.ExecContext(ctx, `VACUUM INTO ?`, path); err != nil {
				return fmt.Errorf("backup before rebuild: %w", err)
			}
			if err = os.Chmod(path, 0600); err != nil {
				return err
			}
			log.Printf("Database copy before rebuild: %s", path)
		}
	}
	// Tables are swapped one by one, so references are checked once at the end.
	if _, err = c.ExecContext(ctx, `PRAGMA foreign_keys=OFF`); err != nil {
		return err
	}
	defer c.ExecContext(ctx, `PRAGMA foreign_keys=ON`)
	tx, err := c.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	sequences := map[string]int64{}
	rows, err := tx.Query(`SELECT name,seq FROM sqlite_sequence`)
	if err == nil {
		for rows.Next() {
			var name string
			var seq int64
			if err = rows.Scan(&name, &seq); err != nil {
				break
			}
			sequences[name] = seq
		}
		if err == nil {
			err = rows.Err()
		}
		rows.Close()
	}
	if err != nil {
		return err
	}

	stamp := "'" + dbTime(now) + "'"
	copies := map[string]string{
		"projects": `SELECT id,name,position,archived!=0,completed!=0 FROM projects`,
		"stages":   `SELECT id,project_id,name,position,archived!=0 FROM stages`,
		// Only tasks are completed and tidied away; list items were given the
		// same stamps by the shared checkbox handler, which meant nothing.
		"todos": `SELECT id,category,text,COALESCE(due_date,''),COALESCE(done,0)!=0,COALESCE(archived,0)!=0,
			project_id,stage_id,repeat,CASE WHEN repeated_from IN (SELECT id FROM todos) THEN repeated_from END,revision,
			COALESCE(` + ts("created_at") + `,` + stamp + `),
			CASE WHEN category='todo' THEN ` + ts("completed_at") + ` END,` + ts("archived_at") + `,
			CASE WHEN category='todo' THEN ` + ts("archive_after") + ` END
			FROM todos ORDER BY id`,
		"notes": `SELECT id,title,COALESCE(content,''),COALESCE(archived,0)!=0,revision,
			COALESCE(` + ts("created_at") + `,` + stamp + `),COALESCE(` + ts("updated_at") + `,` + ts("created_at") + `,` + stamp + `)
			FROM notes ORDER BY id`,
		"project_notes":      `SELECT project_id,note_id FROM project_notes`,
		"sessions":           `SELECT token,` + ts("expires_at") + `,COALESCE(` + ts("NULLIF(last_seen,'')") + `,` + stamp + `) FROM sessions WHERE ` + ts("expires_at") + ` IS NOT NULL`,
		"push_subscriptions": `SELECT endpoint,p256dh,auth,COALESCE(` + ts("created_at") + `,` + stamp + `) FROM push_subscriptions`,
		"push_deliveries":    `SELECT endpoint,event_key,payload,attempts,` + ts("next_attempt") + `,` + ts("expires_at") + `,status,result FROM push_deliveries`,
		"settings":           `SELECT key,value FROM settings WHERE key NOT IN (` + retiredSettings + `)`,
		"schema_migrations":  `SELECT version,applied_at FROM schema_migrations`,
	}
	for _, t := range schemaTables {
		if _, err = tx.Exec("CREATE TABLE " + t.name + "_new (" + t.columns + ")"); err != nil {
			return fmt.Errorf("create %s: %w", t.name, err)
		}
		if _, err = tx.Exec("INSERT INTO " + t.name + "_new " + copies[t.name]); err != nil {
			return fmt.Errorf("copy %s: %w", t.name, err)
		}
	}

	// Every row and every stored timestamp must have survived the copy.
	checks := []struct{ label, before, after string }{
		{"projects", `SELECT count(*) FROM projects`, `SELECT count(*) FROM projects_new`},
		{"stages", `SELECT count(*) FROM stages`, `SELECT count(*) FROM stages_new`},
		{"todos", `SELECT count(*) FROM todos`, `SELECT count(*) FROM todos_new`},
		{"notes", `SELECT count(*) FROM notes`, `SELECT count(*) FROM notes_new`},
		{"project notes", `SELECT count(*) FROM project_notes`, `SELECT count(*) FROM project_notes_new`},
		{"push subscriptions", `SELECT count(*) FROM push_subscriptions`, `SELECT count(*) FROM push_subscriptions_new`},
		{"push deliveries", `SELECT count(*) FROM push_deliveries`, `SELECT count(*) FROM push_deliveries_new WHERE next_attempt IS NOT NULL AND expires_at IS NOT NULL`},
		{"archive times", `SELECT count(archived_at) FROM todos`, `SELECT count(archived_at) FROM todos_new`},
		{"completion times", `SELECT count(completed_at) FROM todos WHERE category='todo'`, `SELECT count(completed_at) FROM todos_new`},
		{"cleanup times", `SELECT count(archive_after) FROM todos WHERE category='todo'`, `SELECT count(archive_after) FROM todos_new`},
		{"readable task times", `SELECT 0`, `SELECT count(*) FROM todos WHERE (created_at IS NOT NULL AND ` + ts("created_at") + ` IS NULL) OR (archived_at IS NOT NULL AND ` + ts("archived_at") + ` IS NULL)`},
		{"readable note times", `SELECT 0`, `SELECT count(*) FROM notes WHERE (created_at IS NOT NULL AND ` + ts("created_at") + ` IS NULL) OR (updated_at IS NOT NULL AND ` + ts("updated_at") + ` IS NULL)`},
	}
	for _, check := range checks {
		var before, after int
		if err = tx.QueryRow(check.before).Scan(&before); err == nil {
			err = tx.QueryRow(check.after).Scan(&after)
		}
		if err != nil {
			return fmt.Errorf("verify %s: %w", check.label, err)
		}
		if before != after {
			return fmt.Errorf("verify %s: %d before, %d after", check.label, before, after)
		}
	}

	for _, t := range schemaTables {
		if _, err = tx.Exec("DROP TABLE " + t.name); err != nil {
			return err
		}
	}
	if _, err = tx.Exec(`DROP TABLE IF EXISTS weekly_reviews`); err != nil {
		return err
	}
	for _, t := range schemaTables {
		if _, err = tx.Exec("ALTER TABLE " + t.name + "_new RENAME TO " + t.name); err != nil {
			return err
		}
	}
	for _, statement := range schemaIndexes {
		if _, err = tx.Exec(statement); err != nil {
			return err
		}
	}
	// Copying explicit ids sets each counter to the highest id present; keep
	// the old counter when rows at the top had been deleted.
	for name, seq := range sequences {
		if _, err = tx.Exec(`UPDATE sqlite_sequence SET seq=max(seq,?) WHERE name=?`, seq, name); err != nil {
			return err
		}
	}
	var problems []string
	rows, err = tx.Query(`PRAGMA foreign_key_check`)
	if err != nil {
		return err
	}
	for rows.Next() {
		var table, parent string
		var rowid, fk sql.NullInt64
		if err = rows.Scan(&table, &rowid, &parent, &fk); err != nil {
			break
		}
		problems = append(problems, fmt.Sprintf("%s row %d -> %s", table, rowid.Int64, parent))
	}
	rows.Close()
	if err != nil {
		return err
	}
	if len(problems) > 0 {
		return fmt.Errorf("broken references after rebuild: %s", strings.Join(problems, ", "))
	}
	if _, err = tx.Exec(`INSERT INTO schema_migrations VALUES(?,?)`, schemaVersion, dbTime(now)); err != nil {
		return err
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	// Reclaim the space of the dropped tables.
	_, err = c.ExecContext(ctx, `VACUUM`)
	return err
}
