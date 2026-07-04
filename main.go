package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"io/fs"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	_ "github.com/mattn/go-sqlite3"
)

//go:embed static/*
var staticFiles embed.FS

//go:embed templates/*
var templateFiles embed.FS

var (
	db                       *sql.DB
	templates                *template.Template
	passcode                 string
	inactivityTimeoutMinutes = 45
	validNameRe              = regexp.MustCompile(`^[^\x00-\x1f\x7f]+$`)
	// assetVersion is a short content hash of the code assets, appended as
	// ?v= to asset URLs and baked into the service-worker cache name so a
	// deploy invalidates stale JS/CSS instead of serving it for up to a day.
	assetVersion string
)

// computeAssetVersion hashes the code assets so the version changes only when
// one of them changes.
func computeAssetVersion() string {
	h := sha256.New()
	for _, name := range []string{"static/app.js", "static/style.css", "static/htmx.min.js"} {
		b, err := staticFiles.ReadFile(name)
		if err != nil {
			// Fall back to a build-time value so the app still boots.
			return strconv.FormatInt(time.Now().Unix(), 16)
		}
		h.Write(b)
	}
	return hex.EncodeToString(h.Sum(nil))[:8]
}

// Login lockout: after maxLoginFailures consecutive failures, logins are
// blocked entirely for loginLockout. Global (not per-IP) since this is a
// single-user app and per-IP state is defeated by proxies anyway.
const (
	maxLoginFailures = 5
	loginLockout     = 1 * time.Minute
)

var (
	loginMu       sync.Mutex
	loginFailures int
	loginBlocked  time.Time
)

const sessionDuration = 72 * time.Hour

// parseDBTime parses a timestamp string as returned by go-sqlite3. Directly
// selected DATETIME columns come back as RFC3339 ("2006-01-02T15:04:05Z"),
// while values wrapped in an expression such as a COALESCE around archived_at
// come back in SQLite's plain "2006-01-02 15:04:05" form. Both represent UTC.
func parseDBTime(s string) (time.Time, bool) {
	for _, layout := range []string{time.RFC3339, "2006-01-02 15:04:05"} {
		if t, err := time.ParseInLocation(layout, s, time.UTC); err == nil {
			return t.UTC(), true
		}
	}
	return time.Time{}, false
}

func renderTemplate(w http.ResponseWriter, name string, data any) {
	var buf bytes.Buffer
	if err := templates.ExecuteTemplate(&buf, name, data); err != nil {
		log.Printf("Template error (%s): %v", name, err)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}
	buf.WriteTo(w)
}

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// CSRF defense-in-depth on top of the SameSite=Strict cookie: reject
		// state-changing requests whose Origin doesn't match this host. A
		// missing Origin (non-browser clients, some same-origin GETs) is allowed.
		if r.Method != http.MethodGet && r.Method != http.MethodHead && !sameOrigin(r) {
			http.Error(w, "cross-origin request forbidden", http.StatusForbidden)
			return
		}
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "strict-origin-when-cross-origin")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self' 'unsafe-inline'; style-src 'self' 'unsafe-inline'; img-src 'self' data:")
		// Default to no-store; the static and service-worker handlers override
		// this with their own cache policy. Keeps user data (notes, tasks) out
		// of shared/intermediary caches.
		w.Header().Set("Cache-Control", "no-store")
		// Only advertise HSTS over HTTPS so plain-HTTP local runs still work.
		if isHTTPS(r) {
			w.Header().Set("Strict-Transport-Security", "max-age=63072000; includeSubDomains")
		}
		next.ServeHTTP(w, r)
	})
}

// sameOrigin reports whether a state-changing request originated from this same
// host. Requests without an Origin header pass (curl, older same-origin flows).
func sameOrigin(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return true
	}
	u, err := url.Parse(origin)
	if err != nil {
		return false
	}
	return u.Host == r.Host
}

func handleHealth(w http.ResponseWriter, _ *http.Request) {
	if err := db.Ping(); err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusServiceUnavailable)
		w.Write([]byte(`{"status":"error"}`))
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Write([]byte(`{"status":"ok"}`))
}

// loggingMiddleware wraps a handler and emits one log line per request:
// METHOD /path STATUS duration
func loggingMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rw := &responseWriter{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rw, r)
		// skip noisy static-asset lines in logs
		if !strings.HasPrefix(r.URL.Path, "/static/") {
			log.Printf("%s %s %d %s", r.Method, r.URL.Path, rw.status, time.Since(start).Round(time.Millisecond))
		}
	})
}

type responseWriter struct {
	http.ResponseWriter
	status int
}

func (rw *responseWriter) WriteHeader(code int) {
	rw.status = code
	rw.ResponseWriter.WriteHeader(code)
}

func startAutoArchiveTodos() {
	go func() {
		for {
			// Don't archive a task that's still upcoming: keep it until its due
			// date has passed (or it has no due date at all).
			res, err := db.Exec(
				"UPDATE todos SET archived = 1, archived_at = CURRENT_TIMESTAMP WHERE category = 'todo' AND archived = 0 AND created_at <= datetime('now', '-7 days') AND (due_date = '' OR due_date < date('now'))",
			)
			if err == nil {
				if n, _ := res.RowsAffected(); n > 0 {
					log.Printf("Auto-archived %d todo items", n)
				}
			}
			time.Sleep(1 * time.Hour)
		}
	}()
}

