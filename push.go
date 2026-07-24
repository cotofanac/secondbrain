package main

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	webpush "github.com/SherClockHolmes/webpush-go"
)

// reminderTime is a daily wall-clock time (local/TZ) at which a reminder fires.
// enabled is false when the reminder is turned off ("off" in the env var).
type reminderTime struct {
	hour, min int
	enabled   bool
}

var (
	// Defaults are used when the env var is unset. Overridden in
	// configurePushReminders from HABIT_REMINDER_TIME / TASK_REMINDER_TIME.
	habitReminder = reminderTime{hour: 20, min: 0, enabled: true}
	taskReminder  = reminderTime{hour: 9, min: 0, enabled: true}

	vapidPublicKey  string
	vapidPrivateKey string
	// pushSubject is the VAPID "sub" claim: a mailto: or https: contact the push
	// service can reach about the sender. Overridable via PUSH_SUBJECT.
	pushSubject = "mailto:secondbrain@localhost"

	// Bounded client so a slow push service can't hang a reminder run or the
	// /push/test request forever.
	pushHTTPClient = &http.Client{Timeout: 15 * time.Second}
)

// parseReminderTime parses an "HH:MM" (24-hour) value or "off". An empty string
// keeps def. It returns an error on malformed input so main() can fail loudly,
// matching the INACTIVITY_LOGOUT_MINUTES handling.
func parseReminderTime(raw string, def reminderTime) (reminderTime, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return def, nil
	}
	if strings.EqualFold(raw, "off") {
		return reminderTime{enabled: false}, nil
	}
	parts := strings.Split(raw, ":")
	if len(parts) != 2 {
		return reminderTime{}, fmt.Errorf("want HH:MM or off, got %q", raw)
	}
	h, err1 := strconv.Atoi(strings.TrimSpace(parts[0]))
	m, err2 := strconv.Atoi(strings.TrimSpace(parts[1]))
	if err1 != nil || err2 != nil || h < 0 || h > 23 || m < 0 || m > 59 {
		return reminderTime{}, fmt.Errorf("want HH:MM (00:00-23:59) or off, got %q", raw)
	}
	return reminderTime{hour: h, min: m, enabled: true}, nil
}

// configurePushReminders reads the reminder-time env vars. Call from main().
func configurePushReminders() {
	var err error
	if habitReminder, err = parseReminderTime(os.Getenv("HABIT_REMINDER_TIME"), habitReminder); err != nil {
		log.Fatalf("HABIT_REMINDER_TIME: %v", err)
	}
	if taskReminder, err = parseReminderTime(os.Getenv("TASK_REMINDER_TIME"), taskReminder); err != nil {
		log.Fatalf("TASK_REMINDER_TIME: %v", err)
	}
	if sub := strings.TrimSpace(os.Getenv("PUSH_SUBJECT")); sub != "" {
		pushSubject = sub
	}
	log.Printf("Reminders: habits %s, tasks %s", reminderDesc(habitReminder), reminderDesc(taskReminder))
}

func reminderDesc(rt reminderTime) string {
	if !rt.enabled {
		return "off"
	}
	return fmt.Sprintf("%02d:%02d", rt.hour, rt.min)
}

// --- settings key/value store ---

func getSetting(key string) string {
	var v string
	db.QueryRow("SELECT value FROM settings WHERE key = ?", key).Scan(&v)
	return v
}

func setSetting(key, value string) {
	db.Exec(
		"INSERT INTO settings (key, value) VALUES (?, ?) ON CONFLICT(key) DO UPDATE SET value = excluded.value",
		key, value,
	)
}

// initPush loads the VAPID key pair from settings, generating and persisting one
// on first run so the self-hosted user needs zero configuration. Call after
// initDB().
func initPush() {
	pub := getSetting("vapid_public_key")
	priv := getSetting("vapid_private_key")
	if pub == "" || priv == "" {
		var err error
		priv, pub, err = webpush.GenerateVAPIDKeys()
		if err != nil {
			log.Fatalf("generate VAPID keys: %v", err)
		}
		setSetting("vapid_public_key", pub)
		setSetting("vapid_private_key", priv)
		log.Printf("Generated VAPID key pair for push notifications")
	}
	vapidPublicKey = pub
	vapidPrivateKey = priv
}

// --- endpoint validation ---

// validatePushEndpoint rejects subscription endpoints that would turn the
// reminder sender into an SSRF probe of whatever network the server sits on.
// The endpoint is attacker-chosen JSON that we then POST to on a daily timer,
// so an unvalidated value reaches internal addresses (link-local metadata
// services, LAN routers) forever. Real push services — FCM, Apple, Mozilla —
// are always public HTTPS origins, so requiring that costs nothing.
func validatePushEndpoint(raw string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("malformed URL")
	}
	if u.Scheme != "https" {
		return fmt.Errorf("scheme must be https, got %q", u.Scheme)
	}
	host := u.Hostname()
	if host == "" {
		return fmt.Errorf("no host")
	}
	// A literal IP skips DNS entirely; otherwise every address the name
	// resolves to must be public, so a name with one internal A record
	// can't slip through.
	if ip := net.ParseIP(host); ip != nil {
		if !isPublicIP(ip) {
			return fmt.Errorf("host %s is not a public address", host)
		}
		return nil
	}
	ips, err := net.LookupIP(host)
	if err != nil {
		return fmt.Errorf("host %s does not resolve", host)
	}
	if len(ips) == 0 {
		return fmt.Errorf("host %s resolved to no addresses", host)
	}
	for _, ip := range ips {
		if !isPublicIP(ip) {
			return fmt.Errorf("host %s resolves to non-public address %s", host, ip)
		}
	}
	return nil
}

