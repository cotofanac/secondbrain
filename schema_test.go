package main

import (
	"database/sql"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

// schemaSQL lists every table and index definition, normalized so a table
// created directly and one renamed into place compare equal.
func schemaSQL(t *testing.T, conn *sql.DB) map[string]string {
	t.Helper()
	rows, err := conn.Query(`SELECT name,sql FROM sqlite_master WHERE sql IS NOT NULL AND name!='sqlite_sequence'`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	space := regexp.MustCompile(`\s+`)
	out := map[string]string{}
	for rows.Next() {
		var name, text string
		if err = rows.Scan(&name, &text); err != nil {
			t.Fatal(err)
		}
		out[name] = space.ReplaceAllString(strings.ReplaceAll(text, `"`, ""), " ")
	}
	return out
}

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

// TestRebuildNormalizesVersion4Database upgrades a database shaped like
// production before migration 5 and checks the result matches a new one.
func TestRebuildNormalizesVersion4Database(t *testing.T) {
	dir := t.TempDir()
	previousBackups := backupDir
	backupDir = filepath.Join(dir, "backups")
	t.Cleanup(func() { backupDir = previousBackups })

	conn := openTestDB(t, filepath.Join(dir, "old.db"))
	upgradeLegacyTables(conn)
	now := time.Date(2026, 9, 28, 8, 0, 0, 0, time.UTC)
	if err := migrateWorkspace(conn, now.AddDate(0, 0, -14)); err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`INSERT INTO projects(id,name,position) VALUES(1,'Car',1)`,
		`INSERT INTO todos(id,category,text,due_date,done,archived,project_id,created_at,archived_at,completed_at,archive_after,repeat) VALUES
			(1,'todo','Open task','2026-10-01',0,0,1,'2026-05-28 21:19:29',NULL,NULL,NULL,'weekly'),
			(2,'todo','Done task','',1,0,NULL,'2026-09-20 10:00:00',NULL,'2026-09-27T19:00:00Z','2026-10-04T19:00:00Z',''),
			(3,'groceries','Milk','',1,0,NULL,'2026-09-20 10:00:00',NULL,'2026-09-27T19:00:00Z','2026-10-04T19:00:00Z',''),
			(4,'todo','Archived without a date','',0,1,NULL,'2026-06-01 09:00:00',NULL,NULL,NULL,''),
			(5,'shopping','Lamp','',0,1,NULL,'2026-06-01 09:00:00','2026-06-02 09:30:00',NULL,NULL,'')`,
		`INSERT INTO todos(id,category,text,repeated_from) VALUES(7,'todo','Next of a deleted task',99)`,
		`INSERT INTO todos(id,category,text) VALUES(8,'todo','Deleted later')`,
		`DELETE FROM todos WHERE id=8`,
		`INSERT INTO notes(id,title,content,updated_at,created_at) VALUES(1,'Quick Notes',NULL,'2026-06-01 23:02:37','2026-05-28 21:20:19')`,
		`INSERT INTO sessions(token,expires_at,last_seen) VALUES('live','2099-01-01 00:00:00','2026-09-27 19:02:35'),('fresh','2099-01-01 00:00:00','')`,
		`INSERT INTO push_subscriptions(endpoint,p256dh,auth,created_at) VALUES('https://push.example/a','p','a','2026-09-14 15:58:28')`,
		`INSERT INTO push_deliveries(endpoint,event_key,payload,next_attempt,expires_at,status) VALUES('https://push.example/a','test:1','{}','2026-09-14T15:58:30.349182018Z','2026-09-14T15:58:30.349182018Z','accepted')`,
		`INSERT INTO weekly_reviews(period_end,content,created_at) VALUES('2026-09-14T00:00:00Z','{}','2026-09-14T00:00:00Z')`,
		`INSERT INTO settings(key,value) VALUES('timezone','Europe/Bucharest'),('task_reminder_sent','2026-09-14'),('review_day','0'),('review_enabled','false')`,
	} {
		if _, err := conn.Exec(q); err != nil {
			t.Fatal(err)
		}
	}

	if err := migrate(conn, now); err != nil {
		t.Fatal(err)
	}
	if err := migrate(conn, now.Add(time.Hour)); err != nil {
		t.Fatalf("second run: %v", err)
	}
	if _, err := os.Stat(filepath.Join(backupDir, "secondbrain-before-v5.db")); err != nil {
		t.Fatalf("no copy written before the rebuild: %v", err)
	}

	fresh := openTestDB(t, filepath.Join(dir, "new.db"))
	if err := migrate(fresh, now); err != nil {
		t.Fatal(err)
	}
	got, want := schemaSQL(t, conn), schemaSQL(t, fresh)
	for name, text := range want {
		if got[name] != text {
			t.Errorf("%s differs from a new database:\n got %s\nwant %s", name, got[name], text)
		}
	}
	for name := range got {
		if _, ok := want[name]; !ok {
			t.Errorf("rebuilt database still has %s", name)
		}
	}

	type row struct{ created, completed, archived, after sql.NullString }
	todo := func(id int) row {
		var r row
		if err := conn.QueryRow(`SELECT created_at,completed_at,archived_at,archive_after FROM todos WHERE id=?`, id).Scan(&r.created, &r.completed, &r.archived, &r.after); err != nil {
			t.Fatalf("task %d: %v", id, err)
		}
		return r
	}
	if r := todo(1); r.created.String != "2026-05-28T21:19:29Z" {
		t.Errorf("created_at = %q, want RFC3339", r.created.String)
	}
	if r := todo(2); r.completed.String != "2026-09-27T19:00:00Z" || r.after.String != "2026-10-04T19:00:00Z" {
		t.Errorf("completed task lost its stamps: %+v", r)
	}
	if r := todo(3); r.completed.Valid || r.after.Valid {
		t.Errorf("list item kept task stamps: %+v", r)
	}
	if r := todo(4); r.archived.Valid {
		t.Errorf("unknown archive time was invented: %+v", r)
	}
	if r := todo(5); r.archived.String != "2026-06-02T09:30:00Z" {
		t.Errorf("archived_at = %q", r.archived.String)
	}
	var repeatedFrom sql.NullInt64
	conn.QueryRow(`SELECT repeated_from FROM todos WHERE id=7`).Scan(&repeatedFrom)
	if repeatedFrom.Valid {
		t.Error("link to a missing task survived")
	}
	var content, updated string
	conn.QueryRow(`SELECT content,updated_at FROM notes WHERE id=1`).Scan(&content, &updated)
	if content != "" || updated != "2026-06-01T23:02:37Z" {
		t.Errorf("note = %q updated %q", content, updated)
	}
	var seen, next, subscribed string
	conn.QueryRow(`SELECT last_seen FROM sessions WHERE token='live'`).Scan(&seen)
	conn.QueryRow(`SELECT next_attempt FROM push_deliveries`).Scan(&next)
	conn.QueryRow(`SELECT created_at FROM push_subscriptions`).Scan(&subscribed)
	if seen != "2026-09-27T19:02:35Z" || next != "2026-09-14T15:58:30Z" || subscribed != "2026-09-14T15:58:28Z" {
		t.Errorf("times not normalized: session %q, delivery %q, subscription %q", seen, next, subscribed)
	}
	var retired, seq int
	conn.QueryRow(`SELECT count(*) FROM settings WHERE key IN (` + retiredSettings + `)`).Scan(&retired)
	if retired != 0 {
		t.Errorf("%d retired settings remain", retired)
	}
	// Task 8 was deleted before the rebuild; its id must not be handed out again.
	if _, err := conn.Exec(`INSERT INTO todos(category,text) VALUES('todo','After rebuild')`); err != nil {
		t.Fatal(err)
	}
	conn.QueryRow(`SELECT max(id) FROM todos`).Scan(&seq)
	if seq != 9 {
		t.Errorf("new task id = %d, want 9", seq)
	}
	if _, err := conn.Exec(`INSERT INTO todos(category,text,repeat) VALUES('todo','Bad','hourly')`); err == nil {
		t.Error("invalid repeat accepted")
	}
	if _, err := conn.Exec(`INSERT INTO todos(category,text,project_id) VALUES('todo','Orphan',42)`); err == nil {
		t.Error("foreign keys are not enforced after the rebuild")
	}
}
