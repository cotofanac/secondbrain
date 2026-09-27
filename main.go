package main

import (
	"bytes"
	"context"
	"crypto/sha256"
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
	"sort"
	"strconv"
	"strings"
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
	// validNameRe guards note titles and project and heading names. It is deliberately
	// permissive: anything except C0/C7F control characters is allowed, so
	// punctuation, accents and emoji all pass. It is not an XSS defence —
	// html/template escapes every value by context at render time — it only
	// keeps control characters out of stored names and log lines.
	validNameRe = regexp.MustCompile(`^[^\x00-\x1f\x7f]+$`)
	// assetVersion is a short content hash of the code assets, appended as
	// ?v= to asset URLs and baked into the service-worker cache name so a
	// deploy invalidates stale JS/CSS instead of serving it for up to a day.
	assetVersion string
)

// computeAssetVersion hashes the code assets so the version changes only when
// one of them changes.
func computeAssetVersion() string {
	h := sha256.New()
	for _, name := range []string{"static/app.js", "static/notes.js", "static/style.css", "static/htmx.min.js", "static/workspace.js", "static/push.js", "static/login.js", "static/sw.js", "static/manifest.json"} {
		b, err := staticFiles.ReadFile(name)
		if err != nil {
			// Fall back to a build-time value so the app still boots.
			return strconv.FormatInt(appNow().Unix(), 16)
		}
		h.Write(b)
	}
	return hex.EncodeToString(h.Sum(nil))[:8]
}

const sessionDuration = 72 * time.Hour

// formatDueDate shows a stored YYYY-MM-DD date as "Jan 2".
func formatDueDate(s string) string {
	if s == "" {
		return ""
	}
	t, err := time.Parse("2006-01-02", s)
	if err != nil {
		return s
	}
	return t.Format("Jan 2")
}

// parseDBTime parses a stored UTC RFC3339 timestamp.
func parseDBTime(s string) (time.Time, bool) {
	t, err := time.Parse(time.RFC3339, s)
	return t.UTC(), err == nil
}

func renderTemplate(w http.ResponseWriter, r *http.Request, name string, data any) {
	var buf bytes.Buffer
	if err := templates.ExecuteTemplate(&buf, name, data); err != nil {
		log.Printf("Template error (%s): %v", name, err)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}
	writeHTML(w, r, buf.Bytes())
}

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Bound every state-changing request before FormValue or a JSON decoder
		// reads it. Notes allow up to 1 MB, leaving room for form encoding while
		// preventing an authenticated or accidental oversized upload from
		// consuming unbounded memory or temporary disk space.
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			r.Body = http.MaxBytesReader(w, r.Body, 2<<20)
		}
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
		// Scripts come only from /static: templates carry no inline script or
		// on* handlers (see data-call in app.js), so injected markup cannot run
		// code. Styles keep 'unsafe-inline' for style attributes (progress
		// widths) and htmx's indicator styles; that cannot execute script.
		// base-uri stops a planted <base> from re-pointing every relative URL,
		// form-action stops a planted form from posting the passcode off-origin,
		// object-src blocks plugin content, and frame-ancestors is the modern
		// X-Frame-Options.
		w.Header().Set("Content-Security-Policy",
			"default-src 'self'; script-src 'self'; "+
				"style-src 'self' 'unsafe-inline'; img-src 'self' data:; "+
				"base-uri 'self'; form-action 'self'; object-src 'none'; "+
				"frame-ancestors 'none'")
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
	expectedScheme := "http"
	if isHTTPS(r) {
		expectedScheme = "https"
	}
	return strings.EqualFold(u.Scheme, expectedScheme) && strings.EqualFold(u.Host, r.Host)
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
		start := appNow()
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

