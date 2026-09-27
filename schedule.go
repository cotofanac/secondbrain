package main

import (
	"encoding/json"
	"log"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
	_ "time/tzdata"
)

// calendarZone is the zone "today" is counted in. It comes from TZ, set once
// in the deployment; tests swap it.
var calendarZone atomic.Pointer[time.Location]

const defaultZone = "Europe/Bucharest"

func appLocation() *time.Location {
	if loc := calendarZone.Load(); loc != nil {
		return loc
	}
	loc, _ := time.LoadLocation(defaultZone)
	return loc
}
func appNow() time.Time { return time.Now().In(appLocation()) }

// configureCalendar reads TZ (a named zone such as Europe/London); unset
// means Europe/Bucharest. Call from main() before the database opens.
func configureCalendar() {
	name := strings.TrimSpace(os.Getenv("TZ"))
	if name == "" || name == "Local" {
		name = defaultZone
	}
	loc, err := time.LoadLocation(name)
	if err != nil {
		log.Fatalf("TZ: %v", err)
	}
	calendarZone.Store(loc)
	log.Printf("Calendar time zone: %s", loc)
}

func registerScheduleRoutes() {
	http.HandleFunc("/today", authMiddleware(handleToday))
	http.HandleFunc("/today/reschedule", authMiddleware(handleTodayReschedule))
	http.HandleFunc("/push/status", authMiddleware(handlePushStatus))
}

type TodayItem struct {
	ID, Revision                int
	Text, Date, Today, Tomorrow string
	Project, Repeat             string
}

type TodayView struct {
	Date, ISODate           string
	Overdue, Due, Upcoming  []TodayItem
	Suggestions             []TodayItem
	CompletedToday, DueLeft int
	// Reminder is the daily reminder time ("09:00"), empty when it is off.
	Reminder string
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
		if err = rows.Scan(&item.ID, &item.Text, &item.Date, &item.Revision, &item.Project, &item.Repeat); err != nil {
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
	base := `SELECT t.id,t.text,t.due_date,t.revision,COALESCE(p.name,''),t.repeat
		FROM todos t LEFT JOIN projects p ON p.id=t.project_id
		WHERE t.category='todo' AND t.done=0 AND ` + activeTaskSQL
	var err error
	view.Overdue, err = scanTodayItems(base+` AND t.due_date!='' AND t.due_date<? ORDER BY t.due_date,t.id`, today)
	if err != nil {
		return view, err
	}
	view.Due, err = scanTodayItems(base+` AND t.due_date=? ORDER BY t.id`, today)
	if err != nil {
		return view, err
	}
	view.Upcoming, err = scanTodayItems(base+` AND t.due_date>? AND t.due_date<? ORDER BY t.due_date,t.id`, today, limit)
	if err != nil {
		return view, err
	}
	// Suggestions are deterministic and transparent: one unscheduled task from
	// each project first, followed by standalone tasks, in the user's list order.
	view.Suggestions, err = scanTodayItems(base + ` AND t.due_date='' AND (t.project_id IS NULL OR t.id=(
		SELECT t2.id FROM todos t2 WHERE t2.project_id=t.project_id AND t2.category='todo' AND t2.done=0
		AND t2.archived=0 AND t2.due_date=''
		ORDER BY t2.id LIMIT 1))
		ORDER BY CASE WHEN t.project_id IS NULL THEN 1 ELSE 0 END,COALESCE(p.position,0),t.id LIMIT 3`)
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
	if taskReminder.enabled {
		view.Reminder = reminderDesc(taskReminder)
	}
	return view, nil
}

func handleToday(w http.ResponseWriter, r *http.Request) {
	view, err := loadToday(appNow())
	if err != nil {
		http.Error(w, "Today unavailable", http.StatusInternalServerError)
		return
	}
	renderTemplate(w, r, "today.html", view)
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
func queueNotification(event string, payload pushPayload, now, expires time.Time) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	_, err = db.Exec(`INSERT OR IGNORE INTO push_deliveries(endpoint,event_key,payload,next_attempt,expires_at) SELECT endpoint,?,?,?,? FROM push_subscriptions`, event, string(body), now.UTC().Format(time.RFC3339), expires.UTC().Format(time.RFC3339))
	return err
}

// deliveryRetention keeps recent results for the Notifications status line
// while stopping push_deliveries from growing without bound.
const deliveryRetention = 30 * 24 * time.Hour

// pruneDeliveries removes finished delivery rows older than deliveryRetention.
// Pending rows are left to processDeliveries, which expires them itself.
func pruneDeliveries(now time.Time) (int64, error) {
	res, err := db.Exec(`DELETE FROM push_deliveries WHERE status!='pending' AND next_attempt<?`, now.Add(-deliveryRetention).UTC().Format(time.RFC3339))
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// runScheduleTick queues the day's reminder once its time has come and sends
// whatever is waiting. It receives its clock explicitly; the queued_task
// setting keeps a restart from sending the same day twice.
func runScheduleTick(now time.Time) error {
	now = now.In(appLocation())
	today := now.Format("2006-01-02")
	if shouldSendReminder(taskReminder, now, getSetting("queued_task")) {
		tasks, err := dueTodayTasks(now)
		if err != nil {
			return err
		}
		// The day is settled at the reminder time either way, so a task added
		// later that day never sets off a late reminder. Nothing due stays silent.
		if len(tasks) == 0 {
			log.Printf("Reminder %s: nothing due, not sent", today)
		} else {
			payload := pushPayload{Title: "Tasks due", Body: taskReminderBody(tasks), Tag: "tasks", URL: "/?view=today"}
			expires := time.Date(now.Year(), now.Month(), now.Day()+1, 0, 0, 0, 0, now.Location())
			if err = queueNotification("task:"+today, payload, now, expires); err != nil {
				return err
			}
			var devices int
			if err = db.QueryRow(`SELECT count(*) FROM push_subscriptions`).Scan(&devices); err != nil {
				return err
			}
			log.Printf("Reminder %s: %d task(s) due, queued for %d device(s)", today, len(tasks), devices)
		}
		setSetting("queued_task", today)
	}
	return processDeliveries(now)
}
func processDeliveries(now time.Time) error {
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
		// Only task reminders remain; anything else still queued is skipped.
		enabled := strings.HasPrefix(d.Key, "task:") && taskReminder.enabled
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
		log.Printf("Reminder delivery to %s: %s (%s)", pushHost(d.Endpoint), status, result.Message)
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

// pushHost names a device's push service (Apple, Google, Mozilla) for the log
// without writing its full, secret subscription URL there.
func pushHost(endpoint string) string {
	if u, err := url.Parse(endpoint); err == nil && u.Host != "" {
		return u.Host
	}
	return "unknown push service"
}
