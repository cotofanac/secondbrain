package main

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"html/template"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sort"
	"strings"
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
		{"", def, false}, // empty keeps default
		{"09:30", reminderTime{9, 30, true}, false},  //
		{"00:00", reminderTime{0, 0, true}, false},   //
		{"23:59", reminderTime{23, 59, true}, false}, //
		{" 8:05 ", reminderTime{8, 5, true}, false},  // whitespace tolerated
		{"off", reminderTime{enabled: false}, false}, //
		{"OFF", reminderTime{enabled: false}, false}, // case-insensitive
		{"24:00", reminderTime{}, true},              // hour out of range
		{"12:60", reminderTime{}, true},              // minute out of range
		{"noon", reminderTime{}, true},               // not HH:MM
		{"12", reminderTime{}, true},                 // missing minutes
		{"12:00:00", reminderTime{}, true},           // too many parts
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
		archived INTEGER DEFAULT 0,
		archived_at DATETIME DEFAULT NULL
	)`); err != nil {
		t.Fatalf("create table: %v", err)
	}
	db = testDB
	for _, statement := range []string{`CREATE TABLE IF NOT EXISTS notes(id INTEGER PRIMARY KEY,title TEXT,archived INTEGER DEFAULT 0)`, `CREATE TABLE IF NOT EXISTS push_subscriptions(endpoint TEXT PRIMARY KEY,p256dh TEXT,auth TEXT)`} {
		if _, err := testDB.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	if err := migrateWorkspace(testDB, time.Now()); err != nil {
		t.Fatal(err)
	}
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

	insert("Overdue", "2026-07-01", 0, 0, "todo")      // included (past due)
	insert("Due today", "2026-07-06", 0, 0, "todo")    // included
	insert("Future", "2026-07-10", 0, 0, "todo")       // excluded (not yet due)
	insert("No date", "", 0, 0, "todo")                // excluded (no due date)
	insert("Done", "2026-07-01", 1, 0, "todo")         // excluded (completed)
	insert("Archived", "2026-07-01", 0, 1, "todo")     // excluded (archived)
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

// setupTestNotesDB points the global db at a fresh in-memory SQLite instance
// holding just the notes table, for exercising the save handler.
func setupTestNotesDB(t *testing.T) {
	t.Helper()
	testDB, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	testDB.SetMaxOpenConns(1)
	if _, err := testDB.Exec(`CREATE TABLE notes (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		title TEXT NOT NULL UNIQUE,
		content TEXT DEFAULT '',
		updated_at DATETIME DEFAULT CURRENT_TIMESTAMP,
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
		archived INTEGER DEFAULT 0
	)`); err != nil {
		t.Fatalf("create table: %v", err)
	}
	db = testDB
	t.Cleanup(func() { testDB.Close() })
}

func saveNoteRequest(t *testing.T, id, content, updatedAt string) *httptest.ResponseRecorder {
	t.Helper()
	form := url.Values{}
	form.Set("id", id)
	form.Set("content", content)
	if updatedAt != "" {
		form.Set("updated_at", updatedAt)
	}
	req := httptest.NewRequest(http.MethodPost, "/notes/save", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	handleSaveNote(rec, req)
	return rec
}

func noteField(t *testing.T, id int, col string) string {
	t.Helper()
	var v string
	if err := db.QueryRow("SELECT "+col+" FROM notes WHERE id = ?", id).Scan(&v); err != nil {
		t.Fatalf("read %s: %v", col, err)
	}
	return v
}

// TestSaveNoteOptimisticConcurrency covers the conditional-write path. The
// timestamp the client echoes back is RFC3339 ("...T...Z") because database/sql
// renders a scanned DATETIME that way, while the column stores SQLite's
// "... ..." form — so a same-token save must still match. A regression here
// (comparing the two formats raw) makes every save report a false conflict.
func TestSaveNoteOptimisticConcurrency(t *testing.T) {
	setupTestNotesDB(t)
	if _, err := db.Exec(
		"INSERT INTO notes (id, title, content, updated_at) VALUES (1, 'n', 'original', '2026-07-01 10:00:00')",
	); err != nil {
		t.Fatalf("seed: %v", err)
	}

	// The token as the page would have rendered it.
	token := noteField(t, 1, "updated_at")
	if !strings.Contains(token, "T") {
		t.Fatalf("expected scanned timestamp in RFC3339 form, got %q", token)
	}

	// Current token -> the write lands.
	if rec := saveNoteRequest(t, "1", "first edit", token); rec.Code != http.StatusOK {
		t.Fatalf("save with current token = %d, want 200 (body %q)", rec.Code, rec.Body.String())
	}
	if got := noteField(t, 1, "content"); got != "first edit" {
		t.Errorf("content = %q, want %q", got, "first edit")
	}

	// The same token is now stale (the row advanced to CURRENT_TIMESTAMP), so a
	// second device holding it must be refused rather than clobbering the note.
	rec := saveNoteRequest(t, "1", "clobber", token)
	if rec.Code != http.StatusConflict {
		t.Fatalf("save with stale token = %d, want 409", rec.Code)
	}
	if got := noteField(t, 1, "content"); got != "first edit" {
		t.Errorf("content after refused save = %q, want it unchanged at %q", got, "first edit")
	}

	// No token at all keeps the old unconditional behaviour.
	if rec := saveNoteRequest(t, "1", "forced", ""); rec.Code != http.StatusOK {
		t.Fatalf("save without token = %d, want 200", rec.Code)
	}
	if got := noteField(t, 1, "content"); got != "forced" {
		t.Errorf("content = %q, want %q", got, "forced")
	}
}

func TestIsPublicIP(t *testing.T) {
	cases := []struct {
		ip   string
		want bool
	}{
		{"8.8.8.8", true},
		{"1.1.1.1", true},
		{"2606:4700:4700::1111", true},
		{"127.0.0.1", false},       // loopback
		{"::1", false},             // loopback v6
		{"10.0.0.5", false},        // private
		{"172.16.0.1", false},      // private
		{"192.168.1.1", false},     // private
		{"169.254.169.254", false}, // link-local metadata service
		{"0.0.0.0", false},         // unspecified
		{"100.64.0.1", false},      // CGNAT
		{"224.0.0.1", false},       // multicast
		{"240.0.0.1", false},       // reserved
		{"192.0.0.1", false},       // IETF protocol assignments
		{"fd00::1", false},         // unique local
		{"fe80::1", false},         // link-local v6
	}
	for _, c := range cases {
		ip := net.ParseIP(c.ip)
		if ip == nil {
			t.Fatalf("bad test IP %q", c.ip)
		}
		if got := isPublicIP(ip); got != c.want {
			t.Errorf("isPublicIP(%s) = %v, want %v", c.ip, got, c.want)
		}
	}
}

// TestValidatePushEndpointRejects covers the cases decided without a DNS
// lookup, so the test stays offline.
func TestValidatePushEndpointRejects(t *testing.T) {
	bad := []string{
		"",                               // no scheme
		"https://",                       // no host
		"https://127.0.0.1/x",            // loopback literal
		"https://169.254.169.254/latest", // metadata service
		"https://10.1.2.3/x",             // private literal
		"https://[::1]/x",                // loopback v6 literal
		// Public literal IPs, so these isolate the scheme check: nothing else
		// in the function can reject them and no DNS lookup is involved.
		"http://8.8.8.8/x", // plaintext
		"ftp://8.8.8.8/x",  // wrong scheme
		"//8.8.8.8/x",      // scheme-relative
	}
	for _, e := range bad {
		if err := validatePushEndpoint(e); err == nil {
			t.Errorf("validatePushEndpoint(%q) = nil, want an error", e)
		}
	}

	// A public literal needs no DNS and must pass.
	if err := validatePushEndpoint("https://8.8.8.8/push/abc"); err != nil {
		t.Errorf("validatePushEndpoint(public literal) = %v, want nil", err)
	}
}

// setupTestTemplates parses the real templates with stub funcs, so handlers that
// re-render a list at the end of a write can run under test.
func setupTestTemplates(t *testing.T) {
	t.Helper()
	prev := templates
	templates = template.Must(template.New("").Funcs(template.FuncMap{
		"formatDate":      func(string) string { return "" },
		"formatUpdated":   func(string) string { return "" },
		"isOverdue":       func(string) bool { return false },
		"todoArchiveHint": func(string, string) string { return "" },
		"todoArchivedAgo": func(string) string { return "" },
		"assetVersion":    func() string { return "test" },
	}).ParseFS(templateFiles, "templates/*.html"))
	t.Cleanup(func() { templates = prev })
}

func addTodoRequest(t *testing.T, category, text string) *httptest.ResponseRecorder {
	t.Helper()
	form := url.Values{}
	form.Set("category", category)
	form.Set("text", text)
	req := httptest.NewRequest(http.MethodPost, "/todos/add", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	handleAddTodo(rec, req)
	return rec
}

func todoCount(t *testing.T, category string) int {
	t.Helper()
	var n int
	if err := db.QueryRow("SELECT COUNT(*) FROM todos WHERE category = ?", category).Scan(&n); err != nil {
		t.Fatalf("count %s: %v", category, err)
	}
	return n
}

// TestAddTodoReusesExistingListItem pins the reuse contract that the add path's
// transaction protects: a re-added grocery revives its existing row instead of
// stacking a duplicate, since todos has no UNIQUE constraint to catch one.
func TestAddTodoReusesExistingListItem(t *testing.T) {
	setupTestDB(t)
	setupTestTemplates(t)

	// A grocery that was checked off and archived.
	if _, err := db.Exec(
		"INSERT INTO todos (category, text, done, archived) VALUES ('groceries','Potatoes',1,1)",
	); err != nil {
		t.Fatalf("seed: %v", err)
	}

	// Re-adding with different casing revives that row rather than adding one.
	if rec := addTodoRequest(t, "groceries", "potatoes"); rec.Code != http.StatusOK {
		t.Fatalf("add = %d, want 200 (body %q)", rec.Code, rec.Body.String())
	}
	if n := todoCount(t, "groceries"); n != 1 {
		t.Fatalf("grocery rows = %d, want 1 (reuse, not duplicate)", n)
	}
	var done, archived int
	if err := db.QueryRow("SELECT done, archived FROM todos WHERE category='groceries'").
		Scan(&done, &archived); err != nil {
		t.Fatalf("read flags: %v", err)
	}
	if done != 0 || archived != 0 {
		t.Errorf("done=%d archived=%d, want 0/0 (revived row should be active and unchecked)", done, archived)
	}

	// A genuinely new item still inserts.
	if rec := addTodoRequest(t, "groceries", "Carrots"); rec.Code != http.StatusOK {
		t.Fatalf("add new = %d, want 200", rec.Code)
	}
	if n := todoCount(t, "groceries"); n != 2 {
		t.Errorf("grocery rows = %d, want 2", n)
	}

	// Tasks are not a reusable checklist, so identical text inserts twice.
	addTodoRequest(t, "todo", "Same")
	addTodoRequest(t, "todo", "Same")
	if n := todoCount(t, "todo"); n != 2 {
		t.Errorf("todo rows = %d, want 2 (tasks are not de-duplicated)", n)
	}
}

// TestArchiveStaleTodosUsesSuppliedDate pins the local-vs-UTC boundary. due_date
// holds the calendar date the user picked, so the cutoff has to come from the
// caller's local clock, not SQLite's UTC date('now').
//
// The dates here sit years in the past precisely so the two differ: if the
// query ever goes back to date('now'), every one of these due dates is in the
// past relative to the real UTC today and the "not yet due" rows archive too.
func TestArchiveStaleTodosUsesSuppliedDate(t *testing.T) {
	setupTestDB(t)
	now := date("2026-09-13")
	for _, row := range []struct {
		text  string
		done  int
		after string
	}{{"unfinished", 0, "2020-01-01T00:00:00Z"}, {"complete old", 1, "2026-09-12T23:59:59Z"}, {"complete later", 1, "2026-09-14T00:00:00Z"}} {
		if _, err := db.Exec(`INSERT INTO todos(category,text,done,archive_after) VALUES('todo',?,?,?)`, row.text, row.done, row.after); err != nil {
			t.Fatal(err)
		}
	}
	n, err := archiveStaleTodos(now)
	if err != nil || n != 1 {
		t.Fatalf("archived=%d err=%v", n, err)
	}
	var name string
	if err := db.QueryRow(`SELECT text FROM todos WHERE archived=1`).Scan(&name); err != nil || name != "complete old" {
		t.Fatalf("archived %q err=%v", name, err)
	}
}

func TestLoadSuggestionsIsBoundedAndRecencyPicked(t *testing.T) {
	setupTestDB(t)

	// More entries than the cap, oldest first, so the newest maxSuggestions
	// are the ones that should survive.
	total := maxSuggestions + 50
	for i := 0; i < total; i++ {
		if _, err := db.Exec(
			"INSERT INTO todos (category, text, created_at) VALUES ('groceries', ?, ?)",
			fmt.Sprintf("item%03d", i),
			fmt.Sprintf("2020-01-01 00:%02d:%02d", i/60, i%60),
		); err != nil {
			t.Fatalf("insert %d: %v", i, err)
		}
	}
	// A duplicate must collapse rather than consume a slot twice.
	if _, err := db.Exec(
		"INSERT INTO todos (category, text, created_at) VALUES ('groceries', 'item249', '2020-01-01 23:59:59')",
	); err != nil {
		t.Fatalf("insert duplicate: %v", err)
	}

	got := loadSuggestions("groceries")
	if len(got) != maxSuggestions {
		t.Fatalf("len(suggestions) = %d, want %d", len(got), maxSuggestions)
	}
	// Recency decides which survive: item000..item049 are the oldest and drop.
	if got[0] != "item050" {
		t.Errorf("first suggestion = %q, want %q (oldest entries should be dropped)", got[0], "item050")
	}
	if got[len(got)-1] != "item249" {
		t.Errorf("last suggestion = %q, want %q", got[len(got)-1], "item249")
	}
	// Output is sorted for display.
	if !sort.StringsAreSorted(got) {
		t.Error("suggestions are not sorted")
	}
	// Other categories are unaffected.
	if n := len(loadSuggestions("shopping")); n != 0 {
		t.Errorf("shopping suggestions = %d, want 0", n)
	}
}

// postForm drives a handler directly with a form-encoded POST.
func postForm(t *testing.T, h http.HandlerFunc, path string, vals url.Values) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(vals.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	h(rec, req)
	return rec
}

// triggerDetail pulls the detail object for a named event out of an HX-Trigger
// header, so tests assert on the contract the client actually consumes.
func triggerDetail(t *testing.T, rec *httptest.ResponseRecorder, event string) map[string]any {
	t.Helper()
	raw := rec.Header().Get("HX-Trigger")
	if raw == "" {
		t.Fatalf("no HX-Trigger header set")
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(raw), &payload); err != nil {
		t.Fatalf("HX-Trigger %q is not valid JSON: %v", raw, err)
	}
	detail, ok := payload[event].(map[string]any)
	if !ok {
		t.Fatalf("HX-Trigger %q has no %q object", raw, event)
	}
	return detail
}

func setupTestHabitsDB(t *testing.T) {
	t.Helper()
	testDB, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	testDB.SetMaxOpenConns(1)
	for _, stmt := range []string{
		`CREATE TABLE habits (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			name TEXT NOT NULL UNIQUE,
			period TEXT NOT NULL DEFAULT 'day',
			target INTEGER NOT NULL DEFAULT 1,
			archived INTEGER DEFAULT 0,
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP
		)`,
		`CREATE TABLE habit_logs (
			habit_id INTEGER NOT NULL,
			date TEXT NOT NULL,
			count INTEGER NOT NULL DEFAULT 1,
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
			PRIMARY KEY (habit_id, date)
		)`,
	} {
		if _, err := testDB.Exec(stmt); err != nil {
			t.Fatalf("create table: %v", err)
		}
	}
	db = testDB
	t.Cleanup(func() { testDB.Close() })
}

// TestArchiveTodoOffersUndo covers the whole one-tap-archive reversal: the
// server advertises the undo, and the undo restores without dumping the user
// into the archive view they were never in.
func TestArchiveTodoOffersUndo(t *testing.T) {
	setupTestDB(t)
	setupTestTemplates(t)
	if _, err := db.Exec("INSERT INTO todos (id, category, text) VALUES (1, 'todo', 'Buy milk')"); err != nil {
		t.Fatalf("seed: %v", err)
	}

	rec := postForm(t, handleDeleteTodo, "/todos/delete", url.Values{"id": {"1"}})
	if rec.Code != http.StatusOK {
		t.Fatalf("archive = %d, want 200", rec.Code)
	}
	detail := triggerDetail(t, rec, "sbUndo")
	if detail["kind"] != "todo" {
		t.Errorf("undo kind = %v, want %q", detail["kind"], "todo")
	}
	if id, _ := detail["id"].(float64); int(id) != 1 {
		t.Errorf("undo id = %v, want 1", detail["id"])
	}

	var archived int
	if err := db.QueryRow("SELECT archived FROM todos WHERE id = 1").Scan(&archived); err != nil {
		t.Fatalf("read archived: %v", err)
	}
	if archived != 1 {
		t.Fatalf("archived = %d, want 1", archived)
	}

	// Undo: restores the row AND renders the live list, not the archive.
	rec = postForm(t, handleRestoreTodo, "/todos/restore",
		url.Values{"id": {"1"}, "return": {"list"}})
	if rec.Code != http.StatusOK {
		t.Fatalf("undo = %d, want 200", rec.Code)
	}
	if err := db.QueryRow("SELECT archived FROM todos WHERE id = 1").Scan(&archived); err != nil {
		t.Fatalf("read archived: %v", err)
	}
	if archived != 0 {
		t.Errorf("archived after undo = %d, want 0", archived)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "Buy milk") {
		t.Error("undo response should contain the restored item")
	}
	if strings.Contains(body, "archive-header") {
		t.Error("undo rendered the archive view; return=list should render the live list")
	}

	// Without return=list, restoring still re-renders the archive as before.
	postForm(t, handleDeleteTodo, "/todos/delete", url.Values{"id": {"1"}})
	rec = postForm(t, handleRestoreTodo, "/todos/restore", url.Values{"id": {"1"}})
	if !strings.Contains(rec.Body.String(), "archive-header") {
		t.Error("restore without return=list should render the archive view")
	}
}

// TestCreateNoteRevivesArchivedTitle: title is UNIQUE across archived rows, so
// creating a name whose only holder is archived must revive it rather than
// dead-ending on an error about something the user cannot see.
func TestCreateNoteRevivesArchivedTitle(t *testing.T) {
	setupTestNotesDB(t)
	setupTestTemplates(t)
	if _, err := db.Exec(
		"INSERT INTO notes (id, title, content, archived) VALUES (1, 'Ideas', 'kept text', 1)",
	); err != nil {
		t.Fatalf("seed: %v", err)
	}

	rec := postForm(t, handleCreateNote, "/notes/create", url.Values{"title": {"Ideas"}})
	if rec.Code != http.StatusOK {
		t.Fatalf("create = %d, want 200 (body %q)", rec.Code, rec.Body.String())
	}
	if d := triggerDetail(t, rec, "sbNotice"); d["message"] == "" {
		t.Error("expected an sbNotice explaining the revival")
	}

	var archived, count int
	var content string
	if err := db.QueryRow("SELECT archived, content FROM notes WHERE id = 1").
		Scan(&archived, &content); err != nil {
		t.Fatalf("read note: %v", err)
	}
	if archived != 0 {
		t.Errorf("archived = %d, want 0 (note should have been revived)", archived)
	}
	if content != "kept text" {
		t.Errorf("content = %q, want the archived note's text preserved", content)
	}
	if err := db.QueryRow("SELECT COUNT(*) FROM notes").Scan(&count); err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 1 {
		t.Errorf("note count = %d, want 1 (revive, not duplicate)", count)
	}

	// Now that it is active, the same name is a real conflict again.
	rec = postForm(t, handleCreateNote, "/notes/create", url.Values{"title": {"Ideas"}})
	if rec.Code != http.StatusConflict {
		t.Errorf("duplicate active title = %d, want 409", rec.Code)
	}
}

func TestAddHabitRevivesArchivedName(t *testing.T) {
	setupTestHabitsDB(t)
	setupTestTemplates(t)
	if _, err := db.Exec(
		"INSERT INTO habits (id, name, period, target, archived) VALUES (1, 'Run', 'day', 1, 1)",
	); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if _, err := db.Exec(
		"INSERT INTO habit_logs (habit_id, date, count) VALUES (1, '2026-07-01', 1)",
	); err != nil {
		t.Fatalf("seed log: %v", err)
	}

	rec := postForm(t, handleAddHabit, "/habits/add",
		url.Values{"name": {"Run"}, "period": {"week"}, "target": {"3"}})
	if rec.Code != http.StatusOK {
		t.Fatalf("add = %d, want 200 (body %q)", rec.Code, rec.Body.String())
	}
	if d := triggerDetail(t, rec, "sbNotice"); d["message"] == "" {
		t.Error("expected an sbNotice explaining the revival")
	}

	var archived, target, logs, count int
	var period string
	if err := db.QueryRow("SELECT archived, period, target FROM habits WHERE id = 1").
		Scan(&archived, &period, &target); err != nil {
		t.Fatalf("read habit: %v", err)
	}
	if archived != 0 {
		t.Errorf("archived = %d, want 0", archived)
	}
	// Reviving adopts the newly chosen schedule rather than silently keeping the old one.
	if period != "week" || target != 3 {
		t.Errorf("period/target = %s/%d, want week/3", period, target)
	}
	if err := db.QueryRow("SELECT COUNT(*) FROM habit_logs WHERE habit_id = 1").Scan(&logs); err != nil {
		t.Fatalf("count logs: %v", err)
	}
	if logs != 1 {
		t.Errorf("habit_logs = %d, want 1 (history must survive the revival)", logs)
	}
	if err := db.QueryRow("SELECT COUNT(*) FROM habits").Scan(&count); err != nil {
		t.Fatalf("count habits: %v", err)
	}
	if count != 1 {
		t.Errorf("habit count = %d, want 1", count)
	}

	// Active duplicate is still a conflict.
	rec = postForm(t, handleAddHabit, "/habits/add", url.Values{"name": {"Run"}, "period": {"day"}})
	if rec.Code != http.StatusConflict {
		t.Errorf("duplicate active name = %d, want 409", rec.Code)
	}
}
