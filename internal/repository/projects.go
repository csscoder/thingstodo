package repository

import (
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/collinjanssen/thingstodo/internal/model"
)

func isUniqueConstraintError(err error) bool {
	return err != nil && strings.Contains(err.Error(), "UNIQUE constraint failed")
}

type ProjectRepository struct {
	db        DBTX
	conn      *sql.DB
	tx        *sql.Tx
	changeLog *ChangeLogRepository
}

func NewProjectRepository(db *sql.DB, changeLog *ChangeLogRepository) *ProjectRepository {
	return &ProjectRepository{db: db, conn: db, changeLog: changeLog}
}

// Conn returns the underlying connection pool, for boundary validation
// lookups (repository.Exists) outside repository transactions.
func (r *ProjectRepository) Conn() *sql.DB {
	return r.conn
}

// WithTx returns a shallow copy of the repository bound to tx: its statements
// and nested inTx calls reuse tx instead of opening new transactions. It is
// nil-safe for the change log.
func (r *ProjectRepository) WithTx(tx *sql.Tx) *ProjectRepository {
	cp := *r
	cp.db = tx
	cp.tx = tx
	if cp.changeLog != nil {
		cp.changeLog = cp.changeLog.WithTx(tx)
	}
	return &cp
}

// inTx runs fn inside a single transaction: the bound one when the repository
// is a WithTx copy, otherwise a new transaction owned by this call.
func (r *ProjectRepository) inTx(fn func(db DBTX) error) error {
	if r.tx != nil {
		return fn(r.tx)
	}
	return InTx(r.conn, func(tx *sql.Tx) error { return fn(tx) })
}