func main() {
	passcode = os.Getenv("PASSCODE")
	if len(passcode) != 8 || !regexp.MustCompile(`^\d{8}$`).MatchString(passcode) {
		log.Fatal("PASSCODE env must be exactly 8 digits")
	}

	assetVersion = computeAssetVersion()
	log.Printf("Asset version: %s", assetVersion)

	if raw := os.Getenv("INACTIVITY_LOGOUT_MINUTES"); raw != "" {
		mins, err := strconv.Atoi(raw)
		if err != nil || mins <= 0 {
			log.Fatal("INACTIVITY_LOGOUT_MINUTES must be a positive integer")
		}
		inactivityTimeoutMinutes = mins
		log.Printf("Inactivity logout set to %d minutes", inactivityTimeoutMinutes)
	}

	initDB()
	defer db.Close()

	funcMap := template.FuncMap{
		"formatDate": func(s string) string {
			if s == "" {
				return ""
			}
			t, err := time.Parse("2006-01-02", s)
			if err != nil {
				return s
			}
			return t.Format("Jan 2")
		},
		"formatUpdated": func(s string) string {
			// Stored timestamps are UTC; display them in the server's local zone
			// (set via TZ) so the wall clock is right.
			t, ok := parseDBTime(s)
			if !ok {
				return ""
			}
			t = t.In(time.Local)
			if time.Since(t) < 24*time.Hour {
				return t.Format("3:04 PM")
			}
			if t.Year() == time.Now().Year() {
				return t.Format("Jan 2")
			}
			return t.Format("Jan 2, 2006")
		},
		"isOverdue": func(s string) bool {
			if s == "" {
				return false
			}
			// Compare ISO date strings so "today" follows the local timezone;
			// time.Truncate works in UTC and flips dates a few hours early/late.
			return s < time.Now().Format("2006-01-02")
		},
		"todoArchiveHint": func(createdAt, dueDate string) string {
			if createdAt == "" {
				return ""
			}
			// A task due today or later isn't eligible for auto-archive yet,
			// so don't tease an archive countdown for it.
			if dueDate != "" && dueDate >= time.Now().Format("2006-01-02") {
				return ""
			}
			t, ok := parseDBTime(createdAt)
			if !ok {
				return ""
			}
			ageHours := int(time.Since(t).Hours())
			hoursLeft := 7*24 - ageHours
			if hoursLeft < 0 || hoursLeft >= 3*24 {
				return ""
			}
			if hoursLeft < 1 {
				return "Archives soon"
			}
			if hoursLeft < 24 {
				return fmt.Sprintf("Archives in %dh", hoursLeft)
			}
			daysLeft := (hoursLeft + 23) / 24
			if daysLeft == 1 {
				return "Archives in 1 day"
			}
			return fmt.Sprintf("Archives in %d days", daysLeft)
		},
		"todoArchivedAgo": func(archivedAt string) string {
			if archivedAt == "" {
				return ""
			}
			t, ok := parseDBTime(archivedAt)
			if !ok {
				return ""
			}
			hours := int(time.Since(t).Hours())
			if hours < 1 {
				return "Archived just now"
			}
			if hours < 24 {
				return fmt.Sprintf("Archived %dh ago", hours)
			}
			days := hours / 24
			if days == 1 {
				return "Archived 1 day ago"
			}
			return fmt.Sprintf("Archived %d days ago", days)
		},
		"assetVersion": func() string { return assetVersion },
	}
	templates = template.Must(template.New("").Funcs(funcMap).ParseFS(templateFiles, "templates/*.html"))
	log.Printf("Templates loaded")

	staticFS, _ := fs.Sub(staticFiles, "static")
	staticHandler := http.StripPrefix("/static/", http.FileServer(http.FS(staticFS)))
	http.Handle("/static/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Code assets are cache-busted with ?v= query params, so a day of caching is safe.
		w.Header().Set("Cache-Control", "public, max-age=86400")
		staticHandler.ServeHTTP(w, r)
	}))

	// Serve the service worker with the current asset version injected, so its
	// cache name changes on every deploy and the browser installs the new SW
	// (which purges the old cache on activate). The exact path wins over the
	// "/static/" subtree handler above.
	swSource, _ := staticFiles.ReadFile("static/sw.js")
	swBody := bytes.ReplaceAll(swSource, []byte("__ASSET_VERSION__"), []byte(assetVersion))
	http.HandleFunc("/static/sw.js", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/javascript")
		w.Header().Set("Cache-Control", "no-cache")
		w.Write(swBody)
	})

	http.HandleFunc("/health", handleHealth)
	http.HandleFunc("/search", authMiddleware(handleSearch))
	http.HandleFunc("/", authMiddleware(handleIndex))
	http.HandleFunc("/login", handleLogin)
	http.HandleFunc("/logout", handleLogout)

	// Todo API
	http.HandleFunc("/todos", authMiddleware(handleTodos))
	http.HandleFunc("/todos/add", authMiddleware(handleAddTodo))
	http.HandleFunc("/todos/edit", authMiddleware(handleEditTodo))
	http.HandleFunc("/todos/toggle", authMiddleware(handleToggleTodo))
	http.HandleFunc("/todos/delete", authMiddleware(handleDeleteTodo))
	http.HandleFunc("/todos/clear-checked", authMiddleware(handleClearChecked))
	http.HandleFunc("/todos/archive", authMiddleware(handleArchiveTodos))
	http.HandleFunc("/todos/restore", authMiddleware(handleRestoreTodo))
	http.HandleFunc("/todos/permanent-delete", authMiddleware(handlePermanentDeleteTodo))

	// Notes API
	http.HandleFunc("/notes", authMiddleware(handleNotes))
	http.HandleFunc("/notes/save", authMiddleware(handleSaveNote))
	http.HandleFunc("/notes/create", authMiddleware(handleCreateNote))
	http.HandleFunc("/notes/delete", authMiddleware(handleDeleteNote))
	http.HandleFunc("/notes/rename", authMiddleware(handleRenameNote))
	http.HandleFunc("/notes/archive", authMiddleware(handleArchiveNotes))
	http.HandleFunc("/notes/restore", authMiddleware(handleRestoreNote))
	http.HandleFunc("/notes/permanent-delete", authMiddleware(handlePermanentDeleteNote))

	// Habits API
	http.HandleFunc("/habits", authMiddleware(handleHabits))
	http.HandleFunc("/habits/add", authMiddleware(handleAddHabit))
	http.HandleFunc("/habits/rename", authMiddleware(handleRenameHabit))
	http.HandleFunc("/habits/toggle", authMiddleware(handleToggleHabit))
	http.HandleFunc("/habits/delete", authMiddleware(handleDeleteHabit))
	http.HandleFunc("/habits/archive", authMiddleware(handleArchiveHabits))
	http.HandleFunc("/habits/restore", authMiddleware(handleRestoreHabit))
	http.HandleFunc("/habits/permanent-delete", authMiddleware(handlePermanentDeleteHabit))

	startAutoArchiveTodos()

	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}

	server := &http.Server{
		Addr:              ":" + port,
		Handler:           loggingMiddleware(securityHeaders(http.DefaultServeMux)),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       2 * time.Minute,
	}

	// Graceful shutdown: finish in-flight requests and close the DB cleanly
	// (checkpoints the SQLite WAL) when Docker sends SIGTERM.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		server.Shutdown(shutdownCtx)
	}()

	log.Printf("SecondBrain listening on :%s (inactivity timeout: %d min)", port, inactivityTimeoutMinutes)
	if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatal(err)
	}
	log.Printf("Shutting down")
}

