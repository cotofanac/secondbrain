package main

import (
	"database/sql"
	"encoding/json"
	"html"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	webpush "github.com/SherClockHolmes/webpush-go"
)

func setupWorkspaceDB(t *testing.T) {
	t.Helper()
	previous := db
	zone := calendarZone.Load()
	t.Setenv("DATA_DIR", t.TempDir())
	initDB()
	initScheduleSettings()
	setupTestTemplates(t)
	t.Cleanup(func() { db.Close(); db = previous; calendarZone.Store(zone) })
}
func execSQL(t *testing.T, q string, args ...any) {
	t.Helper()
	if _, err := db.Exec(q, args...); err != nil {
		t.Fatal(err)
	}
}
func formRequest(t *testing.T, h http.HandlerFunc, path string, values url.Values) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest("POST", path, strings.NewReader(values.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.Header.Set("HX-Target", "todo-items")
	w := httptest.NewRecorder()
	h(w, r)
	return w
}
func requireOK(t *testing.T, w *httptest.ResponseRecorder) {
	t.Helper()
	if w.Code != 200 {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}
}

func TestMigrationPreservesLegacyData(t *testing.T) {
	conn, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	conn.SetMaxOpenConns(1)
	for _, q := range []string{`CREATE TABLE todos(id INTEGER PRIMARY KEY,category TEXT,text TEXT,done INTEGER,archived INTEGER,due_date TEXT)`, `CREATE TABLE notes(id INTEGER PRIMARY KEY)`, `CREATE TABLE push_subscriptions(endpoint TEXT PRIMARY KEY)`, `INSERT INTO todos VALUES(1,'todo','old open',0,0,'2020-01-01'),(2,'todo','old complete',1,0,''),(3,'todo','archived',0,1,'')`} {
		if _, err = conn.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	now := date("2026-09-13")
	if err = migrateWorkspace(conn, now); err != nil {
		t.Fatal(err)
	}
	if err = migrateWorkspace(conn, now.AddDate(0, 0, 1)); err != nil {
		t.Fatal(err)
	}
	var count int
	conn.QueryRow(`SELECT count(*) FROM todos`).Scan(&count)
	if count != 3 {
		t.Fatalf("lost rows: %d", count)
	}
	var project, stage sql.NullInt64
	var completed sql.NullString
	var after string
	if err = conn.QueryRow(`SELECT project_id,stage_id,completed_at,archive_after FROM todos WHERE id=2`).Scan(&project, &stage, &completed, &after); err != nil {
		t.Fatal(err)
	}
	if project.Valid || stage.Valid || completed.Valid || after != "2026-09-20T00:00:00Z" {
		t.Fatalf("migration fabricated history or altered baseline: %v %v %v %s", project, stage, completed, after)
	}
	var archived int
	conn.QueryRow(`SELECT archived FROM todos WHERE id=3`).Scan(&archived)
	if archived != 1 {
		t.Fatal("archived task restored")
	}
	var todoRevision, noteRevision, migrationCount int
	if err = conn.QueryRow(`SELECT revision FROM todos WHERE id=1`).Scan(&todoRevision); err != nil {
		t.Fatal(err)
	}
	if _, err = conn.Exec(`INSERT INTO notes(id) VALUES(1)`); err != nil {
		t.Fatal(err)
	}
	if err = conn.QueryRow(`SELECT revision FROM notes WHERE id=1`).Scan(&noteRevision); err != nil {
		t.Fatal(err)
	}
	if err = conn.QueryRow(`SELECT count(*) FROM schema_migrations WHERE version IN (1,2)`).Scan(&migrationCount); err != nil {
		t.Fatal(err)
	}
	if todoRevision != 1 || noteRevision != 1 || migrationCount != 2 {
		t.Fatalf("revision migration not repeatable: todo=%d note=%d migrations=%d", todoRevision, noteRevision, migrationCount)
	}
}

func TestTaskTitleRevisionConflict(t *testing.T) {
	setupWorkspaceDB(t)
	execSQL(t, `INSERT INTO todos(id,category,text,done,archived,due_date) VALUES(1,'todo','Original',0,0,'')`)
	first := formRequest(t, handleEditTodo, "/todos/edit", url.Values{"id": {"1"}, "text": {"First client"}, "revision": {"1"}})
	requireOK(t, first)
	var saved map[string]any
	if err := json.Unmarshal(first.Body.Bytes(), &saved); err != nil || saved["revision"] != float64(2) {
		t.Fatalf("unexpected save response %q: %v", first.Body.String(), err)
	}
	stale := formRequest(t, handleEditTodo, "/todos/edit", url.Values{"id": {"1"}, "text": {"Second client"}, "revision": {"1"}})
	if stale.Code != http.StatusConflict {
		t.Fatalf("stale task save = %d, want 409: %s", stale.Code, stale.Body.String())
	}
	var conflict struct {
		Task Todo `json:"task"`
	}
	if err := json.Unmarshal(stale.Body.Bytes(), &conflict); err != nil {
		t.Fatal(err)
	}
	if conflict.Task.Text != "First client" || conflict.Task.Revision != 2 {
		t.Fatalf("conflict returned %#v", conflict.Task)
	}
}

func TestProjectTasksMembershipAndLifecycle(t *testing.T) {
	setupWorkspaceDB(t)
	execSQL(t, `INSERT INTO projects(id,name) VALUES(1,'Car'),(2,'Home')`)
	execSQL(t, `INSERT INTO stages(id,project_id,name) VALUES(1,1,'Licence'),(2,2,'Kitchen')`)
	bad := formRequest(t, handleAddTodo, "/todos/add", url.Values{"category": {"todo"}, "text": {"Bad membership"}, "project_id": {"1"}, "stage_id": {"2"}})
	if bad.Code != 400 {
		t.Fatalf("accepted foreign stage: %d", bad.Code)
	}
	requireOK(t, formRequest(t, handleAddTodo, "/todos/add", url.Values{"category": {"todo"}, "text": {"Book lessons"}, "project_id": {"1"}, "stage_id": {"1"}}))
	var id int
	db.QueryRow(`SELECT id FROM todos WHERE text='Book lessons'`).Scan(&id)
	requireOK(t, formRequest(t, handleToggleTodo, "/todos/toggle", url.Values{"id": {"1"}}))
	requireOK(t, formRequest(t, handleTaskSave, "/task/save", url.Values{"id": {"1"}, "revision": {"1"}, "text": {"Book lessons"}, "due_date": {"2026-10-01"}, "project_id": {"2"}, "stage_id": {"2"}}))
	var done, project, stage int
	var completed string
	db.QueryRow(`SELECT done,project_id,stage_id,completed_at FROM todos WHERE id=?`, id).Scan(&done, &project, &stage, &completed)
	if done != 1 || project != 2 || stage != 2 || completed == "" {
		t.Fatalf("move lost task state %d %d %d %q", done, project, stage, completed)
	}
	requireOK(t, formRequest(t, handleTaskSave, "/task/save", url.Values{"id": {"1"}, "revision": {"2"}, "text": {"Standalone again"}, "due_date": {""}, "project_id": {"0"}, "stage_id": {"0"}}))
	var standalone bool
	db.QueryRow(`SELECT project_id IS NULL AND stage_id IS NULL FROM todos WHERE id=1`).Scan(&standalone)
	if !standalone {
		t.Fatal("cannot unassign task")
	}
}

func TestProjectArchiveCompletionAndCounts(t *testing.T) {
	setupWorkspaceDB(t)
	execSQL(t, `INSERT INTO projects(id,name) VALUES(1,'Car')`)
	execSQL(t, `INSERT INTO stages(id,project_id,name) VALUES(1,1,'Licence')`)
	execSQL(t, `INSERT INTO todos(id,category,text,project_id,stage_id,done,archived,due_date) VALUES(1,'todo','Complete',1,1,1,1,''),(2,'todo','Pending',1,1,0,0,'2020-01-01')`)
	ws, err := loadWorkspace()
	if err != nil {
		t.Fatal(err)
	}
	if ws.Projects[0].Group.Completed != 1 || ws.Projects[0].Group.Total != 2 {
		t.Fatal("archived completion excluded from progress")
	}
	w := formRequest(t, handleProjectAction, "/projects/action", url.Values{"id": {"1"}, "action": {"complete"}})
	if w.Code != 409 {
		t.Fatal("completed project with pending task")
	}
	w = formRequest(t, handleProjectAction, "/projects/action", url.Values{"id": {"1"}, "action": {"archive"}})
	requireOK(t, w)
	if !strings.Contains(w.Header().Get("HX-Trigger"), "sbUndo") {
		t.Fatal("missing archive undo")
	}
	tasks, err := dueTodayTasks(date("2026-09-13"))
	if err != nil || len(tasks) != 0 {
		t.Fatalf("archived project leaked into reminders %v %v", tasks, err)
	}
	requireOK(t, formRequest(t, handleProjectAction, "/projects/action", url.Values{"id": {"1"}, "action": {"restore"}}))
	tasks, err = dueTodayTasks(date("2026-09-13"))
	if err != nil || len(tasks) != 1 {
		t.Fatal("restoring project lost pending task")
	}
	requireOK(t, formRequest(t, handleStageAction, "/stages/action", url.Values{"id": {"1"}, "action": {"archive"}}))
	tasks, _ = dueTodayTasks(date("2026-09-13"))
	if len(tasks) != 0 {
		t.Fatal("archived stage leaked into reminders")
	}
	requireOK(t, formRequest(t, handleStageAction, "/stages/action", url.Values{"id": {"1"}, "action": {"restore"}}))
	requireOK(t, formRequest(t, handleToggleTodo, "/todos/toggle", url.Values{"id": {"2"}}))
	requireOK(t, formRequest(t, handleProjectAction, "/projects/action", url.Values{"id": {"1"}, "action": {"complete"}}))
	requireOK(t, formRequest(t, handleProjectAction, "/projects/action", url.Values{"id": {"1"}, "action": {"reopen"}}))
}

func TestProjectNoteLinksAndOrdering(t *testing.T) {
	setupWorkspaceDB(t)
	execSQL(t, `INSERT INTO projects(id,name,position) VALUES(1,'Car',0),(2,'Home',1),(3,'Trip',2)`)
	requireOK(t, formRequest(t, handleProjectNotes, "/projects/notes", url.Values{"project_id": {"1"}, "note_id": {"1"}, "action": {"link"}}))
	requireOK(t, formRequest(t, handleProjectNotes, "/projects/notes", url.Values{"project_id": {"2"}, "note_id": {"1"}, "action": {"link"}}))
	requireOK(t, formRequest(t, handleProjectNotes, "/projects/notes", url.Values{"project_id": {"1"}, "note_id": {"1"}, "action": {"unlink"}}))
	var n int
	db.QueryRow(`SELECT count(*) FROM notes WHERE id=1`).Scan(&n)
	if n != 1 {
		t.Fatal("unlink deleted original note")
	}
	db.QueryRow(`SELECT count(*) FROM project_notes WHERE project_id=2`).Scan(&n)
	if n != 1 {
		t.Fatal("unlink affected other project")
	}
	requireOK(t, formRequest(t, handleProjectAction, "/projects/action", url.Values{"id": {"3"}, "action": {"up"}}))
	ws, _ := loadWorkspace()
	if ws.Projects[1].ID != 3 {
		t.Fatal("ordering failed")
	}
}

func TestWorkspaceTemplatesAndSearch(t *testing.T) {
	setupWorkspaceDB(t)
	execSQL(t, `INSERT INTO projects(id,name) VALUES(1,'Car')`)
	execSQL(t, `INSERT INTO stages(id,project_id,name) VALUES(1,1,'Car research')`)
	execSQL(t, `INSERT INTO todos(id,category,text,project_id,stage_id) VALUES(1,'todo','Car shortlist',1,1)`)
	for _, c := range []struct {
		path string
		h    http.HandlerFunc
	}{{"/", handleIndex}, {"/workspace", handleWorkspace}, {"/task/detail?id=1", handleTaskDetail}, {"/projects/detail?id=1", handleProjectDetail}, {"/settings", handleSettings}, {"/review", handleReview}, {"/search?q=Car", handleSearch}} {
		w := httptest.NewRecorder()
		c.h(w, httptest.NewRequest("GET", c.path, nil))
		requireOK(t, w)
		if w.Body.Len() == 0 {
			t.Fatal("empty template", c.path)
		}
	}
	w := httptest.NewRecorder()
	handleSearch(w, httptest.NewRequest("GET", "/search?q=Car", nil))
	for _, expected := range []string{"openProject(1)", "openStageResult(1)", "openTodoResult('todo',1)"} {
		if !strings.Contains(strings.ReplaceAll(html.UnescapeString(w.Body.String()), " ", ""), expected) {
			t.Errorf("search missing %s", expected)
		}
	}
}

func TestHabitTemplateExposesKeyboardRename(t *testing.T) {
	setupWorkspaceDB(t)
	execSQL(t, `INSERT INTO habits(id,name,period,target) VALUES(1,'Read','day',1)`)
	w := httptest.NewRecorder()
	handleHabits(w, httptest.NewRequest(http.MethodGet, "/habits", nil))
	requireOK(t, w)
	body := w.Body.String()
	if !strings.Contains(body, `data-action="rename-habit"`) || !strings.Contains(body, `aria-label="Rename Read"`) {
		t.Fatalf("habit rename is not keyboard accessible: %s", body)
	}
}

func TestConcurrentNoteArchiveKeepsOneActive(t *testing.T) {
	setupWorkspaceDB(t)
	execSQL(t, `INSERT INTO notes(title,content) VALUES('Second','')`)
	rows, err := db.Query(`SELECT id FROM notes WHERE archived=0 ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for rows.Next() {
		var id int
		if err := rows.Scan(&id); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, strconv.Itoa(id))
	}
	rows.Close()
	if len(ids) != 2 {
		t.Fatalf("active notes = %d, want 2", len(ids))
	}

	start := make(chan struct{})
	codes := make(chan int, 2)
	var wg sync.WaitGroup
	for _, id := range ids {
		wg.Add(1)
		go func(id string) {
			defer wg.Done()
			<-start
			codes <- formRequest(t, handleDeleteNote, "/notes/delete", url.Values{"id": {id}}).Code
		}(id)
	}
	close(start)
	wg.Wait()
	close(codes)

	var active int
	if err := db.QueryRow(`SELECT count(*) FROM notes WHERE archived=0`).Scan(&active); err != nil {
		t.Fatal(err)
	}
	if active != 1 {
		t.Fatalf("active notes = %d, want 1", active)
	}
	var ok, rejected int
	for code := range codes {
		if code == http.StatusOK {
			ok++
		} else if code == http.StatusBadRequest {
			rejected++
		}
	}
	if ok != 1 || rejected != 1 {
		t.Fatalf("archive statuses: ok=%d rejected=%d", ok, rejected)
	}
}

func TestWeeklyCalendarDST(t *testing.T) {
	loc, _ := time.LoadLocation("Europe/Bucharest")
	for _, dateRaw := range []string{"2026-03-29T19:00:00", "2026-10-25T19:00:00", "2026-09-14T09:00:00"} {
		now, _ := time.ParseInLocation("2006-01-02T15:04:05", dateRaw, loc)
		at := latestWeeklyOccurrence(now, 0, "18:00")
		if at.Weekday() != time.Sunday || at.Hour() != 18 || at.After(now) || now.Sub(at) > 7*24*time.Hour {
			t.Fatalf("bad wall time %v -> %v", now, at)
		}
	}
	now, _ := time.ParseInLocation("2006-01-02 15:04", "2026-09-13 17:00", loc)
	if at := latestWeeklyOccurrence(now, 0, "18:00"); at.Day() != 6 {
		t.Fatal("future occurrence used")
	}
}

func TestDeliveryRetriesArePerDeviceAndBounded(t *testing.T) {
	setupWorkspaceDB(t)
	old := deliverPush
	t.Cleanup(func() { deliverPush = old })
	calls := map[string]int{}
	for _, endpoint := range []string{"https://push.example/a", "https://push.example/b"} {
		if err := saveSubscription(webpush.Subscription{Endpoint: endpoint, Keys: webpush.Keys{P256dh: "key", Auth: "auth"}}); err != nil {
			t.Fatal(err)
		}
	}
	deliverPush = func(endpoint, p, a string, payload pushPayload) PushResult {
		calls[endpoint]++
		if strings.HasSuffix(endpoint, "/a") {
			return PushResult{Accepted: true, Message: "accepted"}
		}
		return PushResult{Retry: true, Message: "temporary failure"}
	}
	now := date("2026-09-13")
	s := ScheduleSettings{TaskEnabled: true}
	if err := queueNotification("task:2026-09-13", pushPayload{Title: "test"}, now, now.AddDate(0, 0, 1)); err != nil {
		t.Fatal(err)
	}
	for _, after := range []time.Duration{0, time.Minute, 6 * time.Minute, 15 * time.Minute} {
		if err := processDeliveries(now.Add(after), s); err != nil {
			t.Fatal(err)
		}
	}
	if calls["https://push.example/a"] != 1 || calls["https://push.example/b"] != 3 {
		t.Fatalf("retry counts %v", calls)
	}
	// Reconciliation must not delete delivery records through INSERT OR REPLACE.
	if err := saveSubscription(webpush.Subscription{Endpoint: "https://push.example/a", Keys: webpush.Keys{P256dh: "new", Auth: "new"}}); err != nil {
		t.Fatal(err)
	}
	var n int
	db.QueryRow(`SELECT count(*) FROM push_deliveries WHERE endpoint='https://push.example/a'`).Scan(&n)
	if n != 1 {
		t.Fatal("reconciliation erased delivery history")
	}
}

func TestWeeklyReviewSnapshotsAndLatestMissedOnly(t *testing.T) {
	setupWorkspaceDB(t)
	setSetting("task_enabled", "false")
	setSetting("habit_enabled", "false")
	setSetting("review_enabled", "true")
	setSetting("review_enabled_at", "2026-08-01T00:00:00Z")
	now := time.Date(2026, 9, 14, 9, 0, 0, 0, appLocation())
	end := latestWeeklyOccurrence(now, 0, "18:00")
	execSQL(t, `INSERT INTO todos(category,text,done,completed_at) VALUES('todo','Finished',1,?),('todo','Old unknown',1,NULL),('groceries','Milk',1,?)`, end.Add(-time.Hour).UTC().Format(time.RFC3339), end.Add(-time.Hour).UTC().Format(time.RFC3339))
	if err := runScheduleTick(now); err != nil {
		t.Fatal(err)
	}
	if err := runScheduleTick(now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	var n int
	db.QueryRow(`SELECT count(*) FROM weekly_reviews`).Scan(&n)
	if n != 1 {
		t.Fatalf("generated backlog/duplicate: %d", n)
	}
	var content string
	db.QueryRow(`SELECT content FROM weekly_reviews`).Scan(&content)
	var review WeeklyReview
	if err := json.Unmarshal([]byte(content), &review); err != nil {
		t.Fatal(err)
	}
	if len(review.Completed) != 1 || review.Completed[0].Text != "Finished" {
		t.Fatalf("wrong completion history %+v", review.Completed)
	}
	execSQL(t, `UPDATE todos SET text='Changed' WHERE text='Finished'`)
	if err := runScheduleTick(now.Add(2 * time.Hour)); err != nil {
		t.Fatal(err)
	}
	var after string
	db.QueryRow(`SELECT content FROM weekly_reviews`).Scan(&after)
	if content != after {
		t.Fatal("snapshot changed")
	}
}

func TestPushTestTargetsCurrentDeviceAndAuth(t *testing.T) {
	setupWorkspaceDB(t)
	old := deliverPush
	t.Cleanup(func() { deliverPush = old })
	var called []string
	deliverPush = func(endpoint, p, a string, payload pushPayload) PushResult {
		called = append(called, endpoint)
		return PushResult{Accepted: true, Message: "Accepted by push service"}
	}
	execSQL(t, `INSERT INTO push_subscriptions(endpoint,p256dh,auth) VALUES('https://push.example/a','p','a'),('https://push.example/b','p','a')`)
	w := httptest.NewRecorder()
	handlePushTest(w, httptest.NewRequest("POST", "/push/test", strings.NewReader(`{"endpoint":"https://push.example/b"}`)))
	requireOK(t, w)
	if len(called) != 1 || called[0] != "https://push.example/b" {
		t.Fatalf("test broadcast: %v", called)
	}
	w = httptest.NewRecorder()
	authMiddleware(handlePushTest)(w, httptest.NewRequest("POST", "/push/test", strings.NewReader(`{}`)))
	if w.Code != 401 || !strings.Contains(w.Header().Get("Content-Type"), "application/json") {
		t.Fatal("expired API login was not JSON 401")
	}
}

func TestDisabledExpiredDeliveryAndSettings(t *testing.T) {
	setupWorkspaceDB(t)
	old := deliverPush
	t.Cleanup(func() { deliverPush = old })
	calls := 0
	deliverPush = func(endpoint, p, a string, payload pushPayload) PushResult {
		calls++
		return PushResult{Accepted: true}
	}
	execSQL(t, `INSERT INTO push_subscriptions(endpoint,p256dh,auth) VALUES('https://push.example/a','p','a')`)
	now := date("2026-09-13")
	if err := queueNotification("task:expired", pushPayload{}, now.Add(-time.Hour), now); err != nil {
		t.Fatal(err)
	}
	if err := queueNotification("habit:disabled", pushPayload{}, now, now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := processDeliveries(now, ScheduleSettings{TaskEnabled: true}); err != nil {
		t.Fatal(err)
	}
	if calls != 0 {
		t.Fatal("sent disabled or expired notification")
	}
	bad := formRequest(t, handleSaveSettings, "/settings/save", url.Values{"timezone": {"Not/AZone"}})
	if bad.Code != 400 {
		t.Fatal("invalid timezone accepted")
	}
	before, _ := loadSchedule()
	if before.Timezone != "Europe/Bucharest" {
		t.Fatal("invalid save altered timezone")
	}
	good := formRequest(t, handleSaveSettings, "/settings/save", url.Values{"timezone": {"Europe/London"}, "task_time": {"10:00"}, "habit_time": {"21:00"}, "review_time": {"18:00"}, "review_day": {"0"}, "task_enabled": {"on"}})
	requireOK(t, good)
	if appLocation().String() != "Europe/London" {
		t.Fatal("shared calendar timezone was not updated")
	}
}

func TestArchivedTaskDeepLinkAndLoginReturn(t *testing.T) {
	setupWorkspaceDB(t)
	execSQL(t, `INSERT INTO projects(id,name,archived) VALUES(1,'Car',1)`)
	execSQL(t, `INSERT INTO todos(id,category,text,project_id) VALUES(1,'todo','Hidden task',1)`)
	if _, err := getTask(1); err == nil {
		t.Fatal("archived project task available for editing")
	}
	request := httptest.NewRequest("GET", "/?view=review&review=12", nil)
	w := httptest.NewRecorder()
	authMiddleware(handleIndex)(w, request)
	var returnCookie *http.Cookie
	for _, cookie := range w.Result().Cookies() {
		if cookie.Name == "return_to" {
			returnCookie = cookie
		}
	}
	if returnCookie == nil {
		t.Fatal("login lost deep link")
	}
	old := passcode
	passcode = "12345678"
	t.Cleanup(func() { passcode = old })
	request = httptest.NewRequest("POST", "/login", strings.NewReader("passcode=12345678"))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.AddCookie(returnCookie)
	w = httptest.NewRecorder()
	handleLogin(w, request)
	if w.Header().Get("Location") != "/?view=review&review=12" {
		t.Fatalf("wrong login destination: %s", w.Header().Get("Location"))
	}
}

func TestExpiredSubscriptionCannotBeReRegistered(t *testing.T) {
	setupWorkspaceDB(t)
	endpoint := "https://push.example/revoked"
	execSQL(t, `INSERT INTO push_subscriptions(endpoint,p256dh,auth) VALUES(?,'p','a')`, endpoint)
	if err := expireSubscription(endpoint); err != nil {
		t.Fatal(err)
	}
	var n int
	db.QueryRow(`SELECT count(*) FROM push_subscriptions`).Scan(&n)
	if n != 0 {
		t.Fatal("expired subscription retained")
	}
	body, _ := json.Marshal(webpush.Subscription{Endpoint: endpoint, Keys: webpush.Keys{P256dh: "p", Auth: "a"}})
	w := httptest.NewRecorder()
	handlePushSubscribe(w, httptest.NewRequest("POST", "/push/subscribe", strings.NewReader(string(body))))
	if w.Code != 409 || !strings.Contains(w.Body.String(), `"renew":true`) {
		t.Fatalf("revoked subscription was not recognized: %d %s", w.Code, w.Body.String())
	}
}