// startMaintenance runs hourly housekeeping: archiving, backups and cleanup.
func startMaintenance() {
	go func() {
		for {
			if n, err := archiveStaleTodos(appNow()); err != nil {
				log.Printf("Auto-archive failed: %v", err)
			} else if n > 0 {
				log.Printf("Auto-archived %d todo items", n)
			}
			if path, err := runDailyBackup(appNow()); err != nil {
				log.Printf("Backup failed: %v", err)
			} else if path != "" {
				log.Printf("Backup written: %s", path)
			}
			if n, err := pruneDeliveries(appNow()); err != nil {
				log.Printf("Delivery cleanup failed: %v", err)
			} else if n > 0 {
				log.Printf("Removed %d old notification deliveries", n)
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

	configurePushReminders()
	configureTrustedProxies()

	configureBackups(dataDirectory())
	initDB()
	defer db.Close()

	initPush()
	initScheduleSettings()

	funcMap := template.FuncMap{
		"formatDate": formatDueDate,
		"formatUpdated": func(s string) string {
			// Stored timestamps are UTC; display them in the server's local zone
			// (set via TZ) so the wall clock is right.
			t, ok := parseDBTime(s)
			if !ok {
				return ""
			}
			t = t.In(appLocation())
			if time.Since(t) < 24*time.Hour {
				return t.Format("3:04 PM")
			}
			if t.Year() == appNow().Year() {
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
			return s < appNow().Format("2006-01-02")
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
	if err := compressStaticAssets(staticFS); err != nil {
		log.Fatalf("Compress static assets: %v", err)
	}
	staticHandler := serveStatic(http.StripPrefix("/static/", http.FileServer(http.FS(staticFS))))
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
		w.Header().Set("Service-Worker-Allowed", "/")
		w.Header().Set("Content-Type", "application/javascript")
		w.Header().Set("Cache-Control", "no-cache")
		w.Write(swBody)
	})

	http.HandleFunc("/health", handleHealth)
	http.HandleFunc("/search", authMiddleware(handleSearch))
	http.HandleFunc("/", authMiddleware(handleIndex))
	http.HandleFunc("/login", handleLogin)
	http.HandleFunc("/logout", handleLogout)

	registerWorkspaceRoutes()
	registerScheduleRoutes()

	// Todo API
	http.HandleFunc("/todos", authMiddleware(handleTodos))
	http.HandleFunc("/todos/add", authMiddleware(handleAddTodo))
	http.HandleFunc("/todos/toggle", authMiddleware(handleToggleTodo))
	http.HandleFunc("/todos/delete", authMiddleware(handleDeleteTodo))
	http.HandleFunc("/todos/clear-checked", authMiddleware(handleClearChecked))
	http.HandleFunc("/todos/restore", authMiddleware(handleRestoreTodo))
	http.HandleFunc("/todos/permanent-delete", authMiddleware(handlePermanentDeleteTodo))

	// Notes API
	http.HandleFunc("/notes", authMiddleware(handleNotes))
	http.HandleFunc("/notes/save", authMiddleware(handleSaveNote))
	http.HandleFunc("/notes/create", authMiddleware(handleCreateNote))
	http.HandleFunc("/notes/delete", authMiddleware(handleDeleteNote))
	http.HandleFunc("/notes/rename", authMiddleware(handleRenameNote))
	http.HandleFunc("/notes/restore", authMiddleware(handleRestoreNote))
	http.HandleFunc("/notes/permanent-delete", authMiddleware(handlePermanentDeleteNote))

	// Push notifications
	http.HandleFunc("/push/public-key", authMiddleware(handlePushPublicKey))
	http.HandleFunc("/push/subscribe", authMiddleware(handlePushSubscribe))
	http.HandleFunc("/push/unsubscribe", authMiddleware(handlePushUnsubscribe))
	http.HandleFunc("/push/test", authMiddleware(handlePushTest))

	startMaintenance()
	startPushReminders()

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

func dataDirectory() string {
	if dir := os.Getenv("DATA_DIR"); dir != "" {
		return dir
	}
	return "./data"
}

func initDB() {
	dataDir := dataDirectory()
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

	if err := migrate(db, appNow()); err != nil {
		log.Fatalf("Database migration failed: %v", err)
	}

	// Seed a default note if none exist
	var count int
	db.QueryRow("SELECT COUNT(*) FROM notes").Scan(&count)
	if count == 0 {
		db.Exec("INSERT INTO notes (title, content) VALUES (?, ?)", "Quick Notes", "")
		log.Printf("Database initialized with default note")
	}
	log.Printf("Database ready")
}

// --- Pages ---

// maxSuggestions bounds the autocomplete list. Both datalists are inlined into
// the page on every load, so this query must not grow with the table. In
// practice the reuse logic in handleAddTodo keeps the row count near the number
// of distinct items the user actually buys, but nothing enforced that.
const maxSuggestions = 200

// loadSuggestions returns autocomplete entries for a checklist category, newest
// first (a recently used item is the likeliest next entry) and then sorted for
// a stable, scannable datalist.
func loadSuggestions(category string) []string {
	rows, err := db.Query(
		`SELECT text FROM todos WHERE category = ?
		 GROUP BY text ORDER BY MAX(created_at) DESC LIMIT ?`,
		category, maxSuggestions,
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
	// The query orders by recency to pick *which* entries survive the limit;
	// present them alphabetically, as before.
	sort.Strings(items)
	return items
}

func handleIndex(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
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
		db.QueryRow("SELECT content, updated_at, revision FROM notes WHERE id = ?", currentNote.ID).Scan(&currentNote.Content, &currentNote.UpdatedAt, &currentNote.Revision)
	}

	workspace, err := loadWorkspace()
	if err != nil {
		http.Error(w, "Could not load workspace", 500)
		return
	}
	today, err := loadToday(appNow())
	if err != nil {
		http.Error(w, "Could not load Today", 500)
		return
	}

	data := struct {
		Notes                    []Note
		CurrentNote              Note
		InactivityTimeoutSeconds int
		Timezone                 string
		Workspace                Workspace
		Today                    TodayView
		Sidebar                  SidebarView
		GrocerySuggestions       []string
		ShoppingSuggestions      []string
	}{
		Timezone:                 appLocation().String(),
		Workspace:                workspace,
		Today:                    today,
		Sidebar:                  sidebarFrom(workspace, today),
		Notes:                    notesList,
		CurrentNote:              currentNote,
		InactivityTimeoutSeconds: inactivityTimeoutMinutes * 60,
		GrocerySuggestions:       loadSuggestions("groceries"),
		ShoppingSuggestions:      loadSuggestions("shopping"),
	}

	renderTemplate(w, r, "index.html", data)
}

// nameConflictMessage says *which* row is holding a name after a UNIQUE
// violation. Both columns are unique across archived rows, so without this the
// user can be blocked by an item that appears nowhere in the current view.
// table and column are compile-time literals from the call sites, never user
// input, so interpolating them is safe.
func nameConflictMessage(table, column, value, noun string) string {
	var archived int
	err := db.QueryRow(
		fmt.Sprintf("SELECT archived FROM %s WHERE %s = ?", table, column), value,
	).Scan(&archived)
	if err == nil && archived == 1 {
		return "An archived " + noun + " already uses that name"
	}
	return "A " + noun + " with this name already exists"
}

// hxTrigger asks htmx to dispatch a named client event when the response
// arrives, carrying detail as JSON. The JSON rides in an HTTP header, which
// browsers decode as Latin-1, so keep message text ASCII.
//
// Events in use: "sbUndo" ({kind, id}) offers to reverse a one-tap archive,
// "sbNotice" ({message}) shows a plain confirmation, and "sbWorkspaceChanged"
// ({message?}) refreshes the task lists after a change made elsewhere.
func hxTrigger(w http.ResponseWriter, name string, detail any) {
	payload, err := json.Marshal(map[string]any{name: detail})
	if err != nil {
		log.Printf("hxTrigger %s: %v", name, err)
		return
	}
	w.Header().Set("HX-Trigger", string(payload))
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
