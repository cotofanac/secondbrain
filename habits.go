package main

import (
	"database/sql"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"
)

type Habit struct {
	ID          int
	Name        string
	Period      string // day | week | month | year
	Target      int
	Progress    int    // completions in the current period (for day: today's count)
	Percent     int    // min(100, Progress*100/Target); progress-bar width
	Done        bool   // Progress >= Target
	Streak      int    // consecutive completed periods
	StreakLabel string // "12d" / "4w" / "3mo" / "" (empty for year, or streak 0)
	Total       int    // lifetime completions (sum of counts); used in the archive
}

// HabitsView groups habits by period for the unified Habits view.
type HabitsView struct {
	Daily   []Habit
	Weekly  []Habit
	Monthly []Habit
	Yearly  []Habit
}

func groupHabits(hs []Habit) HabitsView {
	var v HabitsView
	for _, h := range hs {
		switch h.Period {
		case "week":
			v.Weekly = append(v.Weekly, h)
		case "month":
			v.Monthly = append(v.Monthly, h)
		case "year":
			v.Yearly = append(v.Yearly, h)
		default:
			v.Daily = append(v.Daily, h)
		}
	}
	return v
}

// habitLog is one stored completion row: a date and how many completions
// landed on it (count lets several completions share a calendar day).
type habitLog struct {
	date  string
	count int
}

// startOfPeriod returns the first day of the period containing t, as a
// date-only time in t's location. Weeks start Monday (ISO 8601).
func startOfPeriod(t time.Time, period string) time.Time {
	y, m, d := t.Date()
	loc := t.Location()
	switch period {
	case "week":
		offset := (int(t.Weekday()) + 6) % 7 // days since Monday
		return time.Date(y, m, d, 0, 0, 0, 0, loc).AddDate(0, 0, -offset)
	case "month":
		return time.Date(y, m, 1, 0, 0, 0, 0, loc)
	case "year":
		return time.Date(y, 1, 1, 0, 0, 0, 0, loc)
	default: // day
		return time.Date(y, m, d, 0, 0, 0, 0, loc)
	}
}

// prevPeriodStart returns the first day of the period immediately before the
// one beginning at start (which must already be a period start).
func prevPeriodStart(start time.Time, period string) time.Time {
	switch period {
	case "week":
		return start.AddDate(0, 0, -7)
	case "month":
		return start.AddDate(0, -1, 0)
	case "year":
		return start.AddDate(-1, 0, 0)
	default:
		return start.AddDate(0, 0, -1)
	}
}

// periodKey is a stable bucket identifier for the period containing t, used to
// sum completions per period. Weeks use ISO 8601 week numbering (Monday-based),
// consistent with startOfPeriod.
func periodKey(t time.Time, period string) string {
	switch period {
	case "week":
		y, w := t.ISOWeek()
		return fmt.Sprintf("%04d-W%02d", y, w)
	case "month":
		return t.Format("2006-01")
	case "year":
		return t.Format("2006")
	default:
		return t.Format("2006-01-02")
	}
}

// periodStreak counts consecutive periods (ending at now's period) whose summed
// completions in sums meet target. The current period counts if already met,
// but an as-yet-unmet current period does not break the run (grace), mirroring
// the daily today-or-yesterday grace.
func periodStreak(sums map[string]int, now time.Time, period string, target int) int {
	streak := 0
	cur := startOfPeriod(now, period)
	if sums[periodKey(cur, period)] >= target {
		streak++
	}
	for q := prevPeriodStart(cur, period); sums[periodKey(q, period)] >= target; q = prevPeriodStart(q, period) {
		streak++
	}
	return streak
}