func initDB() {
	dataDir := os.Getenv("DATA_DIR")
	if dataDir == "" {
		dataDir = "./data"
	}
	os.MkdirAll(dataDir, 0750)

	dbPath := dataDir + "/secondbrain.db"
	log.Printf("Opening database: %s", dbPath)
	var err error
	db, err = sql.Open("sqlite3", dbPath+"?_journal_mode=WAL&_busy_timeout=5000&_foreign_keys=on")
	if err != nil {
		log.Fatal(err)
	}
	// SQLite allows one writer at a time; a single connection serializes all
	// access and eliminates "database is locked" errors at this scale.
	db.SetMaxOpenConns(1)

	schema := []string{
		`CREATE TABLE IF NOT EXISTS todos (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			category TEXT NOT NULL CHECK(category IN ('groceries','todo','shopping')),
			text TEXT NOT NULL,
			due_date TEXT DEFAULT '',
			done INTEGER DEFAULT 0,
			position INTEGER DEFAULT 0,
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
			archived INTEGER DEFAULT 0
		)`,
		`CREATE TABLE IF NOT EXISTS notes (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			title TEXT NOT NULL UNIQUE,
			content TEXT DEFAULT '',
			updated_at DATETIME DEFAULT CURRENT_TIMESTAMP,
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
			archived INTEGER DEFAULT 0
		)`,
		`CREATE TABLE IF NOT EXISTS habits (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			name TEXT NOT NULL UNIQUE,
			archived INTEGER DEFAULT 0,
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP
		)`,
		`CREATE TABLE IF NOT EXISTS habit_logs (
			habit_id INTEGER NOT NULL,
			date TEXT NOT NULL,
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
			PRIMARY KEY (habit_id, date),
			FOREIGN KEY (habit_id) REFERENCES habits(id) ON DELETE CASCADE
		)`,
		`CREATE TABLE IF NOT EXISTS sessions (
			token TEXT PRIMARY KEY,
			expires_at TEXT NOT NULL
		)`,
	}
	for _, stmt := range schema {
		if _, err := db.Exec(stmt); err != nil {
			log.Fatal(err)
		}
	}

	// Older sessions stored expires_at as RFC3339 ("...T...Z"), which sorts
	// wrong against SQLite's datetime('now') ("... ...") in string comparisons
	// and let tokens live up to ~24h past expiry. Normalize to the SQLite format.
	db.Exec("UPDATE sessions SET expires_at = replace(replace(expires_at, 'T', ' '), 'Z', '') WHERE expires_at LIKE '%T%'")
	// Server-side idle expiry: track when each session was last used so a stolen
	// token can't outlive the inactivity window even with JS disabled.
	db.Exec("ALTER TABLE sessions ADD COLUMN last_seen TEXT NOT NULL DEFAULT ''")
	db.Exec("UPDATE sessions SET last_seen = datetime('now') WHERE last_seen = ''")
	db.Exec("DELETE FROM sessions WHERE expires_at <= datetime('now')")
	db.Exec("ALTER TABLE todos ADD COLUMN archived_at DATETIME DEFAULT NULL")
	// Foreign keys were historically off, so habit deletes may have left
	// orphaned logs behind; clean them up once at startup.
	db.Exec("DELETE FROM habit_logs WHERE habit_id NOT IN (SELECT id FROM habits)")
	db.Exec("CREATE INDEX IF NOT EXISTS idx_todos_category_archived ON todos(category, archived)")

	// Seed a default note if none exist
	var count int
	db.QueryRow("SELECT COUNT(*) FROM notes").Scan(&count)
	if count == 0 {
		db.Exec("INSERT INTO notes (title, content) VALUES (?, ?)", "Quick Notes", "")
		log.Printf("Database initialized with default note")
	}
	log.Printf("Database ready")
}