// isPublicIP reports whether ip is routable on the public internet. Everything
// loopback, private, link-local, multicast, unspecified, CGNAT or reserved is
// treated as off-limits.
func isPublicIP(ip net.IP) bool {
	if ip.IsLoopback() || ip.IsUnspecified() || ip.IsPrivate() ||
		ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() ||
		ip.IsInterfaceLocalMulticast() || ip.IsMulticast() {
		return false
	}
	if v4 := ip.To4(); v4 != nil {
		switch {
		case v4[0] == 0, // 0.0.0.0/8
			v4[0] >= 240, // 240.0.0.0/4 reserved
			v4[0] == 100 && v4[1] >= 64 && v4[1] < 128, // 100.64.0.0/10 CGNAT
			v4[0] == 192 && v4[1] == 0 && v4[2] == 0:   // 192.0.0.0/24 IETF
			return false
		}
	}
	return true
}

// --- subscription storage ---

func saveSubscription(s webpush.Subscription) error {
	_, err := db.Exec(
		"INSERT OR REPLACE INTO push_subscriptions (endpoint, p256dh, auth) VALUES (?, ?, ?)",
		s.Endpoint, s.Keys.P256dh, s.Keys.Auth,
	)
	return err
}

func deleteSubscription(endpoint string) {
	db.Exec("DELETE FROM push_subscriptions WHERE endpoint = ?", endpoint)
}

func loadSubscriptions() []webpush.Subscription {
	rows, err := db.Query("SELECT endpoint, p256dh, auth FROM push_subscriptions")
	if err != nil {
		log.Printf("push: load subscriptions: %v", err)
		return nil
	}
	defer rows.Close()
	var subs []webpush.Subscription
	for rows.Next() {
		var s webpush.Subscription
		rows.Scan(&s.Endpoint, &s.Keys.P256dh, &s.Keys.Auth)
		subs = append(subs, s)
	}
	return subs
}

// --- sending ---

// pushPayload is the JSON the service worker's 'push' handler expects.
type pushPayload struct {
	Title string `json:"title"`
	Body  string `json:"body"`
	Tag   string `json:"tag"`
	URL   string `json:"url"`
}

// sendPush encrypts and delivers a payload to every stored subscription.
// Subscriptions the push service reports as gone (404/410) are pruned so we
// stop retrying them; other failures are logged without retry.
func sendPush(p pushPayload) {
	subs := loadSubscriptions()
	if len(subs) == 0 {
		return
	}
	msg, err := json.Marshal(p)
	if err != nil {
		log.Printf("push: marshal payload: %v", err)
		return
	}
	for i := range subs {
		s := subs[i]
		// Rows stored before endpoint validation existed are still in the
		// table, so re-check the scheme here. Kept to a parse (no DNS) so a
		// flaky resolver can't silently drop a legitimate reminder.
		if u, err := url.Parse(s.Endpoint); err != nil || u.Scheme != "https" {
			log.Printf("push: skipping non-https endpoint %s", shortEndpoint(s.Endpoint))
			continue
		}
		resp, err := webpush.SendNotification(msg, &s, &webpush.Options{
			HTTPClient:      pushHTTPClient,
			Subscriber:      pushSubject,
			VAPIDPublicKey:  vapidPublicKey,
			VAPIDPrivateKey: vapidPrivateKey,
			TTL:             86400,
			Urgency:         webpush.UrgencyNormal,
		})
		if err != nil {
			log.Printf("push: send to %s: %v", shortEndpoint(s.Endpoint), err)
			continue
		}
		switch {
		case resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusGone:
			deleteSubscription(s.Endpoint)
			log.Printf("push: dropped expired subscription %s", shortEndpoint(s.Endpoint))
		case resp.StatusCode >= 400:
			body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
			log.Printf("push: %s returned %d: %s", shortEndpoint(s.Endpoint), resp.StatusCode, strings.TrimSpace(string(body)))
		}
		resp.Body.Close()
	}
}

func shortEndpoint(e string) string {
	if len(e) > 40 {
		return e[:40] + "..."
	}
	return e
}

// --- daily reminders ---

