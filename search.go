package main

import (
	"net/http"
	"strings"
)

type SearchResultData struct {
	Query    string
	Notes    []Note
	Todos    []Todo
	Habits   []Habit
	Projects []Project
	Stages   []Stage
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
	rows, err = db.Query(`SELECT id,name,period,target FROM habits WHERE archived=0 AND name LIKE ? ORDER BY name LIMIT 10`, pattern)
	if err == nil {
		for rows.Next() {
			var h Habit
			if rows.Scan(&h.ID, &h.Name, &h.Period, &h.Target) == nil {
				data.Habits = append(data.Habits, h)
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
	rows, err = db.Query(`SELECT s.id,s.project_id,s.name,p.name FROM stages s JOIN projects p ON p.id=s.project_id WHERE s.archived=0 AND p.archived=0 AND p.completed=0 AND s.name LIKE ? ORDER BY p.position,s.position,s.id LIMIT 10`, pattern)
	if err == nil {
		for rows.Next() {
			var s Stage
			if rows.Scan(&s.ID, &s.ProjectID, &s.Name, &s.ProjectName) == nil {
				data.Stages = append(data.Stages, s)
			}
		}
		rows.Close()
	}
	renderTemplate(w, "search.html", data)
}