func loadHabits() []Habit { return loadHabitsAt(appNow()) }
func loadHabitsAt(now time.Time) []Habit {
	today := now.Format("2006-01-02")
	rows, err := db.Query(`
		SELECT id, name, period, target
		FROM habits
		WHERE archived = 0
		ORDER BY created_at ASC
	`)
	if err != nil {
		return nil
	}
	defer rows.Close()

	var habits []Habit
	for rows.Next() {
		var h Habit
		rows.Scan(&h.ID, &h.Name, &h.Period, &h.Target)
		if h.Target < 1 {
			h.Target = 1
		}
		habits = append(habits, h)
	}
	if len(habits) == 0 {
		return habits
	}

	// Only the recent unbroken run matters for streaks; a three-year floor keeps
	// this read bounded as habit_logs grows, while still covering long weekly
	// (up to ~156w) and monthly (up to ~36mo) streaks. The bound is computed in
	// Go (local time) to match the local "today" used everywhere else — SQLite's
	// date('now') is UTC and would drift the boundary near midnight.
	floor := now.AddDate(-3, 0, 0).Format("2006-01-02")
	logRows, err := db.Query(`
		SELECT habit_id, date, count FROM habit_logs
		WHERE habit_id IN (SELECT id FROM habits WHERE archived = 0)
		  AND date >= ?
		ORDER BY habit_id ASC, date DESC
	`, floor)
	if err != nil {
		return habits
	}
	defer logRows.Close()

	logsMap := make(map[int][]habitLog)
	for logRows.Next() {
		var hid, cnt int
		var d string
		logRows.Scan(&hid, &d, &cnt)
		logsMap[hid] = append(logsMap[hid], habitLog{date: d, count: cnt})
	}

	yesterday := now.AddDate(0, 0, -1).Format("2006-01-02")
	for i := range habits {
		h := &habits[i]
		logs := logsMap[h.ID]

		// Current-period progress: logs are DESC by date, so sum counts until we
		// fall out of the current period.
		startStr := startOfPeriod(now, h.Period).Format("2006-01-02")
		for _, l := range logs {
			if l.date < startStr {
				break
			}
			h.Progress += l.count
		}
		h.Done = h.Progress >= h.Target
		if p := h.Progress * 100 / h.Target; p > 100 {
			h.Percent = 100
		} else {
			h.Percent = p
		}

		switch h.Period {
		case "week", "month":
			// Streak = consecutive periods meeting target, with grace: an
			// as-yet-unmet current period does not break the run.
			sums := make(map[string]int)
			for _, l := range logs {
				t, err := time.Parse("2006-01-02", l.date)
				if err != nil {
					continue
				}
				sums[periodKey(t, h.Period)] += l.count
			}
			streak := periodStreak(sums, now, h.Period, h.Target)
			h.Streak = streak
			if streak > 0 {
				unit := "w"
				if h.Period == "month" {
					unit = "mo"
				}
				h.StreakLabel = fmt.Sprintf("%d%s", streak, unit)
			}
		case "year":
			// No streak for yearly goals — not meaningful.
		default:
			// Daily: presence-based streak with today-or-yesterday grace.
			if len(logs) == 0 || (logs[0].date != today && logs[0].date != yesterday) {
				break
			}
			streak := 0
			expected := logs[0].date
			for _, l := range logs {
				if l.date != expected {
					break
				}
				streak++
				t, _ := time.Parse("2006-01-02", expected)
				expected = t.AddDate(0, 0, -1).Format("2006-01-02")
			}
			h.Streak = streak
			if streak > 0 {
				h.StreakLabel = fmt.Sprintf("%dd", streak)
			}
		}
	}
	return habits
}

func handleHabits(w http.ResponseWriter, r *http.Request) {
	renderTemplate(w, r, "habit-list", groupHabits(loadHabits()))
}