// shouldSendReminder reports whether a reminder should fire now, given the local
// date it was last sent (""=never). It fires at most once per calendar day: at
// or after the configured time, when it hasn't already gone out today. Firing
// "late" after downtime is intentional — a reminder missed while the server was
// down is still delivered when it comes back, rather than silently skipped.
func shouldSendReminder(rt reminderTime, now time.Time, lastSentDate string) bool {
	if !rt.enabled {
		return false
	}
	if lastSentDate == now.Format("2006-01-02") {
		return false
	}
	fireAt := time.Date(now.Year(), now.Month(), now.Day(), rt.hour, rt.min, 0, 0, now.Location())
	return !now.Before(fireAt)
}

// startPushReminders wakes once a minute and delivers each due reminder once per
// day. Modeled on startAutoArchiveTodos. The per-reminder date marker is written
// after the decision even when nothing was sent, so an empty list doesn't cause
// repeated re-checks for the rest of the day.
func startPushReminders() {
	if !habitReminder.enabled && !taskReminder.enabled {
		return
	}
	go func() {
		for {
			now := time.Now()
			if shouldSendReminder(habitReminder, now, getSetting("habit_reminder_sent")) {
				sendHabitReminder()
				setSetting("habit_reminder_sent", now.Format("2006-01-02"))
			}
			if shouldSendReminder(taskReminder, now, getSetting("task_reminder_sent")) {
				sendTaskReminder(now)
				setSetting("task_reminder_sent", now.Format("2006-01-02"))
			}
			time.Sleep(1 * time.Minute)
		}
	}()
}

func sendHabitReminder() {
	left := 0
	for _, h := range loadHabits() {
		if h.Period == "day" && !h.Done {
			left++
		}
	}
	if left == 0 {
		return
	}
	noun := "habit"
	if left != 1 {
		noun = "habits"
	}
	sendPush(pushPayload{
		Title: "Habits",
		Body:  fmt.Sprintf("%d %s left to check off today", left, noun),
		Tag:   "habits",
		URL:   "/",
	})
}

func sendTaskReminder(now time.Time) {
	tasks, err := dueTodayTasks(now)
	if err != nil {
		log.Printf("push: due-task query: %v", err)
		return
	}
	if len(tasks) == 0 {
		return
	}
	sendPush(pushPayload{
		Title: "Tasks due",
		Body:  taskReminderBody(tasks),
		Tag:   "tasks",
		URL:   "/",
	})
}

// dueTodayTasks returns the text of active to-do items due today or earlier,
// oldest due date first. "Today" is the local date (matching the rest of the
// app), not SQLite's UTC date('now').
func dueTodayTasks(now time.Time) ([]string, error) {
	rows, err := db.Query(`
		SELECT text FROM todos
		WHERE category = 'todo' AND archived = 0 AND done = 0
		  AND due_date != '' AND due_date <= ?
		ORDER BY due_date ASC, position ASC`, now.Format("2006-01-02"))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var tasks []string
	for rows.Next() {
		var t string
		rows.Scan(&t)
		tasks = append(tasks, t)
	}
	return tasks, rows.Err()
}

// taskReminderBody lists up to three task names, summarizing any remainder.
func taskReminderBody(tasks []string) string {
	const max = 3
	if len(tasks) <= max {
		return strings.Join(tasks, ", ")
	}
	return strings.Join(tasks[:max], ", ") + fmt.Sprintf(" and %d more", len(tasks)-max)
}

// --- HTTP endpoints (registered behind authMiddleware) ---

func handlePushPublicKey(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"key": vapidPublicKey})
}

func handlePushSubscribe(w http.ResponseWriter, r *http.Request) {
	if !requirePost(w, r) {
		return
	}
	var sub webpush.Subscription
	if err := json.NewDecoder(io.LimitReader(r.Body, 8192)).Decode(&sub); err != nil ||
		sub.Endpoint == "" || sub.Keys.P256dh == "" || sub.Keys.Auth == "" {
		http.Error(w, "invalid subscription", http.StatusBadRequest)
		return
	}
	if err := validatePushEndpoint(sub.Endpoint); err != nil {
		log.Printf("push: rejected subscription endpoint: %v", err)
		http.Error(w, "invalid subscription endpoint", http.StatusBadRequest)
		return
	}
	if err := saveSubscription(sub); err != nil {
		http.Error(w, "could not save subscription", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func handlePushUnsubscribe(w http.ResponseWriter, r *http.Request) {
	if !requirePost(w, r) {
		return
	}
	var body struct {
		Endpoint string `json:"endpoint"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 8192)).Decode(&body); err != nil || body.Endpoint == "" {
		http.Error(w, "invalid request", http.StatusBadRequest)
		return
	}
	deleteSubscription(body.Endpoint)
	w.WriteHeader(http.StatusNoContent)
}

// handlePushTest delivers a notification to all subscriptions immediately, to
// verify end-to-end delivery from the browser.
func handlePushTest(w http.ResponseWriter, r *http.Request) {
	if !requirePost(w, r) {
		return
	}
	sendPush(pushPayload{
		Title: "SecondBrain",
		Body:  "Notifications are working",
		Tag:   "test",
		URL:   "/",
	})
	w.WriteHeader(http.StatusNoContent)
}
