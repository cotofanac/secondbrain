package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func undoToken(t *testing.T, response *httptest.ResponseRecorder) string {
	t.Helper()
	var triggers map[string]struct{ Token string }
	if err := json.Unmarshal([]byte(response.Header().Get("HX-Trigger")), &triggers); err != nil {
		t.Fatal(err)
	}
	token := triggers["sbUndo"].Token
	if token == "" {
		t.Fatal("no undo capability")
	}
	return token
}

func TestDesiredCompletionIsIdempotentAndInvalidatesStaleEdits(t *testing.T) {
	setupWorkspaceDB(t)
	execSQL(t, `INSERT INTO todos(category,text,due_date,repeat) VALUES('todo','Daily walk','2026-10-06','daily')`)
	done := true
	now := time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)
	var group sync.WaitGroup
	for range 8 {
		group.Add(1)
		go func() {
			defer group.Done()
			if _, err := setTaskDone(1, &done, 1, now); err != nil {
				t.Error(err)
			}
		}()
	}
	group.Wait()
	var count, revision int
	if err := db.QueryRow(`SELECT count(*) FROM todos WHERE repeated_from=1`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT revision FROM todos WHERE id=1`).Scan(&revision); err != nil {
		t.Fatal(err)
	}
	if count != 1 || revision != 2 {
		t.Fatalf("%d repeat rows, revision %d", count, revision)
	}
	stale := formRequest(t, handleTaskSave, "/task/save", url.Values{"id": {"1"}, "revision": {"1"}, "text": {"Stale title"}})
	if stale.Code != 409 {
		t.Fatalf("stale edit status %d", stale.Code)
	}
}

func TestUndoDoesNotOverwriteNewerChanges(t *testing.T) {
	setupWorkspaceDB(t)
	execSQL(t, `INSERT INTO todos(category,text) VALUES('todo','Keep me')`)
	result, err := archiveTask(1)
	if err != nil {
		t.Fatal(err)
	}
	execSQL(t, `UPDATE todos SET text='Newer change',revision=revision+1 WHERE id=1`)
	if _, err = applyUndo(result.Undo); !errors.Is(err, errConflict) {
		t.Fatalf("undo: %v", err)
	}
	var archived int
	var text string
	if err = db.QueryRow(`SELECT archived,text FROM todos WHERE id=1`).Scan(&archived, &text); err != nil {
		t.Fatal(err)
	}
	if archived != 1 || text != "Newer change" {
		t.Fatal("undo replaced a newer change")
	}
}

func TestBatchUndoKeepsNewerChangesAndRestoresOthers(t *testing.T) {
	setupWorkspaceDB(t)
	execSQL(t, `INSERT INTO todos(category,text,done) VALUES('groceries','Apples',1),('groceries','Coffee',1)`)
	response := formRequest(t, handleClearChecked, "/todos/clear-checked", url.Values{"category": {"groceries"}})
	requireOK(t, response)
	token := undoToken(t, response)
	execSQL(t, `UPDATE todos SET text='Fresh coffee',revision=revision+1 WHERE id=2`)
	skipped, err := applyUndo(token)
	if err != nil {
		t.Fatal(err)
	}
	if skipped != 1 {
		t.Fatalf("skipped %d newer changes", skipped)
	}
	var done int
	if err = db.QueryRow(`SELECT done FROM todos WHERE id=1`).Scan(&done); err != nil {
		t.Fatal(err)
	}
	if done != 1 {
		t.Fatal("unchanged item was not restored")
	}
	if err = db.QueryRow(`SELECT done FROM todos WHERE id=2`).Scan(&done); err != nil {
		t.Fatal(err)
	}
	if done != 0 {
		t.Fatal("newer item was overwritten")
	}
}

func TestInactiveNoteCannotBeSavedOrRenamed(t *testing.T) {
	setupWorkspaceDB(t)
	execSQL(t, `INSERT INTO notes(id,title,archived) VALUES(2,'Past note',1)`)
	response := formRequest(t, handleSaveNote, "/notes/save", url.Values{"id": {"2"}, "revision": {"1"}, "content": {"Stale content"}})
	if response.Code != 404 {
		t.Fatalf("inactive save status %d", response.Code)
	}
	response = formRequest(t, handleRenameNote, "/notes/rename", url.Values{"id": {"2"}, "title": {"Stale rename"}})
	if response.Code != 404 {
		t.Fatalf("inactive rename status %d", response.Code)
	}
}

func TestScopedReadsRespectCancellationAndFailures(t *testing.T) {
	setupWorkspaceDB(t)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := loadSidebar(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled sidebar: %v", err)
	}
	if _, err := loadTaskGroup(ctx, "todo", 0, 0); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled group: %v", err)
	}
	execSQL(t, `DROP TABLE notes`)
	response := httptest.NewRecorder()
	handleSearch(response, httptest.NewRequest("GET", "/search?q=hello", nil))
	if response.Code != 500 {
		t.Fatalf("failed read appeared as empty search: %d", response.Code)
	}
}

func TestHeadingUndoRestoresMembershipAtomically(t *testing.T) {
	setupWorkspaceDB(t)
	execSQL(t, `INSERT INTO projects(id,name) VALUES(1,'House')`)
	execSQL(t, `INSERT INTO headings(id,project_id,name,position) VALUES(1,1,'Kitchen',3)`)
	execSQL(t, `INSERT INTO todos(category,text,project_id,heading_id) VALUES('todo','Tiles',1,1),('todo','Paint',1,1)`)
	response := formRequest(t, handleHeadingAction, "/headings/action", url.Values{"id": {"1"}, "action": {"archive"}})
	requireOK(t, response)
	token := undoToken(t, response)
	if _, err := applyUndo(token); err != nil {
		t.Fatal(err)
	}
	if _, err := applyUndo(token); err != nil {
		t.Fatal("duplicate undo", err)
	}
	var count int
	if err := db.QueryRow(`SELECT count(*) FROM todos WHERE archived=0 AND heading_id=1`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 2 {
		t.Fatalf("restored %d tasks", count)
	}
}

func TestArchivePagesAndSearchExposeOlderHistory(t *testing.T) {
	setupWorkspaceDB(t)
	execSQL(t, `WITH RECURSIVE n(x) AS (VALUES(1) UNION ALL SELECT x+1 FROM n WHERE x<205)
		INSERT INTO todos(category,text,archived) SELECT 'todo',printf('Old task %03d',x),1 FROM n`)
	page, err := archivedTodosPage(t.Context(), "todo", "%", 200)
	if err != nil {
		t.Fatal(err)
	}
	if len(page) != 5 {
		t.Fatalf("older page: %d", len(page))
	}
	items, err := archivedTodosPage(t.Context(), "todo", "%Old task 001%", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].Text != "Old task 001" {
		t.Fatalf("archived search: %+v", items)
	}
	response := httptest.NewRecorder()
	handleArchive(response, httptest.NewRequest("GET", "/archive", nil))
	requireOK(t, response)
	if response.Body.String() == "" {
		t.Fatal("empty archive")
	}
}

func TestSidebarAndGroupsExcludeInactiveBodies(t *testing.T) {
	setupWorkspaceDB(t)
	execSQL(t, `INSERT INTO projects(id,name,archived) VALUES(1,'Active',0),(2,'Past',1)`)
	execSQL(t, `INSERT INTO todos(category,text,project_id) VALUES('todo','Visible',1),('todo','Hidden',2),('todo','Inbox',NULL)`)
	ws, err := loadWorkspace()
	if err != nil {
		t.Fatal(err)
	}
	if ws.ArchivedProjectCount != 1 || len(ws.Projects) != 1 {
		t.Fatal("inactive project included in active summaries")
	}
	sidebar, err := loadSidebar(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if sidebar.Inbox != 1 || len(sidebar.Projects) != 1 {
		t.Fatalf("sidebar %+v", sidebar)
	}
	group, err := loadTaskGroup(t.Context(), "todo", 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(group.Group.Tasks) != 1 || group.Group.Tasks[0].Text != "Inbox" {
		t.Fatalf("group %+v", group)
	}
}

func TestSnapshotRestoresApplicationStateAtStartup(t *testing.T) {
	setupWorkspaceDB(t)
	oldReminder := taskReminder
	t.Cleanup(func() { taskReminder = oldReminder })
	oldDir, oldKeep := backupDir, backupKeep
	t.Cleanup(func() { backupDir, backupKeep = oldDir, oldKeep })
	backupDir = filepath.Join(t.TempDir(), "snapshots")
	backupKeep = 7
	execSQL(t, `INSERT INTO projects(id,name) VALUES(1,'Restore project')`)
	execSQL(t, `INSERT INTO headings(id,project_id,name) VALUES(1,1,'Restore heading')`)
	execSQL(t, `UPDATE notes SET content='Restore this note',revision=4 WHERE id=1`)
	execSQL(t, `INSERT INTO settings(key,value) VALUES('vapid_private','fixture-private'),('vapid_public','fixture-public')`)
	execSQL(t, `INSERT INTO todos(category,text,due_date,repeat,project_id,heading_id) VALUES('todo','Restore repeat','2026-10-06','weekly',1,1)`)
	done := true
	if _, err := setTaskDone(1, &done, 1, time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)); err != nil {
		t.Fatal(err)
	}
	execSQL(t, `INSERT INTO push_subscriptions(endpoint,p256dh,auth) VALUES('https://push.example/restore','fixture-key','fixture-auth')`)
	execSQL(t, `INSERT INTO push_deliveries(endpoint,event_key,payload,next_attempt,expires_at) VALUES('https://push.example/restore','task:2026-10-06','{}','2026-10-06T09:00:00Z','2026-10-07T09:00:00Z')`)
	path, err := runDailyBackup(appNow())
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
	restored := t.TempDir()
	if err = os.WriteFile(filepath.Join(restored, "secondbrain.db"), data, 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("DATA_DIR", restored)
	t.Setenv("TZ", "America/New_York")
	t.Setenv("TASK_REMINDER_TIME", "08:45")
	configureCalendar()
	configurePushReminders()
	initDB()
	for _, query := range []string{
		`SELECT count(*) FROM notes WHERE content='Restore this note' AND revision=4`,
		`SELECT count(*) FROM projects WHERE name='Restore project'`,
		`SELECT count(*) FROM headings WHERE name='Restore heading'`,
		`SELECT count(*) FROM todos WHERE repeated_from=1 AND repeat='weekly' AND project_id=1 AND heading_id=1`,
		`SELECT count(*) FROM settings WHERE key='vapid_private' AND value='fixture-private'`,
		`SELECT count(*) FROM push_deliveries WHERE event_key='task:2026-10-06'`,
	} {
		var n int
		if err = db.QueryRow(query).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n != 1 {
			t.Fatalf("restoration failed: %s", query)
		}
	}
	if taskReminder.hour != 8 || taskReminder.min != 45 || !taskReminder.enabled {
		t.Fatal("deployment reminder lost")
	}
	if appLocation().String() != "America/New_York" {
		t.Fatal("deployment calendar lost")
	}
}