func handleAddHabit(w http.ResponseWriter, r *http.Request) {
	if !requirePost(w, r) {
		return
	}

	name := strings.TrimSpace(r.FormValue("name"))
	if name == "" || len(name) > 120 {
		http.Error(w, "Invalid habit name", http.StatusBadRequest)
		return
	}
	if !validNameRe.MatchString(name) {
		http.Error(w, "Invalid characters in habit name", http.StatusBadRequest)
		return
	}

	period := r.FormValue("period")
	if period == "" {
		period = "day"
	}
	if period != "day" && period != "week" && period != "month" && period != "year" {
		http.Error(w, "Invalid period", http.StatusBadRequest)
		return
	}

	// Daily habits are a single toggle, so their target is always 1; only
	// periodic goals carry a numeric target.
	target := 1
	if period != "day" {
		if v := strings.TrimSpace(r.FormValue("target")); v != "" {
			n, err := strconv.Atoi(v)
			if err != nil || n < 1 || n > 1000 {
				http.Error(w, "Invalid target", http.StatusBadRequest)
				return
			}
			target = n
		}
	}

	// name is UNIQUE across archived rows too — see the matching comment in
	// handleCreateNote. Reviving the archived habit also keeps its logged
	// history, which is what the archive exists to preserve.
	tx, err := db.Begin()
	if err != nil {
		http.Error(w, "DB error", http.StatusInternalServerError)
		return
	}
	defer tx.Rollback()

	var existingID int64
	var archived int
	var restored bool
	err = tx.QueryRow("SELECT id, archived FROM habits WHERE name = ?", name).
		Scan(&existingID, &archived)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		if _, ierr := tx.Exec(
			"INSERT INTO habits (name, period, target) VALUES (?, ?, ?)", name, period, target,
		); ierr != nil {
			http.Error(w, "DB error", http.StatusInternalServerError)
			return
		}
	case err != nil:
		http.Error(w, "DB error", http.StatusInternalServerError)
		return
	case archived == 1:
		// Adopt the period/target just chosen, so reviving doubles as an edit.
		if _, uerr := tx.Exec(
			"UPDATE habits SET archived = 0, period = ?, target = ? WHERE id = ?",
			period, target, existingID,
		); uerr != nil {
			http.Error(w, "DB error", http.StatusInternalServerError)
			return
		}
		restored = true
	default:
		http.Error(w, "A habit with this name already exists", http.StatusConflict)
		return
	}

	if err := tx.Commit(); err != nil {
		http.Error(w, "DB error", http.StatusInternalServerError)
		return
	}

	if restored {
		log.Printf("Habit restored from archive: id=%d", existingID)
		hxTrigger(w, "sbNotice", map[string]string{
			"message": "Restored an archived habit with that name",
		})
	} else {
		log.Printf("Habit added: period=%s target=%d", period, target)
	}

	if r.Header.Get("HX-Target") == "today-content" {
		handleToday(w, r)
		return
	}
	handleHabits(w, r)
}

func handleRenameHabit(w http.ResponseWriter, r *http.Request) {
	if !requirePost(w, r) {
		return
	}

	id, ok := parseID(w, r)
	if !ok {
		return
	}

	name := strings.TrimSpace(r.FormValue("name"))
	if name == "" || len(name) > 120 {
		http.Error(w, "Invalid habit name", http.StatusBadRequest)
		return
	}
	if !validNameRe.MatchString(name) {
		http.Error(w, "Invalid characters in habit name", http.StatusBadRequest)
		return
	}

	_, err := db.Exec("UPDATE habits SET name = ? WHERE id = ?", name, id)
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			http.Error(w, nameConflictMessage("habits", "name", name, "habit"), http.StatusConflict)
			return
		}
		http.Error(w, "DB error", http.StatusInternalServerError)
		return
	}
	log.Printf("Habit renamed: id=%d", id)

	handleHabits(w, r)
}

func handleUpdateHabit(w http.ResponseWriter, r *http.Request) {
	if !requirePost(w, r) {
		return
	}
	id, ok := parseID(w, r)
	if !ok {
		return
	}
	name := strings.TrimSpace(r.FormValue("name"))
	if name == "" || len(name) > 120 || !validNameRe.MatchString(name) {
		http.Error(w, "Invalid habit name", http.StatusBadRequest)
		return
	}
	period := r.FormValue("period")
	if period != "day" && period != "week" && period != "month" && period != "year" {
		http.Error(w, "Invalid period", http.StatusBadRequest)
		return
	}
	target := 1
	if period != "day" {
		var err error
		target, err = strconv.Atoi(r.FormValue("target"))
		if err != nil || target < 1 || target > 1000 {
			http.Error(w, "Invalid target", http.StatusBadRequest)
			return
		}
	}
	res, err := db.Exec("UPDATE habits SET name=?,period=?,target=? WHERE id=? AND archived=0", name, period, target, id)
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			http.Error(w, nameConflictMessage("habits", "name", name, "habit"), http.StatusConflict)
			return
		}
		http.Error(w, "DB error", http.StatusInternalServerError)
		return
	}
	if n, _ := res.RowsAffected(); n == 0 {
		http.Error(w, "Habit not found", http.StatusNotFound)
		return
	}
	log.Printf("Habit updated: id=%d period=%s target=%d", id, period, target)
	handleHabits(w, r)
}

