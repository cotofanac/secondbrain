package main

import (
	"database/sql"
	"errors"
	"log"
	"net/http"
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
	ProjectID  int `json:"project_id"`
	StageID    int `json:"stage_id"`
	Revision   int `json:"revision"`
}

// handleTodos renders the task workspace. Mutations call it after changing a
// row; the Today view and task groups have their own renderers.
func handleTodos(w http.ResponseWriter, r *http.Request) {
	handleWorkspace(w, r)
}

func handleAddTodo(w http.ResponseWriter, r *http.Request) {
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
				"UPDATE todos SET done = 0, archived = 0, due_date = '' WHERE id = ?",
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
		project, stage, err := validMembership(tx, r.FormValue("project_id"), r.FormValue("stage_id"))
		if err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		_, err = tx.Exec("INSERT INTO todos (category,text,due_date,project_id,stage_id) VALUES(?,?,?,NULLIF(?,0),NULLIF(?,0))", category, text, dueDate, project, stage)
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

	var category string
	var projectID, stageID int
	err := db.QueryRow(`SELECT t.category,COALESCE(t.project_id,0),COALESCE(t.stage_id,0) FROM todos t WHERE t.id=? AND `+activeTaskSQL, id).Scan(&category, &projectID, &stageID)
	if err != nil {
		http.Error(w, "Not found", http.StatusNotFound)
		return
	}

	if _, err := db.Exec(`UPDATE todos SET completed_at=CASE WHEN done=0 THEN ? ELSE NULL END,
 archive_after=CASE WHEN done=0 THEN ? ELSE NULL END, done=1-done WHERE id=?`, appNow().UTC().Format(time.RFC3339), appNow().UTC().AddDate(0, 0, 7).Format(time.RFC3339), id); err != nil {
		http.Error(w, "DB error", http.StatusInternalServerError)
		return
	}
	log.Printf("Todo toggled: id=%d [%s]", id, category)

	if r.Header.Get("HX-Target") == "today-content" {
		handleToday(w, r)
		return
	}
	if r.FormValue("response") == "task-group" {
		handleTaskGroup(w, r, category, projectID, stageID)
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

	var category string
	err := db.QueryRow("SELECT category FROM todos WHERE id = ?", id).Scan(&category)
	if err != nil {
		http.Error(w, "Not found", http.StatusNotFound)
		return
	}

	if _, err := db.Exec("UPDATE todos SET archived = 1, archived_at = CURRENT_TIMESTAMP WHERE id = ?", id); err != nil {
		http.Error(w, "DB error", http.StatusInternalServerError)
		return
	}
	log.Printf("Todo archived: id=%d [%s]", id, category)

	// Archiving is a single tap on a small control with no confirmation step,
	// so offer the reversal rather than making the user go find the archive.
	hxTrigger(w, "sbUndo", map[string]any{"kind": "todo", "id": id})

	r.URL.RawQuery = "category=" + category
	handleTodos(w, r)
}

func handleClearChecked(w http.ResponseWriter, r *http.Request) {
	if !requirePost(w, r) {
		return
	}

	category := r.FormValue("category")
	if category != "groceries" && category != "shopping" {
		http.Error(w, "Invalid category", http.StatusBadRequest)
		return
	}

	if _, err := db.Exec("UPDATE todos SET done = 0 WHERE category = ? AND archived = 0 AND done = 1", category); err != nil {
		http.Error(w, "DB error", http.StatusInternalServerError)
		return
	}
	log.Printf("Cleared checked items: [%s]", category)

	r.URL.RawQuery = "category=" + category
	handleTodos(w, r)
}

func handleArchiveTodos(w http.ResponseWriter, r *http.Request) {
	category := r.URL.Query().Get("category")
	if category != "groceries" && category != "todo" && category != "shopping" {
		category = "groceries"
	}

	rows, err := db.Query(
		"SELECT id, category, text, due_date, done, COALESCE(archived_at, '') FROM todos WHERE category = ? AND archived = 1 ORDER BY COALESCE(archived_at, created_at) DESC LIMIT 200",
		category,
	)
	if err != nil {
		http.Error(w, "DB error", http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	var todos []Todo
	for rows.Next() {
		var t Todo
		rows.Scan(&t.ID, &t.Category, &t.Text, &t.DueDate, &t.Done, &t.ArchivedAt)
		todos = append(todos, t)
	}

	data := struct {
		Category string
		Todos    []Todo
	}{category, todos}

	renderTemplate(w, r, "todo-archive.html", data)
}

func handleRestoreTodo(w http.ResponseWriter, r *http.Request) {
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

	if _, err := db.Exec("UPDATE todos SET archived = 0, archive_after=CASE WHEN done=1 THEN ? ELSE NULL END WHERE id = ?", appNow().UTC().AddDate(0, 0, 7).Format(time.RFC3339), id); err != nil {
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
	handleArchiveTodos(w, r)
}

func handlePermanentDeleteTodo(w http.ResponseWriter, r *http.Request) {
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

	r.URL.RawQuery = "category=" + category
	handleArchiveTodos(w, r)
}

// Completed tasks tidy away seven days after completion; unfinished work stays.
func archiveStaleTodos(now time.Time) (int64, error) {
	res, err := db.Exec(`UPDATE todos SET archived=1, archived_at=CURRENT_TIMESTAMP
 WHERE category='todo' AND archived=0 AND done=1 AND archive_after IS NOT NULL AND archive_after<=?`, now.UTC().Format(time.RFC3339))
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}
