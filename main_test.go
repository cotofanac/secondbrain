package main

import (
	"database/sql"
	"testing"
	"time"
)

func date(s string) time.Time {
	t, err := time.Parse("2006-01-02", s)
	if err != nil {
		panic(err)
	}
	return t
}

func TestStartOfPeriod(t *testing.T) {
	cases := []struct {
		in, period, want string
	}{
		// Monday-based weeks (ISO 8601).
		{"2026-07-05", "week", "2026-06-29"}, // Sunday -> back to Monday
		{"2026-06-29", "week", "2026-06-29"}, // Monday -> itself
		{"2026-07-01", "week", "2026-06-29"}, // Wednesday
		// Week straddling the year boundary: 2026-01-01 is a Thursday.
		{"2026-01-01", "week", "2025-12-29"},
		{"2026-07-05", "month", "2026-07-01"},
		{"2026-07-05", "year", "2026-01-01"},
		{"2026-07-05", "day", "2026-07-05"},
	}
	for _, c := range cases {
		got := startOfPeriod(date(c.in), c.period).Format("2006-01-02")
		if got != c.want {
			t.Errorf("startOfPeriod(%s, %s) = %s, want %s", c.in, c.period, got, c.want)
		}
	}
}

func TestPrevPeriodStart(t *testing.T) {
	cases := []struct {
		in, period, want string
	}{
		{"2026-06-29", "week", "2026-06-22"},
		{"2026-01-01", "month", "2025-12-01"}, // month rolls across year
		{"2026-03-01", "month", "2026-02-01"},
		{"2026-01-01", "year", "2025-01-01"},
	}
	for _, c := range cases {
		got := prevPeriodStart(date(c.in), c.period).Format("2006-01-02")
		if got != c.want {
			t.Errorf("prevPeriodStart(%s, %s) = %s, want %s", c.in, c.period, got, c.want)
		}
	}
}

func TestPeriodKey(t *testing.T) {
	// The Monday of the week containing 2026-01-01 (Thursday) is 2025-12-29,
	// but ISO numbering puts it in week 1 of 2026.
	if got := periodKey(date("2025-12-29"), "week"); got != "2026-W01" {
		t.Errorf("periodKey week = %s, want 2026-W01", got)
	}
	if got := periodKey(date("2026-07-05"), "week"); got != "2026-W27" {
		t.Errorf("periodKey week = %s, want 2026-W27", got)
	}
	if got := periodKey(date("2026-07-05"), "month"); got != "2026-07" {
		t.Errorf("periodKey month = %s, want 2026-07", got)
	}
	if got := periodKey(date("2026-07-05"), "year"); got != "2026" {
		t.Errorf("periodKey year = %s, want 2026", got)
	}
}

func TestPeriodStreak(t *testing.T) {
	now := date("2026-07-05") // a Sunday; its week is 2026-W27, month 2026-07
	target := 3

	// Helper to build a sums map from period keys.
	sums := func(pairs map[string]int) map[string]int { return pairs }

	// Current week met plus the two prior weeks met -> streak 3.
	weekly := sums(map[string]int{
		"2026-W27": 3, // current
		"2026-W26": 4, // over-achieved still counts
		"2026-W25": 3,
		// 2026-W24 missing -> breaks
	})
	if got := periodStreak(weekly, now, "week", target); got != 3 {
		t.Errorf("weekly streak = %d, want 3", got)
	}

	// Grace: current week not yet met, but prior two met -> streak 2 (run survives).
	grace := sums(map[string]int{
		"2026-W27": 1, // current below target
		"2026-W26": 3,
		"2026-W25": 5,
	})
	if got := periodStreak(grace, now, "week", target); got != 2 {
		t.Errorf("grace streak = %d, want 2", got)
	}

	// Current met but immediately-prior missed -> streak 1 (broken run).
	broken := sums(map[string]int{
		"2026-W27": 3,
		// 2026-W26 missing
		"2026-W25": 3,
	})
	if got := periodStreak(broken, now, "week", target); got != 1 {
		t.Errorf("broken streak = %d, want 1", got)
	}

	// Empty history -> 0.
	if got := periodStreak(map[string]int{}, now, "week", target); got != 0 {
		t.Errorf("empty streak = %d, want 0", got)
	}

	// Monthly: current + prior month met -> 2.
	monthly := sums(map[string]int{
		"2026-07": 3,
		"2026-06": 3,
		// 2026-05 missing
	})
	if got := periodStreak(monthly, now, "month", target); got != 2 {
		t.Errorf("monthly streak = %d, want 2", got)
	}
}

