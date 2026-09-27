package main

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"
)

func generateToken() string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		// A predictable (all-zero) token would be a critical auth flaw; fail loud.
		panic(fmt.Sprintf("crypto/rand failed: %v", err))
	}
	return hex.EncodeToString(b)
}

func createSession() (string, error) {
	if _, err := db.Exec("DELETE FROM sessions WHERE expires_at <= datetime('now')"); err != nil {
		return "", err
	}
	token := generateToken()
	// Store in SQLite's datetime format so string comparison against
	// datetime('now') is correct (see the migration note in initDB).
	expiry := appNow().UTC().Add(sessionDuration).Format("2006-01-02 15:04:05")
	if _, err := db.Exec("INSERT INTO sessions (token, expires_at, last_seen) VALUES (?, ?, datetime('now'))", token, expiry); err != nil {
		return "", err
	}
	return token, nil
}

func validSession(token string) bool {
	if token == "" {
		return false
	}
	var lastSeen string
	err := db.QueryRow(
		"SELECT last_seen FROM sessions WHERE token = ? AND expires_at > datetime('now')",
		token,
	).Scan(&lastSeen)
	if err != nil {
		return false
	}
	// Server-side idle expiry: a session unused for longer than twice the
	// client inactivity window is dead, even if the token/cookie persists.
	// The 2x margin lets the client-side warning fire first under normal use.
	if lastSeen != "" {
		if seen, perr := time.Parse("2006-01-02 15:04:05", lastSeen); perr == nil {
			idleLimit := 2 * time.Duration(inactivityTimeoutMinutes) * time.Minute
			if time.Since(seen) > idleLimit {
				return false
			}
		}
	}
	touchSession(token, lastSeen)
	return true
}

// touchSession refreshes last_seen, but at most once a minute to bound writes
// on a single-connection SQLite database.
func touchSession(token, lastSeen string) {
	if lastSeen != "" {
		if seen, err := time.Parse("2006-01-02 15:04:05", lastSeen); err == nil && time.Since(seen) < time.Minute {
			return
		}
	}
	db.Exec("UPDATE sessions SET last_seen = datetime('now') WHERE token = ?", token)
}

func getSessionToken(r *http.Request) string {
	cookie, err := r.Cookie("session")
	if err != nil {
		return ""
	}
	return cookie.Value
}

func authMiddleware(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		token := getSessionToken(r)
		if !validSession(token) {
			if r.Method == "GET" && r.URL.Path == "/" && r.URL.RawQuery != "" {
				http.SetCookie(w, &http.Cookie{Name: "return_to", Value: url.QueryEscape(r.URL.RequestURI()), Path: "/", HttpOnly: true, Secure: isHTTPS(r), SameSite: http.SameSiteStrictMode, MaxAge: 600})
			}
			if strings.HasPrefix(r.URL.Path, "/push/") {
				writeJSONError(w, 401, "Session expired. Sign in and try again.")
				return
			}
			if r.Header.Get("HX-Request") == "true" {
				w.Header().Set("HX-Redirect", "/login")
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			http.Redirect(w, r, "/login", http.StatusSeeOther)
			return
		}
		next(w, r)
	}
}

// isHTTPS reports whether the request arrived over TLS, directly or via a
// reverse proxy, so the session cookie can be marked Secure when possible.
func isHTTPS(r *http.Request) bool {
	return r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https"
}

func loginError(w http.ResponseWriter, r *http.Request, isHTMX bool, msg string) {
	if isHTMX {
		w.Header().Set("HX-Retarget", "#error")
		w.Header().Set("HX-Reswap", "innerHTML")
		fmt.Fprint(w, msg)
		return
	}
	renderTemplate(w, r, "login.html", msg)
}

func handleLogin(w http.ResponseWriter, r *http.Request) {
	if r.Method == "GET" {
		// If already logged in, redirect
		if validSession(getSessionToken(r)) {
			http.Redirect(w, r, "/", http.StatusSeeOther)
			return
		}
		renderTemplate(w, r, "login.html", nil)
		return
	}

	if r.Method == "POST" {
		// Rate limiting via small delay to prevent brute force
		time.Sleep(200 * time.Millisecond)

		submitted := r.FormValue("passcode")
		isHTMX := r.Header.Get("HX-Request") == "true"

		client := loginClient(r)
		blocked, ok := checkPasscode(client, submitted)
		if blocked {
			log.Printf("Login blocked (lockout active) from %s", client)
			loginError(w, r, isHTMX, "Too many attempts — try again in a minute")
			return
		}
		if !ok {
			log.Printf("Failed login attempt from %s", client)
			loginError(w, r, isHTMX, "Wrong passcode")
			return
		}

		log.Printf("Successful login from %s", client)
		token, err := createSession()
		if err != nil {
			log.Printf("Create session: %v", err)
			http.Error(w, "Could not start session", http.StatusInternalServerError)
			return
		}
		http.SetCookie(w, &http.Cookie{
			Name:     "session",
			Value:    token,
			Path:     "/",
			HttpOnly: true,
			Secure:   isHTTPS(r),
			SameSite: http.SameSiteStrictMode,
			MaxAge:   int(sessionDuration.Seconds()),
		})
		destination := "/"
		if c, err := r.Cookie("return_to"); err == nil {
			if v, e := url.QueryUnescape(c.Value); e == nil && strings.HasPrefix(v, "/?") {
				destination = v
			}
			http.SetCookie(w, &http.Cookie{Name: "return_to", Path: "/", MaxAge: -1, HttpOnly: true})
		}
		if isHTMX {
			w.Header().Set("HX-Redirect", destination)
			w.WriteHeader(http.StatusOK)
		} else {
			http.Redirect(w, r, destination, http.StatusSeeOther)
		}
	}
}

