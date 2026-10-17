package main

import (
	"bytes"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"testing"
)

func postImport(t *testing.T, files map[string]string, order ...string) (*httptest.ResponseRecorder, map[string]any) {
	t.Helper()
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	for _, name := range order {
		part, err := mw.CreateFormFile("file", name)
		if err != nil {
			t.Fatal(err)
		}
		part.Write([]byte(files[name]))
	}
	mw.Close()
	req := httptest.NewRequest(http.MethodPost, "/notes/import", &body)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	rec := httptest.NewRecorder()
	handleImportNotes(rec, req)
	var result map[string]any
	if rec.Code == http.StatusOK {
		if err := json.Unmarshal(rec.Body.Bytes(), &result); err != nil {
			t.Fatalf("decode %q: %v", rec.Body.String(), err)
		}
	}
	return rec, result
}

func TestImportNotesCreatesSkipsAndUndoes(t *testing.T) {
	setupTestDB(t)
	if _, err := db.Exec("INSERT INTO notes (title, content) VALUES ('Existing', 'keep'), ('Old', 'archived text')"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("UPDATE notes SET archived=1 WHERE title='Old'"); err != nil {
		t.Fatal(err)
	}

	files := map[string]string{
		"Plans.md":     "\xef\xbb\xbf# Plans\r\n\r\n- one\r\n",
		"Recipes.md":   "soup",
		"Existing.md":  "replace me",
		"Old.md":       "replace me too",
		"photo.png":    "not text",
		"dir/Plans.md": "second copy",
	}
	rec, result := postImport(t, files, "Plans.md", "Recipes.md", "Existing.md", "Old.md", "photo.png", "dir/Plans.md")
	if rec.Code != http.StatusOK {
		t.Fatalf("import = %d %q", rec.Code, rec.Body.String())
	}
	if got := len(result["imported"].([]any)); got != 2 {
		t.Fatalf("imported %d notes, want 2: %v", got, result)
	}
	if got := len(result["skipped"].([]any)); got != 4 {
		t.Fatalf("skipped %d files, want 4: %v", got, result)
	}

	var content string
	if err := db.QueryRow("SELECT content FROM notes WHERE title='Plans'").Scan(&content); err != nil {
		t.Fatal(err)
	}
	if content != "# Plans\n\n- one\n" {
		t.Errorf("content = %q, want BOM and CRLF normalized", content)
	}
	if err := db.QueryRow("SELECT content FROM notes WHERE title='Existing'").Scan(&content); err != nil || content != "keep" {
		t.Errorf("existing note changed: %q %v", content, err)
	}
	if err := db.QueryRow("SELECT content FROM notes WHERE title='Old'").Scan(&content); err != nil || content != "archived text" {
		t.Errorf("archived note changed: %q %v", content, err)
	}

	// An edit after the import keeps that note when the import is undone.
	if _, err := db.Exec("UPDATE notes SET content='edited', revision=revision+1 WHERE title='Recipes'"); err != nil {
		t.Fatal(err)
	}
	skipped, err := applyUndo(result["undo"].(string))
	if err != nil || skipped != 1 {
		t.Fatalf("undo skipped=%d err=%v, want 1 skipped", skipped, err)
	}
	var archived int
	db.QueryRow("SELECT archived FROM notes WHERE title='Plans'").Scan(&archived)
	if archived != 1 {
		t.Error("undo should archive the imported note")
	}
	db.QueryRow("SELECT archived FROM notes WHERE title='Recipes'").Scan(&archived)
	if archived != 0 {
		t.Error("undo should keep a note edited since the import")
	}

	// Running the same import again changes nothing.
	_, again := postImport(t, map[string]string{"Plans.md": "x"}, "Plans.md")
	if len(again["imported"].([]any)) != 0 || again["undo"] != nil {
		t.Errorf("re-import should skip: %v", again)
	}
}

func TestImportNotesRejectsEmptyRequest(t *testing.T) {
	setupTestDB(t)
	rec, _ := postImport(t, nil)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("empty import = %d, want 400", rec.Code)
	}
}

func TestImportUndoKeepsOneNote(t *testing.T) {
	setupTestDB(t)
	_, result := postImport(t, map[string]string{"Only.md": "x"}, "Only.md")
	if _, err := db.Exec("UPDATE notes SET archived=1, revision=revision+1 WHERE title<>'Only'"); err != nil {
		t.Fatal(err)
	}
	if _, err := applyUndo(result["undo"].(string)); err == nil {
		t.Fatal("undo should refuse to archive the last active note")
	}
	var active int
	db.QueryRow("SELECT count(*) FROM notes WHERE archived=0").Scan(&active)
	if active != 1 {
		t.Errorf("active notes = %d, want 1", active)
	}
}
