package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
	_ "time/tzdata"
)

var calendarZone atomic.Pointer[time.Location]

func appLocation() *time.Location {
	if loc := calendarZone.Load(); loc != nil {
		return loc
	}
	loc, _ := time.LoadLocation("Europe/Bucharest")
	return loc
}
func appNow() time.Time { return time.Now().In(appLocation()) }

type ScheduleSettings struct {
	TaskEnabled, HabitEnabled     bool
	TaskTime, HabitTime, Timezone string
}

func initScheduleSettings() {
	defaults := map[string]string{"task_enabled": strconv.FormatBool(taskReminder.enabled), "habit_enabled": strconv.FormatBool(habitReminder.enabled), "review_enabled": "false", "task_time": fmt.Sprintf("%02d:%02d", taskReminder.hour, taskReminder.min), "habit_time": fmt.Sprintf("%02d:%02d", habitReminder.hour, habitReminder.min), "timezone": "Europe/Bucharest", "queued_task": getSetting("task_reminder_sent"), "queued_habit": getSetting("habit_reminder_sent")}
	for k, v := range defaults {
		if _, err := db.Exec(`INSERT OR IGNORE INTO settings(key,value) VALUES(?,?)`, k, v); err != nil {
			log.Fatalf("Initialize schedules: %v", err)
		}
	}
	// Weekly reviews are historical only. Disable a legacy installation's
	// setting without deleting its saved reviews or delivery history.
	if _, err := db.Exec(`UPDATE settings SET value='false' WHERE key='review_enabled'`); err != nil {
		log.Fatal(err)
	}
	settings, err := loadSchedule()
	if err != nil {
		log.Fatal(err)
	}
	loc, err := time.LoadLocation(settings.Timezone)
	if err != nil {
		log.Fatal(err)
	}
	calendarZone.Store(loc)
}
func loadSchedule() (ScheduleSettings, error) {
	var s ScheduleSettings
	values := map[string]string{}
	rows, err := db.Query(`SELECT key,value FROM settings WHERE key IN ('task_enabled','habit_enabled','task_time','habit_time','timezone')`)
	if err != nil {
		return s, err
	}
	defer rows.Close()
	for rows.Next() {
		var k, v string
		if err = rows.Scan(&k, &v); err != nil {
			return s, err
		}
		values[k] = v
	}
	if err = rows.Err(); err != nil {
		return s, err
	}
	s.TaskEnabled = values["task_enabled"] == "true"
	s.HabitEnabled = values["habit_enabled"] == "true"
	s.TaskTime = values["task_time"]
	s.HabitTime = values["habit_time"]
	s.Timezone = values["timezone"]
	return s, nil
}
func registerScheduleRoutes() {
	http.HandleFunc("/settings", authMiddleware(handleSettings))
	http.HandleFunc("/settings/save", authMiddleware(handleSaveSettings))
	http.HandleFunc("/today", authMiddleware(handleToday))
	http.HandleFunc("/today/reschedule", authMiddleware(handleTodayReschedule))
	http.HandleFunc("/review", authMiddleware(handleReview))
	http.HandleFunc("/push/status", authMiddleware(handlePushStatus))
}

type TodayItem struct {
	ID, Revision                int
	Text, Date, Today, Tomorrow string
	Project, Stage              string
}

type TodayView struct {
	Date, ISODate               string
	Overdue, Due, Upcoming      []TodayItem
	Suggestions                 []TodayItem
	DailyHabits, PeriodicHabits []Habit
	CompletedToday, DueLeft     int
	DailyDone, DailyTotal       int
}

