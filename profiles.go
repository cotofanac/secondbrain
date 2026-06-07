package main

import (
	"context"
	"log"
	"net/http"
	"regexp"
	"strconv"
	"strings"

	"golang.org/x/crypto/bcrypt"
)

// Profiles configuration (set from env in main()).
var (
	profilesEnabled      = true
	allowProfileCreation = true
	firstProfileName     string
)

var passcodeRe = regexp.MustCompile(`^\d{8}$`)

// Profile is a self-contained workspace with its own passcode and data.
type Profile struct {
	ID        int
	Name      string
	IsPrimary bool
}

type ctxKey string

const profileCtxKey ctxKey = "profileID"

// profileID returns the authenticated profile id stored on the request context.
func profileID(r *http.Request) int {
	if v, ok := r.Context().Value(profileCtxKey).(int); ok {
		return v
	}
	return 0
}

func withProfile(r *http.Request, id int) *http.Request {
	return r.WithContext(context.WithValue(r.Context(), profileCtxKey, id))
}

func validPasscode(p string) bool {
	return passcodeRe.MatchString(p)
}

func hashPasscode(p string) (string, error) {
	b, err := bcrypt.GenerateFromPassword([]byte(p), bcrypt.DefaultCost)
	return string(b), err
}

func checkPasscode(hash, p string) bool {
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(p)) == nil
}

func countProfiles() int {
	var n int
	db.QueryRow("SELECT COUNT(*) FROM profiles").Scan(&n)
	return n
}

func listProfiles() []Profile {
	rows, err := db.Query("SELECT id, name, is_primary FROM profiles ORDER BY is_primary DESC, name ASC")
	if err != nil {
		return nil
	}
	defer rows.Close()
	var profiles []Profile
	for rows.Next() {
		var p Profile
		var primary int
		rows.Scan(&p.ID, &p.Name, &primary)
		p.IsPrimary = primary == 1
		profiles = append(profiles, p)
	}
	return profiles
}

func getProfile(id int) (Profile, bool) {
	var p Profile
	var primary int
	err := db.QueryRow("SELECT id, name, is_primary FROM profiles WHERE id = ?", id).Scan(&p.ID, &p.Name, &primary)
	if err != nil {
		return Profile{}, false
	}
	p.IsPrimary = primary == 1
	return p, true
}

func profilePasscodeHash(id int) (string, bool) {
	var hash string
	err := db.QueryRow("SELECT passcode_hash FROM profiles WHERE id = ?", id).Scan(&hash)
	if err != nil {
		return "", false
	}
	return hash, true
}

func primaryProfileID() int {
	var id int
	err := db.QueryRow("SELECT id FROM profiles WHERE is_primary = 1 ORDER BY id ASC LIMIT 1").Scan(&id)
	if err != nil {
		// Fall back to the lowest id if no profile is flagged primary.
		db.QueryRow("SELECT id FROM profiles ORDER BY id ASC LIMIT 1").Scan(&id)
	}
	return id
}

// createProfile inserts a new profile and seeds it with a default note. When it
// is the very first profile in the database it also claims any pre-existing
// (profile_id = 0) data left over from before profiles were introduced.
func createProfile(name, passcode string, isPrimary bool) (int, error) {
	hash, err := hashPasscode(passcode)
	if err != nil {
		return 0, err
	}
	primary := 0
	if isPrimary {
		primary = 1
	}
	res, err := db.Exec("INSERT INTO profiles (name, passcode_hash, is_primary) VALUES (?, ?, ?)", name, hash, primary)
	if err != nil {
		return 0, err
	}
	id64, _ := res.LastInsertId()
	id := int(id64)

	if countProfiles() == 1 {
		// First profile ever: adopt orphaned data from the pre-profiles schema.
		db.Exec("UPDATE todos SET profile_id = ? WHERE profile_id = 0 OR profile_id IS NULL", id)
		db.Exec("UPDATE notes SET profile_id = ? WHERE profile_id = 0 OR profile_id IS NULL", id)
		db.Exec("UPDATE habits SET profile_id = ? WHERE profile_id = 0 OR profile_id IS NULL", id)
	}

	// Ensure the profile always has at least one note to edit.
	var noteCount int
	db.QueryRow("SELECT COUNT(*) FROM notes WHERE profile_id = ?", id).Scan(&noteCount)
	if noteCount == 0 {
		db.Exec("INSERT INTO notes (profile_id, title, content) VALUES (?, ?, ?)", id, "Quick Notes", "")
	}

	log.Printf("Profile created: %q (id=%d, primary=%v)", name, id, isPrimary)
	return id, nil
}

// seedProfilesFromEnv creates the initial profile from environment configuration
// when the database has none. With profiles disabled, a single default profile
// using PASSCODE is always created. With profiles enabled, a primary profile is
// seeded only when PASSCODE is provided; otherwise the database is left empty so
// the first user completes first-run setup.
func seedProfilesFromEnv() {
	if countProfiles() > 0 {
		return
	}

	name := firstProfileName
	if name == "" {
		name = "Default"
	}

	if !profilesEnabled {
		if _, err := createProfile(name, passcode, true); err != nil {
			log.Fatalf("Failed to seed default profile: %v", err)
		}
		return
	}

	if passcode != "" {
		if _, err := createProfile(name, passcode, true); err != nil {
			log.Fatalf("Failed to seed primary profile: %v", err)
		}
	} else {
		log.Printf("No profiles configured; first-run setup is available at /setup")
	}
}

