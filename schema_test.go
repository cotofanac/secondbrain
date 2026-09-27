package main

import (
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func openTestDB(t *testing.T, path string) *sql.DB {
	t.Helper()
	conn, err := sql.Open("sqlite3", path+"?_foreign_keys=on")
	if err != nil {
		t.Fatal(err)
	}
	conn.SetMaxOpenConns(1)
	t.Cleanup(func() { conn.Close() })
	return conn
}

func TestMigrateCreatesSchemaOnce(t *testing.T) {
	conn := openTestDB(t, filepath.Join(t.TempDir(), "new.db"))
	now := time.Date(2026, 9, 28, 8, 0, 0, 0, time.UTC)
	for i := 0; i < 2; i++ {
		if err := migrate(conn, now); err != nil {
			t.Fatalf("run %d: %v", i+1, err)
		}
	}
	var version, tables int
	conn.QueryRow(`SELECT max(version) FROM schema_migrations`).Scan(&version)
	conn.QueryRow(`SELECT count(*) FROM sqlite_master WHERE type='table' AND name!='sqlite_sequence'`).Scan(&tables)
	if version != schemaVersion || tables != len(schemaTables) {
		t.Fatalf("version %d with %d tables, want %d with %d", version, tables, schemaVersion, len(schemaTables))
	}
	var created string
	if _, err := conn.Exec(`INSERT INTO todos(category,text) VALUES('todo','Check')`); err != nil {
		t.Fatal(err)
	}
	conn.QueryRow(`SELECT created_at FROM todos`).Scan(&created)
	if _, ok := parseDBTime(created); !ok {
		t.Errorf("default created_at %q is not RFC3339", created)
	}
	if _, err := conn.Exec(`INSERT INTO todos(category,text,repeat) VALUES('todo','Bad','hourly')`); err == nil {
		t.Error("invalid repeat accepted")
	}
	if _, err := conn.Exec(`INSERT INTO todos(category,text,project_id) VALUES('todo','Orphan',42)`); err == nil {
		t.Error("foreign keys are not enforced")
	}
}

// Databases from before the September 2026 rebuild must go through that
// release first; this one no longer knows how to upgrade them.
func TestMigrateRefusesOlderDatabases(t *testing.T) {
	for name, setup := range map[string][]string{
		"before numbered migrations": {`CREATE TABLE todos(id INTEGER PRIMARY KEY,category TEXT,text TEXT)`},
		"at migration 4": {
			`CREATE TABLE todos(id INTEGER PRIMARY KEY,category TEXT,text TEXT)`,
			`CREATE TABLE schema_migrations(version INTEGER PRIMARY KEY,applied_at TEXT NOT NULL)`,
			`INSERT INTO schema_migrations VALUES(1,''),(2,''),(3,''),(4,'')`,
		},
	} {
		conn := openTestDB(t, filepath.Join(t.TempDir(), "old.db"))
		for _, q := range setup {
			if _, err := conn.Exec(q); err != nil {
				t.Fatal(err)
			}
		}
		err := migrate(conn, time.Now())
		if err == nil || !strings.Contains(err.Error(), "02c622b") {
			t.Errorf("%s: err = %v, want a pointer to the upgrade release", name, err)
		}
	}
}
