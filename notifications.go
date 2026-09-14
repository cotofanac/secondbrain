package main

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	webpush "github.com/SherClockHolmes/webpush-go"
)

type PushResult struct {
	Accepted bool   `json:"accepted"`
	Retry    bool   `json:"-"`
	Message  string `json:"message"`
}

// Replace only in tests; production uses the bounded Web Push HTTP client.
var deliverPush = sendDevicePush

func expiredSubscriptionKey(endpoint string) string {
	return fmt.Sprintf("expired_push_%x", sha256.Sum256([]byte(endpoint)))
}

// Remember revocations without retaining the endpoint or its encryption keys.
// Otherwise a browser can re-register a revoked subscription on every resume.
func expireSubscription(endpoint string) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.Exec(`INSERT INTO settings(key,value) VALUES(?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value`, expiredSubscriptionKey(endpoint), time.Now().UTC().Format(time.RFC3339)); err != nil {
		return err
	}
	if _, err = tx.Exec(`DELETE FROM push_subscriptions WHERE endpoint=?`, endpoint); err != nil {
		return err
	}
	return tx.Commit()
}

func sendDevicePush(endpoint, p256dh, auth string, p pushPayload) PushResult {
	parsed, err := url.Parse(endpoint)
	if err != nil || parsed.Scheme != "https" {
		return PushResult{Message: "Invalid push endpoint. Enable this device again."}
	}
	body, err := json.Marshal(p)
	if err != nil {
		return PushResult{Message: "Could not prepare notification"}
	}
	sub := webpush.Subscription{Endpoint: endpoint, Keys: webpush.Keys{P256dh: p256dh, Auth: auth}}
	response, err := webpush.SendNotification(body, &sub, &webpush.Options{HTTPClient: pushHTTPClient, Subscriber: pushSubject, VAPIDPublicKey: vapidPublicKey, VAPIDPrivateKey: vapidPrivateKey, TTL: 3600, Urgency: webpush.UrgencyNormal})
	if err != nil {
		return PushResult{Retry: true, Message: "Could not reach push service. Check server connectivity."}
	}
	defer response.Body.Close()
	if response.StatusCode >= 200 && response.StatusCode < 300 {
		return PushResult{Accepted: true, Message: "Accepted by push service. Check this device for the notification."}
	}
	if response.StatusCode == 404 || response.StatusCode == 410 {
		if err := expireSubscription(endpoint); err != nil {
			return PushResult{Retry: true, Message: "Subscription expired; could not save its status. Try enabling this device again."}
		}
		return PushResult{Message: "Subscription expired. Enable this device again."}
	}
	raw, _ := io.ReadAll(io.LimitReader(response.Body, 512))
	reason := strings.TrimSpace(string(raw))
	if response.StatusCode == 401 || response.StatusCode == 403 {
		return PushResult{Message: fmt.Sprintf("Push service rejected credentials (%d). Check VAPID keys and PUSH_SUBJECT. %s", response.StatusCode, reason)}
	}
	return PushResult{Retry: response.StatusCode == 429 || response.StatusCode >= 500, Message: fmt.Sprintf("Push service returned %d. %s", response.StatusCode, reason)}
}
func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(value)
}
func writeJSONError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]string{"error": message})
}
func pushPost(w http.ResponseWriter, r *http.Request) bool {
	if r.Method != "POST" {
		writeJSONError(w, 405, "Use POST")
		return false
	}
	return true
}
func handlePushPublicKey(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, map[string]string{"key": vapidPublicKey})
}
func handlePushSubscribe(w http.ResponseWriter, r *http.Request) {
	if !pushPost(w, r) {
		return
	}
	var sub webpush.Subscription
	if err := json.NewDecoder(io.LimitReader(r.Body, 8192)).Decode(&sub); err != nil || sub.Endpoint == "" || sub.Keys.P256dh == "" || sub.Keys.Auth == "" {
		writeJSONError(w, 400, "Invalid subscription")
		return
	}
	if getSetting(expiredSubscriptionKey(sub.Endpoint)) != "" {
		writeJSON(w, http.StatusConflict, map[string]any{"error": "This subscription expired. Enable this device again to replace it.", "renew": true})
		return
	}
	if err := validatePushEndpoint(sub.Endpoint); err != nil {
		writeJSONError(w, 400, "Could not validate the push service. Check server DNS and network access.")
		return
	}
	if err := saveSubscription(sub); err != nil {
		writeJSONError(w, 500, "Could not save subscription. Try again.")
		return
	}
	writeJSON(w, 200, map[string]bool{"registered": true})
}
func requestedEndpoint(w http.ResponseWriter, r *http.Request) (string, bool) {
	var b struct {
		Endpoint string `json:"endpoint"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 8192)).Decode(&b); err != nil || b.Endpoint == "" {
		writeJSONError(w, 400, "Missing subscription endpoint")
		return "", false
	}
	return b.Endpoint, true
}
func handlePushUnsubscribe(w http.ResponseWriter, r *http.Request) {
	if !pushPost(w, r) {
		return
	}
	endpoint, ok := requestedEndpoint(w, r)
	if !ok {
		return
	}
	if _, err := db.Exec(`DELETE FROM push_subscriptions WHERE endpoint=?`, endpoint); err != nil {
		writeJSONError(w, 500, "Could not disable this device")
		return
	}
	writeJSON(w, 200, map[string]bool{"registered": false})
}
func handlePushStatus(w http.ResponseWriter, r *http.Request) {
	if !pushPost(w, r) {
		return
	}
	endpoint, ok := requestedEndpoint(w, r)
	if !ok {
		return
	}
	var count int
	if err := db.QueryRow(`SELECT count(*) FROM push_subscriptions WHERE endpoint=?`, endpoint).Scan(&count); err != nil {
		writeJSONError(w, 500, "Could not read device status")
		return
	}
	var result string
	db.QueryRow(`SELECT result FROM push_deliveries WHERE endpoint=? AND result!='' ORDER BY next_attempt DESC LIMIT 1`, endpoint).Scan(&result)
	writeJSON(w, 200, map[string]any{"registered": count > 0, "last_result": result})
}
func handlePushTest(w http.ResponseWriter, r *http.Request) {
	if !pushPost(w, r) {
		return
	}
	endpoint, ok := requestedEndpoint(w, r)
	if !ok {
		return
	}
	var p256dh, auth string
	if err := db.QueryRow(`SELECT p256dh,auth FROM push_subscriptions WHERE endpoint=?`, endpoint).Scan(&p256dh, &auth); err != nil {
		writeJSONError(w, 404, "Enable this device before sending a test")
		return
	}
	result := deliverPush(endpoint, p256dh, auth, pushPayload{Title: "SecondBrain", Body: "This device can receive notifications.", Tag: "test", URL: "/?view=settings"})
	status := "failed"
	if result.Accepted {
		status = "accepted"
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	// An expired subscription may have been deleted by the transport.
	_, err := db.Exec(`INSERT INTO push_deliveries(endpoint,event_key,payload,next_attempt,expires_at,attempts,status,result) SELECT endpoint,?,'{}',?,?,1,?,? FROM push_subscriptions WHERE endpoint=?`, "test:"+now, now, now, status, result.Message, endpoint)
	if err != nil {
		writeJSONError(w, 500, "Test sent, but its status could not be saved")
		return
	}
	var registered int
	db.QueryRow(`SELECT count(*) FROM push_subscriptions WHERE endpoint=?`, endpoint).Scan(&registered)
	writeJSON(w, 200, map[string]any{"accepted": result.Accepted, "message": result.Message, "registered": registered > 0})
}