// deleteProfileData removes a profile and all of its owned data.
func deleteProfileData(id int) {
	db.Exec("DELETE FROM habit_logs WHERE habit_id IN (SELECT id FROM habits WHERE profile_id = ?)", id)
	db.Exec("DELETE FROM habits WHERE profile_id = ?", id)
	db.Exec("DELETE FROM notes WHERE profile_id = ?", id)
	db.Exec("DELETE FROM todos WHERE profile_id = ?", id)
	db.Exec("DELETE FROM sessions WHERE profile_id = ?", id)
	db.Exec("DELETE FROM profiles WHERE id = ?", id)
}

// --- Handlers ---

// handleSetup drives first-run creation of the very first profile.
func handleSetup(w http.ResponseWriter, r *http.Request) {
	if !profilesEnabled || countProfiles() > 0 {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}

	if r.Method == "GET" {
		renderTemplate(w, "setup.html", nil)
		return
	}

	name := strings.TrimSpace(r.FormValue("name"))
	pass := r.FormValue("passcode")
	isHTMX := r.Header.Get("HX-Request") == "true"

	if name == "" || len(name) > 60 || !validNameRe.MatchString(name) {
		setupError(w, isHTMX, "Enter a valid profile name")
		return
	}
	if !validPasscode(pass) {
		setupError(w, isHTMX, "Passcode must be exactly 8 digits")
		return
	}

	id, err := createProfile(name, pass, true)
	if err != nil {
		setupError(w, isHTMX, "Could not create profile")
		return
	}

	token := createSession(id)
	setSessionCookie(w, token)
	if isHTMX {
		w.Header().Set("HX-Redirect", "/")
		w.WriteHeader(http.StatusOK)
	} else {
		http.Redirect(w, r, "/", http.StatusSeeOther)
	}
}

func setupError(w http.ResponseWriter, isHTMX bool, msg string) {
	if isHTMX {
		w.Header().Set("HX-Retarget", "#error")
		w.Header().Set("HX-Reswap", "innerHTML")
		w.Write([]byte(msg))
		return
	}
	renderTemplate(w, "setup.html", msg)
}

// handleProfiles renders the profile-management partial for the settings menu.
func handleProfiles(w http.ResponseWriter, r *http.Request) {
	if !profilesEnabled {
		http.Error(w, "Profiles disabled", http.StatusNotFound)
		return
	}
	renderProfilesPartial(w, r, "")
}

func renderProfilesPartial(w http.ResponseWriter, r *http.Request, errMsg string) {
	data := struct {
		Profiles      []Profile
		CurrentID     int
		AllowCreation bool
		Error         string
	}{
		Profiles:      listProfiles(),
		CurrentID:     profileID(r),
		AllowCreation: allowProfileCreation,
		Error:         errMsg,
	}
	renderTemplate(w, "profiles.html", data)
}

// handleCreateProfile creates an additional profile from the UI.
func handleCreateProfile(w http.ResponseWriter, r *http.Request) {
	if !requirePost(w, r) {
		return
	}
	if !profilesEnabled || !allowProfileCreation {
		http.Error(w, "Profile creation disabled", http.StatusForbidden)
		return
	}

	name := strings.TrimSpace(r.FormValue("name"))
	pass := r.FormValue("passcode")

	if name == "" || len(name) > 60 || !validNameRe.MatchString(name) {
		renderProfilesPartial(w, r, "Enter a valid profile name")
		return
	}
	if !validPasscode(pass) {
		renderProfilesPartial(w, r, "Passcode must be exactly 8 digits")
		return
	}

	var exists int
	db.QueryRow("SELECT COUNT(*) FROM profiles WHERE name = ? COLLATE NOCASE", name).Scan(&exists)
	if exists > 0 {
		renderProfilesPartial(w, r, "A profile with that name already exists")
		return
	}

	if _, err := createProfile(name, pass, false); err != nil {
		renderProfilesPartial(w, r, "Could not create profile")
		return
	}
	renderProfilesPartial(w, r, "")
}

// handleDeleteProfile removes a profile (and its data) other than the current,
// primary, or last remaining profile.
func handleDeleteProfile(w http.ResponseWriter, r *http.Request) {
	if !requirePost(w, r) {
		return
	}
	if !profilesEnabled || !allowProfileCreation {
		http.Error(w, "Profile management disabled", http.StatusForbidden)
		return
	}

	id, err := strconv.Atoi(r.FormValue("id"))
	if err != nil {
		http.Error(w, "Invalid ID", http.StatusBadRequest)
		return
	}

	target, ok := getProfile(id)
	if !ok {
		renderProfilesPartial(w, r, "Profile not found")
		return
	}
	if id == profileID(r) {
		renderProfilesPartial(w, r, "You cannot delete the profile you are signed into")
		return
	}
	if target.IsPrimary {
		renderProfilesPartial(w, r, "The primary profile cannot be deleted")
		return
	}
	if countProfiles() <= 1 {
		renderProfilesPartial(w, r, "Cannot delete the last profile")
		return
	}

	deleteProfileData(id)
	log.Printf("Profile deleted: %q (id=%d)", target.Name, id)
	renderProfilesPartial(w, r, "")
}

// handleSwitchProfile logs the current session out and returns to the login
// screen so the user can sign into a different profile.
func handleSwitchProfile(w http.ResponseWriter, r *http.Request) {
	token := getSessionToken(r)
	if token != "" {
		db.Exec("DELETE FROM sessions WHERE token = ?", token)
	}
	clearSessionCookie(w)
	if r.Header.Get("HX-Request") == "true" {
		w.Header().Set("HX-Redirect", "/login")
		w.WriteHeader(http.StatusOK)
		return
	}
	http.Redirect(w, r, "/login", http.StatusSeeOther)
}