func scanTodayItems(query string, args ...any) ([]TodayItem, error) {
	rows, err := db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var items []TodayItem
	for rows.Next() {
		var item TodayItem
		if err = rows.Scan(&item.ID, &item.Text, &item.Date, &item.Revision, &item.Project, &item.Stage); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func loadToday(now time.Time) (TodayView, error) {
	today := now.Format("2006-01-02")
	tomorrow := now.AddDate(0, 0, 1).Format("2006-01-02")
	limit := now.AddDate(0, 0, 8).Format("2006-01-02")
	view := TodayView{Date: now.Format("Monday, 2 January"), ISODate: today}
	base := `SELECT t.id,t.text,t.due_date,t.revision,COALESCE(p.name,''),COALESCE(s.name,'')
		FROM todos t LEFT JOIN projects p ON p.id=t.project_id LEFT JOIN stages s ON s.id=t.stage_id
		WHERE t.category='todo' AND t.done=0 AND ` + activeTaskSQL
	var err error
	view.Overdue, err = scanTodayItems(base+` AND t.due_date!='' AND t.due_date<? ORDER BY t.due_date,t.position,t.id`, today)
	if err != nil {
		return view, err
	}
	view.Due, err = scanTodayItems(base+` AND t.due_date=? ORDER BY t.position,t.id`, today)
	if err != nil {
		return view, err
	}
	view.Upcoming, err = scanTodayItems(base+` AND t.due_date>? AND t.due_date<? ORDER BY t.due_date,t.position,t.id`, today, limit)
	if err != nil {
		return view, err
	}
	// Suggestions are deterministic and transparent: one unscheduled task from
	// each project first, followed by standalone tasks, in the user's list order.
	view.Suggestions, err = scanTodayItems(base + ` AND t.due_date='' AND (t.project_id IS NULL OR t.id=(
		SELECT t2.id FROM todos t2 WHERE t2.project_id=t.project_id AND t2.category='todo' AND t2.done=0
		AND t2.archived=0 AND t2.due_date='' AND (t2.stage_id IS NULL OR EXISTS(
			SELECT 1 FROM stages s2 WHERE s2.id=t2.stage_id AND s2.archived=0))
		ORDER BY t2.position,t2.id LIMIT 1))
		ORDER BY CASE WHEN t.project_id IS NULL THEN 1 ELSE 0 END,COALESCE(p.position,0),t.position,t.id LIMIT 3`)
	if err != nil {
		return view, err
	}
	for _, list := range [][]TodayItem{view.Overdue, view.Due, view.Upcoming, view.Suggestions} {
		for i := range list {
			list[i].Today, list[i].Tomorrow = today, tomorrow
		}
	}
	if err = db.QueryRow(`SELECT COUNT(*) FROM todos WHERE category='todo' AND done=1 AND completed_at>=? AND completed_at<?`,
		time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location()).UTC().Format(time.RFC3339),
		time.Date(now.Year(), now.Month(), now.Day()+1, 0, 0, 0, 0, now.Location()).UTC().Format(time.RFC3339)).Scan(&view.CompletedToday); err != nil {
		return view, err
	}
	view.DueLeft = len(view.Overdue) + len(view.Due)
	for _, habit := range loadHabitsAt(now) {
		if habit.Period == "day" {
			view.DailyHabits = append(view.DailyHabits, habit)
			view.DailyTotal++
			if habit.Done {
				view.DailyDone++
			}
		} else {
			view.PeriodicHabits = append(view.PeriodicHabits, habit)
		}
	}
	return view, nil
}

func handleToday(w http.ResponseWriter, r *http.Request) {
	view, err := loadToday(appNow())
	if err != nil {
		http.Error(w, "Today unavailable", http.StatusInternalServerError)
		return
	}
	renderTemplate(w, "today.html", view)
}
func handleTodayReschedule(w http.ResponseWriter, r *http.Request) {
	if !requirePost(w, r) {
		return
	}
	id, ok := parseID(w, r)
	if !ok {
		return
	}
	revision, err := strconv.Atoi(r.FormValue("revision"))
	if err != nil {
		http.Error(w, "Invalid revision", 400)
		return
	}
	due := r.FormValue("due_date")
	if due != "" {
		if _, err = time.Parse("2006-01-02", due); err != nil {
			http.Error(w, "Invalid date", 400)
			return
		}
	}
	result, err := db.Exec(`UPDATE todos AS t SET due_date=?,revision=revision+1 WHERE id=? AND revision=? AND category='todo' AND done=0 AND `+activeTaskSQL, due, id, revision)
	if err != nil {
		http.Error(w, "Could not reschedule task", 500)
		return
	}
	changed, _ := result.RowsAffected()
	if changed == 0 {
		http.Error(w, "This task changed elsewhere. Refresh and try again.", 409)
		return
	}
	hxTrigger(w, "sbWorkspaceChanged", map[string]any{"message": "Task scheduled"})
	handleToday(w, r)
}
func handleSettings(w http.ResponseWriter, r *http.Request) {
	s, err := loadSchedule()
	if err != nil {
		http.Error(w, "Could not load settings", 500)
		return
	}
	renderTemplate(w, "settings.html", s)
}
func handleSaveSettings(w http.ResponseWriter, r *http.Request) {
	if !requirePost(w, r) {
		return
	}
	zone := strings.TrimSpace(r.FormValue("timezone"))
	if zone == "" || zone == "Local" {
		http.Error(w, "Enter a named time zone, for example Europe/Bucharest", 400)
		return
	}
	loc, err := time.LoadLocation(zone)
	if err != nil {
		http.Error(w, "Choose a valid time zone, for example Europe/Bucharest", 400)
		return
	}
	vals := map[string]string{"timezone": loc.String()}
	for _, kind := range []string{"task", "habit"} {
		raw := r.FormValue(kind + "_time")
		rt, e := parseReminderTime(raw, reminderTime{})
		if e != nil || !rt.enabled {
			http.Error(w, "Enter a valid time for each reminder", 400)
			return
		}
		vals[kind+"_time"] = fmt.Sprintf("%02d:%02d", rt.hour, rt.min)
		vals[kind+"_enabled"] = strconv.FormatBool(r.FormValue(kind+"_enabled") == "on")
	}
	tx, err := db.Begin()
	if err != nil {
		http.Error(w, "DB error", 500)
		return
	}
	defer tx.Rollback()
	vals["review_enabled"] = "false"
	for k, v := range vals {
		if _, err = tx.Exec(`INSERT INTO settings(key,value) VALUES(?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value`, k, v); err != nil {
			http.Error(w, "Could not save settings", 500)
			return
		}
	}
	if err = tx.Commit(); err != nil {
		http.Error(w, "Could not save settings", 500)
		return
	}
	calendarZone.Store(loc)
	hxTrigger(w, "sbNotice", map[string]any{"message": "Settings saved"})
	handleSettings(w, r)
}

type ReviewItem struct {
	ID         int
	Text, Date string
}
type ReviewProgress struct {
	ID          int
	Name        string
	Done, Total int
}
type ReviewReference struct {
	ID    int
	Label string
}
type WeeklyReview struct {
	Saved                        []ReviewReference `json:"-"`
	ID                           int
	Start, End                   string
	Completed, Overdue, Upcoming []ReviewItem
	Projects                     []ReviewProgress
	HabitActivity                int
	Preview                      bool
}

func handleReview(w http.ResponseWriter, r *http.Request) {
	var review WeeklyReview
	var err error
	id := r.URL.Query().Get("id")
	if id == "" {
		handleToday(w, r)
		return
	}
	if id != "" {
		var content string
		err = db.QueryRow(`SELECT id,content FROM weekly_reviews WHERE id=?`, id).Scan(&review.ID, &content)
		if err == nil {
			storedID := review.ID
			err = json.Unmarshal([]byte(content), &review)
			review.ID = storedID
		}
	}
	if err != nil {
		http.Error(w, "Review unavailable", 404)
		return
	}
	rows, e := db.Query(`SELECT id,period_end FROM weekly_reviews ORDER BY period_end DESC`)
	if e != nil {
		http.Error(w, "Could not load saved reviews", 500)
		return
	}
	for rows.Next() {
		var saved ReviewReference
		var stamp string
		if e = rows.Scan(&saved.ID, &stamp); e != nil {
			break
		}
		at, _ := time.Parse(time.RFC3339, stamp)
		saved.Label = at.In(appLocation()).Format("2 Jan 2006")
		review.Saved = append(review.Saved, saved)
	}
	if e == nil {
		e = rows.Err()
	}
	rows.Close()
	if e != nil {
		http.Error(w, "Could not load saved reviews", 500)
		return
	}
	renderTemplate(w, "review.html", review)
}

func queueNotification(event string, payload pushPayload, now, expires time.Time) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	_, err = db.Exec(`INSERT OR IGNORE INTO push_deliveries(endpoint,event_key,payload,next_attempt,expires_at) SELECT endpoint,?,?,?,? FROM push_subscriptions`, event, string(body), now.UTC().Format(time.RFC3339), expires.UTC().Format(time.RFC3339))
	return err
}