func handleLogout(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	token := getSessionToken(r)
	if token != "" {
		db.Exec("DELETE FROM sessions WHERE token = ?", token)
		log.Printf("Session logged out from %s", r.RemoteAddr)
	}
	http.SetCookie(w, &http.Cookie{
		Name:     "session",
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		MaxAge:   -1,
	})
	http.Redirect(w, r, "/login", http.StatusSeeOther)
}

// Login lockout: after maxLoginFailures consecutive failures from one client,
// that client is blocked for loginLockout. Per-client state keeps a stranger
// from locking the owner out; it is only as good as the client address, so
// behind a reverse proxy set TRUSTED_PROXY or every request shares the proxy's
// address and the lockout is effectively global again.
const (
	maxLoginFailures = 5
	loginLockout     = 1 * time.Minute
	// Entries idle this long are forgotten; the cap bounds memory if many
	// addresses fail at once. Past the cap, unseen clients are refused until
	// old entries expire.
	loginStateIdle = 15 * time.Minute
	maxLoginStates = 10000
)

type loginState struct {
	failures     int
	blockedUntil time.Time
	lastSeen     time.Time
}

var (
	loginMu        sync.Mutex
	loginStates    = map[string]*loginState{}
	trustedProxies []netip.Prefix
)

// configureTrustedProxies reads TRUSTED_PROXY: comma-separated IPs or CIDRs of
// reverse proxies whose X-Forwarded-For header may name the real client.
func configureTrustedProxies() {
	prefixes, err := parseTrustedProxies(os.Getenv("TRUSTED_PROXY"))
	if err != nil {
		log.Fatalf("TRUSTED_PROXY: %v", err)
	}
	trustedProxies = prefixes
	if len(prefixes) > 0 {
		log.Printf("Trusting X-Forwarded-For from %d proxy range(s)", len(prefixes))
	}
}

func parseTrustedProxies(raw string) ([]netip.Prefix, error) {
	var prefixes []netip.Prefix
	for _, part := range strings.Split(raw, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		if prefix, err := netip.ParsePrefix(part); err == nil {
			prefixes = append(prefixes, prefix.Masked())
			continue
		}
		addr, err := netip.ParseAddr(part)
		if err != nil {
			return nil, fmt.Errorf("want IPs or CIDRs, got %q", part)
		}
		addr = addr.Unmap()
		prefixes = append(prefixes, netip.PrefixFrom(addr, addr.BitLen()))
	}
	return prefixes, nil
}

func isTrustedProxy(addr netip.Addr) bool {
	addr = addr.Unmap()
	for _, prefix := range trustedProxies {
		if prefix.Contains(addr) {
			return true
		}
	}
	return false
}

// loginClient identifies the client for the lockout. X-Forwarded-For is read
// only when the direct peer is a trusted proxy, and right to left, so a client
// cannot pick its own identity by sending the header itself.
func loginClient(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	peer, err := netip.ParseAddr(host)
	if err != nil || !isTrustedProxy(peer) {
		return host
	}
	hops := strings.Split(strings.Join(r.Header.Values("X-Forwarded-For"), ","), ",")
	for i := len(hops) - 1; i >= 0; i-- {
		hop, err := netip.ParseAddr(strings.TrimSpace(hops[i]))
		if err != nil {
			break
		}
		if !isTrustedProxy(hop) {
			return hop.Unmap().String()
		}
	}
	return host
}

// checkPasscode compares a submitted passcode under the client's lockout. The
// lockout check, the comparison and the failure count share one critical
// section so parallel requests cannot all pass the check before any failure
// is counted.
func checkPasscode(client, submitted string) (blocked, ok bool) {
	loginMu.Lock()
	defer loginMu.Unlock()
	now := appNow()
	state := loginStates[client]
	if state == nil {
		if len(loginStates) >= 256 {
			pruneLoginStates(now)
		}
		if len(loginStates) >= maxLoginStates {
			return true, false
		}
		state = &loginState{}
		loginStates[client] = state
	}
	state.lastSeen = now
	if now.Before(state.blockedUntil) {
		return true, false
	}
	if len(submitted) != 8 || subtle.ConstantTimeCompare([]byte(submitted), []byte(passcode)) != 1 {
		state.failures++
		if state.failures >= maxLoginFailures {
			state.blockedUntil = now.Add(loginLockout)
			state.failures = 0
			log.Printf("Login lockout engaged for %s on %s after repeated failures", loginLockout, client)
		}
		return false, false
	}
	delete(loginStates, client)
	return false, true
}

func pruneLoginStates(now time.Time) {
	for client, state := range loginStates {
		if now.Sub(state.lastSeen) > loginStateIdle && !now.Before(state.blockedUntil) {
			delete(loginStates, client)
		}
	}
}
