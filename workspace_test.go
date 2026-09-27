package main

import (
	"bytes"
	"compress/gzip"
	"database/sql"
	"encoding/json"
	"html"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
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
	first := formRequest(t, handleTaskSave, "/task/save", url.Values{"id": {"1"}, "text": {"First client"}, "revision": {"1"}})
	requireOK(t, first)
	if !strings.Contains(first.Body.String(), `name="revision" value="2"`) {
		t.Fatalf("save did not render the next revision: %s", first.Body.String())
	}
	stale := formRequest(t, handleTaskSave, "/task/save", url.Values{"id": {"1"}, "text": {"Second client"}, "revision": {"1"}})
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
	for _, expected := range []string{`data-call="openProjectResult"data-args='[1]'`, `data-call="openStageResult"data-args='[1]'`, `data-call="openTodoResult"data-args='["todo",1]'`} {
		if !strings.Contains(strings.ReplaceAll(html.UnescapeString(w.Body.String()), " ", ""), expected) {
			t.Errorf("search missing %s", expected)
		}
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

func TestTodaySeparatesAttentionUpcomingAndSuggestions(t *testing.T) {
	setupWorkspaceDB(t)
	now := time.Date(2026, 9, 17, 10, 0, 0, 0, appLocation())
	execSQL(t, `INSERT INTO projects(id,name,position) VALUES(1,'Car',1)`)
	execSQL(t, `INSERT INTO todos(category,text,due_date,done,project_id,position,completed_at) VALUES
		('todo','Late','2026-09-16',0,NULL,0,NULL),
		('todo','Due','2026-09-17',0,NULL,0,NULL),
		('todo','Soon','2026-09-20',0,NULL,0,NULL),
		('todo','Project next','',0,1,0,NULL),
		('todo','Project later','',0,1,1,NULL),
		('todo','Standalone','',0,NULL,0,NULL),
		('todo','Finished','',1,NULL,0,?)`, now.Add(-time.Hour).UTC().Format(time.RFC3339))
	view, err := loadToday(now)
	if err != nil {
		t.Fatal(err)
	}
	if len(view.Overdue) != 1 || view.Overdue[0].Text != "Late" {
		t.Fatalf("overdue = %+v", view.Overdue)
	}
	if len(view.Due) != 1 || view.Due[0].Text != "Due" {
		t.Fatalf("due = %+v", view.Due)
	}
	if len(view.Upcoming) != 1 || view.Upcoming[0].Text != "Soon" {
		t.Fatalf("upcoming = %+v", view.Upcoming)
	}
	if len(view.Suggestions) != 2 || view.Suggestions[0].Text != "Project next" || view.Suggestions[1].Text != "Standalone" {
		t.Fatalf("suggestions = %+v", view.Suggestions)
	}
	if view.CompletedToday != 1 || view.DueLeft != 2 {
		t.Fatalf("stats = completed %d attention %d", view.CompletedToday, view.DueLeft)
	}
	w := httptest.NewRecorder()
	renderTemplate(w, nil, "today.html", view)
	requireOK(t, w)
	if body := w.Body.String(); !strings.Contains(body, "What next") || !strings.Contains(body, `value="2026-09-17"`) {
		t.Fatalf("Today actions missing from rendered view: %s", body)
	}
}

func TestWeeklyReviewsAreRetiredWithoutDeletingHistory(t *testing.T) {
	setupWorkspaceDB(t)
	setSetting("task_enabled", "false")
	setSetting("review_enabled", "true")
	setSetting("review_enabled_at", "2026-08-01T00:00:00Z")
	now := time.Date(2026, 9, 14, 9, 0, 0, 0, appLocation())
	execSQL(t, `INSERT INTO weekly_reviews(period_end,content,created_at) VALUES(?,?,?)`, now.UTC().Format(time.RFC3339), `{"Start":"legacy"}`, now.UTC().Format(time.RFC3339))
	if err := runScheduleTick(now); err != nil {
		t.Fatal(err)
	}
	var n int
	db.QueryRow(`SELECT count(*) FROM weekly_reviews`).Scan(&n)
	if n != 1 {
		t.Fatalf("historical reviews changed: %d", n)
	}
	db.QueryRow(`SELECT count(*) FROM push_deliveries WHERE event_key LIKE 'review:%'`).Scan(&n)
	if n != 0 {
		t.Fatalf("weekly delivery was queued: %d", n)
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
	good := formRequest(t, handleSaveSettings, "/settings/save", url.Values{"timezone": {"Europe/London"}, "task_time": {"10:00"}, "task_enabled": {"on"}})
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

func resetLoginStates(t *testing.T) {
	t.Helper()
	loginMu.Lock()
	loginStates = map[string]*loginState{}
	loginMu.Unlock()
	proxies := trustedProxies
	t.Cleanup(func() {
		loginMu.Lock()
		loginStates = map[string]*loginState{}
		loginMu.Unlock()
		trustedProxies = proxies
	})
}

func TestConcurrentLoginAttemptsRespectLockout(t *testing.T) {
	previous := passcode
	passcode = "12345678"
	resetLoginStates(t)
	t.Cleanup(func() { passcode = previous })
	var wg sync.WaitGroup
	var mu sync.Mutex
	compared := 0
	for i := 0; i < 40; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r := httptest.NewRequest("POST", "/login", strings.NewReader("passcode=00000000"))
			r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			r.Header.Set("HX-Request", "true")
			w := httptest.NewRecorder()
			handleLogin(w, r)
			if strings.Contains(w.Body.String(), "Wrong passcode") {
				mu.Lock()
				compared++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if compared > maxLoginFailures {
		t.Fatalf("%d parallel guesses were checked; lockout allows %d", compared, maxLoginFailures)
	}
}

func TestPermanentDeleteOnlyRemovesArchivedRows(t *testing.T) {
	setupWorkspaceDB(t)
	execSQL(t, `INSERT INTO todos(id,category,text) VALUES(900,'todo','Active task')`)
	var noteID int
	db.QueryRow(`SELECT id FROM notes WHERE archived=0`).Scan(&noteID)
	id := url.Values{"id": {"900"}}
	formRequest(t, handlePermanentDeleteTodo, "/todos/permanent-delete", id)
	formRequest(t, handlePermanentDeleteNote, "/notes/permanent-delete", url.Values{"id": {strconv.Itoa(noteID)}})
	for _, q := range []string{`SELECT count(*) FROM todos WHERE id=900`, `SELECT count(*) FROM notes WHERE id=` + strconv.Itoa(noteID)} {
		var n int
		db.QueryRow(q).Scan(&n)
		if n != 1 {
			t.Fatalf("active row was permanently deleted: %s", q)
		}
	}
	execSQL(t, `UPDATE todos SET archived=1 WHERE id=900`)
	requireOK(t, formRequest(t, handlePermanentDeleteTodo, "/todos/permanent-delete", id))
	var n int
	db.QueryRow(`SELECT count(*) FROM todos WHERE id=900`).Scan(&n)
	if n != 0 {
		t.Fatal("archived task was not deleted")
	}
}

func TestLoginLockoutIsPerClient(t *testing.T) {
	previous := passcode
	passcode = "12345678"
	t.Cleanup(func() { passcode = previous })
	resetLoginStates(t)
	for i := 0; i < maxLoginFailures; i++ {
		checkPasscode("203.0.113.9", "00000000")
	}
	if blocked, _ := checkPasscode("203.0.113.9", "12345678"); !blocked {
		t.Fatal("client was not locked out after repeated failures")
	}
	if blocked, ok := checkPasscode("198.51.100.4", "12345678"); blocked || !ok {
		t.Fatalf("another client was affected by the lockout: blocked=%v ok=%v", blocked, ok)
	}
}

func TestLoginClientTrustsForwardedForOnlyFromProxies(t *testing.T) {
	resetLoginStates(t)
	var err error
	if trustedProxies, err = parseTrustedProxies("10.0.0.0/8, 172.18.0.2"); err != nil {
		t.Fatal(err)
	}
	if _, err := parseTrustedProxies("not-an-ip"); err == nil {
		t.Fatal("invalid TRUSTED_PROXY was accepted")
	}
	cases := []struct{ remote, xff, want string }{
		{"203.0.113.9:4000", "198.51.100.4", "203.0.113.9"},                    // untrusted peer: header ignored
		{"172.18.0.2:4000", "198.51.100.4", "198.51.100.4"},                    // trusted proxy
		{"172.18.0.2:4000", "1.1.1.1, 198.51.100.4, 10.1.2.3", "198.51.100.4"}, // spoofed left entry, proxy chain on the right
		{"172.18.0.2:4000", "", "172.18.0.2"},                                  // no header
		{"172.18.0.2:4000", "garbage", "172.18.0.2"},                           // unparseable header
	}
	for _, c := range cases {
		r := httptest.NewRequest("POST", "/login", nil)
		r.RemoteAddr = c.remote
		if c.xff != "" {
			r.Header.Set("X-Forwarded-For", c.xff)
		}
		if got := loginClient(r); got != c.want {
			t.Errorf("loginClient(%s, %q) = %s, want %s", c.remote, c.xff, got, c.want)
		}
	}
}

func TestPruneDeliveriesKeepsRecentAndPending(t *testing.T) {
	setupWorkspaceDB(t)
	now := time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC)
	old := now.AddDate(0, 0, -40).Format(time.RFC3339)
	recent := now.AddDate(0, 0, -2).Format(time.RFC3339)
	execSQL(t, `INSERT INTO push_subscriptions(endpoint,p256dh,auth) VALUES('https://push.example/a','p','a')`)
	for key, row := range map[string][2]string{"old-accepted": {old, "accepted"}, "old-pending": {old, "pending"}, "recent-failed": {recent, "failed"}} {
		execSQL(t, `INSERT INTO push_deliveries(endpoint,event_key,payload,next_attempt,expires_at,status) VALUES('https://push.example/a',?,'{}',?,?,?)`, key, row[0], row[0], row[1])
	}
	if n, err := pruneDeliveries(now); err != nil || n != 1 {
		t.Fatalf("pruned %d rows (err %v), want 1", n, err)
	}
	var left int
	db.QueryRow(`SELECT count(*) FROM push_deliveries WHERE event_key IN ('old-pending','recent-failed')`).Scan(&left)
	if left != 2 {
		t.Fatalf("kept %d of the pending/recent rows, want 2", left)
	}
}

func TestProjectBodyCountsArchivedCompletions(t *testing.T) {
	setupWorkspaceDB(t)
	execSQL(t, `INSERT INTO projects(id,name) VALUES(1,'Car')`)
	execSQL(t, `INSERT INTO stages(id,project_id,name) VALUES(1,1,'Licence')`)
	execSQL(t, `INSERT INTO todos(id,category,text,project_id,stage_id,done,archived) VALUES(1,'todo','Theory test',1,1,1,1),(2,'todo','Driving test',1,1,0,0)`)
	ws, err := loadWorkspace()
	if err != nil {
		t.Fatal(err)
	}
	stage := ws.Projects[0].Stages[0].Group
	w := httptest.NewRecorder()
	handleWorkspaceProject(w, httptest.NewRequest("GET", "/workspace/project?id=1", nil))
	requireOK(t, w)
	want := strconv.Itoa(stage.Completed) + "/" + strconv.Itoa(stage.Total)
	if want != "1/2" || !strings.Contains(w.Body.String(), `<span class="section-count">`+want+`</span>`) {
		t.Fatalf("project body stage count disagrees with the workspace (%s): %s", want, w.Body.String())
	}
}

func TestDailyBackupWritesOncePerDayAndPrunes(t *testing.T) {
	setupWorkspaceDB(t)
	previousDir, previousKeep := backupDir, backupKeep
	backupDir, backupKeep = t.TempDir(), 2
	t.Cleanup(func() { backupDir, backupKeep = previousDir, previousKeep })
	execSQL(t, `INSERT INTO todos(category,text) VALUES('todo','Back me up')`)
	day := time.Date(2026, 9, 25, 3, 0, 0, 0, time.UTC)
	for i := 0; i < 3; i++ {
		path, err := runDailyBackup(day.AddDate(0, 0, i))
		if err != nil || path == "" {
			t.Fatalf("day %d: path %q err %v", i, path, err)
		}
	}
	if path, err := runDailyBackup(day.AddDate(0, 0, 2)); err != nil || path != "" {
		t.Fatalf("second run on the same day wrote %q (err %v)", path, err)
	}
	entries, _ := os.ReadDir(backupDir)
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	if strings.Join(names, ",") != "secondbrain-2026-09-26.db,secondbrain-2026-09-27.db" {
		t.Fatalf("backups kept: %v", names)
	}
	info, err := os.Stat(backupDir + "/secondbrain-2026-09-27.db")
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("backup mode %v err %v", info, err)
	}
	snapshot, err := sql.Open("sqlite3", backupDir+"/secondbrain-2026-09-27.db")
	if err != nil {
		t.Fatal(err)
	}
	defer snapshot.Close()
	var text string
	if err := snapshot.QueryRow(`SELECT text FROM todos WHERE text='Back me up'`).Scan(&text); err != nil {
		t.Fatalf("snapshot is missing data: %v", err)
	}
}

func TestTodayMarksOnlyOverdueDates(t *testing.T) {
	setupWorkspaceDB(t)
	now := appNow()
	execSQL(t, `INSERT INTO todos(category,text,due_date) VALUES('todo','Late',?),('todo','Now',?),('todo','Soon',?)`,
		now.AddDate(0, 0, -3).Format("2006-01-02"), now.Format("2006-01-02"), now.AddDate(0, 0, 2).Format("2006-01-02"))
	w := httptest.NewRecorder()
	handleToday(w, httptest.NewRequest("GET", "/today", nil))
	requireOK(t, w)
	body := w.Body.String()
	if n := strings.Count(body, `class="todo-date overdue"`); n != 1 {
		t.Fatalf("overdue dates marked = %d, want 1", n)
	}
	late := strings.Index(body, "Late")
	if late < 0 || !strings.Contains(body[late:late+800], "todo-date overdue") {
		t.Fatal("the overdue task's date is not marked overdue")
	}
}

func TestTasksSortOverdueThenDatedThenNewest(t *testing.T) {
	setupWorkspaceDB(t)
	execSQL(t, `INSERT INTO todos(id,category,text,due_date,created_at) VALUES
		(1,'todo','Old undated','','2026-01-01 10:00:00'),
		(2,'todo','Next week','2026-10-04','2026-01-02 10:00:00'),
		(3,'todo','Overdue','2026-09-01','2026-01-03 10:00:00'),
		(4,'todo','New undated','','2026-01-04 10:00:00')`)
	ws, err := loadWorkspace()
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, task := range ws.Tasks.Tasks {
		got = append(got, task.Text)
	}
	if strings.Join(got, ",") != "Overdue,Next week,New undated,Old undated" {
		t.Fatalf("task order = %v", got)
	}
}

// The CSP forbids inline script, so templates must use data-call handlers,
// and each named function must be in app.js's allowlist.
func TestTemplatesHaveNoInlineScript(t *testing.T) {
	app, err := staticFiles.ReadFile("static/app.js")
	if err != nil {
		t.Fatal(err)
	}
	start := strings.Index(string(app), "const CALLABLE = new Set([")
	end := strings.Index(string(app)[start:], "]);")
	allowed := string(app)[start : start+end]
	inline := regexp.MustCompile(`(?i)\son[a-z]+\s*=|<script(?:\s[^>]*)?>\s*[^<\s]|javascript:`)
	calls := regexp.MustCompile(`data-call="([^"]+)"`)
	entries, _ := templateFiles.ReadDir("templates")
	for _, entry := range entries {
		body, _ := templateFiles.ReadFile("templates/" + entry.Name())
		if loc := inline.FindIndex(body); loc != nil {
			t.Errorf("%s has inline script near %q", entry.Name(), body[loc[0]:min(len(body), loc[1]+40)])
		}
		for _, m := range calls.FindAllSubmatch(body, -1) {
			if !strings.Contains(allowed, "'"+string(m[1])+"'") {
				t.Errorf("%s calls %s, which is not in CALLABLE", entry.Name(), m[1])
			}
		}
	}
}

func TestStaticAndPagesAreGzipped(t *testing.T) {
	setupWorkspaceDB(t)
	staticFS, _ := fs.Sub(staticFiles, "static")
	if err := compressStaticAssets(staticFS); err != nil {
		t.Fatal(err)
	}
	handler := serveStatic(http.StripPrefix("/static/", http.FileServer(http.FS(staticFS))))
	for _, gzipOK := range []bool{true, false} {
		r := httptest.NewRequest("GET", "/static/style.css", nil)
		if gzipOK {
			r.Header.Set("Accept-Encoding", "gzip, deflate")
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		requireOK(t, w)
		body := w.Body.Bytes()
		if gzipOK {
			if w.Header().Get("Content-Encoding") != "gzip" || !strings.HasPrefix(w.Header().Get("Content-Type"), "text/css") {
				t.Fatalf("compressed CSS headers: %v", w.Header())
			}
			zr, err := gzip.NewReader(bytes.NewReader(body))
			if err != nil {
				t.Fatal(err)
			}
			body, _ = io.ReadAll(zr)
		} else if w.Header().Get("Content-Encoding") != "" {
			t.Fatal("compressed a response for a client without gzip")
		}
		if want, _ := staticFiles.ReadFile("static/style.css"); !bytes.Equal(body, want) {
			t.Fatalf("static body differs (gzip=%v)", gzipOK)
		}
	}
	r := httptest.NewRequest("GET", "/", nil)
	r.Header.Set("Accept-Encoding", "gzip")
	w := httptest.NewRecorder()
	handleIndex(w, r)
	requireOK(t, w)
	zr, err := gzip.NewReader(w.Body)
	if err != nil || w.Header().Get("Content-Encoding") != "gzip" {
		t.Fatalf("page not gzipped: %v %v", err, w.Header())
	}
	if page, _ := io.ReadAll(zr); !bytes.Contains(page, []byte("<!DOCTYPE html>")) {
		t.Fatal("gzipped page did not decode to HTML")
	}
}

func TestBackupDirIsConfigurable(t *testing.T) {
	previousDir, previousKeep := backupDir, backupKeep
	t.Cleanup(func() { backupDir, backupKeep = previousDir, previousKeep })
	t.Setenv("BACKUP_KEEP", "")
	t.Setenv("BACKUP_DIR", "")
	configureBackups("/data")
	if backupDir != filepath.Join("/data", "backups") {
		t.Fatalf("default backup dir = %s", backupDir)
	}
	custom := t.TempDir()
	t.Setenv("BACKUP_DIR", custom)
	configureBackups("/data")
	if backupDir != custom {
		t.Fatalf("BACKUP_DIR ignored: %s", backupDir)
	}
}