// --- Auth ---

func generateToken() string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		// A predictable (all-zero) token would be a critical auth flaw; fail loud.
		panic(fmt.Sprintf("crypto/rand failed: %v", err))
	}
	return hex.EncodeToString(b)
}

func createSession() string {
	db.Exec("DELETE FROM sessions WHERE expires_at <= datetime('now')")
	token := generateToken()
	// Store in SQLite's datetime format so string comparison against
	// datetime('now') is correct (see the migration note in initDB).
	expiry := time.Now().UTC().Add(sessionDuration).Format("2006-01-02 15:04:05")
	db.Exec("INSERT INTO sessions (token, expires_at, last_seen) VALUES (?, ?, datetime('now'))", token, expiry)
	return token
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

func loginError(w http.ResponseWriter, isHTMX bool, msg string) {
	if isHTMX {
		w.Header().Set("HX-Retarget", "#error")
		w.Header().Set("HX-Reswap", "innerHTML")
		fmt.Fprint(w, msg)
		return
	}
	renderTemplate(w, "login.html", msg)
}

func handleLogin(w http.ResponseWriter, r *http.Request) {
	if r.Method == "GET" {
		// If already logged in, redirect
		if validSession(getSessionToken(r)) {
			http.Redirect(w, r, "/", http.StatusSeeOther)
			return
		}
		renderTemplate(w, "login.html", nil)
		return
	}

	if r.Method == "POST" {
		// Rate limiting via small delay to prevent brute force
		time.Sleep(200 * time.Millisecond)

		submitted := r.FormValue("passcode")
		isHTMX := r.Header.Get("HX-Request") == "true"

		loginMu.Lock()
		blocked := time.Now().Before(loginBlocked)
		loginMu.Unlock()
		if blocked {
			log.Printf("Login blocked (lockout active) from %s", r.RemoteAddr)
			loginError(w, isHTMX, "Too many attempts — try again in a minute")
			return
		}

		if len(submitted) != 8 || subtle.ConstantTimeCompare([]byte(submitted), []byte(passcode)) != 1 {
			loginMu.Lock()
			loginFailures++
			if loginFailures >= maxLoginFailures {
				loginBlocked = time.Now().Add(loginLockout)
				loginFailures = 0
				log.Printf("Login lockout engaged for %s after repeated failures", loginLockout)
			}
			loginMu.Unlock()
			log.Printf("Failed login attempt from %s", r.RemoteAddr)
			loginError(w, isHTMX, "Wrong passcode")
			return
		}

		loginMu.Lock()
		loginFailures = 0
		loginMu.Unlock()

		log.Printf("Successful login from %s", r.RemoteAddr)
		token := createSession()
		http.SetCookie(w, &http.Cookie{
			Name:     "session",
			Value:    token,
			Path:     "/",
			HttpOnly: true,
			Secure:   isHTTPS(r),
			SameSite: http.SameSiteStrictMode,
			MaxAge:   int(sessionDuration.Seconds()),
		})
		if isHTMX {
			w.Header().Set("HX-Redirect", "/")
			w.WriteHeader(http.StatusOK)
		} else {
			http.Redirect(w, r, "/", http.StatusSeeOther)
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

// --- Pages ---

func loadSuggestions(category string) []string {
	rows, err := db.Query(
		"SELECT DISTINCT text FROM todos WHERE category = ? ORDER BY text ASC",
		category,
	)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var items []string
	for rows.Next() {
		var s string
		rows.Scan(&s)
		items = append(items, s)
	}
	return items
}

func handleIndex(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}

	// Fetch initial groceries for server-side render
	rows, err := db.Query(
		"SELECT id, category, text, due_date, done FROM todos WHERE category = 'groceries' AND archived = 0 ORDER BY done ASC, position ASC, created_at DESC",
	)
	var todos []Todo
	if err == nil {
		defer rows.Close()
		for rows.Next() {
			var t Todo
			rows.Scan(&t.ID, &t.Category, &t.Text, &t.DueDate, &t.Done)
			todos = append(todos, t)
		}
	}

	// Fetch notes list and current note
	noteRows, err := db.Query("SELECT id, title FROM notes WHERE archived = 0 ORDER BY title ASC")
	var notesList []Note
	if err == nil {
		defer noteRows.Close()
		for noteRows.Next() {
			var n Note
			noteRows.Scan(&n.ID, &n.Title)
			notesList = append(notesList, n)
		}
	}

	var currentNote Note
	if len(notesList) > 0 {
		currentNote.ID = notesList[0].ID
		currentNote.Title = notesList[0].Title
		db.QueryRow("SELECT content, updated_at FROM notes WHERE id = ?", currentNote.ID).Scan(&currentNote.Content, &currentNote.UpdatedAt)
	}

	habits := loadHabits()

	data := struct {
		Todos                    []Todo
		Notes                    []Note
		CurrentNote              Note
		Habits                   []Habit
		InactivityTimeoutSeconds int
		GrocerySuggestions       []string
		ShoppingSuggestions      []string
	}{
		Todos:                    todos,
		Notes:                    notesList,
		CurrentNote:              currentNote,
		Habits:                   habits,
		InactivityTimeoutSeconds: inactivityTimeoutMinutes * 60,
		GrocerySuggestions:       loadSuggestions("groceries"),
		ShoppingSuggestions:      loadSuggestions("shopping"),
	}

	renderTemplate(w, "index.html", data)
}

// --- Todos ---

type Todo struct {
	ID         int
	Category   string
	Text       string
	DueDate    string
	Done       bool
	CreatedAt  string
	ArchivedAt string
}

func handleTodos(w http.ResponseWriter, r *http.Request) {
	category := r.URL.Query().Get("category")
	if category != "groceries" && category != "todo" && category != "shopping" {
		category = "groceries"
	}

	rows, err := db.Query(
		"SELECT id, category, text, due_date, done, created_at FROM todos WHERE category = ? AND archived = 0 ORDER BY done ASC, position ASC, created_at DESC",
		category,
	)
	if err != nil {
		http.Error(w, "DB error", http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	var todos []Todo
	for rows.Next() {
		var t Todo
		rows.Scan(&t.ID, &t.Category, &t.Text, &t.DueDate, &t.Done, &t.CreatedAt)
		todos = append(todos, t)
	}

	data := struct {
		Category string
		Todos    []Todo
	}{category, todos}

	renderTemplate(w, "todo-list.html", data)
}

func handleAddTodo(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	category := r.FormValue("category")
	if category != "groceries" && category != "todo" && category != "shopping" {
		http.Error(w, "Invalid category", http.StatusBadRequest)
		return
	}

	text := strings.TrimSpace(r.FormValue("text"))
	if text == "" || len(text) > 500 {
		http.Error(w, "Invalid text", http.StatusBadRequest)
		return
	}

	dueDate := ""
	if category == "todo" {
		dueDate = r.FormValue("due_date")
		if dueDate != "" {
			if _, err := time.Parse("2006-01-02", dueDate); err != nil {
				http.Error(w, "Invalid date", http.StatusBadRequest)
				return
			}
		}
	}

	// For list-type categories, reuse an existing item (active or archived) rather than
	// creating a duplicate. This keeps recurring items (e.g. "potatoes") as a single row.
	var action string
	if category == "groceries" || category == "shopping" {
		var existingID int
		err := db.QueryRow(
			"SELECT id FROM todos WHERE category = ? AND text = ? COLLATE NOCASE LIMIT 1",
			category, text,
		).Scan(&existingID)
		if err == nil {
			_, err = db.Exec(
				"UPDATE todos SET done = 0, archived = 0, due_date = '' WHERE id = ?",
				existingID,
			)
			if err != nil {
				http.Error(w, "DB error", http.StatusInternalServerError)
				return
			}
			action = "restored"
		} else {
			_, err = db.Exec(
				"INSERT INTO todos (category, text, due_date) VALUES (?, ?, ?)",
				category, text, dueDate,
			)
			if err != nil {
				http.Error(w, "DB error", http.StatusInternalServerError)
				return
			}
			action = "added"
		}
	} else {
		_, err := db.Exec(
			"INSERT INTO todos (category, text, due_date) VALUES (?, ?, ?)",
			category, text, dueDate,
		)
		if err != nil {
			http.Error(w, "DB error", http.StatusInternalServerError)
			return
		}
		action = "added"
	}
	log.Printf("Todo %s: [%s] %q", action, category, text)

	// Return updated list
	r.URL.RawQuery = "category=" + category
	handleTodos(w, r)
}

func handleEditTodo(w http.ResponseWriter, r *http.Request) {
	if !requirePost(w, r) {
		return
	}

	id, ok := parseID(w, r)
	if !ok {
		return
	}

	text := strings.TrimSpace(r.FormValue("text"))
	if text == "" || len(text) > 500 {
		http.Error(w, "Invalid text", http.StatusBadRequest)
		return
	}

	var category string
	err := db.QueryRow("SELECT category FROM todos WHERE id = ? AND archived = 0", id).Scan(&category)
	if err != nil {
		http.Error(w, "Not found", http.StatusNotFound)
		return
	}

	_, err = db.Exec("UPDATE todos SET text = ? WHERE id = ?", text, id)
	if err != nil {
		http.Error(w, "DB error", http.StatusInternalServerError)
		return
	}
	log.Printf("Todo edited: id=%d %q", id, text)

	r.URL.RawQuery = "category=" + category
	handleTodos(w, r)
}

func handleToggleTodo(w http.ResponseWriter, r *http.Request) {
	if !requirePost(w, r) {
		return
	}

	id, ok := parseID(w, r)
	if !ok {
		return
	}

	var category string
	err := db.QueryRow("SELECT category FROM todos WHERE id = ?", id).Scan(&category)
	if err != nil {
		http.Error(w, "Not found", http.StatusNotFound)
		return
	}

	if _, err := db.Exec("UPDATE todos SET done = CASE WHEN done = 0 THEN 1 ELSE 0 END WHERE id = ?", id); err != nil {
		http.Error(w, "DB error", http.StatusInternalServerError)
		return
	}
	log.Printf("Todo toggled: id=%d [%s]", id, category)

	r.URL.RawQuery = "category=" + category
	handleTodos(w, r)
}

func handleDeleteTodo(w http.ResponseWriter, r *http.Request) {
	if !requirePost(w, r) {
		return
	}

	id, ok := parseID(w, r)
	if !ok {
		return
	}

	var category string
	err := db.QueryRow("SELECT category FROM todos WHERE id = ?", id).Scan(&category)
	if err != nil {
		http.Error(w, "Not found", http.StatusNotFound)
		return
	}

	if _, err := db.Exec("UPDATE todos SET archived = 1, archived_at = CURRENT_TIMESTAMP WHERE id = ?", id); err != nil {
		http.Error(w, "DB error", http.StatusInternalServerError)
		return
	}
	log.Printf("Todo archived: id=%d [%s]", id, category)

	r.URL.RawQuery = "category=" + category
	handleTodos(w, r)
}

func handleClearChecked(w http.ResponseWriter, r *http.Request) {
	if !requirePost(w, r) {
		return
	}

	category := r.FormValue("category")
	if category != "groceries" && category != "shopping" {
		http.Error(w, "Invalid category", http.StatusBadRequest)
		return
	}

	if _, err := db.Exec("UPDATE todos SET done = 0 WHERE category = ? AND archived = 0 AND done = 1", category); err != nil {
		http.Error(w, "DB error", http.StatusInternalServerError)
		return
	}
	log.Printf("Cleared checked items: [%s]", category)

	r.URL.RawQuery = "category=" + category
	handleTodos(w, r)
}

func handleArchiveTodos(w http.ResponseWriter, r *http.Request) {
	category := r.URL.Query().Get("category")
	if category != "groceries" && category != "todo" && category != "shopping" {
		category = "groceries"
	}

	rows, err := db.Query(
		"SELECT id, category, text, due_date, done, COALESCE(archived_at, '') FROM todos WHERE category = ? AND archived = 1 ORDER BY COALESCE(archived_at, created_at) DESC",
		category,
	)
	if err != nil {
		http.Error(w, "DB error", http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	var todos []Todo
	for rows.Next() {
		var t Todo
		rows.Scan(&t.ID, &t.Category, &t.Text, &t.DueDate, &t.Done, &t.ArchivedAt)
		todos = append(todos, t)
	}

	data := struct {
		Category string
		Todos    []Todo
	}{category, todos}

	renderTemplate(w, "todo-archive.html", data)
}

func handleRestoreTodo(w http.ResponseWriter, r *http.Request) {
	if !requirePost(w, r) {
		return
	}

	id, ok := parseID(w, r)
	if !ok {
		return
	}

	var category string
	err := db.QueryRow("SELECT category FROM todos WHERE id = ?", id).Scan(&category)
	if err != nil {
		http.Error(w, "Not found", http.StatusNotFound)
		return
	}

	if _, err := db.Exec("UPDATE todos SET archived = 0 WHERE id = ?", id); err != nil {
		http.Error(w, "DB error", http.StatusInternalServerError)
		return
	}
	log.Printf("Todo restored: id=%d [%s]", id, category)

	r.URL.RawQuery = "category=" + category
	handleArchiveTodos(w, r)
}

func handlePermanentDeleteTodo(w http.ResponseWriter, r *http.Request) {
	if !requirePost(w, r) {
		return
	}

	id, ok := parseID(w, r)
	if !ok {
		return
	}

	var category string
	err := db.QueryRow("SELECT category FROM todos WHERE id = ?", id).Scan(&category)
	if err != nil {
		http.Error(w, "Not found", http.StatusNotFound)
		return
	}

	if _, err := db.Exec("DELETE FROM todos WHERE id = ?", id); err != nil {
		http.Error(w, "DB error", http.StatusInternalServerError)
		return
	}
	log.Printf("Todo permanently deleted: id=%d [%s]", id, category)

	r.URL.RawQuery = "category=" + category
	handleArchiveTodos(w, r)
}

// --- Notes ---

type Note struct {
	ID        int
	Title     string
	Content   string
	UpdatedAt string
}

type Habit struct {
	ID        int
	Name      string
	DoneToday bool
	Streak    int
}

func requirePost(w http.ResponseWriter, r *http.Request) bool {
	if r.Method != "POST" {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return false
	}
	return true
}

func parseID(w http.ResponseWriter, r *http.Request) (int, bool) {
	id, err := strconv.Atoi(r.FormValue("id"))
	if err != nil {
		http.Error(w, "Invalid ID", http.StatusBadRequest)
		return 0, false
	}
	return id, true
}

func handleNotes(w http.ResponseWriter, r *http.Request) {
	noteIDStr := r.URL.Query().Get("id")

	// Get all note titles for dropdown
	rows, err := db.Query("SELECT id, title FROM notes WHERE archived = 0 ORDER BY title ASC")
	if err != nil {
		http.Error(w, "DB error", http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	var notesList []Note
	for rows.Next() {
		var n Note
		rows.Scan(&n.ID, &n.Title)
		notesList = append(notesList, n)
	}

	if len(notesList) == 0 {
		http.Error(w, "No notes", http.StatusNotFound)
		return
	}

	// Determine which note to show
	var currentNote Note
	if noteIDStr != "" {
		noteID, _ := strconv.Atoi(noteIDStr)
		db.QueryRow("SELECT id, title, content, updated_at FROM notes WHERE id = ? AND archived = 0", noteID).Scan(
			&currentNote.ID, &currentNote.Title, &currentNote.Content, &currentNote.UpdatedAt,
		)
	}
	if currentNote.ID == 0 {
		currentNote.ID = notesList[0].ID
		currentNote.Title = notesList[0].Title
		db.QueryRow("SELECT content, updated_at FROM notes WHERE id = ?", currentNote.ID).Scan(&currentNote.Content, &currentNote.UpdatedAt)
	}

	data := struct {
		Notes       []Note
		CurrentNote Note
	}{notesList, currentNote}

	renderTemplate(w, "notes.html", data)
}

func handleSaveNote(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	id, err := strconv.Atoi(r.FormValue("id"))
	if err != nil {
		http.Error(w, "Invalid ID", http.StatusBadRequest)
		return
	}

	content := r.FormValue("content")
	if len(content) > 1_000_000 {
		http.Error(w, "Content too large", http.StatusBadRequest)
		return
	}

	// Optimistic concurrency: if the client sends the timestamp it loaded,
	// reject the save if another device already wrote a newer version.
	clientUpdatedAt := r.FormValue("updated_at")
	if clientUpdatedAt != "" {
		var currentUpdatedAt string
		db.QueryRow("SELECT updated_at FROM notes WHERE id = ?", id).Scan(&currentUpdatedAt)
		if currentUpdatedAt != clientUpdatedAt {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusConflict)
			json.NewEncoder(w).Encode(map[string]string{"status": "conflict"})
			return
		}
	}

	_, err = db.Exec("UPDATE notes SET content = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?", content, id)
	if err != nil {
		http.Error(w, "DB error", http.StatusInternalServerError)
		return
	}
	log.Printf("Note saved: id=%d (%d bytes)", id, len(content))

	var newUpdatedAt string
	db.QueryRow("SELECT updated_at FROM notes WHERE id = ?", id).Scan(&newUpdatedAt)

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "saved", "updated_at": newUpdatedAt})
}

func handleCreateNote(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	title := strings.TrimSpace(r.FormValue("title"))
	if title == "" || len(title) > 200 {
		http.Error(w, "Invalid title", http.StatusBadRequest)
		return
	}

	// Sanitize: only allow alphanumeric, spaces, hyphens, underscores
	if !validNameRe.MatchString(title) {
		http.Error(w, "Invalid characters in title", http.StatusBadRequest)
		return
	}

	result, err := db.Exec("INSERT INTO notes (title, content) VALUES (?, ?)", title, "")
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			http.Error(w, "Note with this name already exists", http.StatusConflict)
			return
		}
		http.Error(w, "DB error", http.StatusInternalServerError)
		return
	}

	newID, _ := result.LastInsertId()
	log.Printf("Note created: %q (id=%d)", title, newID)
	r.URL.RawQuery = "id=" + strconv.FormatInt(newID, 10)
	handleNotes(w, r)
}

func handleDeleteNote(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	id, err := strconv.Atoi(r.FormValue("id"))
	if err != nil {
		http.Error(w, "Invalid ID", http.StatusBadRequest)
		return
	}

	// Don't archive if it's the last active note
	var count int
	db.QueryRow("SELECT COUNT(*) FROM notes WHERE archived = 0").Scan(&count)
	if count <= 1 {
		http.Error(w, "Cannot delete the last note", http.StatusBadRequest)
		return
	}

	db.Exec("UPDATE notes SET archived = 1 WHERE id = ?", id)
	log.Printf("Note archived: id=%d", id)

	r.URL.RawQuery = ""
	handleNotes(w, r)
}

func handleRenameNote(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	id, err := strconv.Atoi(r.FormValue("id"))
	if err != nil {
		http.Error(w, "Invalid ID", http.StatusBadRequest)
		return
	}

	title := strings.TrimSpace(r.FormValue("title"))
	if title == "" || len(title) > 200 {
		http.Error(w, "Invalid title", http.StatusBadRequest)
		return
	}

	if !validNameRe.MatchString(title) {
		http.Error(w, "Invalid characters in title", http.StatusBadRequest)
		return
	}

	_, err = db.Exec("UPDATE notes SET title = ? WHERE id = ?", title, id)
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			http.Error(w, "A note with this name already exists", http.StatusConflict)
			return
		}
		http.Error(w, "DB error", http.StatusInternalServerError)
		return
	}
	log.Printf("Note renamed: id=%d -> %q", id, title)

	r.URL.RawQuery = "id=" + strconv.Itoa(id)
	handleNotes(w, r)
}

type SearchResultData struct {
	Query string
	Notes []Note
	Todos []Todo
}

func handleSearch(w http.ResponseWriter, r *http.Request) {
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	if q == "" {
		w.Write([]byte(""))
		return
	}

	pattern := "%" + q + "%"

	noteRows, err := db.Query(
		"SELECT id, title FROM notes WHERE archived = 0 AND (title LIKE ? OR content LIKE ?) ORDER BY updated_at DESC LIMIT 10",
		pattern, pattern,
	)
	var notes []Note
	if err == nil {
		defer noteRows.Close()
		for noteRows.Next() {
			var n Note
			noteRows.Scan(&n.ID, &n.Title)
			notes = append(notes, n)
		}
	}

	todoRows, err := db.Query(
		"SELECT id, category, text FROM todos WHERE archived = 0 AND done = 0 AND text LIKE ? ORDER BY created_at DESC LIMIT 10",
		pattern,
	)
	var todos []Todo
	if err == nil {
		defer todoRows.Close()
		for todoRows.Next() {
			var t Todo
			todoRows.Scan(&t.ID, &t.Category, &t.Text)
			todos = append(todos, t)
		}
	}

	renderTemplate(w, "search.html", SearchResultData{Query: q, Notes: notes, Todos: todos})
}

func handleArchiveNotes(w http.ResponseWriter, r *http.Request) {
	rows, err := db.Query("SELECT id, title FROM notes WHERE archived = 1 ORDER BY title ASC")
	if err != nil {
		http.Error(w, "DB error", http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	var notes []Note
	for rows.Next() {
		var n Note
		rows.Scan(&n.ID, &n.Title)
		notes = append(notes, n)
	}

	renderTemplate(w, "notes-archive.html", notes)
}

func handleRestoreNote(w http.ResponseWriter, r *http.Request) {
	if !requirePost(w, r) {
		return
	}

	id, ok := parseID(w, r)
	if !ok {
		return
	}

	if _, err := db.Exec("UPDATE notes SET archived = 0 WHERE id = ?", id); err != nil {
		http.Error(w, "DB error", http.StatusInternalServerError)
		return
	}
	handleArchiveNotes(w, r)
}

func handlePermanentDeleteNote(w http.ResponseWriter, r *http.Request) {
	if !requirePost(w, r) {
		return
	}

	id, ok := parseID(w, r)
	if !ok {
		return
	}

	if _, err := db.Exec("DELETE FROM notes WHERE id = ?", id); err != nil {
		http.Error(w, "DB error", http.StatusInternalServerError)
		return
	}
	handleArchiveNotes(w, r)
}

func loadHabits() []Habit {
	today := time.Now().Format("2006-01-02")
	rows, err := db.Query(`
		SELECT h.id, h.name,
		       CASE WHEN hl.habit_id IS NOT NULL THEN 1 ELSE 0 END as done_today
		FROM habits h
		LEFT JOIN habit_logs hl ON hl.habit_id = h.id AND hl.date = ?
		WHERE h.archived = 0
		ORDER BY h.created_at ASC
	`, today)
	if err != nil {
		return nil
	}
	defer rows.Close()

	var habits []Habit
	for rows.Next() {
		var h Habit
		var doneInt int
		rows.Scan(&h.ID, &h.Name, &doneInt)
		h.DoneToday = doneInt == 1
		habits = append(habits, h)
	}
	if len(habits) == 0 {
		return habits
	}

	// Only the recent unbroken run matters for streaks; a one-year floor keeps
	// this read bounded as habit_logs grows indefinitely over time.
	logRows, err := db.Query(`
		SELECT habit_id, date FROM habit_logs
		WHERE habit_id IN (SELECT id FROM habits WHERE archived = 0)
		  AND date >= date('now', '-365 days')
		ORDER BY habit_id ASC, date DESC
	`)
	if err != nil {
		return habits
	}
	defer logRows.Close()

	logsMap := make(map[int][]string)
	for logRows.Next() {
		var hid int
		var d string
		logRows.Scan(&hid, &d)
		logsMap[hid] = append(logsMap[hid], d)
	}

	yesterday := time.Now().AddDate(0, 0, -1).Format("2006-01-02")
	for i := range habits {
		dates := logsMap[habits[i].ID]
		if len(dates) == 0 || (dates[0] != today && dates[0] != yesterday) {
			continue
		}
		streak := 0
		expected := dates[0]
		for _, d := range dates {
			if d != expected {
				break
			}
			streak++
			t, _ := time.Parse("2006-01-02", expected)
			expected = t.AddDate(0, 0, -1).Format("2006-01-02")
		}
		habits[i].Streak = streak
	}
	return habits
}

func handleHabits(w http.ResponseWriter, r *http.Request) {
	habits := loadHabits()
	renderTemplate(w, "habits.html", habits)
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

	_, err := db.Exec("INSERT INTO habits (name) VALUES (?)", name)
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			http.Error(w, "Habit already exists", http.StatusConflict)
			return
		}
		http.Error(w, "DB error", http.StatusInternalServerError)
		return
	}
	log.Printf("Habit added: %q", name)

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
			http.Error(w, "Habit already exists", http.StatusConflict)
			return
		}
		http.Error(w, "DB error", http.StatusInternalServerError)
		return
	}
	log.Printf("Habit renamed: id=%d -> %q", id, name)

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

	today := time.Now().Format("2006-01-02")
	res, err := db.Exec("DELETE FROM habit_logs WHERE habit_id = ? AND date = ?", id, today)
	if err != nil {
		http.Error(w, "DB error", http.StatusInternalServerError)
		return
	}

	if n, _ := res.RowsAffected(); n == 0 {
		_, err = db.Exec("INSERT INTO habit_logs (habit_id, date) VALUES (?, ?)", id, today)
		if err != nil {
			http.Error(w, "DB error", http.StatusInternalServerError)
			return
		}
		log.Printf("Habit checked: id=%d date=%s", id, today)
	} else {
		log.Printf("Habit unchecked: id=%d date=%s", id, today)
	}

	handleHabits(w, r)
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

	handleHabits(w, r)
}

func handleArchiveHabits(w http.ResponseWriter, r *http.Request) {
	rows, err := db.Query("SELECT id, name FROM habits WHERE archived = 1 ORDER BY created_at DESC")
	if err != nil {
		http.Error(w, "DB error", http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	var habits []Habit
	for rows.Next() {
		var h Habit
		rows.Scan(&h.ID, &h.Name)
		habits = append(habits, h)
	}

	renderTemplate(w, "habits-archive.html", habits)
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

	_, err := db.Exec("DELETE FROM habits WHERE id = ?", id)
	if err != nil {
		http.Error(w, "DB error", http.StatusInternalServerError)
		return
	}

	handleArchiveHabits(w, r)
}
