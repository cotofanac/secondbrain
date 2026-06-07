package main

import (
	"fmt"
	"log"
	"net"
	"net/http"
	"time"
)

// Login lockout configuration (overridable via env in main()).
var (
	loginMaxAttempts    = 5
	loginLockoutMinutes = 15
)

const maxLockout = 24 * time.Hour

// clientIP extracts the remote IP (without port) used to key login lockouts.
// RemoteAddr is used directly; proxy headers are intentionally not trusted.
func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// loginLocked reports whether the given IP is currently locked out and, if so,
// how much time remains.
func loginLocked(ip string) (bool, time.Duration) {
	var lockedUntil string
	err := db.QueryRow("SELECT locked_until FROM login_attempts WHERE ip = ?", ip).Scan(&lockedUntil)
	if err != nil || lockedUntil == "" {
		return false, 0
	}
	t, err := time.Parse(time.RFC3339, lockedUntil)
	if err != nil {
		return false, 0
	}
	if time.Now().Before(t) {
		return true, time.Until(t)
	}
	return false, 0
}

// recordLoginFailure increments the failure counter for an IP. Once the counter
// reaches loginMaxAttempts the IP is locked out for an escalating duration that
// doubles with each successive lockout, capped at maxLockout.
func recordLoginFailure(ip string) {
	var failCount, lockoutLevel int
	db.QueryRow(
		"SELECT fail_count, lockout_level FROM login_attempts WHERE ip = ?", ip,
	).Scan(&failCount, &lockoutLevel)

	failCount++
	lockedUntil := ""
	if failCount >= loginMaxAttempts {
		lockoutLevel++
		d := time.Duration(loginLockoutMinutes) * time.Minute
		for i := 1; i < lockoutLevel; i++ {
			d *= 2
			if d >= maxLockout {
				d = maxLockout
				break
			}
		}
		if d > maxLockout {
			d = maxLockout
		}
		lockedUntil = time.Now().Add(d).Format(time.RFC3339)
		failCount = 0 // reset attempt counter; lockout_level persists for escalation
		log.Printf("Login lockout: IP %s locked for %s (level %d)", ip, d.Round(time.Minute), lockoutLevel)
	}

	db.Exec(`INSERT INTO login_attempts (ip, fail_count, lockout_level, locked_until, last_attempt)
		VALUES (?, ?, ?, ?, datetime('now'))
		ON CONFLICT(ip) DO UPDATE SET
			fail_count = excluded.fail_count,
			lockout_level = excluded.lockout_level,
			locked_until = excluded.locked_until,
			last_attempt = excluded.last_attempt`,
		ip, failCount, lockoutLevel, lockedUntil)
}

// clearLoginFailures removes the lockout record for an IP after a success.
func clearLoginFailures(ip string) {
	db.Exec("DELETE FROM login_attempts WHERE ip = ?", ip)
}

// humanizeDuration renders a coarse, human-friendly remaining-lockout string.
func humanizeDuration(d time.Duration) string {
	if d < time.Minute {
		secs := int(d.Seconds())
		if secs < 1 {
			secs = 1
		}
		return fmt.Sprintf("%d second%s", secs, plural(secs))
	}
	mins := int(d.Minutes())
	if mins < 60 {
		if mins < 1 {
			mins = 1
		}
		return fmt.Sprintf("%d minute%s", mins, plural(mins))
	}
	hours := int(d.Hours())
	return fmt.Sprintf("%d hour%s", hours, plural(hours))
}

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}
