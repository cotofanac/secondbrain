package main

import (
	"context"
	"fmt"
	"strings"
)

// ProjectProgress is an aggregate; TaskGroup contains only displayed rows.
type ProjectProgress struct{ Total, Completed int }

func fillProjectProgress(ctx context.Context, projects []ProjectSummary) error {
	if len(projects) == 0 {
		return nil
	}
	args := make([]any, len(projects))
	lookup := make(map[int]*ProjectSummary, len(projects))
	for i := range projects {
		projects[i].Progress = ProjectProgress{}
		args[i] = projects[i].ID
		lookup[projects[i].ID] = &projects[i]
	}
	// SQLite otherwise chooses the category index and scans archived bodies.
	// This covering index is part of schema 9 and matches this progress rule.
	rows, err := db.QueryContext(ctx, `SELECT project_id,count(*),COALESCE(sum(done),0) FROM todos INDEXED BY idx_todos_progress WHERE category='todo' AND (archived=0 OR done=1) AND project_id IN (`+strings.TrimSuffix(strings.Repeat("?,", len(projects)), ",")+`) GROUP BY project_id`, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var id, total, done int
		if err = rows.Scan(&id, &total, &done); err != nil {
			return err
		}
		if p := lookup[id]; p != nil {
			p.Progress = ProjectProgress{Total: total, Completed: done}
		}
	}
	return rows.Err()
}

func loadProjectSummaries(ctx context.Context, archived bool) ([]ProjectSummary, error) {
	filter := "archived=0 AND completed=0"
	if archived {
		filter = "(archived=1 OR completed=1)"
	}
	rows, err := db.QueryContext(ctx, `SELECT id,name,archived,completed FROM projects WHERE `+filter+` ORDER BY position,id`)
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

func loadTaskGroup(ctx context.Context, category string, projectID, headingID int) (taskGroupView, error) {
	g := TaskGroup{Key: "tasks", Name: "Tasks", Category: category, ProjectID: projectID, HeadingID: headingID}
	view := taskGroupView{Group: g}
	if category == "groceries" {
		g.Key, g.Name = "groceries", "Groceries"
	}
	if category == "shopping" {
		g.Key, g.Name = "shopping", "Buys"
	}
	if projectID != 0 {
		p := ProjectSummary{ID: projectID}
		if err := db.QueryRowContext(ctx, `SELECT name FROM projects WHERE id=? AND archived=0 AND completed=0`, projectID).Scan(&p.Name); err != nil {
			return view, err
		}
		projects := []ProjectSummary{p}
		if err := fillProjectProgress(ctx, projects); err != nil {
			return view, err
		}
		view.Project = &projects[0]
		g.Key = fmt.Sprint("project-", projectID)
		if headingID != 0 {
			if err := db.QueryRowContext(ctx, `SELECT name FROM headings WHERE id=? AND project_id=?`, headingID, projectID).Scan(&g.Name); err != nil {
				return view, err
			}
			g.Key = fmt.Sprint("heading-", headingID)
		}
	}
	rows, err := db.QueryContext(ctx, `SELECT id,category,text,due_date,done,COALESCE(project_id,0),COALESCE(heading_id,0),revision,repeat
		FROM todos t WHERE `+activeTaskSQL+` AND category=? AND project_id IS NULLIF(?,0) AND heading_id IS NULLIF(?,0) `+taskOrderSQL, category, projectID, headingID)
	if err != nil {
		return view, err
	}
	defer rows.Close()
	for rows.Next() {
		var t Todo
		if err = rows.Scan(&t.ID, &t.Category, &t.Text, &t.DueDate, &t.Done, &t.ProjectID, &t.HeadingID, &t.Revision, &t.Repeat); err != nil {
			return view, err
		}
		g.add(t)
	}
	view.Group = g
	return view, rows.Err()
}

func loadSidebar(ctx context.Context) (SidebarView, error) {
	var v SidebarView
	today := appNow().Format("2006-01-02")
	// Enumerate active memberships first. CROSS JOIN fixes that small outer
	// set, so hidden project history cannot turn a count into a broad row scan.
	rows, err := db.QueryContext(ctx, `SELECT category,sum(n),sum(due),sum(overdue) FROM (
        SELECT category,count(*) AS n,COALESCE(sum(due_date<>'' AND due_date<=?),0) AS due,
          COALESCE(sum(due_date<>'' AND due_date<?),0) AS overdue
          FROM todos t WHERE project_id IS NULL AND done=0 AND `+activeTaskSQL+` GROUP BY category
        UNION ALL
        SELECT t.category,count(*),COALESCE(sum(t.due_date<>'' AND t.due_date<=?),0),
          COALESCE(sum(t.due_date<>'' AND t.due_date<?),0)
          FROM projects p CROSS JOIN todos t ON t.project_id=p.id
          WHERE p.archived=0 AND p.completed=0 AND t.done=0 AND `+activeTaskSQL+` GROUP BY t.category
    ) GROUP BY category`, today, today, today, today)
	if err != nil {
		return v, err
	}
	for rows.Next() {
		var category string
		var count, due, overdue int
		if err = rows.Scan(&category, &count, &due, &overdue); err != nil {
			rows.Close()
			return v, err
		}
		switch category {
		case "todo":
			v.Today, v.Overdue = due, overdue
		case "groceries":
			v.Groceries = count
		case "shopping":
			v.Buys = count
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return v, err
	}
	if err = db.QueryRowContext(ctx, `SELECT count(*) FROM todos WHERE category='todo' AND archived=0 AND done=0 AND project_id IS NULL`).Scan(&v.Inbox); err != nil {
		return v, err
	}
	v.Projects, err = loadProjectSummaries(ctx, false)
	return v, err
}
