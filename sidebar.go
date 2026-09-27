package main

import "net/http"

// SidebarView holds the counts and project list shown in the desktop sidebar
// (and, for the counts, the phone's Today tab and destination sheet).
type SidebarView struct {
	Today, Overdue, Inbox, Groceries, Buys int
	Projects                               []Project
	// OOB marks a refresh: each part replaces its counterpart in the page.
	OOB bool
}

func sidebarFrom(ws Workspace, today TodayView) SidebarView {
	return SidebarView{
		Today:     today.DueLeft,
		Overdue:   len(today.Overdue),
		Inbox:     len(ws.Tasks.Tasks),
		Groceries: len(ws.Groceries.Tasks),
		Buys:      len(ws.Buys.Tasks),
		Projects:  ws.Projects,
	}
}

func handleSidebar(w http.ResponseWriter, r *http.Request) {
	ws, err := loadWorkspace()
	if err != nil {
		http.Error(w, "Could not load lists", http.StatusInternalServerError)
		return
	}
	today, err := loadToday(appNow())
	if err != nil {
		http.Error(w, "Could not load Today", http.StatusInternalServerError)
		return
	}
	view := sidebarFrom(ws, today)
	view.OOB = true
	renderTemplate(w, r, "sidebar-live", view)
}

// ArchiveView is the single archive: one kind of archived item at a time.
type ArchiveView struct {
	Kind     string
	Kinds    []ArchiveKind
	Todos    []Todo
	Notes    []Note
	Projects []Project
}
type ArchiveKind struct{ Key, Label string }

var archiveKinds = []ArchiveKind{{"todo", "Tasks"}, {"projects", "Projects"}, {"groceries", "Groceries"}, {"shopping", "Buys"}, {"notes", "Notes"}}

func handleArchive(w http.ResponseWriter, r *http.Request) {
	renderArchive(w, r, r.URL.Query().Get("kind"))
}

func renderArchive(w http.ResponseWriter, r *http.Request, kind string) {
	view := ArchiveView{Kind: "todo", Kinds: archiveKinds}
	for _, k := range archiveKinds {
		if k.Key == kind {
			view.Kind = kind
		}
	}
	var err error
	switch view.Kind {
	case "projects":
		var ws Workspace
		if ws, err = loadWorkspace(); err == nil {
			view.Projects = ws.ArchivedProjects
		}
	case "notes":
		view.Notes, err = archivedNotes()
	default:
		view.Todos, err = archivedTodos(view.Kind)
	}
	if err != nil {
		http.Error(w, "Could not load the archive", http.StatusInternalServerError)
		return
	}
	renderTemplate(w, r, "archive.html", view)
}

func archivedTodos(category string) ([]Todo, error) {
	rows, err := db.Query(`SELECT id,category,text,due_date,done,COALESCE(archived_at,'') FROM todos
		WHERE category=? AND archived=1 ORDER BY COALESCE(archived_at,created_at) DESC LIMIT 200`, category)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var todos []Todo
	for rows.Next() {
		var t Todo
		if err = rows.Scan(&t.ID, &t.Category, &t.Text, &t.DueDate, &t.Done, &t.ArchivedAt); err != nil {
			return nil, err
		}
		todos = append(todos, t)
	}
	return todos, rows.Err()
}

func archivedNotes() ([]Note, error) {
	rows, err := db.Query(`SELECT id,title FROM notes WHERE archived=1 ORDER BY lower(title) LIMIT 200`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var notes []Note
	for rows.Next() {
		var n Note
		if err = rows.Scan(&n.ID, &n.Title); err != nil {
			return nil, err
		}
		notes = append(notes, n)
	}
	return notes, rows.Err()
}
