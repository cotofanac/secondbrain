package main

import (
	"database/sql"
	"time"
)

type taskCommandResult struct {
	Task    Todo
	Next    string
	Repeats bool
	Undo    string
}

// A completion is a desired state. Delivering the same command twice has no
// second effect, including no duplicate repeat occurrence.
func setTaskDone(id int, desired *bool, expected int, now time.Time) (taskCommandResult, error) {
	var result taskCommandResult
	tx, err := db.Begin()
	if err != nil {
		return result, err
	}
	defer tx.Rollback()
	t := &result.Task
	err = tx.QueryRow(`SELECT id,category,COALESCE(project_id,0),COALESCE(heading_id,0),done,revision FROM todos t WHERE id=? AND `+activeTaskSQL, id).
		Scan(&t.ID, &t.Category, &t.ProjectID, &t.HeadingID, &t.Done, &t.Revision)
	if err != nil {
		return result, err
	}
	done := !t.Done
	if desired != nil {
		done = *desired
	}
	if t.Done == done {
		return result, nil
	}
	if expected > 0 && expected != t.Revision {
		return result, errConflict
	}
	var completed, archiveAfter any
	if done && t.Category == "todo" {
		completed = dbTime(now)
		archiveAfter = dbTime(now.AddDate(0, 0, 7))
	}
	if _, err = tx.Exec(`UPDATE todos SET done=?,completed_at=?,archive_after=?,revision=revision+1 WHERE id=?`, done, completed, archiveAfter, id); err != nil {
		return result, err
	}
	result.Next, result.Repeats, err = repeatAfterToggle(tx, id, done, now.Format("2006-01-02"))
	if err != nil {
		return result, err
	}
	t.Done = done
	t.Revision++
	return result, tx.Commit()
}

func archiveTask(id int) (taskCommandResult, error) {
	var result taskCommandResult
	tx, err := db.Begin()
	if err != nil {
		return result, err
	}
	defer tx.Rollback()
	t := &result.Task
	var archivedAt sql.NullString
	err = tx.QueryRow(`SELECT id,category,COALESCE(project_id,0),COALESCE(heading_id,0),revision,archived_at FROM todos t WHERE id=? AND `+activeTaskSQL, id).
		Scan(&t.ID, &t.Category, &t.ProjectID, &t.HeadingID, &t.Revision, &archivedAt)
	if err != nil {
		return result, err
	}
	if _, err = tx.Exec(`UPDATE todos SET archived=1,archived_at=?,revision=revision+1 WHERE id=?`, dbTime(appNow()), id); err != nil {
		return result, err
	}
	var at any
	if archivedAt.Valid {
		at = archivedAt.String
	}
	result.Undo, err = recordUndo(tx, undoOperation{Changes: []undoChange{{Table: "todos", ID: id, Revision: t.Revision + 1, Values: map[string]any{"archived": 0, "archived_at": at}}}})
	if err != nil {
		return result, err
	}
	return result, tx.Commit()
}
