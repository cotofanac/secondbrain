package main

import (
	"bytes"
	"database/sql"
	"errors"
	"io"
	"log"
	"net/http"
	"path/filepath"
	"strings"
	"unicode/utf8"
)

// Markdown import is one path for the app and for scripts: POST /notes/import
// with multipart "file" parts. Each file becomes a note named after the file;
// a name already in use (active or archived) is skipped, never overwritten, so
// running an import twice is harmless. One undo archives the new notes again.

const maxImportNoteBytes = 1_000_000

type importedNote struct {
	File  string `json:"file"`
	Title string `json:"title"`
	ID    int64  `json:"id,omitempty"`
}

type skippedNote struct {
	File   string `json:"file"`
	Title  string `json:"title,omitempty"`
	Reason string `json:"reason"`
}

var importExtensions = map[string]bool{".md": true, ".markdown": true, ".txt": true}

// importTitle derives a note title from an uploaded file name.
func importTitle(name string) (string, string) {
	base := filepath.Base(strings.ReplaceAll(name, `\`, "/"))
	ext := strings.ToLower(filepath.Ext(base))
	if !importExtensions[ext] {
		return "", "not a Markdown file"
	}
	title := strings.TrimSpace(strings.TrimSuffix(base, filepath.Ext(base)))
	if title == "" || len(title) > 200 || !validNameRe.MatchString(title) {
		return "", "file name can't be a note title"
	}
	return title, ""
}

// importContent normalizes a file body the way the editor would store it.
func importContent(raw []byte) (string, string) {
	raw = bytes.TrimPrefix(raw, []byte("\xef\xbb\xbf"))
	if !utf8.Valid(raw) {
		return "", "not UTF-8 text"
	}
	content := strings.ReplaceAll(string(raw), "\r\n", "\n")
	return strings.ReplaceAll(content, "\r", "\n"), ""
}

func handleImportNotes(w http.ResponseWriter, r *http.Request) {
	mutationEffects(w, "notes")
	if !requirePost(w, r) {
		return
	}
	if err := r.ParseMultipartForm(2 << 20); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			writeJSONError(w, http.StatusRequestEntityTooLarge, "Too much to import at once. Try fewer files.")
			return
		}
		writeJSONError(w, http.StatusBadRequest, "Send Markdown files as multipart \"file\" fields")
		return
	}
	defer r.MultipartForm.RemoveAll()
	files := r.MultipartForm.File["file"]
	if len(files) == 0 {
		writeJSONError(w, http.StatusBadRequest, "No files to import")
		return
	}

	type candidate struct {
		file, title, content string
	}
	var candidates []candidate
	imported := []importedNote{}
	skipped := []skippedNote{}
	seen := map[string]bool{}
	for _, header := range files {
		name := filepath.Base(strings.ReplaceAll(header.Filename, `\`, "/"))
		title, reason := importTitle(name)
		if reason == "" && seen[title] {
			reason = "listed twice"
		}
		if reason == "" && header.Size > maxImportNoteBytes {
			reason = "larger than 1 MB"
		}
		var content string
		if reason == "" {
			f, err := header.Open()
			if err != nil {
				writeJSONError(w, http.StatusInternalServerError, "Could not read upload")
				return
			}
			raw, err := io.ReadAll(io.LimitReader(f, maxImportNoteBytes+1))
			f.Close()
			if err != nil {
				writeJSONError(w, http.StatusInternalServerError, "Could not read upload")
				return
			}
			if len(raw) > maxImportNoteBytes {
				reason = "larger than 1 MB"
			} else {
				content, reason = importContent(raw)
			}
		}
		if reason != "" {
			skipped = append(skipped, skippedNote{File: name, Title: title, Reason: reason})
			continue
		}
		seen[title] = true
		candidates = append(candidates, candidate{name, title, content})
	}

	tx, err := db.Begin()
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "Could not import notes")
		return
	}
	defer tx.Rollback()
	var changes []undoChange
	now := dbTime(appNow())
	for _, c := range candidates {
		var archived int
		err := tx.QueryRow("SELECT archived FROM notes WHERE title = ?", c.title).Scan(&archived)
		switch {
		case err == nil && archived == 1:
			skipped = append(skipped, skippedNote{File: c.file, Title: c.title, Reason: "an archived note has this name"})
			continue
		case err == nil:
			skipped = append(skipped, skippedNote{File: c.file, Title: c.title, Reason: "a note with this name exists"})
			continue
		case !errors.Is(err, sql.ErrNoRows):
			writeJSONError(w, http.StatusInternalServerError, "Could not import notes")
			return
		}
		res, err := tx.Exec("INSERT INTO notes (title, content, updated_at) VALUES (?, ?, ?)", c.title, c.content, now)
		if err != nil {
			writeJSONError(w, http.StatusInternalServerError, "Could not import notes")
			return
		}
		id, _ := res.LastInsertId()
		imported = append(imported, importedNote{File: c.file, Title: c.title, ID: id})
		changes = append(changes, undoChange{Table: "notes", ID: int(id), Revision: 1, Values: map[string]any{"archived": 1}})
	}

	result := map[string]any{"imported": imported, "skipped": skipped}
	if len(changes) > 0 {
		// Edited notes keep their place; the rest go back to the archive.
		token, err := recordUndo(tx, undoOperation{Changes: changes, SkipChanged: true})
		if err != nil {
			writeJSONError(w, http.StatusInternalServerError, "Could not import notes")
			return
		}
		result["undo"] = token
	}
	if err := tx.Commit(); err != nil {
		writeJSONError(w, http.StatusInternalServerError, "Could not import notes")
		return
	}
	log.Printf("Notes imported: %d new, %d skipped", len(imported), len(skipped))
	writeJSON(w, http.StatusOK, result)
}