func TestParseReminderTime(t *testing.T) {
	def := reminderTime{hour: 20, min: 0, enabled: true}

	cases := []struct {
		raw     string
		want    reminderTime
		wantErr bool
	}{
		{"", def, false},                                     // empty keeps default
		{"09:30", reminderTime{9, 30, true}, false},          //
		{"00:00", reminderTime{0, 0, true}, false},           //
		{"23:59", reminderTime{23, 59, true}, false},         //
		{" 8:05 ", reminderTime{8, 5, true}, false},          // whitespace tolerated
		{"off", reminderTime{enabled: false}, false},         //
		{"OFF", reminderTime{enabled: false}, false},         // case-insensitive
		{"24:00", reminderTime{}, true},                      // hour out of range
		{"12:60", reminderTime{}, true},                      // minute out of range
		{"noon", reminderTime{}, true},                       // not HH:MM
		{"12", reminderTime{}, true},                         // missing minutes
		{"12:00:00", reminderTime{}, true},                   // too many parts
	}
	for _, c := range cases {
		got, err := parseReminderTime(c.raw, def)
		if (err != nil) != c.wantErr {
			t.Errorf("parseReminderTime(%q) err = %v, wantErr %v", c.raw, err, c.wantErr)
			continue
		}
		if !c.wantErr && got != c.want {
			t.Errorf("parseReminderTime(%q) = %+v, want %+v", c.raw, got, c.want)
		}
	}
}

func TestShouldSendReminder(t *testing.T) {
	at := func(h, m int) time.Time { return time.Date(2026, 7, 6, h, m, 0, 0, time.Local) }
	rt := reminderTime{hour: 9, min: 0, enabled: true}
	today := "2026-07-06"

	// Disabled never fires.
	if shouldSendReminder(reminderTime{enabled: false}, at(10, 0), "") {
		t.Error("disabled reminder should not fire")
	}
	// Before the configured time: no.
	if shouldSendReminder(rt, at(8, 59), "") {
		t.Error("should not fire before configured time")
	}
	// At/after the time, not yet sent today: yes.
	if !shouldSendReminder(rt, at(9, 0), "") {
		t.Error("should fire at configured time when unsent")
	}
	if !shouldSendReminder(rt, at(23, 0), "") {
		t.Error("should still fire later the same day if unsent (downtime catch-up)")
	}
	// Already sent today: no.
	if shouldSendReminder(rt, at(9, 5), today) {
		t.Error("should not fire twice on the same day")
	}
	// Sent yesterday, new day past the time: yes.
	if !shouldSendReminder(rt, at(9, 5), "2026-07-05") {
		t.Error("should fire again on a new day")
	}
}

func TestTaskReminderBody(t *testing.T) {
	cases := []struct {
		tasks []string
		want  string
	}{
		{[]string{"Pay rent"}, "Pay rent"},
		{[]string{"A", "B", "C"}, "A, B, C"},
		{[]string{"A", "B", "C", "D"}, "A, B, C and 1 more"},
		{[]string{"A", "B", "C", "D", "E"}, "A, B, C and 2 more"},
	}
	for _, c := range cases {
		if got := taskReminderBody(c.tasks); got != c.want {
			t.Errorf("taskReminderBody(%v) = %q, want %q", c.tasks, got, c.want)
		}
	}
}

// setupTestDB points the global db at a fresh in-memory SQLite instance with the
// todos table, for exercising query helpers. MaxOpenConns(1) keeps every query
// on the one connection that owns the :memory: database.
func setupTestDB(t *testing.T) {
	t.Helper()
	testDB, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	testDB.SetMaxOpenConns(1)
	if _, err := testDB.Exec(`CREATE TABLE todos (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		category TEXT NOT NULL,
		text TEXT NOT NULL,
		due_date TEXT DEFAULT '',
		done INTEGER DEFAULT 0,
		position INTEGER DEFAULT 0,
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
		archived INTEGER DEFAULT 0
	)`); err != nil {
		t.Fatalf("create table: %v", err)
	}
	db = testDB
	t.Cleanup(func() { testDB.Close() })
}

func TestDueTodayTasks(t *testing.T) {
	setupTestDB(t)
	now := date("2026-07-06")

	// text, due_date, done, archived, category
	insert := func(text, due string, done, archived int, category string) {
		if _, err := db.Exec(
			"INSERT INTO todos (category, text, due_date, done, archived, position) VALUES (?, ?, ?, ?, ?, ?)",
			category, text, due, done, archived, 0,
		); err != nil {
			t.Fatalf("insert %q: %v", text, err)
		}
	}

	insert("Overdue", "2026-07-01", 0, 0, "todo")     // included (past due)
	insert("Due today", "2026-07-06", 0, 0, "todo")   // included
	insert("Future", "2026-07-10", 0, 0, "todo")      // excluded (not yet due)
	insert("No date", "", 0, 0, "todo")               // excluded (no due date)
	insert("Done", "2026-07-01", 1, 0, "todo")        // excluded (completed)
	insert("Archived", "2026-07-01", 0, 1, "todo")    // excluded (archived)
	insert("Grocery", "2026-07-01", 0, 0, "groceries") // excluded (wrong category)

	got, err := dueTodayTasks(now)
	if err != nil {
		t.Fatalf("dueTodayTasks: %v", err)
	}
	// Ordered by due_date ASC: Overdue (07-01) then Due today (07-06).
	want := []string{"Overdue", "Due today"}
	if len(got) != len(want) {
		t.Fatalf("dueTodayTasks = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("dueTodayTasks[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}
