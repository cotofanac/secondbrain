package main

import (
	"context"
	"net/http"
	"strings"
)

type SearchResultData struct {
	Query    string
	Notes    []Note
	Todos    []Todo
	Projects []ProjectSummary
}

func handleSearch(w http.ResponseWriter, r *http.Request) {
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	if q == "" {
		return
	}
	ctx, cancel := readContext(r)
	defer cancel()
	data, err := loadSearch(ctx, q)
	if err != nil {
		writeDomainError(w, err, "Could not search")
		return
	}
	renderTemplate(w, r, "search.html", data)
}

// Return a complete result or an error. A failed database read must not look
// like an empty search, and each cursor closes before the next query starts.
func loadSearch(ctx context.Context, q string) (SearchResultData, error) {
	data := SearchResultData{Query: q}
	pattern := "%" + q + "%"
	rows, err := db.QueryContext(ctx, `SELECT id,title FROM notes WHERE archived=0 AND (title LIKE ? OR content LIKE ?) ORDER BY updated_at DESC,id DESC LIMIT 10`, pattern, pattern)
	if err != nil {
		return data, err
	}
	for rows.Next() {
		var n Note
		if err = rows.Scan(&n.ID, &n.Title); err != nil {
			rows.Close()
			return data, err
		}
		data.Notes = append(data.Notes, n)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return data, err
	}
	rows, err = db.QueryContext(ctx, `SELECT t.id,t.category,t.text,t.done FROM todos t WHERE `+activeTaskSQL+` AND text LIKE ? ORDER BY t.done,t.created_at DESC,t.id DESC LIMIT 15`, pattern)
	if err != nil {
		return data, err
	}
	for rows.Next() {
		var t Todo
		if err = rows.Scan(&t.ID, &t.Category, &t.Text, &t.Done); err != nil {
			rows.Close()
			return data, err
		}
		data.Todos = append(data.Todos, t)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return data, err
	}
	rows, err = db.QueryContext(ctx, `SELECT id,name FROM projects WHERE archived=0 AND completed=0 AND name LIKE ? ORDER BY position,id LIMIT 10`, pattern)
	if err != nil {
		return data, err
	}
	for rows.Next() {
		var p ProjectSummary
		if err = rows.Scan(&p.ID, &p.Name); err != nil {
			rows.Close()
			return data, err
		}
		data.Projects = append(data.Projects, p)
	}
	err = rows.Err()
	rows.Close()
	return data, err
}
