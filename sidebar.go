package main

import (
	"context"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

// SidebarView holds the counts and project list shown in the desktop sidebar
// (and, for the counts, the phone's Today tab and destination sheet).
type SidebarView struct {
	Today, Overdue, Inbox, Groceries, Buys int
	Projects                               []ProjectSummary
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
	ctx, cancel := readContext(r)
	defer cancel()
	view, err := loadSidebar(ctx)
	if err != nil {
		writeDomainError(w, err, "Could not load lists")
		return
	}
	view.OOB = true
	renderTemplate(w, r, "sidebar-live", view)
}

// ArchiveView is the single archive: one kind of archived item at a time.
type ArchiveView struct {
	Kind                 string
	Query                string
	Offset               int
	PreviousURL, NextURL string
	Kinds                []ArchiveKind
	Todos                []Todo
	Notes                []Note
	Projects             []ProjectSummary
}
type ArchiveKind struct{ Key, Label string }

var archiveKinds = []ArchiveKind{{"todo", "Tasks"}, {"projects", "Projects"}, {"groceries", "Groceries"}, {"shopping", "Buys"}, {"notes", "Notes"}}

func handleArchive(w http.ResponseWriter, r *http.Request) {
	renderArchive(w, r, r.URL.Query().Get("kind"))
}

func renderArchive(w http.ResponseWriter, r *http.Request, kind string) {
	ctx, cancel := readContext(r)
	defer cancel()
	view := ArchiveView{Kind: "todo", Kinds: archiveKinds}
	for _, k := range archiveKinds {
		if k.Key == kind {
			view.Kind = kind
		}
	}
	view.Query = strings.TrimSpace(r.FormValue("q"))
	view.Offset, _ = strconv.Atoi(r.FormValue("offset"))
	if view.Offset < 0 {
		view.Offset = 0
	}
	var err error
	pattern := "%" + view.Query + "%"
	switch view.Kind {
	case "projects":
		view.Projects, err = loadArchivedProjects(ctx, pattern, view.Offset)
	case "notes":
		view.Notes, err = archivedNotesPage(ctx, pattern, view.Offset)
	default:
		view.Todos, err = archivedTodosPage(ctx, view.Kind, pattern, view.Offset)
	}
	pageURL := func(offset int) string {
		return "/archive?" + url.Values{"kind": {view.Kind}, "q": {view.Query}, "offset": {strconv.Itoa(offset)}}.Encode()
	}
	if view.Offset > 0 {
		view.PreviousURL = pageURL(max(0, view.Offset-200))
	}
	if len(view.Todos) > 200 {
		view.Todos = view.Todos[:200]
		view.NextURL = pageURL(view.Offset + 200)
	}
	if len(view.Notes) > 200 {
		view.Notes = view.Notes[:200]
		view.NextURL = pageURL(view.Offset + 200)
	}
	if len(view.Projects) > 200 {
		view.Projects = view.Projects[:200]
		view.NextURL = pageURL(view.Offset + 200)
	}
	if err != nil {
		http.Error(w, "Could not load the archive", http.StatusInternalServerError)
		return
	}
	renderTemplate(w, r, "archive.html", view)
}

func archivedTodos(category string) ([]Todo, error) {
	return archivedTodosPage(context.Background(), category, "%", 0)
}

func archivedTodosPage(ctx context.Context, category, pattern string, offset int) ([]Todo, error) {
	rows, err := db.QueryContext(ctx, `SELECT id,category,text,due_date,done,COALESCE(archived_at,'') FROM todos
		WHERE category=? AND archived=1 AND text LIKE ? ORDER BY COALESCE(archived_at,created_at) DESC,id DESC LIMIT 201 OFFSET ?`, category, pattern, offset)
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

func archivedNotes() ([]Note, error) { return archivedNotesPage(context.Background(), "%", 0) }

func archivedNotesPage(ctx context.Context, pattern string, offset int) ([]Note, error) {
	rows, err := db.QueryContext(ctx, `SELECT id,title FROM notes WHERE archived=1 ORDER BY lower(title) LIMIT 200`)
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

func loadArchivedProjects(ctx context.Context, pattern string, offset int) ([]ProjectSummary, error) {
	rows, err := db.QueryContext(ctx, `SELECT id,name,archived,completed FROM projects WHERE (archived=1 OR completed=1) AND name LIKE ? ORDER BY position,id LIMIT 201 OFFSET ?`, pattern, offset)
	if err != nil {
		return nil, err
	}
	var projects []ProjectSummary
	for rows.Next() {
		var p ProjectSummary
		if err = rows.Scan(&p.ID, &p.Name, &p.Archived, &p.Completed); err != nil {
			rows.Close()
			return nil, err
		}
		projects = append(projects, p)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	return projects, fillProjectProgress(ctx, projects)
}
