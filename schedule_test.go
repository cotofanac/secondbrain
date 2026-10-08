package main

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
)

func TestRescheduleFromToday(t *testing.T) {
	setupWorkspaceDB(t)
	execSQL(t, `INSERT INTO todos(id,category,text) VALUES(1,'todo','An idea')`)
	for _, due := range []string{appNow().Format("2006-01-02"), ""} {
		var revision int
		if err := db.QueryRow(`SELECT revision FROM todos WHERE id=1`).Scan(&revision); err != nil {
			t.Fatal(err)
		}
		values := url.Values{"id": {"1"}, "revision": {strconv.Itoa(revision)}, "due_date": {due}}
		req := httptest.NewRequest("POST", "/today/reschedule", strings.NewReader(values.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.Header.Set("HX-Target", "today-content")
		rec := httptest.NewRecorder()
		handleTodayReschedule(rec, req)
		requireOK(t, rec)
		if !strings.Contains(rec.Body.String(), `class="today-page"`) {
			t.Fatal("Today reschedule returned a workspace")
		}
		var saved string
		if err := db.QueryRow(`SELECT due_date FROM todos WHERE id=1`).Scan(&saved); err != nil {
			t.Fatal(err)
		}
		if saved != due {
			t.Fatalf("date = %q, want %q", saved, due)
		}
		undoToken(t, rec)
	}
}

func TestDoTodayFromProjectAndUndo(t *testing.T) {
	t.Setenv("TZ", "Pacific/Kiritimati")
	setupWorkspaceDB(t)
	execSQL(t, `INSERT INTO projects(id,name) VALUES(1,'Home')`)
	execSQL(t, `INSERT INTO headings(id,project_id,name) VALUES(1,1,'Kitchen')`)
	execSQL(t, `INSERT INTO todos(id,category,text,due_date,project_id,heading_id,repeat) VALUES(1,'todo','Measure shelves','2030-03-14',1,1,'weekly')`)
	values := url.Values{"id": {"1"}, "revision": {"1"}, "due_date": {"today"}, "response": {"task-group"}}
	rec := formRequest(t, handleTodayReschedule, "/today/reschedule", values)
	requireOK(t, rec)
	if !strings.Contains(rec.Body.String(), `data-group="heading-1"`) || strings.Contains(rec.Body.String(), `class="workspace-section`) {
		t.Fatalf("did not return just the original group: %s", rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), `>Do today</button>`) {
		t.Fatal("shortcut still shown on a task already due today")
	}
	var due, repeat string
	var project, heading, revision int
	read := func() {
		t.Helper()
		if err := db.QueryRow(`SELECT due_date,repeat,project_id,heading_id,revision FROM todos WHERE id=1`).Scan(&due, &repeat, &project, &heading, &revision); err != nil {
			t.Fatal(err)
		}
	}
	read()
	if due != appNow().Format("2006-01-02") || repeat != "weekly" || project != 1 || heading != 1 || revision != 2 {
		t.Fatalf("scheduled task: date=%s repeat=%s project=%d heading=%d revision=%d", due, repeat, project, heading, revision)
	}
	if !strings.Contains(rec.Header().Get("X-SB-Effects"), "today") {
		t.Fatal("Today was not invalidated")
	}
	token := undoToken(t, rec)
	// A repeated request with an old revision cannot overwrite another edit.
	stale := formRequest(t, handleTodayReschedule, "/today/reschedule", values)
	if stale.Code != http.StatusConflict {
		t.Fatalf("stale reschedule = %d", stale.Code)
	}
	if _, err := applyUndo(token); err != nil {
		t.Fatal(err)
	}
	read()
	if due != "2030-03-14" || project != 1 || heading != 1 || revision != 3 {
		t.Fatalf("undo: date=%s project=%d heading=%d revision=%d", due, project, heading, revision)
	}
	values.Set("revision", "3")
	rec = formRequest(t, handleTodayReschedule, "/today/reschedule", values)
	requireOK(t, rec)
	execSQL(t, `UPDATE todos SET text='Newer edit',revision=revision+1 WHERE id=1`)
	if _, err := applyUndo(undoToken(t, rec)); err != errConflict {
		t.Fatalf("undo after newer edit = %v, want conflict", err)
	}
}

func TestDoTodayOnlySchedulesActiveTasks(t *testing.T) {
	setupWorkspaceDB(t)
	execSQL(t, `INSERT INTO projects(id,name,archived,completed) VALUES(1,'Archived',1,0),(2,'Completed',0,1)`)
	execSQL(t, `INSERT INTO todos(id,category,text,done,archived,project_id) VALUES
		(1,'todo','Done',1,0,NULL),(2,'todo','Archived',0,1,NULL),
		(3,'todo','Archived project',0,0,1),(4,'todo','Completed project',0,0,2),
		(5,'groceries','Apples',0,0,NULL),(6,'shopping','Lamp',0,0,NULL)`)
	for _, id := range []string{"1", "2", "3", "4", "5", "6", "999"} {
		rec := formRequest(t, handleTodayReschedule, "/today/reschedule", url.Values{"id": {id}, "revision": {"1"}, "due_date": {"today"}})
		if rec.Code != http.StatusConflict {
			t.Errorf("task %s: status %d", id, rec.Code)
		}
	}
	var changed, undos int
	if err := db.QueryRow(`SELECT count(*) FROM todos WHERE due_date!='' OR revision!=1`).Scan(&changed); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT count(*) FROM undo_operations`).Scan(&undos); err != nil {
		t.Fatal(err)
	}
	if changed != 0 || undos != 0 {
		t.Fatalf("failed reschedules changed %d tasks and created %d undos", changed, undos)
	}
}

func TestRescheduleRollsBackWhenUndoFails(t *testing.T) {
	setupWorkspaceDB(t)
	execSQL(t, `INSERT INTO todos(id,category,text) VALUES(1,'todo','Task')`)
	execSQL(t, `CREATE TRIGGER reject_undo BEFORE INSERT ON undo_operations BEGIN SELECT RAISE(ABORT,'undo unavailable'); END`)
	rec := formRequest(t, handleTodayReschedule, "/today/reschedule", url.Values{"id": {"1"}, "revision": {"1"}, "due_date": {"today"}})
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("reschedule = %d, want 500", rec.Code)
	}
	var due string
	var revision int
	if err := db.QueryRow(`SELECT due_date,revision FROM todos WHERE id=1`).Scan(&due, &revision); err != nil {
		t.Fatal(err)
	}
	if due != "" || revision != 1 {
		t.Fatalf("failed undo recording left date=%s revision=%d", due, revision)
	}
}
