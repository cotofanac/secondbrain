package main

import (
	"database/sql"
	"time"
)

// validRepeat reports whether rule is one a task can carry. The words read
// naturally after "Repeats"; the empty string means the task does not repeat.
func validRepeat(rule string) bool {
	switch rule {
	case "", "daily", "weekly", "monthly", "yearly":
		return true
	}
	return false
}

// nextDueDate returns the first occurrence after today. It steps from the
// task's due date, so finishing late skips the missed occurrences and a task
// due on the 31st stays at the end of shorter months. An undated task repeats
// from today.
func nextDueDate(due, rule, today string) string {
	anchor, err := time.Parse("2006-01-02", due)
	if err != nil {
		anchor, _ = time.Parse("2006-01-02", today)
	}
	for k := 1; ; k++ {
		var next time.Time
		switch rule {
		case "weekly":
			next = anchor.AddDate(0, 0, 7*k)
		case "monthly":
			next = addMonthsClamped(anchor, k)
		case "yearly":
			next = addMonthsClamped(anchor, 12*k)
		default:
			next = anchor.AddDate(0, 0, k)
		}
		if s := next.Format("2006-01-02"); s > today {
			return s
		}
	}
}

// addMonthsClamped moves a date by whole months, using the last day of the
// target month when it is shorter (Jan 31 + 1 month = Feb 28 or 29).
func addMonthsClamped(t time.Time, months int) time.Time {
	first := time.Date(t.Year(), t.Month()+time.Month(months), 1, 0, 0, 0, 0, time.UTC)
	day := min(t.Day(), first.AddDate(0, 1, -1).Day())
	return time.Date(first.Year(), first.Month(), day, 0, 0, 0, 0, time.UTC)
}

// repeatAfterToggle keeps a repeating task's series in step with its
// checkbox and reports whether the task repeats. Completing it adds the next
// occurrence and returns its due date. Reopening it removes that occurrence
// again if it has not been touched; if it has, the reopened task stops
// repeating so the series is not duplicated.
func repeatAfterToggle(tx *sql.Tx, id int, completing bool, today string) (next string, repeats bool, err error) {
	var category, text, due, rule string
	var project, heading sql.NullInt64
	err = tx.QueryRow(`SELECT category,text,due_date,repeat,project_id,heading_id FROM todos WHERE id=?`, id).
		Scan(&category, &text, &due, &rule, &project, &heading)
	if err != nil || category != "todo" || rule == "" {
		return "", false, err
	}
	if completing {
		next = nextDueDate(due, rule, today)
		_, err = tx.Exec(`INSERT INTO todos(category,text,due_date,project_id,heading_id,repeat,repeated_from) VALUES('todo',?,?,?,?,?,?)`,
			text, next, project, heading, rule, id)
		return next, true, err
	}
	res, err := tx.Exec(`DELETE FROM todos WHERE repeated_from=? AND done=0 AND archived=0 AND revision=1`, id)
	if err != nil {
		return "", true, err
	}
	if n, _ := res.RowsAffected(); n > 0 {
		return "", true, nil
	}
	_, err = tx.Exec(`UPDATE todos SET repeat='' WHERE id=? AND EXISTS(SELECT 1 FROM todos WHERE repeated_from=?)`, id, id)
	return "", true, err
}
