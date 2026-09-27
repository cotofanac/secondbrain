package main

import (
	"database/sql"
	"fmt"
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

// The version 5 tables that migration 6 changes, as that release created them.
var version5Tables = []string{
	`CREATE TABLE projects (id INTEGER PRIMARY KEY,
		name TEXT NOT NULL,
		position INTEGER NOT NULL DEFAULT 0,
		archived INTEGER NOT NULL DEFAULT 0 CHECK(archived IN (0,1)),
		completed INTEGER NOT NULL DEFAULT 0 CHECK(completed IN (0,1)))`,
	`CREATE TABLE stages (id INTEGER PRIMARY KEY,
		project_id INTEGER NOT NULL REFERENCES projects(id),
		name TEXT NOT NULL,
		position INTEGER NOT NULL DEFAULT 0,
		archived INTEGER NOT NULL DEFAULT 0 CHECK(archived IN (0,1)))`,
	`CREATE TABLE todos (id INTEGER PRIMARY KEY AUTOINCREMENT,
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
		created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ','now')),
		completed_at TEXT,
		archived_at TEXT,
		archive_after TEXT)`,
	`CREATE INDEX idx_todos_project_stage ON todos(project_id,stage_id,archived)`,
	`CREATE INDEX idx_todos_due ON todos(category,archived,done,due_date)`,
	`CREATE INDEX idx_todos_completed ON todos(completed_at)`,
	`CREATE INDEX idx_todos_archive_after ON todos(archive_after)`,
	`CREATE INDEX idx_todos_repeated_from ON todos(repeated_from)`,
	`CREATE INDEX idx_stages_project ON stages(project_id,position)`,
	`CREATE TABLE schema_migrations(version INTEGER PRIMARY KEY,applied_at TEXT NOT NULL)`,
	`INSERT INTO schema_migrations VALUES(1,''),(2,''),(3,''),(4,''),(5,'')`,
}

func tableShape(t *testing.T, conn *sql.DB, table string) string {
	t.Helper()
	rows, err := conn.Query(`SELECT name,type,"notnull",COALESCE(dflt_value,''),pk FROM pragma_table_info(?) ORDER BY cid`, table)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var shape []string
	for rows.Next() {
		var name, kind, dflt string
		var notNull, pk int
		if err := rows.Scan(&name, &kind, &notNull, &dflt, &pk); err != nil {
			t.Fatal(err)
		}
		shape = append(shape, fmt.Sprint(name, " ", kind, " ", notNull, " ", dflt, " ", pk))
	}
	var fks []string
	fkRows, err := conn.Query(`SELECT "table","from","to" FROM pragma_foreign_key_list(?) ORDER BY "from"`, table)
	if err != nil {
		t.Fatal(err)
	}
	defer fkRows.Close()
	for fkRows.Next() {
		var target, from, to string
		fkRows.Scan(&target, &from, &to)
		fks = append(fks, from+"->"+target+"."+to)
	}
	var indexes []string
	idxRows, err := conn.Query(`SELECT name FROM sqlite_master WHERE type='index' AND tbl_name=? AND sql IS NOT NULL ORDER BY name`, table)
	if err != nil {
		t.Fatal(err)
	}
	defer idxRows.Close()
	for idxRows.Next() {
		var name string
		idxRows.Scan(&name)
		indexes = append(indexes, name)
	}
	return strings.Join(shape, "\n") + "\nFK " + strings.Join(fks, ",") + "\nIDX " + strings.Join(indexes, ",")
}

func TestMigrateStagesToHeadings(t *testing.T) {
	dir := t.TempDir()
	conn := openTestDB(t, filepath.Join(dir, "v5.db"))
	for _, q := range version5Tables {
		if _, err := conn.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	for _, q := range []string{
		`INSERT INTO projects(id,name) VALUES(1,'Car')`,
		`INSERT INTO stages(id,project_id,name,archived) VALUES(1,1,'Licence',0),(2,1,'Dropped',1)`,
		`INSERT INTO todos(id,category,text,project_id,stage_id,done,archived) VALUES
			(1,'todo','Theory test',1,1,0,0),(2,'todo','Hidden plan',1,2,0,0),(3,'todo','Old archive',1,2,1,1),(4,'todo','Loose',1,NULL,0,0)`,
	} {
		if _, err := conn.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	now := time.Date(2026, 9, 28, 8, 0, 0, 0, time.UTC)
	if err := migrate(conn, now); err != nil {
		t.Fatal(err)
	}
	if err := migrate(conn, now); err != nil {
		t.Fatalf("second run: %v", err)
	}
	fresh := openTestDB(t, filepath.Join(dir, "fresh.db"))
	if err := migrate(fresh, now); err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"headings", "todos"} {
		if got, want := tableShape(t, conn, table), tableShape(t, fresh, table); got != want {
			t.Errorf("migrated %s differs from a new database:\n%s\nwant\n%s", table, got, want)
		}
	}
	var headings, version int
	conn.QueryRow(`SELECT count(*) FROM headings`).Scan(&headings)
	conn.QueryRow(`SELECT max(version) FROM schema_migrations`).Scan(&version)
	if headings != 1 || version != schemaVersion {
		t.Fatalf("%d headings at version %d, want 1 at %d", headings, version, schemaVersion)
	}
	type row struct {
		heading    int
		archived   bool
		archivedAt string
	}
	want := map[int]row{1: {1, false, ""}, 2: {0, true, dbTime(now)}, 3: {0, true, ""}, 4: {0, false, ""}}
	for id, w := range want {
		var got row
		conn.QueryRow(`SELECT COALESCE(heading_id,0),archived,COALESCE(archived_at,'') FROM todos WHERE id=?`, id).Scan(&got.heading, &got.archived, &got.archivedAt)
		if got != w {
			t.Errorf("task %d = %+v, want %+v", id, got, w)
		}
	}
	snapshots, _ := filepath.Glob(filepath.Join(dir, "v5.db.v5-*"))
	if len(snapshots) != 1 {
		t.Errorf("found %d copies of the version 5 database, want 1", len(snapshots))
	}
}
