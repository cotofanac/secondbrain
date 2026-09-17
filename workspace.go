package main

import (
	"database/sql"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// activeTaskSQL also excludes children of archived stages and projects.
const activeTaskSQL = `t.archived=0 AND (t.project_id IS NULL OR EXISTS(SELECT 1 FROM projects p WHERE p.id=t.project_id AND p.archived=0 AND p.completed=0)) AND (t.stage_id IS NULL OR EXISTS(SELECT 1 FROM stages s WHERE s.id=t.stage_id AND s.archived=0))`

type TaskGroup struct {
	Key, Name, Category                  string
	ProjectID, StageID, Total, Completed int
	Tasks, Done                          []Todo
}
type Stage struct {
	ID, ProjectID int
	Name          string
	ProjectName   string
	Archived      bool
	Group         TaskGroup
}
type Project struct {
	ID                  int
	Name                string
	Archived, Completed bool
	Group               TaskGroup
	Stages              []Stage
	Notes               []Note
}
type Workspace struct {
	Tasks, Groceries, Buys     TaskGroup
	Projects, ArchivedProjects []Project
}

func loadWorkspace() (Workspace, error) {
	w := Workspace{Tasks: TaskGroup{Key: "tasks", Name: "Tasks", Category: "todo"}, Groceries: TaskGroup{Key: "groceries", Name: "Groceries", Category: "groceries"}, Buys: TaskGroup{Key: "shopping", Name: "Buys", Category: "shopping"}}
	rows, err := db.Query(`SELECT id,name,archived,completed FROM projects ORDER BY position,id`)
	if err != nil {
		return w, err
	}
	var projects []Project
	for rows.Next() {
		var p Project
		if err = rows.Scan(&p.ID, &p.Name, &p.Archived, &p.Completed); err != nil {
			rows.Close()
			return w, err
		}
		p.Group = TaskGroup{Key: fmt.Sprint("project-", p.ID), Category: "todo", ProjectID: p.ID}
		projects = append(projects, p)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return w, err
	}
	pm := map[int]*Project{}
	for i := range projects {
		pm[projects[i].ID] = &projects[i]
	}
	rows, err = db.Query(`SELECT id,project_id,name,archived FROM stages ORDER BY position,id`)
	if err != nil {
		return w, err
	}
	for rows.Next() {
		var s Stage
		if err = rows.Scan(&s.ID, &s.ProjectID, &s.Name, &s.Archived); err != nil {
			rows.Close()
			return w, err
		}
		s.Group = TaskGroup{Key: fmt.Sprint("stage-", s.ID), Name: s.Name, Category: "todo", ProjectID: s.ProjectID, StageID: s.ID}
		if p := pm[s.ProjectID]; p != nil {
			p.Stages = append(p.Stages, s)
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return w, err
	}
	sm := map[int]*Stage{}
	for i := range projects {
		for j := range projects[i].Stages {
			s := &projects[i].Stages[j]
			sm[s.ID] = s
		}
	}
	rows, err = db.Query(`SELECT id,category,text,due_date,done,COALESCE(project_id,0),COALESCE(stage_id,0),archived,revision FROM todos WHERE archived=0 OR (done=1 AND project_id IS NOT NULL) ORDER BY done,position,created_at DESC,id DESC`)
	if err != nil {
		return w, err
	}
	for rows.Next() {
		var t Todo
		var archived bool
		if err = rows.Scan(&t.ID, &t.Category, &t.Text, &t.DueDate, &t.Done, &t.ProjectID, &t.StageID, &archived, &t.Revision); err != nil {
			rows.Close()
			return w, err
		}
		group := &w.Tasks
		if t.Category == "groceries" {
			group = &w.Groceries
		} else if t.Category == "shopping" {
			group = &w.Buys
		} else if p := pm[t.ProjectID]; p != nil {
			group = &p.Group
			if s := sm[t.StageID]; s != nil {
				group = &s.Group
				if !s.Archived && (!archived || t.Done) {
					p.Group.Total++
					if t.Done {
						p.Group.Completed++
					}
				}
			}
		}
		if !archived || t.Done {
			group.Total++
			if t.Done {
				group.Completed++
			}
		}
		if !archived {
			if t.Done {
				group.Done = append(group.Done, t)
			} else {
				group.Tasks = append(group.Tasks, t)
			}
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return w, err
	}
	for _, p := range projects {
		if p.Archived || p.Completed {
			w.ArchivedProjects = append(w.ArchivedProjects, p)
		} else {
			w.Projects = append(w.Projects, p)
		}
	}
	return w, nil
}
func handleWorkspace(w http.ResponseWriter, r *http.Request) {
	data, err := loadWorkspace()
	if err != nil {
		http.Error(w, "Could not load tasks", 500)
		return
	}
	renderTemplate(w, "workspace.html", data)
}

// A stage can only belong to its selected active project. Zero means standalone.
func validMembership(tx *sql.Tx, projectRaw, stageRaw string) (int, int, error) {
	parse := func(s string) (int, error) {
		if s == "" {
			return 0, nil
		}
		n, e := strconv.Atoi(s)
		if e != nil || n < 0 {
			return 0, fmt.Errorf("Invalid project or stage")
		}
		return n, nil
	}
	p, err := parse(projectRaw)
	if err != nil {
		return 0, 0, err
	}
	s, err := parse(stageRaw)
	if err != nil {
		return 0, 0, err
	}
	var n int
	if p != 0 {
		if err = tx.QueryRow(`SELECT count(*) FROM projects WHERE id=? AND archived=0 AND completed=0`, p).Scan(&n); err != nil {
			return 0, 0, err
		}
		if n != 1 {
			return 0, 0, fmt.Errorf("Choose an active project")
		}
	}
	if s != 0 {
		if err = tx.QueryRow(`SELECT count(*) FROM stages WHERE id=? AND project_id=? AND archived=0`, s, p).Scan(&n); err != nil {
			return 0, 0, err
		}
		if n != 1 {
			return 0, 0, fmt.Errorf("Stage does not belong to this project")
		}
	}
	return p, s, nil
}
func registerWorkspaceRoutes() {
	for path, h := range map[string]http.HandlerFunc{"/workspace": handleWorkspace, "/workspace/project": handleWorkspaceProject, "/projects/action": handleProjectAction, "/stages/action": handleStageAction, "/task/detail": handleTaskDetail, "/task/save": handleTaskSave, "/projects/detail": handleProjectDetail, "/projects/notes": handleProjectNotes} {
		http.HandleFunc(path, authMiddleware(h))
	}
}
func handleWorkspaceProject(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.Atoi(r.URL.Query().Get("id"))
	var p Project
	if err := db.QueryRow(`SELECT id,name FROM projects WHERE id=? AND archived=0 AND completed=0`, id).Scan(&p.ID, &p.Name); err != nil {
		http.Error(w, "Project unavailable", 404)
		return
	}
	p.Group = TaskGroup{Key: fmt.Sprint("project-", id), Category: "todo", ProjectID: id}
	rows, err := db.Query(`SELECT id,name,archived FROM stages WHERE project_id=? ORDER BY position,id`, id)
	if err != nil {
		http.Error(w, "Could not load project", 500)
		return
	}
	stageIndex := map[int]int{}
	for rows.Next() {
		var s Stage
		if err = rows.Scan(&s.ID, &s.Name, &s.Archived); err != nil {
			rows.Close()
			http.Error(w, "Could not load project", 500)
			return
		}
		s.ProjectID = id
		s.Group = TaskGroup{Key: fmt.Sprint("stage-", s.ID), Name: s.Name, Category: "todo", ProjectID: id, StageID: s.ID}
		stageIndex[s.ID] = len(p.Stages)
		p.Stages = append(p.Stages, s)
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		http.Error(w, "Could not load project", 500)
		return
	}
	rows.Close()
	rows, err = db.Query(`SELECT id,category,text,due_date,done,COALESCE(stage_id,0),revision FROM todos WHERE project_id=? AND archived=0 ORDER BY done,position,created_at DESC,id DESC`, id)
	if err != nil {
		http.Error(w, "Could not load project", 500)
		return
	}
	defer rows.Close()
	for rows.Next() {
		var t Todo
		if err = rows.Scan(&t.ID, &t.Category, &t.Text, &t.DueDate, &t.Done, &t.StageID, &t.Revision); err != nil {
			http.Error(w, "Could not load project", 500)
			return
		}
		t.ProjectID = id
		g := &p.Group
		if i, ok := stageIndex[t.StageID]; ok {
			g = &p.Stages[i].Group
			p.Group.Total++
			if t.Done {
				p.Group.Completed++
			}
		}
		g.Total++
		if t.Done {
			g.Completed++
			g.Done = append(g.Done, t)
		} else {
			g.Tasks = append(g.Tasks, t)
		}
	}
	if err = rows.Err(); err != nil {
		http.Error(w, "Could not load project", 500)
		return
	}
	renderTemplate(w, "project-body", p)
}
func handleProjectAction(w http.ResponseWriter, r *http.Request) { handleStructureAction(w, r, false) }
func handleStageAction(w http.ResponseWriter, r *http.Request)   { handleStructureAction(w, r, true) }
func handleStructureAction(w http.ResponseWriter, r *http.Request, stage bool) {
	if !requirePost(w, r) {
		return
	}
	action := r.FormValue("action")
	name := strings.TrimSpace(r.FormValue("name"))
	id, _ := strconv.Atoi(r.FormValue("id"))
	parent, _ := strconv.Atoi(r.FormValue("project_id"))
	tx, err := db.Begin()
	if err != nil {
		http.Error(w, "DB error", 500)
		return
	}
	defer tx.Rollback()
	table := "projects"
	if stage {
		table = "stages"
	}
	if action == "create" || action == "rename" {
		if name == "" || len(name) > 200 || !validNameRe.MatchString(name) {
			http.Error(w, "Enter a name of 1–200 characters", 400)
			return
		}
	}
	if action != "create" {
		var existing int
		if err = tx.QueryRow("SELECT id FROM "+table+" WHERE id=?", id).Scan(&existing); err != nil {
			http.Error(w, "Not found", 404)
			return
		}
	}
	switch action {
	case "create":
		if stage {
			if _, _, err = validMembership(tx, strconv.Itoa(parent), ""); err == nil {
				_, err = tx.Exec(`INSERT INTO stages(name,project_id,position) SELECT ?,?,COALESCE(MAX(position),0)+1 FROM stages WHERE project_id=?`, name, parent, parent)
			}
		} else {
			_, err = tx.Exec(`INSERT INTO projects(name,position) SELECT ?,COALESCE(MAX(position),0)+1 FROM projects`, name)
		}
	case "rename":
		_, err = tx.Exec("UPDATE "+table+" SET name=? WHERE id=?", name, id)
	case "archive":
		_, err = tx.Exec("UPDATE "+table+" SET archived=1 WHERE id=?", id)
	case "restore":
		_, err = tx.Exec("UPDATE "+table+" SET archived=0 WHERE id=?", id)
	case "complete":
		if stage {
			http.Error(w, "Invalid action", 400)
			return
		}
		var remaining int
		err = tx.QueryRow(`SELECT count(*) FROM todos t WHERE project_id=? AND done=0 AND `+activeTaskSQL, id).Scan(&remaining)
		if err == nil && remaining > 0 {
			http.Error(w, "Complete or archive the remaining tasks first", 409)
			return
		}
		if err == nil {
			_, err = tx.Exec(`UPDATE projects SET completed=1 WHERE id=? AND archived=0`, id)
		}
	case "reopen":
		if stage {
			http.Error(w, "Invalid action", 400)
			return
		}
		_, err = tx.Exec(`UPDATE projects SET completed=0,archived=0 WHERE id=?`, id)
	case "up", "down":
		// Normalize sibling positions before exchanging adjacent entries.
		condition := "archived=0"
		args := []any{}
		if stage {
			condition += " AND project_id=(SELECT project_id FROM stages WHERE id=?)"
			args = append(args, id)
		} else {
			condition += " AND completed=0"
		}
		rows, e := tx.Query("SELECT id FROM "+table+" WHERE "+condition+" ORDER BY position,id", args...)
		if e != nil {
			err = e
			break
		}
		var ids []int
		for rows.Next() {
			var n int
			if e = rows.Scan(&n); e != nil {
				break
			}
			ids = append(ids, n)
		}
		if e == nil {
			e = rows.Err()
		}
		rows.Close()
		if e != nil {
			err = e
			break
		}
		for i, n := range ids {
			if n == id {
				j := i - 1
				if action == "down" {
					j = i + 1
				}
				if j >= 0 && j < len(ids) {
					ids[i], ids[j] = ids[j], ids[i]
				}
				break
			}
		}
		for i, n := range ids {
			if _, err = tx.Exec("UPDATE "+table+" SET position=? WHERE id=?", i, n); err != nil {
				break
			}
		}
	default:
		http.Error(w, "Invalid action", 400)
		return
	}
	if err != nil {
		http.Error(w, "Could not update: "+err.Error(), 400)
		return
	}
	if err = tx.Commit(); err != nil {
		http.Error(w, "DB error", 500)
		return
	}
	if action == "archive" {
		kind := "project"
		if stage {
			kind = "stage"
		}
		hxTrigger(w, "sbUndo", map[string]any{"kind": kind, "id": id})
	}
	handleWorkspace(w, r)
}

type TaskDetail struct {
	Task     Todo
	Projects []Project
}

func loadTaskChoices() ([]Project, error) {
	rows, err := db.Query(`SELECT id,name FROM projects WHERE archived=0 AND completed=0 ORDER BY position,id`)
	if err != nil {
		return nil, err
	}
	var projects []Project
	index := map[int]int{}
	for rows.Next() {
		var p Project
		if err = rows.Scan(&p.ID, &p.Name); err != nil {
			rows.Close()
			return nil, err
		}
		index[p.ID] = len(projects)
		projects = append(projects, p)
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()
	rows, err = db.Query(`SELECT id,project_id,name FROM stages WHERE archived=0 ORDER BY position,id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var s Stage
		if err = rows.Scan(&s.ID, &s.ProjectID, &s.Name); err != nil {
			return nil, err
		}
		if i, ok := index[s.ProjectID]; ok {
			projects[i].Stages = append(projects[i].Stages, s)
		}
	}
	return projects, rows.Err()
}

func getTask(id int) (Todo, error) {
	var t Todo
	err := db.QueryRow(`SELECT id,category,text,due_date,done,COALESCE(project_id,0),COALESCE(stage_id,0),revision FROM todos t WHERE t.id=? AND `+activeTaskSQL, id).Scan(&t.ID, &t.Category, &t.Text, &t.DueDate, &t.Done, &t.ProjectID, &t.StageID, &t.Revision)
	return t, err
}
func handleTaskDetail(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.Atoi(r.URL.Query().Get("id"))
	t, err := getTask(id)
	if err != nil {
		http.Error(w, "Task unavailable. It may have been archived.", 404)
		return
	}
	projects, err := loadTaskChoices()
	if err != nil {
		http.Error(w, "DB error", 500)
		return
	}
	renderTemplate(w, "task-detail.html", TaskDetail{t, projects})
}
func handleTaskSave(w http.ResponseWriter, r *http.Request) {
	if !requirePost(w, r) {
		return
	}
	id, ok := parseID(w, r)
	if !ok {
		return
	}
	name := strings.TrimSpace(r.FormValue("text"))
	revision, err := strconv.Atoi(r.FormValue("revision"))
	if err != nil || revision < 1 {
		http.Error(w, "Invalid task revision", 400)
		return
	}
	due := r.FormValue("due_date")
	if name == "" || len(name) > 500 {
		http.Error(w, "Enter task text (up to 500 characters)", 400)
		return
	}
	if due != "" {
		if _, err := time.Parse("2006-01-02", due); err != nil {
			http.Error(w, "Invalid date", 400)
			return
		}
	}
	tx, err := db.Begin()
	if err != nil {
		http.Error(w, "DB error", 500)
		return
	}
	defer tx.Rollback()
	var category string
	if err = tx.QueryRow(`SELECT t.category FROM todos t WHERE t.id=? AND `+activeTaskSQL, id).Scan(&category); err != nil {
		http.Error(w, "Not found", 404)
		return
	}
	p, s := 0, 0
	if category == "todo" {
		p, s, err = validMembership(tx, r.FormValue("project_id"), r.FormValue("stage_id"))
	} else {
		due = ""
	}
	if err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	res, err := tx.Exec(`UPDATE todos SET text=?,due_date=?,project_id=NULLIF(?,0),stage_id=NULLIF(?,0),revision=revision+1 WHERE id=? AND revision=?`, name, due, p, s, id, revision)
	if err == nil {
		if n, countErr := res.RowsAffected(); countErr != nil {
			err = countErr
		} else if n == 0 {
			tx.Rollback()
			latest, getErr := getTask(id)
			if getErr != nil {
				http.Error(w, "Task unavailable", 404)
				return
			}
			writeJSON(w, http.StatusConflict, map[string]any{"status": "conflict", "task": latest})
			return
		}
	}
	if err == nil {
		err = tx.Commit()
	}
	if err != nil {
		http.Error(w, "Could not save task", 500)
		return
	}
	hxTrigger(w, "sbWorkspaceChanged", map[string]any{"message": "Task saved"})
	r.URL.RawQuery = "id=" + strconv.Itoa(id)
	handleTaskDetail(w, r)
}
func handleProjectDetail(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.Atoi(r.URL.Query().Get("id"))
	ws, err := loadWorkspace()
	if err != nil {
		http.Error(w, "DB error", 500)
		return
	}
	var project *Project
	for _, p := range append(ws.Projects, ws.ArchivedProjects...) {
		if p.ID == id {
			v := p
			project = &v
			break
		}
	}
	if project == nil {
		http.Error(w, "Project unavailable", 404)
		return
	}
	rows, err := db.Query(`SELECT n.id,n.title,EXISTS(SELECT 1 FROM project_notes pn WHERE pn.note_id=n.id AND pn.project_id=?) FROM notes n WHERE n.archived=0 ORDER BY n.title`, id)
	if err != nil {
		http.Error(w, "DB error", 500)
		return
	}
	var available []Note
	for rows.Next() {
		var n Note
		var linked bool
		if err = rows.Scan(&n.ID, &n.Title, &linked); err != nil {
			break
		}
		if linked {
			project.Notes = append(project.Notes, n)
		} else {
			available = append(available, n)
		}
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		http.Error(w, "DB error", 500)
		return
	}
	renderTemplate(w, "project-detail.html", struct {
		Project   *Project
		Available []Note
	}{project, available})
}
func handleProjectNotes(w http.ResponseWriter, r *http.Request) {
	if !requirePost(w, r) {
		return
	}
	p, _ := strconv.Atoi(r.FormValue("project_id"))
	n, _ := strconv.Atoi(r.FormValue("note_id"))
	var err error
	switch r.FormValue("action") {
	case "link":
		var result sql.Result
		result, err = db.Exec(`INSERT OR IGNORE INTO project_notes(project_id,note_id) SELECT p.id,n.id FROM projects p,notes n WHERE p.id=? AND n.id=? AND p.archived=0 AND n.archived=0`, p, n)
		if err == nil {
			if count, _ := result.RowsAffected(); count == 0 {
				http.Error(w, "Note or project unavailable, or already linked", 409)
				return
			}
		}
	case "unlink":
		_, err = db.Exec(`DELETE FROM project_notes WHERE project_id=? AND note_id=?`, p, n)
	default:
		http.Error(w, "Invalid action", 400)
		return
	}
	if err != nil {
		http.Error(w, "Could not update note link", 500)
		return
	}
	r.URL.RawQuery = "id=" + strconv.Itoa(p)
	handleProjectDetail(w, r)
}