func (r *ProjectRepository) List(areaID, status *string) ([]model.ProjectListItem, error) {
	query := `
		SELECT p.id, p.title, p.notes, p.area_id, p.status, p.when_date, p.deadline,
			p.sort_order, p.created_at, p.updated_at,
			COALESCE((SELECT COUNT(*) FROM tasks WHERE project_id = p.id AND deleted_at IS NULL), 0),
			COALESCE((SELECT COUNT(*) FROM tasks WHERE project_id = p.id AND status = 'completed' AND deleted_at IS NULL), 0)
		FROM projects p`

	var conditions []string
	var args []interface{}
	if areaID != nil {
		conditions = append(conditions, "p.area_id = ?")
		args = append(args, *areaID)
	}
	if status != nil {
		conditions = append(conditions, "p.status = ?")
		args = append(args, *status)
	}
	if len(conditions) > 0 {
		query += " WHERE " + strings.Join(conditions, " AND ")
	}
	query += " ORDER BY p.sort_order ASC"

	rows, err := r.db.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("list projects: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var projects []model.ProjectListItem
	for rows.Next() {
		var p model.ProjectListItem
		if err := rows.Scan(
			&p.ID, &p.Title, &p.Notes, &p.AreaID, &p.Status, &p.WhenDate, &p.Deadline,
			&p.SortOrder, &p.CreatedAt, &p.UpdatedAt,
			&p.TaskCount, &p.CompletedTaskCount,
		); err != nil {
			return nil, err
		}
		if p.AreaID != nil {
			p.Area = getRef(r.db, "areas", *p.AreaID)
		}
		p.Tags = getProjectTags(r.db, p.ID)
		projects = append(projects, p)
	}
	if projects == nil {
		projects = []model.ProjectListItem{}
	}
	return projects, rows.Err()
}

func (r *ProjectRepository) GetByID(id string) (*model.ProjectDetail, error) {
	var p model.ProjectDetail
	err := r.db.QueryRow(`
		SELECT p.id, p.title, p.notes, p.area_id, p.status, p.when_date, p.deadline,
			p.sort_order, p.created_at, p.updated_at,
			COALESCE((SELECT COUNT(*) FROM tasks WHERE project_id = p.id AND deleted_at IS NULL), 0),
			COALESCE((SELECT COUNT(*) FROM tasks WHERE project_id = p.id AND status = 'completed' AND deleted_at IS NULL), 0)
		FROM projects p WHERE p.id = ?`, id).Scan(
		&p.ID, &p.Title, &p.Notes, &p.AreaID, &p.Status, &p.WhenDate, &p.Deadline,
		&p.SortOrder, &p.CreatedAt, &p.UpdatedAt,
		&p.TaskCount, &p.CompletedTaskCount,
	)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	if p.AreaID != nil {
		p.Area = getRef(r.db, "areas", *p.AreaID)
	}
	p.Tags = getProjectTags(r.db, id)

	// Load headings with tasks
	headingRows, err := r.db.Query(
		"SELECT id, title, project_id, sort_order FROM headings WHERE project_id = ? ORDER BY sort_order", id)
	if err != nil {
		return nil, err
	}
	defer func() { _ = headingRows.Close() }()

	var headings []model.HeadingWithTasks
	for headingRows.Next() {
		var h model.HeadingWithTasks
		if err := headingRows.Scan(&h.ID, &h.Title, &h.ProjectID, &h.SortOrder); err != nil {
			return nil, fmt.Errorf("scan heading: %w", err)
		}
		h.Tasks = getTaskListItems(r.db, "heading_id", h.ID)
		headings = append(headings, h)
	}
	if headings == nil {
		headings = []model.HeadingWithTasks{}
	}
	p.Headings = headings

	// Tasks without heading
	p.TasksWithoutHeading = getTaskListItemsNoHeading(r.db, id)

	// Completed tasks (all time)
	completedRows, err := r.db.Query(`
		SELECT t.id, t.title, t.notes, t.status, t.when_date, t.when_evening, t.high_priority,
			t.deadline, t.project_id, t.area_id, t.heading_id,
			t.sort_order_today, t.sort_order_project, t.sort_order_heading,
			t.completed_at, t.canceled_at, t.deleted_at, t.created_at, t.updated_at,
			COALESCE((SELECT COUNT(*) FROM checklist_items WHERE task_id = t.id), 0),
			COALESCE((SELECT COUNT(*) FROM checklist_items WHERE task_id = t.id AND completed = 1), 0),
			CASE WHEN t.notes != '' THEN 1 ELSE 0 END,
			CASE WHEN EXISTS(SELECT 1 FROM attachments WHERE task_id = t.id AND type = 'link') THEN 1 ELSE 0 END,
			CASE WHEN EXISTS(SELECT 1 FROM attachments WHERE task_id = t.id AND type = 'file') THEN 1 ELSE 0 END,
			CASE WHEN EXISTS(SELECT 1 FROM repeat_rules WHERE task_id = t.id) THEN 1 ELSE 0 END,
			CASE WHEN EXISTS(SELECT 1 FROM reminders WHERE task_id = t.id) THEN 1 ELSE 0 END,
			(SELECT type FROM reminders WHERE task_id = t.id ORDER BY created_at LIMIT 1),
			(SELECT value FROM reminders WHERE task_id = t.id ORDER BY created_at LIMIT 1),
			(SELECT exact_at FROM reminders WHERE task_id = t.id ORDER BY created_at LIMIT 1),
			(SELECT start_time FROM task_schedules WHERE task_id = t.id ORDER BY sort_order ASC LIMIT 1),
			(SELECT end_time FROM task_schedules WHERE task_id = t.id ORDER BY sort_order ASC LIMIT 1)
		FROM tasks t
		WHERE t.project_id = ?
			AND t.status IN ('completed', 'canceled', 'wont_do')
			AND t.deleted_at IS NULL
		ORDER BY COALESCE(t.completed_at, t.canceled_at, t.updated_at) DESC`, id)
	if err != nil {
		return nil, err
	}
	defer func() { _ = completedRows.Close() }()
	p.CompletedTasks = scanTaskListItems(r.db, completedRows)

	return &p, nil
}

func (r *ProjectRepository) Create(input model.CreateProjectInput) (*model.ProjectDetail, error) {
	id := input.ID
	if id == "" {
		id = model.NewID()
	}
	var maxSort float64
	_ = r.db.QueryRow("SELECT COALESCE(MAX(sort_order), 0) FROM projects").Scan(&maxSort)

	_, err := r.db.Exec(`
		INSERT INTO projects (id, title, notes, area_id, when_date, deadline, sort_order)
		VALUES (?, ?, ?, ?, ?, ?, ?)`,
		id, input.Title, input.Notes, input.AreaID, input.WhenDate, input.Deadline, maxSort+1024)
	if err != nil {
		if isUniqueConstraintError(err) {
			return nil, ErrDuplicateProjectName
		}
		return nil, fmt.Errorf("create project: %w", err)
	}
	if len(input.TagIDs) > 0 {
		if err := r.setTags(id, input.TagIDs); err != nil {
			return nil, fmt.Errorf("set project tags: %w", err)
		}
	}
	project, err := r.GetByID(id)
	if err == nil && project != nil {
		logChange(r.changeLog, "project", id, "create", nil, project, "", "")
	}
	return project, err
}

func (r *ProjectRepository) Update(id string, input model.UpdateProjectInput) (*model.ProjectDetail, error) {
	var sets []string
	var args []interface{}

	if input.Title != nil {
		sets = append(sets, "title = ?")
		args = append(args, *input.Title)
	}
	if input.Notes != nil {
		sets = append(sets, "notes = ?")
		args = append(args, *input.Notes)
	}
	if _, ok := input.Raw["area_id"]; ok {
		sets = append(sets, "area_id = ?")
		args = append(args, input.AreaID)
	}
	if input.Status != nil {
		sets = append(sets, "status = ?")
		args = append(args, *input.Status)
	}
	if _, ok := input.Raw["when_date"]; ok {
		sets = append(sets, "when_date = ?")
		args = append(args, input.WhenDate)
	}
	if _, ok := input.Raw["deadline"]; ok {
		sets = append(sets, "deadline = ?")
		args = append(args, input.Deadline)
	}

	if len(sets) > 0 {
		sets = append(sets, "updated_at = datetime('now')")
		args = append(args, id)
		_, err := r.db.Exec("UPDATE projects SET "+strings.Join(sets, ", ")+" WHERE id = ?", args...)
		if err != nil {
			if isUniqueConstraintError(err) {
				return nil, ErrDuplicateProjectName
			}
			return nil, err
		}
	}
	if input.TagIDs != nil {
		if err := r.setTags(id, input.TagIDs); err != nil {
			return nil, fmt.Errorf("set project tags: %w", err)
		}
	}
	project, err := r.GetByID(id)
	if err == nil && project != nil {
		var changedFields []string
		for k := range input.Raw {
			changedFields = append(changedFields, k)
		}
		if input.TagIDs != nil {
			changedFields = append(changedFields, "tag_ids")
		}
		logChange(r.changeLog, "project", id, "update", changedFields, project, "", "")
	}
	return project, err
}

func (r *ProjectRepository) Delete(id string) error {
	_, err := r.db.Exec("DELETE FROM projects WHERE id = ?", id)
	if err == nil {
		logChange(r.changeLog, "project", id, "delete", nil, map[string]string{"id": id}, "", "")
	}
	return err
}

// DeleteToTrash deletes the project and sends its child tasks to the Trash
// instead of destroying them: every task gets deleted_at set and its
// containers (project, heading, area) cleared, so restoring it yields a
// loose task; status is left untouched. Children already in the trash only
// have their containers cleared. Headings go as plain rows, each logged the
// way HeadingRepository.Delete logs one. The whole operation runs in one
// transaction, with a task change logged per trashed child and a project
// change for the deletion itself.
func (r *ProjectRepository) DeleteToTrash(id string) error {
	now := time.Now().UTC().Format("2006-01-02 15:04:05")
	run := func(tx *sql.Tx) error {
		cl := r.WithTx(tx).changeLog // change log bound to the transaction

		rows, err := tx.Query(
			"SELECT id, title, status FROM tasks WHERE project_id = ? AND deleted_at IS NULL", id)
		if err != nil {
			return fmt.Errorf("collect project tasks: %w", err)
		}
		type childTask struct {
			id, title, status string
		}
		var children []childTask
		for rows.Next() {
			var c childTask
			if err := rows.Scan(&c.id, &c.title, &c.status); err != nil {
				_ = rows.Close()
				return fmt.Errorf("scan project task: %w", err)
			}
			children = append(children, c)
		}
		if err := rows.Close(); err != nil {
			return err
		}
		if err := rows.Err(); err != nil {
			return err
		}

		for _, c := range children {
			if _, err := tx.Exec(`UPDATE tasks
				SET deleted_at = ?, updated_at = ?, project_id = NULL, heading_id = NULL, area_id = NULL
				WHERE id = ?`, now, now, c.id); err != nil {
				return fmt.Errorf("trash project task %s: %w", c.id, err)
			}
			snapshot := map[string]interface{}{
				"id": c.id, "title": c.title, "status": c.status, "deleted_at": now,
				"project_id": nil, "heading_id": nil, "area_id": nil,
			}
			if err := logChange(cl, "task", c.id, "delete", nil, snapshot, "", ""); err != nil {
				return fmt.Errorf("log trashed task %s: %w", c.id, err)
			}
		}

		// Children already in the trash keep their trash date; only clear
		// the containers that are about to dangle.
		if _, err := tx.Exec(
			"UPDATE tasks SET project_id = NULL, heading_id = NULL, area_id = NULL WHERE project_id = ?", id,
		); err != nil {
			return fmt.Errorf("detach trashed project tasks: %w", err)
		}

		headingRows, err := tx.Query("SELECT id FROM headings WHERE project_id = ?", id)
		if err != nil {
			return fmt.Errorf("collect project headings: %w", err)
		}
		var headingIDs []string
		for headingRows.Next() {
			var hid string
			if err := headingRows.Scan(&hid); err != nil {
				_ = headingRows.Close()
				return fmt.Errorf("scan project heading: %w", err)
			}
			headingIDs = append(headingIDs, hid)
		}
		if err := headingRows.Close(); err != nil {
			return err
		}
		if err := headingRows.Err(); err != nil {
			return err
		}

		if _, err := tx.Exec("DELETE FROM headings WHERE project_id = ?", id); err != nil {
			return fmt.Errorf("delete project headings: %w", err)
		}
		for _, hid := range headingIDs {
			if err := logChange(cl, "heading", hid, "delete", nil, map[string]string{"id": hid}, "", ""); err != nil {
				return fmt.Errorf("log deleted heading %s: %w", hid, err)
			}
		}

		if _, err := tx.Exec("DELETE FROM projects WHERE id = ?", id); err != nil {
			return fmt.Errorf("delete project: %w", err)
		}
		return logChange(cl, "project", id, "delete", nil, map[string]string{"id": id}, "", "")
	}
	if r.tx != nil {
		return run(r.tx)
	}
	return InTx(r.conn, run)
}

func (r *ProjectRepository) Complete(id string) (*model.ProjectDetail, error) {
	_, err := r.db.Exec(
		"UPDATE projects SET status = 'completed', updated_at = datetime('now') WHERE id = ?", id)
	if err != nil {
		return nil, err
	}
	project, err := r.GetByID(id)
	if err == nil && project != nil {
		logChange(r.changeLog, "project", id, "update", []string{"status"}, project, "", "")
	}
	return project, err
}

func (r *ProjectRepository) Reorder(items []model.SimpleReorderItem) error {
	if err := r.inTx(func(db DBTX) error {
		for _, item := range items {
			_, err := db.Exec("UPDATE projects SET sort_order = ? WHERE id = ?", item.SortOrder, item.ID)
			if err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		return err
	}

	for _, item := range items {
		p, err := r.GetByID(item.ID)
		if err == nil && p != nil {
			logChange(r.changeLog, "project", item.ID, "update", []string{"sort_order"}, p, "", "")
		}
	}
	return nil
}

// --- shared helpers ---

var validRefTables = map[string]bool{"projects": true, "areas": true, "headings": true}

func getRef(db DBTX, table, id string) *model.Ref {
	if !validRefTables[table] {
		return nil
	}
	var ref model.Ref
	err := db.QueryRow("SELECT id, title FROM "+table+" WHERE id = ?", id).Scan(&ref.ID, &ref.Title)
	if err != nil {
		return nil
	}
	return &ref
}

func getProjectTags(db DBTX, projectID string) []model.TagRef {
	rows, err := db.Query(
		"SELECT t.id, t.title, t.color FROM tags t JOIN project_tags pt ON t.id = pt.tag_id WHERE pt.project_id = ? ORDER BY t.sort_order", projectID)
	if err != nil {
		return []model.TagRef{}
	}
	defer func() { _ = rows.Close() }()
	var tags []model.TagRef
	for rows.Next() {
		var t model.TagRef
		_ = rows.Scan(&t.ID, &t.Title, &t.Color)
		tags = append(tags, t)
	}
	if tags == nil {
		return []model.TagRef{}
	}
	return tags
}

// setProjectTags replaces the project's tag set using the given DBTX, which
// may be the connection pool or an open transaction.
func setProjectTags(db DBTX, projectID string, tagIDs []string) error {
	if _, err := db.Exec("DELETE FROM project_tags WHERE project_id = ?", projectID); err != nil {
		return fmt.Errorf("delete project tags: %w", err)
	}
	for _, tagID := range tagIDs {
		if _, err := db.Exec("INSERT OR IGNORE INTO project_tags (project_id, tag_id) VALUES (?, ?)", projectID, tagID); err != nil {
			return fmt.Errorf("insert project tag: %w", err)
		}
	}
	return nil
}

// setTags runs setProjectTags inside a transaction: the bound one when the
// repository is a WithTx copy, otherwise a new one.
func (r *ProjectRepository) setTags(projectID string, tagIDs []string) error {
	return r.inTx(func(db DBTX) error {
		return setProjectTags(db, projectID, tagIDs)
	})
}

var validFilterCols = map[string]bool{"heading_id": true, "project_id": true, "area_id": true}

func getTaskListItems(db DBTX, filterCol, filterVal string) []model.TaskListItem {
	if !validFilterCols[filterCol] {
		return []model.TaskListItem{}
	}
	rows, err := db.Query(`
		SELECT t.id, t.title, t.notes, t.status, t.when_date, t.when_evening, t.high_priority,
			t.deadline, t.project_id, t.area_id, t.heading_id,
			t.sort_order_today, t.sort_order_project, t.sort_order_heading,
			t.completed_at, t.canceled_at, t.deleted_at, t.created_at, t.updated_at,
			COALESCE((SELECT COUNT(*) FROM checklist_items WHERE task_id = t.id), 0),
			COALESCE((SELECT COUNT(*) FROM checklist_items WHERE task_id = t.id AND completed = 1), 0),
			CASE WHEN t.notes != '' THEN 1 ELSE 0 END,
			CASE WHEN EXISTS(SELECT 1 FROM attachments WHERE task_id = t.id AND type = 'link') THEN 1 ELSE 0 END,
			CASE WHEN EXISTS(SELECT 1 FROM attachments WHERE task_id = t.id AND type = 'file') THEN 1 ELSE 0 END,
			CASE WHEN EXISTS(SELECT 1 FROM repeat_rules WHERE task_id = t.id) THEN 1 ELSE 0 END,
			CASE WHEN EXISTS(SELECT 1 FROM reminders WHERE task_id = t.id) THEN 1 ELSE 0 END,
			(SELECT type FROM reminders WHERE task_id = t.id ORDER BY created_at LIMIT 1),
			(SELECT value FROM reminders WHERE task_id = t.id ORDER BY created_at LIMIT 1),
			(SELECT exact_at FROM reminders WHERE task_id = t.id ORDER BY created_at LIMIT 1),
			(SELECT start_time FROM task_schedules WHERE task_id = t.id ORDER BY sort_order ASC LIMIT 1),
			(SELECT end_time FROM task_schedules WHERE task_id = t.id ORDER BY sort_order ASC LIMIT 1)
		FROM tasks t WHERE t.`+filterCol+` = ? AND t.status = 'open' AND t.deleted_at IS NULL
		ORDER BY t.sort_order_project ASC`, filterVal)
	if err != nil {
		return []model.TaskListItem{}
	}
	defer func() { _ = rows.Close() }()
	tasks := scanTaskListItems(db, rows)
	populateActionableScheduleFlags(db, tasks)
	return tasks
}

func getTaskListItemsNoHeading(db DBTX, projectID string) []model.TaskListItem {
	rows, err := db.Query(`
		SELECT t.id, t.title, t.notes, t.status, t.when_date, t.when_evening, t.high_priority,
			t.deadline, t.project_id, t.area_id, t.heading_id,
			t.sort_order_today, t.sort_order_project, t.sort_order_heading,
			t.completed_at, t.canceled_at, t.deleted_at, t.created_at, t.updated_at,
			COALESCE((SELECT COUNT(*) FROM checklist_items WHERE task_id = t.id), 0),
			COALESCE((SELECT COUNT(*) FROM checklist_items WHERE task_id = t.id AND completed = 1), 0),
			CASE WHEN t.notes != '' THEN 1 ELSE 0 END,
			CASE WHEN EXISTS(SELECT 1 FROM attachments WHERE task_id = t.id AND type = 'link') THEN 1 ELSE 0 END,
			CASE WHEN EXISTS(SELECT 1 FROM attachments WHERE task_id = t.id AND type = 'file') THEN 1 ELSE 0 END,
			CASE WHEN EXISTS(SELECT 1 FROM repeat_rules WHERE task_id = t.id) THEN 1 ELSE 0 END,
			CASE WHEN EXISTS(SELECT 1 FROM reminders WHERE task_id = t.id) THEN 1 ELSE 0 END,
			(SELECT type FROM reminders WHERE task_id = t.id ORDER BY created_at LIMIT 1),
			(SELECT value FROM reminders WHERE task_id = t.id ORDER BY created_at LIMIT 1),
			(SELECT exact_at FROM reminders WHERE task_id = t.id ORDER BY created_at LIMIT 1),
			(SELECT start_time FROM task_schedules WHERE task_id = t.id ORDER BY sort_order ASC LIMIT 1),
			(SELECT end_time FROM task_schedules WHERE task_id = t.id ORDER BY sort_order ASC LIMIT 1)
		FROM tasks t WHERE t.project_id = ? AND t.heading_id IS NULL AND t.status = 'open' AND t.deleted_at IS NULL
		ORDER BY t.sort_order_project ASC`, projectID)
	if err != nil {
		return []model.TaskListItem{}
	}
	defer func() { _ = rows.Close() }()
	tasks := scanTaskListItems(db, rows)
	populateActionableScheduleFlags(db, tasks)
	return tasks
}

func scanTaskListItems(db DBTX, rows *sql.Rows) []model.TaskListItem {
	var tasks []model.TaskListItem
	for rows.Next() {
		var t model.TaskListItem
		var whenEvening, highPriority, hasNotes, hasLinks, hasFiles, hasRepeat, hasReminders int
		_ = rows.Scan(
			&t.ID, &t.Title, &t.Notes, &t.Status, &t.WhenDate, &whenEvening, &highPriority,
			&t.Deadline, &t.ProjectID, &t.AreaID, &t.HeadingID,
			&t.SortOrderToday, &t.SortOrderProject, &t.SortOrderHeading,
			&t.CompletedAt, &t.CanceledAt, &t.DeletedAt, &t.CreatedAt, &t.UpdatedAt,
			&t.ChecklistCount, &t.ChecklistDone,
			&hasNotes, &hasLinks, &hasFiles, &hasRepeat, &hasReminders,
			&t.FirstReminderType, &t.FirstReminderValue, &t.FirstReminderExactAt,
			&t.FirstScheduleTime, &t.FirstScheduleEndTime,
		)
		_ = whenEvening // column retained in DB but no longer exposed
		t.HighPriority = highPriority == 1
		t.HasNotes = hasNotes == 1
		t.HasLinks = hasLinks == 1
		t.HasFiles = hasFiles == 1
		t.HasRepeatRule = hasRepeat == 1
		t.HasReminders = hasReminders == 1
		// Get tags
		tagRows, _ := db.Query(
			"SELECT t.id, t.title, t.color FROM tags t JOIN task_tags tt ON t.id = tt.tag_id WHERE tt.task_id = ?", t.ID)
		if tagRows != nil {
			var tags []model.TagRef
			for tagRows.Next() {
				var tag model.TagRef
				_ = tagRows.Scan(&tag.ID, &tag.Title, &tag.Color)
				tags = append(tags, tag)
			}
			_ = tagRows.Close()
			if tags == nil {
				tags = []model.TagRef{}
			}
			t.Tags = tags
		} else {
			t.Tags = []model.TagRef{}
		}
		// Resolve project/area names
		if t.ProjectID != nil {
			var name string
			if err := db.QueryRow("SELECT title FROM projects WHERE id = ?", *t.ProjectID).Scan(&name); err == nil {
				t.ProjectName = &name
			}
		}
		if t.AreaID != nil {
			var name string
			if err := db.QueryRow("SELECT title FROM areas WHERE id = ?", *t.AreaID).Scan(&name); err == nil {
				t.AreaName = &name
			}
		}
		tasks = append(tasks, t)
	}
	if tasks == nil {
		tasks = []model.TaskListItem{}
	}
	return tasks
}