func handleToggleHabit(w http.ResponseWriter, r *http.Request) {
	if !requirePost(w, r) {
		return
	}

	id, ok := parseID(w, r)
	if !ok {
		return
	}

	// Toggle is for daily habits only; refuse it for periodic goals so a stale
	// or crafted request can never wipe a day that holds multiple completions.
	tx, err := db.Begin()
	if err != nil {
		http.Error(w, "DB error", http.StatusInternalServerError)
		return
	}
	defer tx.Rollback()
	var period string
	if err := tx.QueryRow("SELECT period FROM habits WHERE id = ?", id).Scan(&period); err != nil {
		http.Error(w, "Not found", http.StatusNotFound)
		return
	}
	if period != "day" {
		http.Error(w, "Not a daily habit", http.StatusBadRequest)
		return
	}

	today := appNow().Format("2006-01-02")
	res, err := tx.Exec("DELETE FROM habit_logs WHERE habit_id = ? AND date = ?", id, today)
	if err != nil {
		http.Error(w, "DB error", http.StatusInternalServerError)
		return
	}

	n, err := res.RowsAffected()
	if err != nil {
		http.Error(w, "DB error", http.StatusInternalServerError)
		return
	}
	if n == 0 {
		_, err = tx.Exec("INSERT INTO habit_logs (habit_id, date, count) VALUES (?, ?, 1)", id, today)
		if err != nil {
			http.Error(w, "DB error", http.StatusInternalServerError)
			return
		}
		log.Printf("Habit checked: id=%d date=%s", id, today)
	} else {
		log.Printf("Habit unchecked: id=%d date=%s", id, today)
	}
	if err = tx.Commit(); err != nil {
		http.Error(w, "DB error", http.StatusInternalServerError)
		return
	}

	renderHabitMutation(w, r)
}

func renderHabitMutation(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("HX-Target") == "today-content" {
		handleToday(w, r)
		return
	}
	handleHabits(w, r)
}

// handleIncrementHabit adds one completion to a periodic goal for today,
// accumulating on the single (habit_id, today) row so several completions can
// share a calendar day (e.g. finishing two books in one day for a yearly goal).
func handleIncrementHabit(w http.ResponseWriter, r *http.Request) {
	if !requirePost(w, r) {
		return
	}

	id, ok := parseID(w, r)
	if !ok {
		return
	}

	// Daily habits are a toggle (see handleToggleHabit); stacking counts on them
	// would let one untoggle erase several check-ins.
	tx, err := db.Begin()
	if err != nil {
		http.Error(w, "DB error", http.StatusInternalServerError)
		return
	}
	defer tx.Rollback()
	var period string
	if err := tx.QueryRow("SELECT period FROM habits WHERE id = ? AND archived = 0", id).Scan(&period); err != nil {
		http.Error(w, "Not found", http.StatusNotFound)
		return
	}
	if period == "day" {
		http.Error(w, "Not a periodic goal", http.StatusBadRequest)
		return
	}

	today := appNow().Format("2006-01-02")
	if _, err = tx.Exec(`
		INSERT INTO habit_logs (habit_id, date, count) VALUES (?, ?, 1)
		ON CONFLICT(habit_id, date) DO UPDATE SET count = count + 1
	`, id, today); err != nil {
		http.Error(w, "DB error", http.StatusInternalServerError)
		return
	}
	if err = tx.Commit(); err != nil {
		http.Error(w, "DB error", http.StatusInternalServerError)
		return
	}
	log.Printf("Habit incremented: id=%d date=%s", id, today)

	renderHabitMutation(w, r)
}

