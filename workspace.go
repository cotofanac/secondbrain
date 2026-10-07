package main

import (
	"context"
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
	Key, Name, Category  string
	ProjectID, HeadingID int
	Tasks, Done          []Todo
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
type ProjectSummary struct {
	ID                  int
	Name                string
	Archived, Completed bool
	Progress            ProjectProgress
}

// Project is a loaded body. Workspace and sidebar use summaries instead.
type Project struct {
	ProjectSummary
	Group    TaskGroup
	Headings []Heading
}
type Workspace struct {
	Tasks, Groceries, Buys TaskGroup
	Projects               []ProjectSummary
	ArchivedProjectCount   int
}

// placeTask files a task (or n archived completions, with no row) into its
// project, adding it to the project's progress, and returns the group it is
// listed in: the heading's, or the project's own.
func (p *Project) placeTask(headingID, n int, done bool) *TaskGroup {
	p.Progress.Total += n
	if done {
		p.Progress.Completed += n
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

func loadWorkspace() (Workspace, error) { return loadWorkspaceContext(context.Background()) }
func loadWorkspaceContext(ctx context.Context) (Workspace, error) {
	w := Workspace{Tasks: TaskGroup{Key: "tasks", Name: "Tasks", Category: "todo"}, Groceries: TaskGroup{Key: "groceries", Name: "Groceries", Category: "groceries"}, Buys: TaskGroup{Key: "shopping", Name: "Buys", Category: "shopping"}}
	var err error
	w.Projects, err = loadProjectSummaries(ctx, false)
	if err != nil {
		return w, err
	}
	if err = db.QueryRowContext(ctx, `SELECT count(*) FROM projects WHERE archived=1 OR completed=1`).Scan(&w.ArchivedProjectCount); err != nil {
		return w, err
	}
	rows, err := db.QueryContext(ctx, `SELECT id,category,text,due_date,done,COALESCE(project_id,0),COALESCE(heading_id,0),revision,repeat FROM todos t WHERE t.archived=0 AND t.project_id IS NULL `+taskOrderSQL)
	if err != nil {
		return w, err
	}
	for rows.Next() {
		var t Todo
		if err = rows.Scan(&t.ID, &t.Category, &t.Text, &t.DueDate, &t.Done, &t.ProjectID, &t.HeadingID, &t.Revision, &t.Repeat); err != nil {
			rows.Close()
			return w, err
		}
		switch t.Category {
		case "groceries":
			w.Groceries.add(t)
		case "shopping":
			w.Buys.add(t)
		default:
			w.Tasks.add(t)
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return w, err
	}
	return w, nil
}
func handleWorkspace(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := readContext(r)
	defer cancel()
	data, err := loadWorkspaceContext(ctx)
	if err != nil {
		http.Error(w, "Could not load tasks", 500)
		return
	}
	renderTemplate(w, r, "workspace.html", data)
}

// taskGroupView is one list re-rendered after a change inside it, with the
// count its header shows: the project's progress, or the list's open items.
type taskGroupView struct {
	Group   TaskGroup
	Project *ProjectSummary
}

// handleTaskGroup answers a change made inside one list (an add, a check-off,
// an archive) with just that list, so open projects elsewhere on the page are
// not reloaded. The header count is swapped in out-of-band.
func handleTaskGroup(w http.ResponseWriter, r *http.Request, category string, projectID, headingID int) {
	ctx, cancel := readContext(r)
	defer cancel()
	view, err := loadTaskGroup(ctx, category, projectID, headingID)
	if err != nil {
		writeDomainError(w, err, "Could not load tasks")
		return
	}
	renderTemplate(w, r, "task-group-response", view)
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
	for path, h := range map[string]http.HandlerFunc{"/workspace": handleWorkspace, "/workspace/project": handleWorkspaceProject, "/projects/action": handleProjectAction, "/headings/action": handleHeadingAction, "/task/edit": handleTaskEdit, "/task/save": handleTaskSave, "/undo": handleUndo, "/sidebar": handleSidebar, "/archive": handleArchive} {
		http.HandleFunc(path, authMiddleware(h))
	}
}
func handleWorkspaceProject(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := readContext(r)
	defer cancel()
	id, _ := strconv.Atoi(r.URL.Query().Get("id"))
	var p Project
	if err := db.QueryRowContext(ctx, `SELECT id,name FROM projects WHERE id=? AND archived=0 AND completed=0`, id).Scan(&p.ID, &p.Name); err != nil {
		http.Error(w, "Project unavailable", 404)
		return
	}
	p.Group = TaskGroup{Key: fmt.Sprint("project-", id), Category: "todo", ProjectID: id}
	if err := loadProjectTasksContext(ctx, &p); err != nil {
		http.Error(w, "Could not load project", 500)
		return
	}
	renderTemplate(w, r, "project-body", p)
}

// loadProjectTasks fills one project's headings, tasks and progress, the same
// way loadWorkspace does for all of them.
func loadProjectTasks(p *Project) error { return loadProjectTasksContext(context.Background(), p) }
func loadProjectTasksContext(ctx context.Context, p *Project) error {
	rows, err := db.QueryContext(ctx, `SELECT id,name FROM headings WHERE project_id=? ORDER BY position,id`, p.ID)
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
	rows, err = db.QueryContext(ctx, `SELECT id,category,text,due_date,done,COALESCE(heading_id,0),revision,repeat FROM todos WHERE project_id=? AND archived=0 `+taskOrderSQL, p.ID)
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
	summaries := []ProjectSummary{p.ProjectSummary}
	if err = fillProjectProgress(ctx, summaries); err != nil {
		return err
	}
	p.Progress = summaries[0].Progress
	return nil
}
func handleProjectAction(w http.ResponseWriter, r *http.Request) { handleStructureAction(w, r, false) }
func handleHeadingAction(w http.ResponseWriter, r *http.Request) { handleStructureAction(w, r, true) }

var structureActions = map[bool]map[string]bool{
	false: {"create": true, "rename": true, "up": true, "down": true, "archive": true, "restore": true, "complete": true, "reopen": true},
	true:  {"create": true, "rename": true, "up": true, "down": true, "delete": true, "archive": true},
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
	var archivedTasks int64
	var undoToken string
	var inverse undoOperation
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
			writeDomainError(w, err, "Could not load item")
			return
		}
	}
	if action != "create" {
		if heading {
			var active int
			if err = tx.QueryRow(`SELECT count(*) FROM headings h JOIN projects p ON p.id=h.project_id WHERE h.id=? AND p.archived=0 AND p.completed=0`, id).Scan(&active); err != nil {
				writeDomainError(w, err, "Could not update heading")
				return
			}
			if active != 1 {
				http.Error(w, "Heading unavailable", 404)
				return
			}
		} else if action == "rename" || action == "up" || action == "down" {
			if _, _, err = parseList(tx, fmt.Sprint("project:", id)); err != nil {
				writeDomainError(w, err, "Could not update project")
				return
			}
		}
	}
	if !heading && (action == "archive" || action == "complete") {
		var archived, completed, revision int
		if err = tx.QueryRow(`SELECT archived,completed,revision FROM projects WHERE id=?`, id).Scan(&archived, &completed, &revision); err != nil {
			writeDomainError(w, err, "Could not update project")
			return
		}
		if action == "complete" && archived != 0 {
			http.Error(w, "Project unavailable", 404)
			return
		}
		if (action == "archive" && archived == 0) || (action == "complete" && completed == 0) {
			inverse.Changes = append(inverse.Changes, undoChange{Table: "projects", ID: id, Revision: revision + 1, Values: map[string]any{"archived": archived, "completed": completed}})
		}
	}
	if heading && (action == "delete" || action == "archive") {
		h := &undoHeading{ID: id}
		if err = tx.QueryRow(`SELECT project_id,name,position FROM headings WHERE id=?`, id).Scan(&h.ProjectID, &h.Name, &h.Position); err != nil {
			writeDomainError(w, err, "Could not update heading")
			return
		}
		inverse.Heading = h
		rows, readErr := tx.Query(`SELECT id,revision,archived,archived_at FROM todos WHERE heading_id=?`, id)
		if readErr != nil {
			writeDomainError(w, readErr, "Could not update heading")
			return
		}
		for rows.Next() {
			var taskID, rev, archived int
			var at sql.NullString
			if err = rows.Scan(&taskID, &rev, &archived, &at); err != nil {
				break
			}
			values := map[string]any{"heading_id": id}
			if action == "archive" {
				values["archived"] = archived
				if at.Valid {
					values["archived_at"] = at.String
				} else {
					values["archived_at"] = nil
				}
			}
			inverse.Changes = append(inverse.Changes, undoChange{Table: "todos", ID: taskID, Revision: rev + 1, Values: values})
		}
		if err == nil {
			err = rows.Err()
		}
		rows.Close()
		if err != nil {
			writeDomainError(w, err, "Could not update heading")
			return
		}
	}
	switch action {
	case "create":
		if heading {
			if _, _, err = parseList(tx, fmt.Sprint("project:", parent)); err != nil {
				writeDomainError(w, err, "Could not update list")
				return
			}
			_, err = tx.Exec(`INSERT INTO headings(name,project_id,position) SELECT ?,?,COALESCE(MAX(position),0)+1 FROM headings WHERE project_id=?`, name, parent, parent)
		} else {
			_, err = tx.Exec(`INSERT INTO projects(name,position) SELECT ?,COALESCE(MAX(position),0)+1 FROM projects`, name)
		}
	case "rename":
		_, err = tx.Exec("UPDATE "+table+" SET name=?"+revisionUpdate(heading)+" WHERE id=?", name, id)
	case "delete":
		// A heading only groups tasks: they move up to the project itself.
		if _, err = tx.Exec(`UPDATE todos SET heading_id=NULL,revision=revision+1 WHERE heading_id=?`, id); err == nil {
			_, err = tx.Exec(`DELETE FROM headings WHERE id=?`, id)
		}
	case "archive":
		if !heading {
			_, err = tx.Exec(`UPDATE projects SET archived=1,revision=revision+1 WHERE id=? AND archived=0`, id)
			break
		}
		// A finished section: its open tasks go to the archive with it (each
		// restorable there, at the top of the project) and the heading goes.
		var res sql.Result
		if res, err = tx.Exec(`UPDATE todos SET archived=1,archived_at=? WHERE heading_id=? AND archived=0`, dbTime(time.Now()), id); err == nil {
			archivedTasks, _ = res.RowsAffected()
			if _, err = tx.Exec(`UPDATE todos SET heading_id=NULL,revision=revision+1 WHERE heading_id=?`, id); err == nil {
				_, err = tx.Exec(`DELETE FROM headings WHERE id=?`, id)
			}
		}
	case "restore":
		_, err = tx.Exec(`UPDATE projects SET archived=0,revision=revision+1 WHERE id=? AND archived=1`, id)
	case "complete":
		var remaining int
		err = tx.QueryRow(`SELECT count(*) FROM todos t WHERE project_id=? AND done=0 AND `+activeTaskSQL, id).Scan(&remaining)
		if err == nil && remaining > 0 {
			http.Error(w, "Complete or archive the remaining tasks first", 409)
			return
		}
		if err == nil {
			_, err = tx.Exec(`UPDATE projects SET completed=1,revision=revision+1 WHERE id=? AND archived=0 AND completed=0`, id)
		}
	case "reopen":
		_, err = tx.Exec(`UPDATE projects SET completed=0,archived=0,revision=revision+1 WHERE id=? AND (completed=1 OR archived=1)`, id)
	case "up", "down":
		err = moveSibling(tx, heading, id, action == "up")
	}
	if err != nil {
		writeDomainError(w, err, "Could not update list")
		return
	}
	if inverse.Heading != nil || len(inverse.Changes) > 0 {
		undoToken, err = recordUndo(tx, inverse)
		if err != nil {
			writeDomainError(w, err, "Could not record undo")
			return
		}
	}
	if err = tx.Commit(); err != nil {
		http.Error(w, "DB error", 500)
		return
	}
	if action == "archive" && heading {
		hxTrigger(w, "sbUndo", map[string]any{"kind": "heading", "token": undoToken, "message": fmt.Sprintf("Heading archived with %d task(s)", archivedTasks)})
	} else if action == "archive" && undoToken != "" {
		hxTrigger(w, "sbUndo", map[string]any{"kind": "project", "id": id, "token": undoToken})
	} else if action == "complete" && undoToken != "" {
		hxTrigger(w, "sbUndo", map[string]any{"kind": "project-completed", "id": id, "token": undoToken})
	}
	if action == "delete" && heading {
		hxTrigger(w, "sbUndo", map[string]any{"kind": "heading", "token": undoToken, "message": "Heading removed; tasks moved to the project"})
	}
	mutationEffects(w, "todos", "today", "sidebar")
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
		if _, err = tx.Exec("UPDATE "+table+" SET position=?"+revisionUpdate(heading)+" WHERE id=?", i, n); err != nil {
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
		writeDomainError(w, err, "Could not load item")
		return
	}
	p, h := 0, 0
	if category == "todo" {
		p, h, err = parseList(tx, r.FormValue("list"))
	} else {
		due, repeat = "", ""
	}
	if err != nil {
		writeDomainError(w, err, "Could not update list")
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
	mutationEffects(w, "todos", "today", "sidebar")
	writeJSON(w, http.StatusOK, map[string]any{"status": "saved", "revision": revision + 1})
}

// Headings have no lifecycle revision; project names/order invalidate undo.
func revisionUpdate(heading bool) string {
	if heading {
		return ""
	}
	return ",revision=revision+1"
}
