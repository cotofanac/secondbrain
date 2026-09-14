package main

import (
	"fmt"
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
		"INSERT INTO push_subscriptions (endpoint, p256dh, auth) VALUES (?, ?, ?) ON CONFLICT(endpoint) DO UPDATE SET p256dh=excluded.p256dh,auth=excluded.auth",
		s.Endpoint, s.Keys.P256dh, s.Keys.Auth,
	)
	return err
}

func deleteSubscription(endpoint string) {
	db.Exec("DELETE FROM push_subscriptions WHERE endpoint = ?", endpoint)
}

// pushPayload is the JSON the service worker's 'push' handler expects.
type pushPayload struct {
	Title string `json:"title"`
	Body  string `json:"body"`
	Tag   string `json:"tag"`
	URL   string `json:"url"`
}

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

// startPushReminders runs the persistent per-device scheduler once a minute.
func startPushReminders() {
	go func() {
		for {
			if err := runScheduleTick(appNow()); err != nil {
				log.Printf("Reminder scheduler: %v", err)
			}
			time.Sleep(time.Minute)
		}
	}()
}

// dueTodayTasks returns the text of active to-do items due today or earlier,
// oldest due date first. "Today" is the local date (matching the rest of the
// app), not SQLite's UTC date('now').
func dueTodayTasks(now time.Time) ([]string, error) {
	rows, err := db.Query(`
		SELECT text FROM todos t
		WHERE category = 'todo' AND `+activeTaskSQL+` AND done = 0
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
