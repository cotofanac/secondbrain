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

type Note struct {
	ID        int    `json:"id"`
	Title     string `json:"title"`
	Content   string `json:"content"`
	UpdatedAt string
	Revision  int `json:"revision"`
}

func handleNotes(w http.ResponseWriter, r *http.Request) {
	noteIDStr := r.URL.Query().Get("id")

	// Get all note titles for dropdown
	rows, err := db.Query("SELECT id, title FROM notes WHERE archived = 0 ORDER BY title ASC")
	if err != nil {
		http.Error(w, "DB error", http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	var notesList []Note
	for rows.Next() {
		var n Note
		rows.Scan(&n.ID, &n.Title)
		notesList = append(notesList, n)
	}

	if len(notesList) == 0 {
		http.Error(w, "No notes", http.StatusNotFound)
		return
	}

	// Determine which note to show
	var currentNote Note
	if noteIDStr != "" {
		noteID, _ := strconv.Atoi(noteIDStr)
		db.QueryRow("SELECT id, title, content, updated_at, revision FROM notes WHERE id = ? AND archived = 0", noteID).Scan(
			&currentNote.ID, &currentNote.Title, &currentNote.Content, &currentNote.UpdatedAt, &currentNote.Revision,
		)
	}
	if currentNote.ID == 0 {
		currentNote.ID = notesList[0].ID
		currentNote.Title = notesList[0].Title
		db.QueryRow("SELECT content, updated_at, revision FROM notes WHERE id = ?", currentNote.ID).Scan(&currentNote.Content, &currentNote.UpdatedAt, &currentNote.Revision)
	}

	data := struct {
		Notes       []Note
		CurrentNote Note
	}{notesList, currentNote}

	renderTemplate(w, r, "notes.html", data)
}

func handleSaveNote(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	id, err := strconv.Atoi(r.FormValue("id"))
	if err != nil {
		http.Error(w, "Invalid ID", http.StatusBadRequest)
		return
	}

	content := r.FormValue("content")
	if len(content) > 1_000_000 {
		http.Error(w, "Content too large", http.StatusBadRequest)
		return
	}

	revision, err := strconv.Atoi(r.FormValue("revision"))
	if err != nil || revision < 1 {
		writeJSONError(w, http.StatusBadRequest, "Invalid note revision")
		return
	}
	res, err := db.Exec(
		"UPDATE notes SET content = ?, updated_at = ?, revision = revision + 1 WHERE id = ? AND revision = ?",
		content, dbTime(time.Now()), id, revision,
	)
	if err != nil {
		http.Error(w, "DB error", http.StatusInternalServerError)
		return
	}
	if n, rerr := res.RowsAffected(); rerr != nil {
		writeJSONError(w, http.StatusInternalServerError, "Could not save note")
		return
	} else if n == 0 {
		var latest Note
		if err := db.QueryRow("SELECT id,title,content,updated_at,revision FROM notes WHERE id=? AND archived=0", id).Scan(&latest.ID, &latest.Title, &latest.Content, &latest.UpdatedAt, &latest.Revision); err != nil {
			writeJSONError(w, http.StatusNotFound, "Note unavailable")
			return
		}
		writeJSON(w, http.StatusConflict, map[string]any{"status": "conflict", "note": latest})
		return
	}
	log.Printf("Note saved: id=%d (%d bytes)", id, len(content))

	writeJSON(w, http.StatusOK, map[string]any{"status": "saved", "revision": revision + 1})
}

func handleCreateNote(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	title := strings.TrimSpace(r.FormValue("title"))
	if title == "" || len(title) > 200 {
		http.Error(w, "Invalid title", http.StatusBadRequest)
		return
	}

	// Reject control characters; see validNameRe.
	if !validNameRe.MatchString(title) {
		http.Error(w, "Invalid characters in title", http.StatusBadRequest)
		return
	}

	// title is UNIQUE across archived rows too, so a plain INSERT would refuse
	// a name whose only other holder is invisible in the UI, with no way for
	// the user to act on the message. Bring the archived note back instead —
	// the same "reuse the row" behaviour the checklists already have. Wrapped
	// in a transaction so the lookup and the write can't interleave.
	tx, err := db.Begin()
	if err != nil {
		http.Error(w, "DB error", http.StatusInternalServerError)
		return
	}
	defer tx.Rollback()

	var noteID int64
	var restored bool
	var existingID int64
	var archived int
	err = tx.QueryRow("SELECT id, archived FROM notes WHERE title = ?", title).
		Scan(&existingID, &archived)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		res, ierr := tx.Exec("INSERT INTO notes (title, content) VALUES (?, ?)", title, "")
		if ierr != nil {
			http.Error(w, "DB error", http.StatusInternalServerError)
			return
		}
		noteID, _ = res.LastInsertId()
	case err != nil:
		http.Error(w, "DB error", http.StatusInternalServerError)
		return
	case archived == 1:
		if _, uerr := tx.Exec("UPDATE notes SET archived = 0 WHERE id = ?", existingID); uerr != nil {
			http.Error(w, "DB error", http.StatusInternalServerError)
			return
		}
		noteID, restored = existingID, true
	default:
		http.Error(w, "A note with this name already exists", http.StatusConflict)
		return
	}

	if err := tx.Commit(); err != nil {
		http.Error(w, "DB error", http.StatusInternalServerError)
		return
	}

	if restored {
		log.Printf("Note restored from archive: id=%d", noteID)
		hxTrigger(w, "sbNotice", map[string]string{
			"message": "Restored an archived note with that name",
		})
	} else {
		log.Printf("Note created: id=%d", noteID)
	}
	r.URL.RawQuery = "id=" + strconv.FormatInt(noteID, 10)
	handleNotes(w, r)
}

func handleDeleteNote(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	id, err := strconv.Atoi(r.FormValue("id"))
	if err != nil {
		http.Error(w, "Invalid ID", http.StatusBadRequest)
		return
	}

	tx, err := db.Begin()
	if err != nil {
		http.Error(w, "DB error", http.StatusInternalServerError)
		return
	}
	defer tx.Rollback()

	// Keep the count and archive in one transaction so concurrent requests
	// cannot both decide that another active note remains.
	var count int
	if err = tx.QueryRow("SELECT COUNT(*) FROM notes WHERE archived = 0").Scan(&count); err != nil {
		http.Error(w, "DB error", http.StatusInternalServerError)
		return
	}
	if count <= 1 {
		http.Error(w, "Cannot delete the last note", http.StatusBadRequest)
		return
	}

	res, err := tx.Exec("UPDATE notes SET archived = 1 WHERE id = ? AND archived = 0", id)
	if err != nil {
		http.Error(w, "DB error", http.StatusInternalServerError)
		return
	}
	if n, err := res.RowsAffected(); err != nil || n != 1 {
		http.Error(w, "Note unavailable", http.StatusNotFound)
		return
	}
	if err = tx.Commit(); err != nil {
		http.Error(w, "DB error", http.StatusInternalServerError)
		return
	}
	log.Printf("Note archived: id=%d", id)

	r.URL.RawQuery = ""
	handleNotes(w, r)
}

func handleRenameNote(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	id, err := strconv.Atoi(r.FormValue("id"))
	if err != nil {
		http.Error(w, "Invalid ID", http.StatusBadRequest)
		return
	}

	title := strings.TrimSpace(r.FormValue("title"))
	if title == "" || len(title) > 200 {
		http.Error(w, "Invalid title", http.StatusBadRequest)
		return
	}

	if !validNameRe.MatchString(title) {
		http.Error(w, "Invalid characters in title", http.StatusBadRequest)
		return
	}

	_, err = db.Exec("UPDATE notes SET title = ? WHERE id = ?", title, id)
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			http.Error(w, nameConflictMessage("notes", "title", title, "note"), http.StatusConflict)
			return
		}
		http.Error(w, "DB error", http.StatusInternalServerError)
		return
	}
	log.Printf("Note renamed: id=%d", id)

	r.URL.RawQuery = "id=" + strconv.Itoa(id)
	handleNotes(w, r)
}

