package main

import (
	"database/sql"
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
	TaskEnabled, HabitEnabled, ReviewEnabled  bool
	TaskTime, HabitTime, ReviewTime, Timezone string
	ReviewDay                                 int
	ReviewEnabledAt                           string
}

func initScheduleSettings() {
	defaults := map[string]string{"task_enabled": strconv.FormatBool(taskReminder.enabled), "habit_enabled": strconv.FormatBool(habitReminder.enabled), "review_enabled": "false", "task_time": fmt.Sprintf("%02d:%02d", taskReminder.hour, taskReminder.min), "habit_time": fmt.Sprintf("%02d:%02d", habitReminder.hour, habitReminder.min), "review_time": "18:00", "review_day": "0", "timezone": "Europe/Bucharest", "queued_task": getSetting("task_reminder_sent"), "queued_habit": getSetting("habit_reminder_sent")}
	for k, v := range defaults {
		if _, err := db.Exec(`INSERT OR IGNORE INTO settings(key,value) VALUES(?,?)`, k, v); err != nil {
			log.Fatalf("Initialize schedules: %v", err)
		}
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
	rows, err := db.Query(`SELECT key,value FROM settings WHERE key IN ('task_enabled','habit_enabled','review_enabled','task_time','habit_time','review_time','review_day','timezone','review_enabled_at')`)
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
	s.ReviewEnabled = values["review_enabled"] == "true"
	s.TaskTime = values["task_time"]
	s.HabitTime = values["habit_time"]
	s.ReviewTime = values["review_time"]
	s.Timezone = values["timezone"]
	s.ReviewDay, _ = strconv.Atoi(values["review_day"])
	s.ReviewEnabledAt = values["review_enabled_at"]
	return s, nil
}
func registerScheduleRoutes() {
	http.HandleFunc("/settings", authMiddleware(handleSettings))
	http.HandleFunc("/settings/save", authMiddleware(handleSaveSettings))
	http.HandleFunc("/today", authMiddleware(handleToday))
	http.HandleFunc("/review", authMiddleware(handleReview))
	http.HandleFunc("/push/status", authMiddleware(handlePushStatus))
}

type TodayView struct {
	Date, ISODate          string
	Overdue, Due, Upcoming []ReviewItem
	Habits                 []Habit
	Week                   WeeklyReview
}

func loadToday(now time.Time) (TodayView, error) {
	week, err := buildReview(now)
	if err != nil {
		return TodayView{}, err
	}
	today := now.Format("2006-01-02")
	view := TodayView{Date: now.Format("Monday, 2 January"), ISODate: today, Overdue: week.Overdue, Week: week}
	for _, item := range week.Upcoming {
		if item.Date == today {
			view.Due = append(view.Due, item)
		} else {
			view.Upcoming = append(view.Upcoming, item)
		}
	}
	for _, habit := range loadHabitsAt(now) {
		if habit.Period == "day" {
			view.Habits = append(view.Habits, habit)
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
	for _, kind := range []string{"task", "habit", "review"} {
		raw := r.FormValue(kind + "_time")
		rt, e := parseReminderTime(raw, reminderTime{})
		if e != nil || !rt.enabled {
			http.Error(w, "Enter a valid time for each reminder", 400)
			return
		}
		vals[kind+"_time"] = fmt.Sprintf("%02d:%02d", rt.hour, rt.min)
		vals[kind+"_enabled"] = strconv.FormatBool(r.FormValue(kind+"_enabled") == "on")
	}
	day, err := strconv.Atoi(r.FormValue("review_day"))
	if err != nil || day < 0 || day > 6 {
		http.Error(w, "Choose a weekday", 400)
		return
	}
	vals["review_day"] = strconv.Itoa(day)
	tx, err := db.Begin()
	if err != nil {
		http.Error(w, "DB error", 500)
		return
	}
	defer tx.Rollback()
	var old string
	err = tx.QueryRow(`SELECT value FROM settings WHERE key='review_enabled'`).Scan(&old)
	if err != nil && err != sql.ErrNoRows {
		http.Error(w, "DB error", 500)
		return
	}
	if vals["review_enabled"] == "true" && old != "true" {
		vals["review_enabled_at"] = time.Now().UTC().Format(time.RFC3339)
	}
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

// latestWeeklyOccurrence uses calendar arithmetic so DST does not move wall time.
func latestWeeklyOccurrence(now time.Time, day int, clock string) time.Time {
	rt, _ := parseReminderTime(clock, reminderTime{hour: 18, enabled: true})
	delta := (int(now.Weekday()) - day + 7) % 7
	date := now.AddDate(0, 0, -delta)
	at := time.Date(date.Year(), date.Month(), date.Day(), rt.hour, rt.min, 0, 0, now.Location())
	if at.After(now) {
		at = at.AddDate(0, 0, -7)
	}
	return at
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

func buildReview(end time.Time) (WeeklyReview, error) {
	start := end.AddDate(0, 0, -7)
	review := WeeklyReview{Start: start.Format("2 Jan 2006"), End: end.Format("2 Jan 2006 15:04 MST")}
	rows, err := db.Query(`SELECT id,text FROM todos WHERE category='todo' AND done=1 AND completed_at>=? AND completed_at<? ORDER BY completed_at DESC`, start.UTC().Format(time.RFC3339), end.UTC().Format(time.RFC3339))
	if err != nil {
		return review, err
	}
	for rows.Next() {
		var item ReviewItem
		if err = rows.Scan(&item.ID, &item.Text); err != nil {
			break
		}
		review.Completed = append(review.Completed, item)
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return review, err
	}
	rows, err = db.Query(`SELECT t.id,t.text,t.due_date FROM todos t WHERE t.category='todo' AND t.done=0 AND t.due_date!='' AND t.due_date<? AND `+activeTaskSQL+` ORDER BY t.due_date,t.id`, end.AddDate(0, 0, 7).Format("2006-01-02"))
	if err != nil {
		return review, err
	}
	today := end.Format("2006-01-02")
	for rows.Next() {
		var item ReviewItem
		if err = rows.Scan(&item.ID, &item.Text, &item.Date); err != nil {
			break
		}
		if item.Date < today {
			review.Overdue = append(review.Overdue, item)
		} else {
			review.Upcoming = append(review.Upcoming, item)
		}
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return review, err
	}
	ws, err := loadWorkspace()
	if err != nil {
		return review, err
	}
	for _, p := range ws.Projects {
		review.Projects = append(review.Projects, ReviewProgress{p.ID, p.Name, p.Group.Completed, p.Group.Total})
	}
	err = db.QueryRow(`SELECT COALESCE(SUM(count),0) FROM habit_logs WHERE date>=? AND date<?`, start.Format("2006-01-02"), end.Format("2006-01-02")).Scan(&review.HabitActivity)
	return review, err
}
func snapshotReview(end time.Time) (int, error) {
	var id int
	key := end.UTC().Format(time.RFC3339)
	err := db.QueryRow(`SELECT id FROM weekly_reviews WHERE period_end=?`, key).Scan(&id)
	if err == nil {
		return id, nil
	}
	if err != sql.ErrNoRows {
		return 0, err
	}
	review, err := buildReview(end)
	if err != nil {
		return 0, err
	}
	body, err := json.Marshal(review)
	if err != nil {
		return 0, err
	}
	_, err = db.Exec(`INSERT OR IGNORE INTO weekly_reviews(period_end,content,created_at) VALUES(?,?,?)`, key, string(body), time.Now().UTC().Format(time.RFC3339))
	if err != nil {
		return 0, err
	}
	err = db.QueryRow(`SELECT id FROM weekly_reviews WHERE period_end=?`, key).Scan(&id)
	return id, err
}
func handleReview(w http.ResponseWriter, r *http.Request) {
	var review WeeklyReview
	var err error
	id := r.URL.Query().Get("id")
	if id != "" {
		var content string
		err = db.QueryRow(`SELECT id,content FROM weekly_reviews WHERE id=?`, id).Scan(&review.ID, &content)
		if err == nil {
			storedID := review.ID
			err = json.Unmarshal([]byte(content), &review)
			review.ID = storedID
		}
	} else {
		review, err = buildReview(appNow())
		review.Preview = true
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
	if settings.ReviewEnabled {
		at := latestWeeklyOccurrence(now, settings.ReviewDay, settings.ReviewTime)
		enabledAt, _ := time.Parse(time.RFC3339, settings.ReviewEnabledAt)
		if !at.Before(enabledAt) {
			id, e := snapshotReview(at)
			if e != nil {
				return e
			}
			key := "review:" + at.UTC().Format(time.RFC3339)
			if err = queueNotification(key, pushPayload{Title: "Your weekly review", Body: "A quiet look back, and what’s coming next.", Tag: "review", URL: fmt.Sprintf("/?view=review&review=%d", id)}, now, at.AddDate(0, 0, 7)); err != nil {
				return err
			}
		}
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
		enabled := strings.HasPrefix(d.Key, "task:") && settings.TaskEnabled || strings.HasPrefix(d.Key, "habit:") && settings.HabitEnabled || strings.HasPrefix(d.Key, "review:") && settings.ReviewEnabled
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
