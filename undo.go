package main

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"
)

// Inverses are issued by domain commands, stored server-side and guarded by the
// resulting revision. Undo never replaces an edit made after the original action.
type undoChange struct {
	Table        string
	ID, Revision int
	Values       map[string]any
}
type undoHeading struct {
	ID, ProjectID, Position int
	Name                    string
}
type undoOperation struct {
	Changes     []undoChange
	Heading     *undoHeading
	SkipChanged bool
}

func recordUndo(tx *sql.Tx, operation undoOperation) (string, error) {
	var entropy [24]byte
	if _, err := rand.Read(entropy[:]); err != nil {
		return "", err
	}
	payload, err := json.Marshal(operation)
	if err != nil {
		return "", err
	}
	token := hex.EncodeToString(entropy[:])
	_, err = tx.Exec(`INSERT INTO undo_operations(token,payload,expires_at) VALUES(?,?,?)`, token, string(payload), dbTime(appNow().Add(24*time.Hour)))
	return token, err
}

var undoColumns = map[string]map[string]bool{
	"todos":    {"archived": true, "archived_at": true, "archive_after": true, "heading_id": true, "done": true, "completed_at": true, "due_date": true},
	"notes":    {"archived": true},
	"projects": {"archived": true, "completed": true},
}

func applyUndo(token string) (skipped int, err error) {
	tx, err := db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	var payload string
	var consumed bool
	if err = tx.QueryRow(`SELECT payload,consumed FROM undo_operations WHERE token=? AND expires_at>?`, token, dbTime(appNow())).Scan(&payload, &consumed); err != nil {
		return 0, err
	}
	if consumed {
		return 0, nil
	}
	var operation undoOperation
	if err = json.Unmarshal([]byte(payload), &operation); err != nil {
		return 0, err
	}
	if h := operation.Heading; h != nil {
		var available int
		if err = tx.QueryRow(`SELECT count(*) FROM projects WHERE id=? AND archived=0 AND completed=0`, h.ProjectID).Scan(&available); err != nil {
			return 0, err
		}
		if available != 1 {
			return 0, errConflict
		}
		if err = tx.QueryRow(`SELECT count(*) FROM headings WHERE id=?`, h.ID).Scan(&available); err != nil {
			return 0, err
		}
		if available != 0 {
			return 0, errConflict
		}
		if _, err = tx.Exec(`INSERT INTO headings(id,project_id,name,position) VALUES(?,?,?,?)`, h.ID, h.ProjectID, h.Name, h.Position); err != nil {
			return 0, err
		}
	}
	for _, change := range operation.Changes {
		allowed, ok := undoColumns[change.Table]
		if !ok {
			return 0, fmt.Errorf("unknown undo entity")
		}
		var keys []string
		for key := range change.Values {
			if !allowed[key] {
				return 0, fmt.Errorf("unknown undo field")
			}
			keys = append(keys, key)
		}
		sort.Strings(keys)
		var assignments []string
		var args []any
		for _, key := range keys {
			assignments = append(assignments, key+"=?")
			args = append(args, change.Values[key])
		}
		args = append(args, change.ID, change.Revision)
		res, err := tx.Exec(`UPDATE `+change.Table+` SET `+strings.Join(assignments, ",")+`,revision=revision+1 WHERE id=? AND revision=?`, args...)
		if err != nil {
			return 0, err
		}
		n, err := res.RowsAffected()
		if err != nil {
			return 0, err
		}
		if n != 1 {
			if operation.SkipChanged {
				skipped++
				continue
			}
			return 0, errConflict
		}
	}
	if _, err = tx.Exec(`UPDATE undo_operations SET consumed=1 WHERE token=?`, token); err != nil {
		return 0, err
	}
	return skipped, tx.Commit()
}

func handleUndo(w http.ResponseWriter, r *http.Request) {
	if !requirePost(w, r) {
		return
	}
	skipped, err := applyUndo(r.FormValue("token"))
	if err != nil {
		writeDomainError(w, err, "Could not undo")
		return
	}
	mutationEffects(w, "todos", "today", "notes", "sidebar")
	writeJSON(w, http.StatusOK, map[string]any{"status": "undone", "skipped": skipped})
}

// Both HTMX and fetch consumers read the same effects header. The response's
// target is current; other views become dirty until they next appear.
func mutationEffects(w http.ResponseWriter, views ...string) {
	payload, _ := json.Marshal(views)
	w.Header().Set("X-SB-Effects", string(payload))
}
