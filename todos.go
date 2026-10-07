package main

import (
	"database/sql"
	"errors"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"
)

type Todo struct {
	ID         int    `json:"id"`
	Category   string `json:"category"`
	Text       string `json:"text"`
	DueDate    string `json:"due_date"`
	Done       bool   `json:"done"`
	CreatedAt  string
	ArchivedAt string
	ProjectID  int    `json:"project_id"`
	HeadingID  int    `json:"heading_id"`
	Revision   int    `json:"revision"`
	Repeat     string `json:"repeat"`
}

// handleTodos renders the task workspace. Mutations call it after changing a
// row; the Today view and task groups have their own renderers.
func handleTodos(w http.ResponseWriter, r *http.Request) {
	handleWorkspace(w, r)
}

func handleAddTodo(w http.ResponseWriter, r *http.Request) {
	mutationEffects(w, "todos", "today", "sidebar")
	if r.Method != "POST" {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	category := r.FormValue("category")
	if category != "groceries" && category != "todo" && category != "shopping" {
		http.Error(w, "Invalid category", http.StatusBadRequest)
		return
	}

	text := strings.TrimSpace(r.FormValue("text"))
	if text == "" || len(text) > 500 {
		http.Error(w, "Invalid text", http.StatusBadRequest)
		return
	}

	dueDate := ""
	if category == "todo" {
		dueDate = r.FormValue("due_date")
		if dueDate != "" {
			if _, err := time.Parse("2006-01-02", dueDate); err != nil {
				http.Error(w, "Invalid date", http.StatusBadRequest)
				return
			}
		}
	}

	// For list-type categories, reuse an existing item (active or archived) rather than
	// creating a duplicate. This keeps recurring items (e.g. "potatoes") as a single row.
	//
	// The lookup and the write run in one transaction: SetMaxOpenConns(1)
	// serializes individual statements but not a read-then-write pair, so a
	// double submit (easy to do on a phone) could otherwise interleave between
	// the SELECT and the INSERT and create the duplicate this branch exists to
	// prevent. todos has no UNIQUE constraint to catch that afterwards.
	var action string
	var project, heading int
	if category == "groceries" || category == "shopping" {
		tx, err := db.Begin()
		if err != nil {
			http.Error(w, "DB error", http.StatusInternalServerError)
			return
		}
		defer tx.Rollback()

		var existingID int
		err = tx.QueryRow(
			"SELECT id FROM todos WHERE category = ? AND text = ? COLLATE NOCASE LIMIT 1",
			category, text,
		).Scan(&existingID)
		switch {
		case err == nil:
			if _, err = tx.Exec(
				"UPDATE todos SET done = 0, archived = 0, due_date = '', revision=revision+1 WHERE id = ? AND (done=1 OR archived=1)",
				existingID,
			); err != nil {
				http.Error(w, "DB error", http.StatusInternalServerError)
				return
			}
			action = "restored"
		case errors.Is(err, sql.ErrNoRows):
			if _, err = tx.Exec(
				"INSERT INTO todos (category, text, due_date) VALUES (?, ?, ?)",
				category, text, dueDate,
			); err != nil {
				http.Error(w, "DB error", http.StatusInternalServerError)
				return
			}
			action = "added"
		default:
			http.Error(w, "DB error", http.StatusInternalServerError)
			return
		}

		if err := tx.Commit(); err != nil {
			http.Error(w, "DB error", http.StatusInternalServerError)
			return
		}
	} else {
		tx, err := db.Begin()
		if err != nil {
			http.Error(w, "DB error", 500)
			return
		}
		defer tx.Rollback()
		project, heading, err = parseList(tx, r.FormValue("list"))
		if err != nil {
			writeDomainError(w, err, "Could not add task")
			return
		}
		_, err = tx.Exec("INSERT INTO todos (category,text,due_date,project_id,heading_id) VALUES(?,?,?,NULLIF(?,0),NULLIF(?,0))", category, text, dueDate, project, heading)
		if err == nil {
			err = tx.Commit()
		}
		if err != nil {
			http.Error(w, "DB error", http.StatusInternalServerError)
			return
		}
		action = "added"
	}
	log.Printf("Todo %s: category=%s text_bytes=%d", action, category, len(text))

	// Return updated list
	r.URL.RawQuery = "category=" + category
	if r.Header.Get("HX-Target") == "today-content" {
		handleToday(w, r)
		return
	}
	if r.FormValue("response") == "task-group" {
		handleTaskGroup(w, r, category, project, heading)
		return
	}
	handleTodos(w, r)
}

func handleToggleTodo(w http.ResponseWriter, r *http.Request) {
	if !requirePost(w, r) {
		return
	}

	id, ok := parseID(w, r)
	if !ok {
		return
	}

	var desired *bool
	if raw := r.FormValue("done"); raw != "" {
		value, err := strconv.ParseBool(raw)
		if err != nil {
			http.Error(w, "Invalid completion state", 400)
			return
		}
		desired = &value
	}
	expected, parseErr := strconv.Atoi(r.FormValue("revision"))
	if desired != nil && (parseErr != nil || expected < 1) {
		http.Error(w, "Invalid task revision", 400)
		return
	}
	result, err := setTaskDone(id, desired, expected, appNow())
	if err != nil {
		writeDomainError(w, err, "Could not update task")
		return
	}
	category, projectID, headingID := result.Task.Category, result.Task.ProjectID, result.Task.HeadingID
	next, repeats := result.Next, result.Repeats
	mutationEffects(w, "todos", "today", "sidebar")
	if repeats {
		// The next occurrence changes counts outside the swapped task group.
		// The event fires before the swap, while the form is still in the page;
		// if the refresh lands first, the late swap only touches detached nodes.
		detail := map[string]any{}
		if next != "" {
			detail["message"] = "Next one due " + formatDueDate(next)
		}
		hxTrigger(w, "sbWorkspaceChanged", detail)
	}
	log.Printf("Todo toggled: id=%d [%s]", id, category)

	if r.Header.Get("HX-Target") == "today-content" {
		handleToday(w, r)
		return
	}
	if r.FormValue("response") == "task-group" {
		handleTaskGroup(w, r, category, projectID, headingID)
		return
	}
	r.URL.RawQuery = "category=" + category
	handleTodos(w, r)
}

func handleDeleteTodo(w http.ResponseWriter, r *http.Request) {
	if !requirePost(w, r) {
		return
	}

	id, ok := parseID(w, r)
	if !ok {
		return
	}

	result, err := archiveTask(id)
	if err != nil {
		writeDomainError(w, err, "Could not archive task")
		return
	}
	category, projectID, headingID := result.Task.Category, result.Task.ProjectID, result.Task.HeadingID
	mutationEffects(w, "todos", "today", "sidebar")
	hxTrigger(w, "sbUndo", map[string]any{"kind": "todo", "id": id, "token": result.Undo})
	if r.FormValue("response") == "task-group" {
		handleTaskGroup(w, r, category, projectID, headingID)
		return
	}
	r.URL.RawQuery = "category=" + category
	handleTodos(w, r)
}

func handleClearChecked(w http.ResponseWriter, r *http.Request) {
	mutationEffects(w, "todos", "today", "sidebar")
	if !requirePost(w, r) {
		return
	}

	category := r.FormValue("category")
	if category != "groceries" && category != "shopping" {
		http.Error(w, "Invalid category", http.StatusBadRequest)
		return
	}

	tx, err := db.Begin()
	if err != nil {
		writeDomainError(w, err, "Could not uncheck items")
		return
	}
	defer tx.Rollback()
	rows, err := tx.Query(`SELECT id,revision FROM todos WHERE category=? AND archived=0 AND done=1`, category)
	if err != nil {
		writeDomainError(w, err, "Could not uncheck items")
		return
	}
	var changes []undoChange
	for rows.Next() {
		var id, revision int
		if err = rows.Scan(&id, &revision); err != nil {
			break
		}
		changes = append(changes, undoChange{Table: "todos", ID: id, Revision: revision + 1, Values: map[string]any{"done": 1}})
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		writeDomainError(w, err, "Could not uncheck items")
		return
	}
	if _, err = tx.Exec(`UPDATE todos SET done=0,revision=revision+1 WHERE category=? AND archived=0 AND done=1`, category); err != nil {
		writeDomainError(w, err, "Could not uncheck items")
		return
	}
	var token string
	if len(changes) > 0 {
		token, err = recordUndo(tx, undoOperation{Changes: changes, SkipChanged: true})
		if err != nil {
			writeDomainError(w, err, "Could not record undo")
			return
		}
	}
	if err = tx.Commit(); err != nil {
		writeDomainError(w, err, "Could not uncheck items")
		return
	}
	if token != "" {
		hxTrigger(w, "sbUndo", map[string]any{"kind": "list", "token": token, "message": "Items unchecked"})
	}
	log.Printf("Cleared checked items: [%s]", category)

	r.URL.RawQuery = "category=" + category
	handleTodos(w, r)
}

func handleRestoreTodo(w http.ResponseWriter, r *http.Request) {
	mutationEffects(w, "todos", "today", "sidebar")
	if !requirePost(w, r) {
		return
	}

	id, ok := parseID(w, r)
	if !ok {
		return
	}

	var category string
	err := db.QueryRow("SELECT category FROM todos WHERE id = ?", id).Scan(&category)
	if err != nil {
		http.Error(w, "Not found", http.StatusNotFound)
		return
	}

	if _, err := db.Exec("UPDATE todos SET archived = 0, revision=revision+1, archive_after=CASE WHEN done=1 AND category='todo' THEN ? END WHERE id = ? AND archived=1", dbTime(appNow().AddDate(0, 0, 7)), id); err != nil {
		http.Error(w, "DB error", http.StatusInternalServerError)
		return
	}
	log.Printf("Todo restored: id=%d [%s]", id, category)

	r.URL.RawQuery = "category=" + category
	// Restoring normally happens inside the archive view, which should re-render
	// itself. An undo from the toast is triggered from the main list instead, so
	// it asks for that view back rather than dropping the user into the archive.
	if r.FormValue("return") == "list" {
		handleTodos(w, r)
		return
	}
	renderArchive(w, r, category)
}

func handlePermanentDeleteTodo(w http.ResponseWriter, r *http.Request) {
	mutationEffects(w, "todos", "today", "sidebar")
	if !requirePost(w, r) {
		return
	}

	id, ok := parseID(w, r)
	if !ok {
		return
	}

	// Only archived rows can be deleted forever; archiving is the undoable step.
	var category string
	err := db.QueryRow("SELECT category FROM todos WHERE id = ? AND archived = 1", id).Scan(&category)
	if err != nil {
		http.Error(w, "Not found", http.StatusNotFound)
		return
	}

	if _, err := db.Exec("DELETE FROM todos WHERE id = ? AND archived = 1", id); err != nil {
		http.Error(w, "DB error", http.StatusInternalServerError)
		return
	}
	log.Printf("Todo permanently deleted: id=%d [%s]", id, category)

	renderArchive(w, r, category)
}

// Completed tasks tidy away seven days after completion; unfinished work stays.
func archiveStaleTodos(now time.Time) (int64, error) {
	res, err := db.Exec(`UPDATE todos SET archived=1, archived_at=?,revision=revision+1
 WHERE category='todo' AND archived=0 AND done=1 AND archive_after IS NOT NULL AND archive_after<=?`, dbTime(now), dbTime(now))
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}
