package main

import (
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// activeTaskSQL also excludes tasks under archived or completed projects.
const activeTaskSQL = `t.archived=0 AND (t.project_id IS NULL OR EXISTS(SELECT 1 FROM projects p WHERE p.id=t.project_id AND p.archived=0 AND p.completed=0))`

type TaskGroup struct {
	Key, Name, Category                    string
	ProjectID, HeadingID, Total, Completed int
	Tasks, Done                            []Todo
}

// List names the list a task captured into this group joins (see parseList).
func (g TaskGroup) List() string {
	if g.HeadingID != 0 {
		return fmt.Sprint("heading:", g.HeadingID)
	}
	if g.ProjectID != 0 {
		return fmt.Sprint("project:", g.ProjectID)
	}
	return ""
}

type Heading struct {
	ID, ProjectID int
	Name          string
	Group         TaskGroup
}
type Project struct {
	ID                  int
	Name                string
	Archived, Completed bool
	// Group holds the tasks directly in the project; its counts cover the
	// whole project, headings included.
	Group    TaskGroup
	Headings []Heading
	Notes    []Note
}
type Workspace struct {
	Tasks, Groceries, Buys     TaskGroup
	Projects, ArchivedProjects []Project
}

// placeTask files a task (or n archived completions, with no row) into its
// project, adding it to the project's progress, and returns the group it is
// listed in: the heading's, or the project's own.
func (p *Project) placeTask(headingID, n int, done bool) *TaskGroup {
	p.Group.Total += n
	if done {
		p.Group.Completed += n
	}
	for i := range p.Headings {
		if p.Headings[i].ID == headingID {
			return &p.Headings[i].Group
		}
	}
	return &p.Group
}
func (g *TaskGroup) add(t Todo) {
	if t.Done {
		g.Done = append(g.Done, t)
	} else {
		g.Tasks = append(g.Tasks, t)
	}
}

func newHeading(id, projectID int, name string) Heading {
	return Heading{ID: id, ProjectID: projectID, Name: name, Group: TaskGroup{Key: fmt.Sprint("heading-", id), Name: name, Category: "todo", ProjectID: projectID, HeadingID: id}}
}

// Dated tasks first, soonest (so overdue) on top; undated tasks newest first.
const taskOrderSQL = `ORDER BY done,due_date='',due_date,created_at DESC,id DESC`

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
	rows, err = db.Query(`SELECT id,project_id,name FROM headings ORDER BY position,id`)
	if err != nil {
		return w, err
	}
	for rows.Next() {
		var id, projectID int
		var name string
		if err = rows.Scan(&id, &projectID, &name); err != nil {
			rows.Close()
			return w, err
		}
		if p := pm[projectID]; p != nil {
			p.Headings = append(p.Headings, newHeading(id, projectID, name))
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return w, err
	}
	groupFor := func(category string, projectID, headingID, n int, done bool) *TaskGroup {
		if category == "groceries" {
			return &w.Groceries
		} else if category == "shopping" {
			return &w.Buys
		} else if p := pm[projectID]; p != nil {
			return p.placeTask(headingID, n, done)
		}
		return &w.Tasks
	}
	rows, err = db.Query(`SELECT id,category,text,due_date,done,COALESCE(project_id,0),COALESCE(heading_id,0),revision,repeat FROM todos WHERE archived=0 ` + taskOrderSQL)
	if err != nil {
		return w, err
	}
	for rows.Next() {
		var t Todo
		if err = rows.Scan(&t.ID, &t.Category, &t.Text, &t.DueDate, &t.Done, &t.ProjectID, &t.HeadingID, &t.Revision, &t.Repeat); err != nil {
			rows.Close()
			return w, err
		}
		groupFor(t.Category, t.ProjectID, t.HeadingID, 1, t.Done).add(t)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return w, err
	}
	// Completed project work that has since been tidied into the archive still
	// counts toward progress. Only the totals are needed, not the rows.
	rows, err = db.Query(`SELECT project_id,count(*) FROM todos WHERE archived=1 AND done=1 AND category='todo' AND project_id IS NOT NULL GROUP BY project_id`)
	if err != nil {
		return w, err
	}
	for rows.Next() {
		var projectID, n int
		if err = rows.Scan(&projectID, &n); err != nil {
			rows.Close()
			return w, err
		}
		if p := pm[projectID]; p != nil {
			p.placeTask(0, n, true)
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
	renderTemplate(w, r, "workspace.html", data)
}

func handleTaskGroup(w http.ResponseWriter, r *http.Request, category string, projectID, headingID int) {
	data, err := loadWorkspace()
	if err != nil {
		http.Error(w, "Could not load tasks", http.StatusInternalServerError)
		return
	}
	group := &data.Tasks
	if category == "groceries" {
		group = &data.Groceries
	} else if category == "shopping" {
		group = &data.Buys
	} else if projectID != 0 {
		group = nil
		for i := range data.Projects {
			p := &data.Projects[i]
			if p.ID != projectID {
				continue
			}
			group = &p.Group
			for j := range p.Headings {
				if p.Headings[j].ID == headingID {
					group = &p.Headings[j].Group
				}
			}
			break
		}
	}
	if group == nil {
		http.Error(w, "Task group unavailable", http.StatusNotFound)
		return
	}
	renderTemplate(w, r, "task-group", *group)
}

var errBadList = errors.New("Choose an active project")

// parseList reads the list a task belongs to: "" (the inbox), "project:N" or
// "heading:N", and returns the project and heading ids for an active project.
func parseList(tx *sql.Tx, raw string) (projectID, headingID int, err error) {
	if raw == "" {
		return 0, 0, nil
	}
	kind, rawID, _ := strings.Cut(raw, ":")
	id, err := strconv.Atoi(rawID)
	if err != nil || id <= 0 {
		return 0, 0, errBadList
	}
	switch kind {
	case "project":
		err = tx.QueryRow(`SELECT id FROM projects WHERE id=? AND archived=0 AND completed=0`, id).Scan(&projectID)
	case "heading":
		headingID = id
		err = tx.QueryRow(`SELECT p.id FROM headings h JOIN projects p ON p.id=h.project_id WHERE h.id=? AND p.archived=0 AND p.completed=0`, id).Scan(&projectID)
	default:
		return 0, 0, errBadList
	}
	if errors.Is(err, sql.ErrNoRows) {
		return 0, 0, errBadList
	}
	if err != nil {
		return 0, 0, err
	}
	return projectID, headingID, nil
}
func registerWorkspaceRoutes() {
	for path, h := range map[string]http.HandlerFunc{"/workspace": handleWorkspace, "/workspace/project": handleWorkspaceProject, "/projects/action": handleProjectAction, "/headings/action": handleHeadingAction, "/task/edit": handleTaskEdit, "/task/save": handleTaskSave, "/projects/detail": handleProjectDetail, "/projects/notes": handleProjectNotes, "/sidebar": handleSidebar, "/archive": handleArchive} {
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
	if err := loadProjectTasks(&p); err != nil {
		http.Error(w, "Could not load project", 500)
		return
	}
	renderTemplate(w, r, "project-body", p)
}

// loadProjectTasks fills one project's headings, tasks and progress, the same
// way loadWorkspace does for all of them.
func loadProjectTasks(p *Project) error {
	rows, err := db.Query(`SELECT id,name FROM headings WHERE project_id=? ORDER BY position,id`, p.ID)
	if err != nil {
		return err
	}
	for rows.Next() {
		var id int
		var name string
		if err = rows.Scan(&id, &name); err != nil {
			rows.Close()
			return err
		}
		p.Headings = append(p.Headings, newHeading(id, p.ID, name))
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	rows, err = db.Query(`SELECT id,category,text,due_date,done,COALESCE(heading_id,0),revision,repeat FROM todos WHERE project_id=? AND archived=0 `+taskOrderSQL, p.ID)
	if err != nil {
		return err
	}
	for rows.Next() {
		var t Todo
		if err = rows.Scan(&t.ID, &t.Category, &t.Text, &t.DueDate, &t.Done, &t.HeadingID, &t.Revision, &t.Repeat); err != nil {
			rows.Close()
			return err
		}
		t.ProjectID = p.ID
		p.placeTask(t.HeadingID, 1, t.Done).add(t)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	// Match loadWorkspace: archived completions still count toward progress.
	var n int
	if err = db.QueryRow(`SELECT count(*) FROM todos WHERE project_id=? AND archived=1 AND done=1 AND category='todo'`, p.ID).Scan(&n); err != nil {
		return err
	}
	p.placeTask(0, n, true)
	return nil
}
func handleProjectAction(w http.ResponseWriter, r *http.Request) { handleStructureAction(w, r, false) }
func handleHeadingAction(w http.ResponseWriter, r *http.Request) { handleStructureAction(w, r, true) }

var structureActions = map[bool]map[string]bool{
	false: {"create": true, "rename": true, "up": true, "down": true, "archive": true, "restore": true, "complete": true, "reopen": true},
	true:  {"create": true, "rename": true, "up": true, "down": true, "delete": true},
}

func handleStructureAction(w http.ResponseWriter, r *http.Request, heading bool) {
	if !requirePost(w, r) {
		return
	}
	action := r.FormValue("action")
	if !structureActions[heading][action] {
		http.Error(w, "Invalid action", 400)
		return
	}
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
	if heading {
		table = "headings"
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
		if heading {
			if _, _, err = parseList(tx, fmt.Sprint("project:", parent)); err != nil {
				http.Error(w, err.Error(), 400)
				return
			}
			_, err = tx.Exec(`INSERT INTO headings(name,project_id,position) SELECT ?,?,COALESCE(MAX(position),0)+1 FROM headings WHERE project_id=?`, name, parent, parent)
		} else {
			_, err = tx.Exec(`INSERT INTO projects(name,position) SELECT ?,COALESCE(MAX(position),0)+1 FROM projects`, name)
		}
	case "rename":
		_, err = tx.Exec("UPDATE "+table+" SET name=? WHERE id=?", name, id)
	case "delete":
		// A heading only groups tasks: they move up to the project itself.
		if _, err = tx.Exec(`UPDATE todos SET heading_id=NULL WHERE heading_id=?`, id); err == nil {
			_, err = tx.Exec(`DELETE FROM headings WHERE id=?`, id)
		}
	case "archive":
		_, err = tx.Exec(`UPDATE projects SET archived=1 WHERE id=?`, id)
	case "restore":
		_, err = tx.Exec(`UPDATE projects SET archived=0 WHERE id=?`, id)
	case "complete":
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
		_, err = tx.Exec(`UPDATE projects SET completed=0,archived=0 WHERE id=?`, id)
	case "up", "down":
		err = moveSibling(tx, heading, id, action == "up")
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
		hxTrigger(w, "sbUndo", map[string]any{"kind": "project", "id": id})
	}
	if r.FormValue("return") == "archive" {
		renderArchive(w, r, "projects")
		return
	}
	handleWorkspace(w, r)
}

// moveSibling swaps a project or heading with its neighbour, normalizing the
// positions of its siblings first.
func moveSibling(tx *sql.Tx, heading bool, id int, up bool) error {
	query := `SELECT id FROM projects WHERE archived=0 AND completed=0 ORDER BY position,id`
	var args []any
	if heading {
		query = `SELECT id FROM headings WHERE project_id=(SELECT project_id FROM headings WHERE id=?) ORDER BY position,id`
		args = append(args, id)
	}
	rows, err := tx.Query(query, args...)
	if err != nil {
		return err
	}
	var ids []int
	for rows.Next() {
		var n int
		if err = rows.Scan(&n); err != nil {
			rows.Close()
			return err
		}
		ids = append(ids, n)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for i, n := range ids {
		if n == id {
			j := i + 1
			if up {
				j = i - 1
			}
			if j >= 0 && j < len(ids) {
				ids[i], ids[j] = ids[j], ids[i]
			}
			break
		}
	}
	table := "projects"
	if heading {
		table = "headings"
	}
	for i, n := range ids {
		if _, err = tx.Exec("UPDATE "+table+" SET position=? WHERE id=?", i, n); err != nil {
			return err
		}
	}
	return nil
}

// ListChoice is one entry of the task editor's list picker.
type ListChoice struct{ Value, Label string }

type TaskEditor struct {
	Task  Todo
	List  string
	Lists []ListChoice
}

// loadListChoices lists where a task can go: the inbox, then each active
// project followed by its headings.
func loadListChoices() ([]ListChoice, error) {
	rows, err := db.Query(`SELECT p.id,p.name,COALESCE(h.id,0),COALESCE(h.name,'') FROM projects p LEFT JOIN headings h ON h.project_id=p.id
		WHERE p.archived=0 AND p.completed=0 ORDER BY p.position,p.id,h.position,h.id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	choices := []ListChoice{{"", "Inbox"}}
	last := 0
	for rows.Next() {
		var projectID, headingID int
		var project, heading string
		if err = rows.Scan(&projectID, &project, &headingID, &heading); err != nil {
			return nil, err
		}
		if projectID != last {
			choices = append(choices, ListChoice{fmt.Sprint("project:", projectID), project})
			last = projectID
		}
		if headingID != 0 {
			choices = append(choices, ListChoice{fmt.Sprint("heading:", headingID), project + " › " + heading})
		}
	}
	return choices, rows.Err()
}

func getTask(id int) (Todo, error) {
	var t Todo
	err := db.QueryRow(`SELECT id,category,text,due_date,done,COALESCE(project_id,0),COALESCE(heading_id,0),revision,repeat FROM todos t WHERE t.id=? AND `+activeTaskSQL, id).Scan(&t.ID, &t.Category, &t.Text, &t.DueDate, &t.Done, &t.ProjectID, &t.HeadingID, &t.Revision, &t.Repeat)
	return t, err
}

// handleTaskEdit returns the editor that opens inside a task's row.
func handleTaskEdit(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.Atoi(r.URL.Query().Get("id"))
	t, err := getTask(id)
	if err != nil {
		http.Error(w, "Task unavailable. It may have been archived.", 404)
		return
	}
	view := TaskEditor{Task: t, List: TaskGroup{ProjectID: t.ProjectID, HeadingID: t.HeadingID}.List()}
	if t.Category == "todo" {
		if view.Lists, err = loadListChoices(); err != nil {
			http.Error(w, "DB error", 500)
			return
		}
	}
	renderTemplate(w, r, "task-editor.html", view)
}

// handleTaskSave stores the editor's fields. It answers with JSON only: the
// editor is never re-rendered under the cursor, it just takes the new revision.
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
	repeat := r.FormValue("repeat")
	if !validRepeat(repeat) {
		http.Error(w, "Invalid repeat", 400)
		return
	}
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
	p, h := 0, 0
	if category == "todo" {
		p, h, err = parseList(tx, r.FormValue("list"))
	} else {
		due, repeat = "", ""
	}
	if err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	res, err := tx.Exec(`UPDATE todos SET text=?,due_date=?,repeat=?,project_id=NULLIF(?,0),heading_id=NULLIF(?,0),revision=revision+1 WHERE id=? AND revision=?`, name, due, repeat, p, h, id, revision)
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
	writeJSON(w, http.StatusOK, map[string]any{"status": "saved", "revision": revision + 1})
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
	renderTemplate(w, r, "project-detail.html", struct {
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
