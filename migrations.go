package main

import (
	"database/sql"
	"fmt"
	"time"
)

// Migrations only add schema. Run against a backed-up database before rollout.
func migrateWorkspace(conn *sql.DB, now time.Time) error {
	tx, err := conn.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.Exec(`CREATE TABLE IF NOT EXISTS schema_migrations (version INTEGER PRIMARY KEY, applied_at TEXT NOT NULL)`); err != nil {
		return err
	}
	var count int
	if err = tx.QueryRow(`SELECT count(*) FROM schema_migrations WHERE version=1`).Scan(&count); err != nil {
		return err
	}
	if count != 0 {
		return tx.Commit()
	}
	statements := []string{
		`CREATE TABLE projects (id INTEGER PRIMARY KEY, name TEXT NOT NULL, position INTEGER NOT NULL DEFAULT 0, archived INTEGER NOT NULL DEFAULT 0, completed INTEGER NOT NULL DEFAULT 0)`,
		`CREATE TABLE stages (id INTEGER PRIMARY KEY, project_id INTEGER NOT NULL REFERENCES projects(id), name TEXT NOT NULL, position INTEGER NOT NULL DEFAULT 0, archived INTEGER NOT NULL DEFAULT 0)`,
		`ALTER TABLE todos ADD COLUMN project_id INTEGER REFERENCES projects(id)`,
		`ALTER TABLE todos ADD COLUMN stage_id INTEGER REFERENCES stages(id)`,
		`ALTER TABLE todos ADD COLUMN completed_at TEXT`,
		`ALTER TABLE todos ADD COLUMN archive_after TEXT`,
		`CREATE TABLE project_notes (project_id INTEGER NOT NULL REFERENCES projects(id) ON DELETE CASCADE, note_id INTEGER NOT NULL REFERENCES notes(id) ON DELETE CASCADE, PRIMARY KEY(project_id,note_id))`,
		`CREATE TABLE weekly_reviews (id INTEGER PRIMARY KEY, period_end TEXT NOT NULL UNIQUE, content TEXT NOT NULL, created_at TEXT NOT NULL)`,
		`CREATE TABLE push_deliveries (endpoint TEXT NOT NULL REFERENCES push_subscriptions(endpoint) ON DELETE CASCADE, event_key TEXT NOT NULL, payload TEXT NOT NULL, attempts INTEGER NOT NULL DEFAULT 0, next_attempt TEXT NOT NULL, expires_at TEXT NOT NULL, status TEXT NOT NULL DEFAULT 'pending', result TEXT NOT NULL DEFAULT '', PRIMARY KEY(endpoint,event_key))`,
		`CREATE INDEX idx_todos_project_stage ON todos(project_id,stage_id,archived)`,
		`CREATE INDEX idx_todos_due ON todos(category,archived,done,due_date)`,
		`CREATE INDEX idx_todos_completed ON todos(completed_at)`,
		`CREATE INDEX idx_todos_archive_after ON todos(archive_after)`,
		`CREATE INDEX idx_stages_project ON stages(project_id,position)`,
		`CREATE INDEX idx_deliveries_pending ON push_deliveries(status,next_attempt)`,
	}
	for _, statement := range statements {
		if _, err = tx.Exec(statement); err != nil {
			return fmt.Errorf("workspace migration: %w", err)
		}
	}
	// Old completion dates are unknown: start cleanup's clock without inventing history.
	if _, err = tx.Exec(`UPDATE todos SET archive_after=? WHERE category='todo' AND done=1 AND archived=0`, now.UTC().AddDate(0, 0, 7).Format(time.RFC3339)); err != nil {
		return err
	}
	if _, err = tx.Exec(`INSERT INTO schema_migrations VALUES(1,?)`, now.UTC().Format(time.RFC3339)); err != nil {
		return err
	}
	return tx.Commit()
}