// Tick receives its clock explicitly. Persistence prevents duplicates across restarts.
func runScheduleTick(now time.Time) error {
	settings, err := loadSchedule()
	if err != nil {
		return err
	}
	loc, err := time.LoadLocation(settings.Timezone)
	if err != nil {
		return err
	}
	now = now.In(loc)
	today := now.Format("2006-01-02")
	expires := time.Date(now.Year(), now.Month(), now.Day()+1, 0, 0, 0, 0, loc)
	for _, kind := range []string{"task", "habit"} {
		enabled, clock := settings.TaskEnabled, settings.TaskTime
		if kind == "habit" {
			enabled, clock = settings.HabitEnabled, settings.HabitTime
		}
		rt, e := parseReminderTime(clock, reminderTime{})
		if e != nil {
			return e
		}
		rt.enabled = enabled
		if !shouldSendReminder(rt, now, "") {
			continue
		}
		key := kind + ":" + today
		if getSetting("queued_"+kind) == today {
			continue
		}
		var payload pushPayload
		if kind == "task" {
			tasks, e := dueTodayTasks(now)
			if e != nil {
				return e
			}
			if len(tasks) == 0 {
				continue
			}
			payload = pushPayload{Title: "Tasks due", Body: taskReminderBody(tasks), Tag: "tasks", URL: "/?view=today"}
		} else {
			left := 0
			for _, h := range loadHabitsAt(now) {
				if h.Period == "day" && !h.Done {
					left++
				}
			}
			if left == 0 {
				continue
			}
			payload = pushPayload{Title: "Habits", Body: fmt.Sprintf("%d habits left today", left), Tag: "habits", URL: "/?view=today"}
		}
		if err = queueNotification(key, payload, now, expires); err != nil {
			return err
		}
		setSetting("queued_"+kind, today)
	}
	return processDeliveries(now, settings)
}
func processDeliveries(now time.Time, settings ScheduleSettings) error {
	rows, err := db.Query(`SELECT d.endpoint,d.event_key,d.payload,d.attempts,d.expires_at,s.p256dh,s.auth FROM push_deliveries d JOIN push_subscriptions s ON s.endpoint=d.endpoint WHERE d.status='pending' AND d.next_attempt<=? ORDER BY d.next_attempt LIMIT 50`, now.UTC().Format(time.RFC3339))
	if err != nil {
		return err
	}
	type delivery struct {
		Endpoint, Key, Payload, Expires, P256dh, Auth string
		Attempts                                      int
	}
	var ds []delivery
	for rows.Next() {
		var d delivery
		if err = rows.Scan(&d.Endpoint, &d.Key, &d.Payload, &d.Attempts, &d.Expires, &d.P256dh, &d.Auth); err != nil {
			break
		}
		ds = append(ds, d)
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return err
	}
	for _, d := range ds {
		enabled := strings.HasPrefix(d.Key, "task:") && settings.TaskEnabled || strings.HasPrefix(d.Key, "habit:") && settings.HabitEnabled
		expiry, _ := time.Parse(time.RFC3339, d.Expires)
		if !enabled || !now.Before(expiry) {
			_, err = db.Exec(`UPDATE push_deliveries SET status='skipped',result='Disabled or expired' WHERE endpoint=? AND event_key=?`, d.Endpoint, d.Key)
			if err != nil {
				return err
			}
			continue
		}
		var p pushPayload
		if err = json.Unmarshal([]byte(d.Payload), &p); err != nil {
			return err
		}
		result := deliverPush(d.Endpoint, d.P256dh, d.Auth, p)
		status := "failed"
		if result.Accepted {
			status = "accepted"
		} else if result.Retry && d.Attempts < 2 {
			status = "pending"
		}
		delay := time.Minute
		if d.Attempts > 0 {
			delay = 5 * time.Minute
		}
		_, err = db.Exec(`UPDATE push_deliveries SET attempts=attempts+1,status=?,result=?,next_attempt=? WHERE endpoint=? AND event_key=?`, status, result.Message, now.Add(delay).UTC().Format(time.RFC3339), d.Endpoint, d.Key)
		if err != nil {
			return err
		}
	}
	return nil
}