// handleDecrementHabit undoes the most recent completion of a periodic goal
// within the current period, so a check-in logged on an earlier day of the
// period is still undoable. It floors at zero (no rows → no-op).
func handleDecrementHabit(w http.ResponseWriter, r *http.Request) {
	if !requirePost(w, r) {
		return
	}

	id, ok := parseID(w, r)
	if !ok {
		return
	}

	tx, err := db.Begin()
	if err != nil {
		http.Error(w, "DB error", http.StatusInternalServerError)
		return
	}
	defer tx.Rollback()
	var period string
	if err := tx.QueryRow("SELECT period FROM habits WHERE id = ?", id).Scan(&period); err != nil {
		http.Error(w, "Not found", http.StatusNotFound)
		return
	}
	startStr := startOfPeriod(appNow(), period).Format("2006-01-02")

	var date string
	var count int
	err = tx.QueryRow(`
		SELECT date, count FROM habit_logs
		WHERE habit_id = ? AND date >= ?
		ORDER BY date DESC LIMIT 1
	`, id, startStr).Scan(&date, &count)
	if errors.Is(err, sql.ErrNoRows) {
		tx.Rollback()
		renderHabitMutation(w, r) // nothing to undo this period
		return
	}
	if err != nil {
		http.Error(w, "DB error", http.StatusInternalServerError)
		return
	}

	if count <= 1 {
		_, err = tx.Exec("DELETE FROM habit_logs WHERE habit_id = ? AND date = ?", id, date)
	} else {
		_, err = tx.Exec("UPDATE habit_logs SET count = count - 1 WHERE habit_id = ? AND date = ?", id, date)
	}
	if err != nil {
		http.Error(w, "DB error", http.StatusInternalServerError)
		return
	}
	if err = tx.Commit(); err != nil {
		http.Error(w, "DB error", http.StatusInternalServerError)
		return
	}
	log.Printf("Habit decremented: id=%d date=%s", id, date)

	renderHabitMutation(w, r)
}

func handleDeleteHabit(w http.ResponseWriter, r *http.Request) {
	if !requirePost(w, r) {
		return
	}

	id, ok := parseID(w, r)
	if !ok {
		return
	}

	_, err := db.Exec("UPDATE habits SET archived = 1 WHERE id = ?", id)
	if err != nil {
		http.Error(w, "DB error", http.StatusInternalServerError)
		return
	}
	log.Printf("Habit archived: id=%d", id)

	// Same one-tap, no-confirm path as archiving a task.
	hxTrigger(w, "sbUndo", map[string]any{"kind": "habit", "id": id})

	handleHabits(w, r)
}

func handleArchiveHabits(w http.ResponseWriter, r *http.Request) {
	rows, err := db.Query(`
		SELECT h.id, h.name, h.period, h.target,
		       COALESCE((SELECT SUM(count) FROM habit_logs WHERE habit_id = h.id), 0) AS total
		FROM habits h
		WHERE h.archived = 1
		ORDER BY h.created_at DESC
		LIMIT 200
	`)
	if err != nil {
		http.Error(w, "DB error", http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	var habits []Habit
	for rows.Next() {
		var h Habit
		rows.Scan(&h.ID, &h.Name, &h.Period, &h.Target, &h.Total)
		habits = append(habits, h)
	}

	renderTemplate(w, r, "habits-archive.html", habits)
}

func handleRestoreHabit(w http.ResponseWriter, r *http.Request) {
	if !requirePost(w, r) {
		return
	}

	id, ok := parseID(w, r)
	if !ok {
		return
	}

	_, err := db.Exec("UPDATE habits SET archived = 0 WHERE id = ?", id)
	if err != nil {
		http.Error(w, "DB error", http.StatusInternalServerError)
		return
	}
	log.Printf("Habit restored: id=%d", id)

	// See handleRestoreTodo: an undo from the toast wants the live list back.
	if r.FormValue("return") == "list" {
		handleHabits(w, r)
		return
	}
	handleArchiveHabits(w, r)
}

func handlePermanentDeleteHabit(w http.ResponseWriter, r *http.Request) {
	if !requirePost(w, r) {
		return
	}

	id, ok := parseID(w, r)
	if !ok {
		return
	}

	_, err := db.Exec("DELETE FROM habits WHERE id = ? AND archived = 1", id)
	if err != nil {
		http.Error(w, "DB error", http.StatusInternalServerError)
		return
	}

	handleArchiveHabits(w, r)
}
