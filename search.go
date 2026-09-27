package main

import (
	"net/http"
	"strings"
)

type SearchResultData struct {
	Query    string
	Notes    []Note
	Todos    []Todo
	Projects []Project
}

func handleSearch(w http.ResponseWriter, r *http.Request) {
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	if q == "" {
		w.Write([]byte(""))
		return
	}
	pattern := "%" + q + "%"
	data := SearchResultData{Query: q}

	rows, err := db.Query("SELECT id,title FROM notes WHERE archived=0 AND (title LIKE ? OR content LIKE ?) ORDER BY updated_at DESC LIMIT 10", pattern, pattern)
	if err == nil {
		for rows.Next() {
			var n Note
			if rows.Scan(&n.ID, &n.Title) == nil {
				data.Notes = append(data.Notes, n)
			}
		}
		rows.Close()
	}
	rows, err = db.Query(`SELECT t.id,t.category,t.text,t.done FROM todos t WHERE `+activeTaskSQL+` AND text LIKE ? ORDER BY t.done,t.created_at DESC LIMIT 15`, pattern)
	if err == nil {
		for rows.Next() {
			var t Todo
			if rows.Scan(&t.ID, &t.Category, &t.Text, &t.Done) == nil {
				data.Todos = append(data.Todos, t)
			}
		}
		rows.Close()
	}
	rows, err = db.Query(`SELECT id,name FROM projects WHERE archived=0 AND completed=0 AND name LIKE ? ORDER BY position,id LIMIT 10`, pattern)
	if err == nil {
		for rows.Next() {
			var p Project
			if rows.Scan(&p.ID, &p.Name) == nil {
				data.Projects = append(data.Projects, p)
			}
		}
		rows.Close()
	}
	renderTemplate(w, r, "search.html", data)
}
