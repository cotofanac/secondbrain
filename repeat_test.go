package main

import (
	"database/sql"
	"net/url"
	"strings"
	"testing"
)

func TestNextDueDate(t *testing.T) {
	cases := []struct{ due, rule, today, want string }{
		{"2026-09-27", "daily", "2026-09-27", "2026-09-28"},
		// Finishing late skips the missed occurrences instead of piling them up.
		{"2026-09-20", "daily", "2026-09-27", "2026-09-28"},
		{"2026-09-20", "weekly", "2026-09-27", "2026-10-04"},
		// Finishing early moves on from the due date, not from today.
		{"2026-09-30", "weekly", "2026-09-27", "2026-10-07"},
		// Month ends clamp without drifting to earlier days afterwards.
		{"2026-01-31", "monthly", "2026-01-31", "2026-02-28"},
		{"2026-01-31", "monthly", "2026-03-01", "2026-03-31"},
		{"2028-02-29", "yearly", "2028-02-29", "2029-02-28"},
		// Undated tasks repeat from today.
		{"", "daily", "2026-09-27", "2026-09-28"},
		{"", "monthly", "2026-09-27", "2026-10-27"},
	}
	for _, c := range cases {
		if got := nextDueDate(c.due, c.rule, c.today); got != c.want {
			t.Errorf("nextDueDate(%q, %q, %q) = %s, want %s", c.due, c.rule, c.today, got, c.want)
		}
	}
}

func TestRepeatingTaskCreatesAndRetractsNextOccurrence(t *testing.T) {
	setupWorkspaceDB(t)
	today := appNow().Format("2006-01-02")
	yesterday := appNow().AddDate(0, 0, -1).Format("2006-01-02")
	tomorrow := nextDueDate(yesterday, "daily", today)
	execSQL(t, `INSERT INTO projects(id,name) VALUES(1,'Health')`)
	execSQL(t, `INSERT INTO todos(id,category,text,due_date,project_id,repeat) VALUES(1,'todo','Stretch',?,1,'daily')`, yesterday)
	toggle := func(id string) {
		t.Helper()
		w := formRequest(t, handleToggleTodo, "/todos/toggle", url.Values{"id": {id}})
		requireOK(t, w)
		if !strings.Contains(w.Header().Get("HX-Trigger"), "sbWorkspaceChanged") {
			t.Fatalf("toggle did not ask for a workspace refresh: %v", w.Header())
		}
	}
	count := func(q string, args ...any) int {
		t.Helper()
		var n int
		if err := db.QueryRow(q, args...).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}

	toggle("1")
	var nextID, project int
	var due, rule string
	if err := db.QueryRow(`SELECT id,due_date,repeat,project_id FROM todos WHERE repeated_from=1`).Scan(&nextID, &due, &rule, &project); err != nil {
		t.Fatalf("next occurrence missing: %v", err)
	}
	if due != tomorrow || rule != "daily" || project != 1 {
		t.Fatalf("next occurrence = %s %s project %d, want %s daily project 1", due, rule, project, tomorrow)
	}
	if count(`SELECT done FROM todos WHERE id=1`) != 1 {
		t.Fatal("completed occurrence is not done")
	}

	// Reopening by mistake takes the untouched next occurrence back.
	toggle("1")
	if n := count(`SELECT count(*) FROM todos WHERE repeated_from=1`); n != 0 {
		t.Fatalf("untouched next occurrence kept after reopening: %d", n)
	}
	if count(`SELECT count(*) FROM todos WHERE id=1 AND repeat='daily' AND done=0`) != 1 {
		t.Fatal("reopened task lost its repeat")
	}

	// Once the next occurrence has been edited it stays, and the reopened
	// task stops repeating so the series is not duplicated.
	toggle("1")
	execSQL(t, `UPDATE todos SET text='Stretch 10 min',revision=revision+1 WHERE repeated_from=1`)
	toggle("1")
	if count(`SELECT count(*) FROM todos WHERE repeated_from=1`) != 1 {
		t.Fatal("edited next occurrence was removed")
	}
	if count(`SELECT count(*) FROM todos WHERE id=1 AND repeat=''`) != 1 {
		t.Fatal("reopened task still repeats alongside its next occurrence")
	}
}

func TestTaskSaveStoresRepeat(t *testing.T) {
	setupWorkspaceDB(t)
	execSQL(t, `INSERT INTO todos(id,category,text) VALUES(1,'todo','Water plants'),(2,'groceries','Milk')`)
	bad := formRequest(t, handleTaskSave, "/task/save", url.Values{"id": {"1"}, "revision": {"1"}, "text": {"Water plants"}, "repeat": {"hourly"}})
	if bad.Code != 400 {
		t.Fatalf("invalid repeat accepted: %d", bad.Code)
	}
	requireOK(t, formRequest(t, handleTaskSave, "/task/save", url.Values{"id": {"1"}, "revision": {"1"}, "text": {"Water plants"}, "repeat": {"weekly"}}))
	requireOK(t, formRequest(t, handleTaskSave, "/task/save", url.Values{"id": {"2"}, "revision": {"1"}, "text": {"Milk"}, "repeat": {"weekly"}}))
	var task, item string
	db.QueryRow(`SELECT repeat FROM todos WHERE id=1`).Scan(&task)
	db.QueryRow(`SELECT repeat FROM todos WHERE id=2`).Scan(&item)
	if task != "weekly" || item != "" {
		t.Fatalf("repeat = %q (task), %q (list item); want weekly and none", task, item)
	}
}

func TestMigrationDropsHabitData(t *testing.T) {
	conn, err := sql.Open("sqlite3", ":memory:?_foreign_keys=on")
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	conn.SetMaxOpenConns(1)
	for _, q := range []string{
		`CREATE TABLE todos(id INTEGER PRIMARY KEY,category TEXT,text TEXT,done INTEGER,archived INTEGER,due_date TEXT)`,
		`CREATE TABLE notes(id INTEGER PRIMARY KEY)`,
		`CREATE TABLE push_subscriptions(endpoint TEXT PRIMARY KEY)`,
		`CREATE TABLE settings(key TEXT PRIMARY KEY,value TEXT NOT NULL)`,
		`CREATE TABLE habits(id INTEGER PRIMARY KEY,name TEXT)`,
		`CREATE TABLE habit_logs(habit_id INTEGER NOT NULL REFERENCES habits(id) ON DELETE CASCADE,date TEXT NOT NULL)`,
		`INSERT INTO habits VALUES(1,'Run')`, `INSERT INTO habit_logs VALUES(1,'2026-09-01')`,
		`INSERT INTO settings VALUES('habit_time','20:00'),('task_time','09:00')`,
		`INSERT INTO todos VALUES(1,'todo','Keep me',0,0,'')`,
	} {
		if _, err = conn.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < 2; i++ {
		if err = migrateWorkspace(conn, date("2026-09-27")); err != nil {
			t.Fatal(err)
		}
	}
	var n int
	conn.QueryRow(`SELECT count(*) FROM sqlite_master WHERE name IN ('habits','habit_logs')`).Scan(&n)
	if n != 0 {
		t.Fatalf("habit tables remain: %d", n)
	}
	conn.QueryRow(`SELECT count(*) FROM settings`).Scan(&n)
	if n != 1 {
		t.Fatalf("settings rows = %d, want only task_time", n)
	}
	conn.QueryRow(`SELECT count(*) FROM todos`).Scan(&n)
	if n != 1 {
		t.Fatal("task rows changed")
	}
}
