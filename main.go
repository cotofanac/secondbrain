package main

import (
	"bytes"
	"crypto/rand"
	"crypto/subtle"
	"database/sql"
	"embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"html/template"
	"io/fs"
	"log"
	"net/http"
	"os"
	"regexp"
	"strconv"
	"strings"
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

)

const sessionDuration = 72 * time.Hour

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
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "strict-origin-when-cross-origin")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self' 'unsafe-inline'; style-src 'self' 'unsafe-inline'; img-src 'self' data:")
		next.ServeHTTP(w, r)
	})
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
			res, err := db.Exec(
				"UPDATE todos SET archived = 1, archived_at = CURRENT_TIMESTAMP WHERE category = 'todo' AND archived = 0 AND created_at <= datetime('now', '-7 days')",
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
			t, err := time.Parse("2006-01-02 15:04:05", s)
			if err != nil {
				return ""
			}
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
			t, err := time.Parse("2006-01-02", s)
			if err != nil {
				return false
			}
			return t.Before(time.Now().Truncate(24 * time.Hour))
		},
		"todoArchiveHint": func(createdAt string) string {
			if createdAt == "" {
				return ""
			}
			t, err := time.Parse("2006-01-02 15:04:05", createdAt)
			if err != nil {
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
			t, err := time.Parse("2006-01-02 15:04:05", archivedAt)
			if err != nil {
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
	}
	templates = template.Must(template.New("").Funcs(funcMap).ParseFS(templateFiles, "templates/*.html"))
	log.Printf("Templates loaded")

	staticFS, _ := fs.Sub(staticFiles, "static")
	http.Handle("/static/", http.StripPrefix("/static/", http.FileServer(http.FS(staticFS))))

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
	log.Printf("SecondBrain listening on :%s (inactivity timeout: %d min)", port, inactivityTimeoutMinutes)
	log.Fatal(http.ListenAndServe(":"+port, loggingMiddleware(securityHeaders(http.DefaultServeMux))))
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
	db, err = sql.Open("sqlite3", dbPath+"?_journal_mode=WAL&_busy_timeout=5000")
	if err != nil {
		log.Fatal(err)
	}

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

	db.Exec("DELETE FROM sessions WHERE expires_at <= datetime('now')")
	db.Exec("ALTER TABLE todos ADD COLUMN archived_at DATETIME DEFAULT NULL")

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
	rand.Read(b)
	return hex.EncodeToString(b)
}

func createSession() string {
	token := generateToken()
	expiry := time.Now().UTC().Add(sessionDuration).Format(time.RFC3339)
	db.Exec("INSERT INTO sessions (token, expires_at) VALUES (?, ?)", token, expiry)
	return token
}

func validSession(token string) bool {
	if token == "" {
		return false
	}
	var count int
	err := db.QueryRow(
		"SELECT COUNT(*) FROM sessions WHERE token = ? AND expires_at > datetime('now')",
		token,
	).Scan(&count)
	return err == nil && count > 0
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

		if len(submitted) != 8 || subtle.ConstantTimeCompare([]byte(submitted), []byte(passcode)) != 1 {
			log.Printf("Failed login attempt from %s", r.RemoteAddr)
			if isHTMX {
				w.Header().Set("HX-Retarget", "#error")
				w.Header().Set("HX-Reswap", "innerHTML")
				fmt.Fprint(w, "Wrong passcode")
			} else {
				renderTemplate(w, "login.html", "Wrong passcode")
			}
			return
		}

		log.Printf("Successful login from %s", r.RemoteAddr)
		token := createSession()
		http.SetCookie(w, &http.Cookie{
			Name:     "session",
			Value:    token,
			Path:     "/",
			HttpOnly: true,
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
	if r.Method != "POST" {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	id, err := strconv.Atoi(r.FormValue("id"))
	if err != nil {
		http.Error(w, "Invalid ID", http.StatusBadRequest)
		return
	}

	var category string
	err = db.QueryRow("SELECT category FROM todos WHERE id = ?", id).Scan(&category)
	if err != nil {
		http.Error(w, "Not found", http.StatusNotFound)
		return
	}

	db.Exec("UPDATE todos SET done = CASE WHEN done = 0 THEN 1 ELSE 0 END WHERE id = ?", id)
	log.Printf("Todo toggled: id=%d [%s]", id, category)

	r.URL.RawQuery = "category=" + category
	handleTodos(w, r)
}

func handleDeleteTodo(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	id, err := strconv.Atoi(r.FormValue("id"))
	if err != nil {
		http.Error(w, "Invalid ID", http.StatusBadRequest)
		return
	}

	var category string
	err = db.QueryRow("SELECT category FROM todos WHERE id = ?", id).Scan(&category)
	if err != nil {
		http.Error(w, "Not found", http.StatusNotFound)
		return
	}

	db.Exec("UPDATE todos SET archived = 1, archived_at = CURRENT_TIMESTAMP WHERE id = ?", id)
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

	db.Exec("UPDATE todos SET done = 0 WHERE category = ? AND archived = 0 AND done = 1", category)
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
	if r.Method != "POST" {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	id, err := strconv.Atoi(r.FormValue("id"))
	if err != nil {
		http.Error(w, "Invalid ID", http.StatusBadRequest)
		return
	}

	var category string
	err = db.QueryRow("SELECT category FROM todos WHERE id = ?", id).Scan(&category)
	if err != nil {
		http.Error(w, "Not found", http.StatusNotFound)
		return
	}

	db.Exec("UPDATE todos SET archived = 0 WHERE id = ?", id)
	log.Printf("Todo restored: id=%d [%s]", id, category)

	r.URL.RawQuery = "category=" + category
	handleArchiveTodos(w, r)
}

func handlePermanentDeleteTodo(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	id, err := strconv.Atoi(r.FormValue("id"))
	if err != nil {
		http.Error(w, "Invalid ID", http.StatusBadRequest)
		return
	}

	var category string
	err = db.QueryRow("SELECT category FROM todos WHERE id = ?", id).Scan(&category)
	if err != nil {
		http.Error(w, "Not found", http.StatusNotFound)
		return
	}

	db.Exec("DELETE FROM todos WHERE id = ?", id)
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
		db.QueryRow("SELECT id, title, content, updated_at FROM notes WHERE id = ?", noteID).Scan(
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
	if r.Method != "POST" {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	id, err := strconv.Atoi(r.FormValue("id"))
	if err != nil {
		http.Error(w, "Invalid ID", http.StatusBadRequest)
		return
	}

	db.Exec("UPDATE notes SET archived = 0 WHERE id = ?", id)
	handleArchiveNotes(w, r)
}

func handlePermanentDeleteNote(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	id, err := strconv.Atoi(r.FormValue("id"))
	if err != nil {
		http.Error(w, "Invalid ID", http.StatusBadRequest)
		return
	}

	db.Exec("DELETE FROM notes WHERE id = ?", id)
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

	logRows, err := db.Query(`
		SELECT habit_id, date FROM habit_logs
		WHERE habit_id IN (SELECT id FROM habits WHERE archived = 0)
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