func handleArchiveNotes(w http.ResponseWriter, r *http.Request) {
	rows, err := db.Query("SELECT id, title FROM notes WHERE archived = 1 ORDER BY title ASC LIMIT 200")
	if err != nil {
		http.Error(w, "DB error", http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	var notes []Note
	for rows.Next() {
		var n Note
		rows.Scan(&n.ID, &n.Title)
		notes = append(notes, n)
	}

	renderTemplate(w, r, "notes-archive.html", notes)
}

func handleRestoreNote(w http.ResponseWriter, r *http.Request) {
	if !requirePost(w, r) {
		return
	}

	id, ok := parseID(w, r)
	if !ok {
		return
	}

	if _, err := db.Exec("UPDATE notes SET archived = 0 WHERE id = ?", id); err != nil {
		http.Error(w, "DB error", http.StatusInternalServerError)
		return
	}
	handleArchiveNotes(w, r)
}

func handlePermanentDeleteNote(w http.ResponseWriter, r *http.Request) {
	if !requirePost(w, r) {
		return
	}

	id, ok := parseID(w, r)
	if !ok {
		return
	}

	// Active notes are never hard-deleted; this also protects the last note.
	if _, err := db.Exec("DELETE FROM notes WHERE id = ? AND archived = 1", id); err != nil {
		http.Error(w, "DB error", http.StatusInternalServerError)
		return
	}
	handleArchiveNotes(w, r)
}
