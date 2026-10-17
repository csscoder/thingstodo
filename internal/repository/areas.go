package repository

import (
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/collinjanssen/thingstodo/internal/model"
)

type AreaRepository struct {
	db        DBTX
	conn      *sql.DB
	tx        *sql.Tx
	changeLog *ChangeLogRepository
}

func NewAreaRepository(db *sql.DB, changeLog *ChangeLogRepository) *AreaRepository {
	return &AreaRepository{db: db, conn: db, changeLog: changeLog}
}

// WithTx returns a shallow copy of the repository bound to tx: its statements
// and nested inTx calls reuse tx instead of opening new transactions. It is
// nil-safe for the change log.
func (r *AreaRepository) WithTx(tx *sql.Tx) *AreaRepository {
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
func (r *AreaRepository) inTx(fn func(db DBTX) error) error {
	if r.tx != nil {
		return fn(r.tx)
	}
	return InTx(r.conn, func(tx *sql.Tx) error { return fn(tx) })
}

func (r *AreaRepository) List() ([]model.Area, error) {
	rows, err := r.db.Query(`
		SELECT a.id, a.title, a.sort_order, a.created_at, a.updated_at,
			COALESCE((SELECT COUNT(*) FROM projects WHERE area_id = a.id), 0),
			COALESCE((SELECT COUNT(*) FROM tasks WHERE area_id = a.id AND deleted_at IS NULL), 0),
			COALESCE((SELECT COUNT(*) FROM tasks WHERE area_id = a.id AND project_id IS NULL AND status = 'open' AND deleted_at IS NULL), 0)
		FROM areas a ORDER BY a.sort_order ASC`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var areas []model.Area
	for rows.Next() {
		var a model.Area
		if err := rows.Scan(&a.ID, &a.Title, &a.SortOrder, &a.CreatedAt, &a.UpdatedAt, &a.ProjectCount, &a.TaskCount, &a.StandaloneTaskCount); err != nil {
			return nil, fmt.Errorf("scan area: %w", err)
		}
		areas = append(areas, a)
	}
	if areas == nil {
		areas = []model.Area{}
	}
	return areas, rows.Err()
}

func (r *AreaRepository) GetByID(id string) (*model.AreaDetail, error) {
	var a model.AreaDetail
	err := r.db.QueryRow(
		"SELECT id, title, sort_order, created_at, updated_at FROM areas WHERE id = ?", id,
	).Scan(&a.ID, &a.Title, &a.SortOrder, &a.CreatedAt, &a.UpdatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	// Projects in this area
	projRows, err := r.db.Query(`
		SELECT p.id, p.title, p.notes, p.area_id, p.status, p.when_date, p.deadline,
			p.sort_order, p.created_at, p.updated_at,
			COALESCE((SELECT COUNT(*) FROM tasks WHERE project_id = p.id AND deleted_at IS NULL), 0),
			COALESCE((SELECT COUNT(*) FROM tasks WHERE project_id = p.id AND status = 'completed' AND deleted_at IS NULL), 0)
		FROM projects p WHERE p.area_id = ? AND p.status = 'open' ORDER BY p.sort_order`, id)
	if err != nil {
		return nil, err
	}
	defer func() { _ = projRows.Close() }()

	var projects []model.ProjectListItem
	for projRows.Next() {
		var p model.ProjectListItem
		if err := projRows.Scan(&p.ID, &p.Title, &p.Notes, &p.AreaID, &p.Status, &p.WhenDate, &p.Deadline,
			&p.SortOrder, &p.CreatedAt, &p.UpdatedAt, &p.TaskCount, &p.CompletedTaskCount); err != nil {
			return nil, fmt.Errorf("scan project: %w", err)
		}
		p.Tags = getProjectTags(r.db, p.ID)
		projects = append(projects, p)
	}
	if projects == nil {
		projects = []model.ProjectListItem{}
	}
	a.Projects = projects

	// Standalone tasks in this area (no project)
	taskRows, err := r.db.Query(`
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
		FROM tasks t WHERE t.area_id = ? AND t.project_id IS NULL AND t.status = 'open' AND t.deleted_at IS NULL
		ORDER BY t.sort_order_today`, id)
	if err != nil {
		return nil, err
	}
	defer func() { _ = taskRows.Close() }()
	a.Tasks = scanTaskListItems(r.db, taskRows)
	populateActionableScheduleFlags(r.db, a.Tasks)

	// Completed standalone tasks (today only)
	today := time.Now().Format("2006-01-02")
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
		WHERE t.area_id = ? AND t.project_id IS NULL
			AND t.status IN ('completed', 'canceled', 'wont_do')
			AND COALESCE(t.completed_at, t.canceled_at, t.updated_at) >= ?
			AND t.deleted_at IS NULL
		ORDER BY COALESCE(t.completed_at, t.canceled_at, t.updated_at) DESC`, id, today)
	if err != nil {
		return nil, err
	}
	defer func() { _ = completedRows.Close() }()
	a.CompletedTasks = scanTaskListItems(r.db, completedRows)

	return &a, nil
}

func (r *AreaRepository) Create(input model.CreateAreaInput) (*model.AreaDetail, error) {
	id := input.ID
	if id == "" {
		id = model.NewID()
	}
	var maxSort float64
	_ = r.db.QueryRow("SELECT COALESCE(MAX(sort_order), 0) FROM areas").Scan(&maxSort)

	_, err := r.db.Exec("INSERT INTO areas (id, title, sort_order) VALUES (?, ?, ?)",
		id, input.Title, maxSort+1024)
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE constraint failed") {
			return nil, ErrDuplicateAreaName
		}
		return nil, fmt.Errorf("create area: %w", err)
	}

	// Answer with the same detail a GET of the area returns, so an
	// idempotent replay of this create responds with an identical body.
	area, err := r.GetByID(id)
	if err == nil && area != nil {
		logChange(r.changeLog, "area", id, "create", nil, area, "", "")
	}
	return area, err
}

func (r *AreaRepository) Update(id string, input model.UpdateAreaInput) (*model.Area, error) {
	if input.Title != nil {
		_, err := r.db.Exec("UPDATE areas SET title = ?, updated_at = datetime('now') WHERE id = ?", *input.Title, id)
		if err != nil {
			if strings.Contains(err.Error(), "UNIQUE constraint failed") {
				return nil, ErrDuplicateAreaName
			}
			return nil, err
		}
	}
	if input.SortOrder != nil {
		_, _ = r.db.Exec("UPDATE areas SET sort_order = ?, updated_at = datetime('now') WHERE id = ?", *input.SortOrder, id)
	}
	var a model.Area
	err := r.db.QueryRow("SELECT id, title, sort_order, created_at, updated_at FROM areas WHERE id = ?", id).
		Scan(&a.ID, &a.Title, &a.SortOrder, &a.CreatedAt, &a.UpdatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err == nil {
		logChange(r.changeLog, "area", id, "update", nil, &a, "", "")
	}
	return &a, err
}

func (r *AreaRepository) Delete(id string) error {
	_, err := r.db.Exec("DELETE FROM areas WHERE id = ?", id)
	if err == nil {
		logChange(r.changeLog, "area", id, "delete", nil, map[string]string{"id": id}, "", "")
	}
	return err
}

func (r *AreaRepository) Reorder(items []model.SimpleReorderItem) error {
	if err := r.inTx(func(db DBTX) error {
		for _, item := range items {
			_, err := db.Exec("UPDATE areas SET sort_order = ? WHERE id = ?", item.SortOrder, item.ID)
			if err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		return err
	}

	for _, item := range items {
		var a model.Area
		err := r.db.QueryRow("SELECT id, title, sort_order, created_at, updated_at FROM areas WHERE id = ?", item.ID).
			Scan(&a.ID, &a.Title, &a.SortOrder, &a.CreatedAt, &a.UpdatedAt)
		if err == nil {
			logChange(r.changeLog, "area", item.ID, "update", []string{"sort_order"}, &a, "", "")
		}
	}
	return nil
}

// DeleteToTrash deletes the area once it holds no projects, and sends its
// standalone tasks (no project) to the Trash instead of destroying them:
// each gets deleted_at set and area_id cleared, status untouched, so
// restoring one yields a loose task. Standalone tasks already in the trash
// only have area_id cleared. The guard and the writes run in one
// transaction, with a task change logged per trashed task and an area
// change for the deletion itself.
func (r *AreaRepository) DeleteToTrash(id string) error {
	now := time.Now().UTC().Format("2006-01-02 15:04:05")
	run := func(tx *sql.Tx) error {
		cl := r.WithTx(tx).changeLog // change log bound to the transaction

		var count int
		if err := tx.QueryRow("SELECT COUNT(*) FROM projects WHERE area_id = ?", id).Scan(&count); err != nil {
			return fmt.Errorf("count area projects: %w", err)
		}
		if count > 0 {
			return ErrHasProjects
		}

		rows, err := tx.Query(
			"SELECT id, title, status FROM tasks WHERE area_id = ? AND project_id IS NULL AND deleted_at IS NULL", id)
		if err != nil {
			return fmt.Errorf("collect area tasks: %w", err)
		}
		type childTask struct {
			id, title, status string
		}
		var children []childTask
		for rows.Next() {
			var c childTask
			if err := rows.Scan(&c.id, &c.title, &c.status); err != nil {
				_ = rows.Close()
				return fmt.Errorf("scan area task: %w", err)
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
			if _, err := tx.Exec(
				"UPDATE tasks SET deleted_at = ?, updated_at = ?, area_id = NULL WHERE id = ?",
				now, now, c.id,
			); err != nil {
				return fmt.Errorf("trash area task %s: %w", c.id, err)
			}
			snapshot := map[string]interface{}{
				"id": c.id, "title": c.title, "status": c.status, "deleted_at": now,
				"area_id": nil,
			}
			if err := logChange(cl, "task", c.id, "delete", nil, snapshot, "", ""); err != nil {
				return fmt.Errorf("log trashed task %s: %w", c.id, err)
			}
		}

		// Standalone tasks already in the trash keep their trash date; only
		// clear the area reference that is about to dangle.
		if _, err := tx.Exec(
			"UPDATE tasks SET area_id = NULL WHERE area_id = ? AND project_id IS NULL", id,
		); err != nil {
			return fmt.Errorf("detach trashed area tasks: %w", err)
		}

		if _, err := tx.Exec("DELETE FROM areas WHERE id = ?", id); err != nil {
			return fmt.Errorf("delete area: %w", err)
		}
		return logChange(cl, "area", id, "delete", nil, map[string]string{"id": id}, "", "")
	}
	if r.tx != nil {
		return run(r.tx)
	}
	return InTx(r.conn, run)
}
