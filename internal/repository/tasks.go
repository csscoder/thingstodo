package repository

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/collinjanssen/thingstodo/internal/model"
)

// normalizeParents enforces the container invariant (D5) before parent
// pointers are written: a task's area is its project's area, and a heading
// belongs to the task's project.
//   - When *projectID is non-nil, *areaID is set to that project's area
//     (nil when the project has none), regardless of what the caller sent.
//   - When the project changed and no heading was given, a heading that
//     does not belong to the new project is cleared; with no new project
//     every heading is foreign and cleared.
//
// It applies uniformly to trashed and live rows.
func normalizeParents(db DBTX, projectID, areaID, headingID **string, projectChanged, headingGiven bool) error {
	if *projectID != nil {
		var projectArea sql.NullString
		err := db.QueryRow("SELECT area_id FROM projects WHERE id = ?", **projectID).Scan(&projectArea)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("normalize parents: load project area: %w", err)
		}
		// An unknown project id keeps the no-area result: FK enforcement is
		// the database's job, bulk moves must not gain a new error path.
		if projectArea.Valid {
			a := projectArea.String
			*areaID = &a
		} else {
			*areaID = nil
		}
	}
	if projectChanged && !headingGiven && *headingID != nil {
		if *projectID == nil {
			*headingID = nil
			return nil
		}
		inProject, err := HeadingInProject(db, **headingID, **projectID)
		if err != nil {
			return fmt.Errorf("normalize parents: heading check: %w", err)
		}
		if !inProject {
			*headingID = nil
		}
	}
	return nil
}

// nullStrPtr converts a scanned NULL-able string into a string pointer.
func nullStrPtr(ns sql.NullString) *string {
	if ns.Valid {
		s := ns.String
		return &s
	}
	return nil
}

// sameID reports whether two nullable ids point at the same value.
func sameID(a, b *string) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

type TaskRepository struct {
	db        DBTX
	conn      *sql.DB
	tx        *sql.Tx
	changeLog *ChangeLogRepository
	loc       *time.Location
	now       func() time.Time
}

func NewTaskRepository(db *sql.DB, changeLog *ChangeLogRepository) *TaskRepository {
	return &TaskRepository{db: db, conn: db, changeLog: changeLog, loc: time.UTC, now: time.Now}
}

// WithLocation returns a copy of the repository whose clock runs in loc, so
// the list= filter resolves "today" in the server timezone like the view
// repository does. A nil loc keeps UTC.
func (r *TaskRepository) WithLocation(loc *time.Location) *TaskRepository {
	if loc == nil {
		return r
	}
	cp := *r
	cp.loc = loc
	return &cp
}

// SetClock overrides the repository clock (default time.Now). The list=
// filter resolves today from it, in the repository's location.
func (r *TaskRepository) SetClock(now func() time.Time) {
	if now != nil {
		r.now = now
	}
}

// today returns the current calendar date in the repository timezone.
func (r *TaskRepository) today() string {
	return r.now().In(r.loc).Format("2006-01-02")
}

// Conn returns the underlying connection pool. Handlers that own a
// transaction spanning several repositories start it with InTx(repo.Conn(), …).
func (r *TaskRepository) Conn() *sql.DB {
	return r.conn
}

// WithTx returns a shallow copy of the repository bound to tx: its statements
// and nested inTx calls reuse tx instead of opening new transactions. It is
// nil-safe for the change log.
func (r *TaskRepository) WithTx(tx *sql.Tx) *TaskRepository {
	cp := *r
	cp.db = tx
	cp.tx = tx
	if cp.changeLog != nil {
		cp.changeLog = cp.changeLog.WithTx(tx)
	}
	return &cp
}

// inTx runs fn inside a single transaction: the bound one when the repository
// is a WithTx copy, otherwise a new transaction owned by this call. Nested
// calls therefore never open a second transaction.
func (r *TaskRepository) inTx(fn func(db DBTX) error) error {
	if r.tx != nil {
		return fn(r.tx)
	}
	return InTx(r.conn, func(tx *sql.Tx) error { return fn(tx) })
}

// taskSort builds the ORDER BY expression and the cursor key extractor for
// a sort name (C1): manual, when_date, deadline, created_at, updated_at,
// title, with an optional leading "-" for descending. manual picks the
// column that owns the task's position: sort_order_heading under a heading
// filter, sort_order_project under a project filter, otherwise
// sort_order_today. numeric reports whether the key is a float (the manual
// sort orders), which is how the cursor decodes it back. NULL when/deadline
// keys sort as the empty string — first ascending, last descending, like
// SQLite NULLs.
func taskSort(f model.TaskFilters) (orderExpr string, numeric bool, keyOf func(model.TaskListItem) any, err error) {
	name := strings.TrimPrefix(f.Sort, "-")
	switch name {
	case "", "manual":
		switch {
		case f.HeadingID != nil:
			return "t.sort_order_heading", true, func(t model.TaskListItem) any { return t.SortOrderHeading }, nil
		case f.ProjectID != nil:
			return "t.sort_order_project", true, func(t model.TaskListItem) any { return t.SortOrderProject }, nil
		default:
			return "t.sort_order_today", true, func(t model.TaskListItem) any { return t.SortOrderToday }, nil
		}
	case "when_date":
		return "COALESCE(t.when_date, '')", false, func(t model.TaskListItem) any {
			if t.WhenDate != nil {
				return *t.WhenDate
			}
			return ""
		}, nil
	case "deadline":
		return "COALESCE(t.deadline, '')", false, func(t model.TaskListItem) any {
			if t.Deadline != nil {
				return *t.Deadline
			}
			return ""
		}, nil
	case "created_at":
		return "t.created_at", false, func(t model.TaskListItem) any { return t.CreatedAt }, nil
	case "updated_at":
		return "t.updated_at", false, func(t model.TaskListItem) any { return t.UpdatedAt }, nil
	case "title":
		return "t.title", false, func(t model.TaskListItem) any { return t.Title }, nil
	default:
		return "", false, nil, fmt.Errorf("list tasks: unknown sort %q", f.Sort)
	}
}

// List returns the tasks matching f (C1): one page of at most f.Limit rows
// (0 = unlimited, the default for existing callers) plus the cursor of the
// next page — "" (omitted in JSON) on the last page. list= reuses the
// shared view predicates (E3); list=trash selects only soft-deleted rows,
// every other query excludes them. project_name and area_name are filled
// through LEFT JOINs, and every sort tie-breaks by id so cursor pages have
// no duplicates or gaps.
func (r *TaskRepository) List(f model.TaskFilters) (tasks []model.TaskListItem, nextCursor string, err error) {
	query := `
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
			(SELECT end_time FROM task_schedules WHERE task_id = t.id ORDER BY sort_order ASC LIMIT 1),
			p.title,
			a.title
		FROM tasks t
		LEFT JOIN projects p ON p.id = t.project_id
		LEFT JOIN areas a ON a.id = t.area_id`

	var conditions []string
	var args []interface{}

	if f.Status != nil {
		conditions = append(conditions, "t.status = ?")
		args = append(args, *f.Status)
	}
	if len(f.Statuses) > 0 {
		placeholders := make([]string, len(f.Statuses))
		for i, s := range f.Statuses {
			placeholders[i] = "?"
			args = append(args, s)
		}
		conditions = append(conditions, "t.status IN ("+strings.Join(placeholders, ",")+")")
	}
	if f.ProjectID != nil {
		conditions = append(conditions, "t.project_id = ?")
		args = append(args, *f.ProjectID)
	}
	if f.AreaID != nil {
		conditions = append(conditions, "t.area_id = ?")
		args = append(args, *f.AreaID)
	}
	if f.HeadingID != nil {
		conditions = append(conditions, "t.heading_id = ?")
		args = append(args, *f.HeadingID)
	}
	if f.WhenDate != nil {
		conditions = append(conditions, "t.when_date = ?")
		args = append(args, *f.WhenDate)
	}
	if f.WhenBefore != nil {
		conditions = append(conditions, "t.when_date < ? AND t.when_date != 'someday'")
		args = append(args, *f.WhenBefore)
	}
	if f.WhenAfter != nil {
		conditions = append(conditions, "t.when_date >= ? AND t.when_date != 'someday'")
		args = append(args, *f.WhenAfter)
	}
	if f.HasWhen != nil {
		if *f.HasWhen {
			conditions = append(conditions, "t.when_date IS NOT NULL")
		} else {
			conditions = append(conditions, "t.when_date IS NULL")
		}
	}
	if f.DeadlineBefore != nil {
		conditions = append(conditions, "t.deadline <= ?")
		args = append(args, *f.DeadlineBefore)
	}
	if f.DeadlineAfter != nil {
		conditions = append(conditions, "t.deadline >= ?")
		args = append(args, *f.DeadlineAfter)
	}
	if f.HasDeadline != nil {
		if *f.HasDeadline {
			conditions = append(conditions, "t.deadline IS NOT NULL")
		} else {
			conditions = append(conditions, "t.deadline IS NULL")
		}
	}
	if f.HighPriority != nil {
		conditions = append(conditions, "t.high_priority = ?")
		args = append(args, boolToInt(*f.HighPriority))
	}
	if f.UpdatedSince != nil {
		conditions = append(conditions, "t.updated_at >= ?")
		args = append(args, *f.UpdatedSince)
	}
	if f.CreatedSince != nil {
		conditions = append(conditions, "t.created_at >= ?")
		args = append(args, *f.CreatedSince)
	}
	if len(f.TagIDs) > 0 {
		placeholders := make([]string, len(f.TagIDs))
		for i, id := range f.TagIDs {
			placeholders[i] = "?"
			args = append(args, id)
		}
		// tag_mode=all requires every listed tag; any (the default) keeps
		// the historical IN semantics.
		if f.TagMode == "all" {
			conditions = append(conditions, fmt.Sprintf(
				"t.id IN (SELECT task_id FROM task_tags WHERE tag_id IN (%s) GROUP BY task_id HAVING COUNT(DISTINCT tag_id) = %d)",
				strings.Join(placeholders, ","), len(f.TagIDs)))
		} else {
			conditions = append(conditions, fmt.Sprintf(
				"t.id IN (SELECT task_id FROM task_tags WHERE tag_id IN (%s))",
				strings.Join(placeholders, ",")))
		}
	}
	if f.Search != nil {
		if !HasSearchTokens(*f.Search) {
			return nil, "", ErrEmptySearch
		}
		conditions = append(conditions, "t.rowid IN (SELECT rowid FROM tasks_fts WHERE tasks_fts MATCH ?)")
		args = append(args, ftsQuery(*f.Search, false))
	}
	if f.List != nil {
		if *f.List == "trash" {
			conditions = append(conditions, "t.deleted_at IS NOT NULL")
		} else {
			// listCondition implies not-trashed for every non-trash list.
			cond, condArgs, err := listCondition(*f.List, r.today(), "")
			if err != nil {
				return nil, "", err
			}
			conditions = append(conditions, cond)
			args = append(args, condArgs...)
		}
	} else {
		conditions = append(conditions, "t.deleted_at IS NULL")
	}

	orderExpr, numeric, keyOf, err := taskSort(f)
	if err != nil {
		return nil, "", err
	}
	desc := strings.HasPrefix(f.Sort, "-")
	if f.Cursor != "" {
		sortKey, id, cerr := decodeCursor(f.Cursor)
		if cerr != nil {
			return nil, "", cerr
		}
		var keyArg interface{}
		if numeric {
			keyArg, cerr = decodeFloatCursor(sortKey)
		} else {
			keyArg = sortKey
		}
		if cerr != nil {
			return nil, "", cerr
		}
		// The keyset mirrors the ORDER BY (key, id): rows strictly beyond
		// the last row the previous page returned. It is appended last so
		// its placeholders line up with the tail of args.
		cmp := ">"
		if desc {
			cmp = "<"
		}
		conditions = append(conditions, fmt.Sprintf("(%s %s ? OR (%s = ? AND t.id > ?))", orderExpr, cmp, orderExpr))
		args = append(args, keyArg, keyArg, id)
	}

	if len(conditions) > 0 {
		query += " WHERE " + strings.Join(conditions, " AND ")
	}
	dir := "ASC"
	if desc {
		dir = "DESC"
	}
	query += " ORDER BY " + orderExpr + " " + dir + ", t.id ASC"
	if f.Limit > 0 {
		// Fetch one row past the page to learn whether a next page exists.
		query += fmt.Sprintf(" LIMIT %d", f.Limit+1)
	}

	rows, err := r.db.Query(query, args...)
	if err != nil {
		return nil, "", fmt.Errorf("list tasks: %w", err)
	}
	defer func() { _ = rows.Close() }()

	for rows.Next() {
		var t model.TaskListItem
		var whenEvening, highPriority, hasNotes, hasLinks, hasFiles, hasRepeat, hasReminders int
		var projectName, areaName sql.NullString
		if err := rows.Scan(
			&t.ID, &t.Title, &t.Notes, &t.Status, &t.WhenDate, &whenEvening, &highPriority,
			&t.Deadline, &t.ProjectID, &t.AreaID, &t.HeadingID,
			&t.SortOrderToday, &t.SortOrderProject, &t.SortOrderHeading,
			&t.CompletedAt, &t.CanceledAt, &t.DeletedAt, &t.CreatedAt, &t.UpdatedAt,
			&t.ChecklistCount, &t.ChecklistDone,
			&hasNotes, &hasLinks, &hasFiles, &hasRepeat, &hasReminders,
			&t.FirstReminderType, &t.FirstReminderValue, &t.FirstReminderExactAt,
			&t.FirstScheduleTime, &t.FirstScheduleEndTime,
			&projectName, &areaName,
		); err != nil {
			return nil, "", fmt.Errorf("scan task: %w", err)
		}
		_ = whenEvening // column retained in DB but no longer exposed
		t.HighPriority = highPriority == 1
		t.HasNotes = hasNotes == 1
		t.HasLinks = hasLinks == 1
		t.HasFiles = hasFiles == 1
		t.HasRepeatRule = hasRepeat == 1
		t.HasReminders = hasReminders == 1
		t.ProjectName = nullStrPtr(projectName)
		t.AreaName = nullStrPtr(areaName)
		t.Tags, _ = r.getTaskTags(t.ID)
		tasks = append(tasks, t)
	}
	if err := rows.Err(); err != nil {
		return nil, "", fmt.Errorf("list tasks: %w", err)
	}
	if f.Limit > 0 && len(tasks) > f.Limit {
		tasks = tasks[:f.Limit]
		nextCursor = encodeCursor(keyOf(tasks[f.Limit-1]), tasks[f.Limit-1].ID)
	}
	if tasks == nil {
		tasks = []model.TaskListItem{}
	}
	return tasks, nextCursor, nil
}
func (r *TaskRepository) GetByID(id string) (*model.TaskDetail, error) {
	var t model.TaskDetail
	var whenEvening, highPriority int
	err := r.db.QueryRow(`
		SELECT id, title, notes, status, when_date, when_evening, high_priority,
			deadline, project_id, area_id, heading_id,
			sort_order_today, sort_order_project, sort_order_heading,
			completed_at, canceled_at, deleted_at, created_at, updated_at
		FROM tasks WHERE id = ?`, id).Scan(
		&t.ID, &t.Title, &t.Notes, &t.Status, &t.WhenDate, &whenEvening, &highPriority,
		&t.Deadline, &t.ProjectID, &t.AreaID, &t.HeadingID,
		&t.SortOrderToday, &t.SortOrderProject, &t.SortOrderHeading,
		&t.CompletedAt, &t.CanceledAt, &t.DeletedAt, &t.CreatedAt, &t.UpdatedAt,
	)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get task: %w", err)
	}
	_ = whenEvening // column retained in DB but no longer exposed
	t.HighPriority = highPriority == 1

	// Load related refs
	if t.ProjectID != nil {
		t.Project = r.getRef("projects", *t.ProjectID)
	}
	if t.AreaID != nil {
		t.Area = r.getRef("areas", *t.AreaID)
	}
	if t.HeadingID != nil {
		t.HeadingRef = r.getRef("headings", *t.HeadingID)
	}

	t.Tags, _ = r.getTaskTags(id)
	t.Checklist, _ = r.getChecklist(id)
	t.Attachments, _ = r.getAttachments(id)
	t.RepeatRule, _ = r.getRepeatRule(id)
	t.Schedules, _ = r.getSchedules(id)
	t.Reminders, _ = r.getReminders(id)

	return &t, nil
}

func (r *TaskRepository) Create(input model.CreateTaskInput) (*model.TaskDetail, error) {
	id := input.ID
	if id == "" {
		id = model.NewID()
	}

	var task *model.TaskDetail
	err := r.inTx(func(db DBTX) error {
		// inTx always passes the open *sql.Tx; br is this repository bound to
		// it, so nested helper methods and change-log writes join the
		// transaction instead of escaping to the pool.
		br := r.WithTx(db.(*sql.Tx))

		// D5: the area follows the project. A new task has no stored
		// project, so a non-nil project counts as changed; a heading sent
		// alongside it is the boundary check's to reject, not ours to clear.
		projectID, areaID, headingID := input.ProjectID, input.AreaID, input.HeadingID
		if err := normalizeParents(db, &projectID, &areaID, &headingID, input.ProjectID != nil, input.HeadingID != nil); err != nil {
			return err
		}

		var maxSort float64
		_ = db.QueryRow("SELECT COALESCE(MAX(sort_order_today), 0) FROM tasks").Scan(&maxSort)

		if _, err := db.Exec(`
			INSERT INTO tasks (id, title, notes, when_date, high_priority, deadline,
				project_id, area_id, heading_id, sort_order_today, sort_order_project, sort_order_heading)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			id, input.Title, input.Notes, input.WhenDate,
			boolToInt(input.HighPriority), input.Deadline, projectID, areaID, headingID,
			maxSort+1024, maxSort+1024, maxSort+1024,
		); err != nil {
			return fmt.Errorf("create task: %w", err)
		}

		if len(input.TagIDs) > 0 {
			if err := br.setTaskTags(id, input.TagIDs); err != nil {
				return fmt.Errorf("set task tags: %w", err)
			}
		}

		// Create first schedule entry if when_date is set
		if input.WhenDate != nil {
			if err := br.syncFirstScheduleDate(id, input.WhenDate); err != nil {
				return fmt.Errorf("sync first schedule date: %w", err)
			}
		}

		t, err := br.GetByID(id)
		if err != nil {
			return err
		}
		if t == nil {
			return fmt.Errorf("task %s not found after create", id)
		}
		task = t
		return logChange(br.changeLog, "task", id, "create", nil, t, "", "")
	})
	if err != nil {
		return nil, err
	}
	return task, nil
}

func (r *TaskRepository) Update(id string, input model.UpdateTaskInput) (*model.TaskDetail, error) {
	var task *model.TaskDetail
	err := r.inTx(func(db DBTX) error {
		// inTx always passes the open *sql.Tx; br is this repository bound to
		// it, so nested helper methods and change-log writes join the
		// transaction instead of escaping to the pool.
		br := r.WithTx(db.(*sql.Tx))

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
		if _, ok := input.Raw["when_date"]; ok {
			sets = append(sets, "when_date = ?")
			args = append(args, input.WhenDate)
		}
		if input.HighPriority != nil {
			sets = append(sets, "high_priority = ?")
			args = append(args, boolToInt(*input.HighPriority))
		}
		if _, ok := input.Raw["deadline"]; ok {
			sets = append(sets, "deadline = ?")
			args = append(args, input.Deadline)
		}

		// D5 container normalization, applied only when the request touches a
		// parent pointer: resolve the effective values, enforce the
		// invariant, then write the keys the client sent plus the values
		// normalization actually changed. Absent keys otherwise stay put.
		var extraFields []string
		_, projectGiven := input.Raw["project_id"]
		_, areaGiven := input.Raw["area_id"]
		_, headingGiven := input.Raw["heading_id"]
		if projectGiven || areaGiven || headingGiven {
			var storedProject, storedArea, storedHeading sql.NullString
			loadErr := db.QueryRow(
				"SELECT project_id, area_id, heading_id FROM tasks WHERE id = ?", id,
			).Scan(&storedProject, &storedArea, &storedHeading)
			if loadErr != nil && !errors.Is(loadErr, sql.ErrNoRows) {
				return fmt.Errorf("load task parents: %w", loadErr)
			}
			if loadErr == nil {
				oldProject, oldArea, oldHeading := nullStrPtr(storedProject), nullStrPtr(storedArea), nullStrPtr(storedHeading)
				projectID, areaID, headingID := oldProject, oldArea, oldHeading
				if projectGiven {
					projectID = input.ProjectID
				}
				if areaGiven {
					areaID = input.AreaID
				}
				if headingGiven {
					headingID = input.HeadingID
				}
				projectChanged := !sameID(projectID, oldProject)
				if err := normalizeParents(db, &projectID, &areaID, &headingID, projectChanged, headingGiven); err != nil {
					return err
				}
				if projectGiven || !sameID(projectID, oldProject) {
					sets = append(sets, "project_id = ?")
					args = append(args, projectID)
				}
				if areaGiven || !sameID(areaID, oldArea) {
					sets = append(sets, "area_id = ?")
					args = append(args, areaID)
					if !areaGiven {
						extraFields = append(extraFields, "area_id")
					}
				}
				if headingGiven || !sameID(headingID, oldHeading) {
					sets = append(sets, "heading_id = ?")
					args = append(args, headingID)
					if !headingGiven {
						extraFields = append(extraFields, "heading_id")
					}
				}
			}
		}

		// Always bump updated_at when there are field changes,
		// or when updated_at is explicitly requested (e.g. review task)
		_, hasUpdatedAt := input.Raw["updated_at"]
		if len(sets) > 0 || hasUpdatedAt {
			sets = append(sets, "updated_at = datetime('now')")
			args = append(args, id)
			if _, err := db.Exec(
				"UPDATE tasks SET "+strings.Join(sets, ", ")+" WHERE id = ?", args...); err != nil {
				return fmt.Errorf("update task: %w", err)
			}
		}

		if input.TagIDs != nil {
			if err := br.setTaskTags(id, input.TagIDs); err != nil {
				return fmt.Errorf("set task tags: %w", err)
			}
		}

		// Sync first schedule entry when when_date changes
		if _, ok := input.Raw["when_date"]; ok {
			if err := br.syncFirstScheduleDate(id, input.WhenDate); err != nil {
				return err
			}
		}

		t, err := br.GetByID(id)
		if err != nil {
			return err
		}
		if t == nil {
			return nil
		}
		var changedFields []string
		for k := range input.Raw {
			changedFields = append(changedFields, k)
		}
		changedFields = append(changedFields, extraFields...)
		if input.TagIDs != nil {
			changedFields = append(changedFields, "tag_ids")
		}
		task = t
		return logChange(br.changeLog, "task", id, "update", changedFields, t, "", "")
	})
	if err != nil {
		return nil, err
	}
	return task, nil
}

// Move relocates a task by exactly the keys present in input.Raw: absent
// container keys and an absent when_date keep their stored values. The
// resolved parents then go through the D5 normalization (area follows the
// project, a foreign heading is cleared unless the request names one), and
// a present when_date syncs the first schedule entry. Everything runs in
// one transaction; a body with no recognized keys writes nothing.
func (r *TaskRepository) Move(id string, input model.MoveTaskInput) (*model.TaskDetail, error) {
	var task *model.TaskDetail
	err := r.inTx(func(db DBTX) error {
		br := r.WithTx(db.(*sql.Tx))

		var storedProject, storedArea, storedHeading sql.NullString
		loadErr := db.QueryRow(
			"SELECT project_id, area_id, heading_id FROM tasks WHERE id = ?", id,
		).Scan(&storedProject, &storedArea, &storedHeading)
		if errors.Is(loadErr, sql.ErrNoRows) {
			return nil // unknown task: nothing to move, caller maps nil to 404
		}
		if loadErr != nil {
			return fmt.Errorf("move task: load: %w", loadErr)
		}

		oldProject, oldArea, oldHeading := nullStrPtr(storedProject), nullStrPtr(storedArea), nullStrPtr(storedHeading)
		projectID, areaID, headingID := oldProject, oldArea, oldHeading
		_, projectGiven := input.Raw["project_id"]
		_, areaGiven := input.Raw["area_id"]
		_, headingGiven := input.Raw["heading_id"]
		_, whenGiven := input.Raw["when_date"]
		if projectGiven {
			projectID = input.ProjectID
		}
		if areaGiven {
			areaID = input.AreaID
		}
		if headingGiven {
			headingID = input.HeadingID
		}

		projectChanged := !sameID(projectID, oldProject)
		if err := normalizeParents(db, &projectID, &areaID, &headingID, projectChanged, headingGiven); err != nil {
			return err
		}

		var sets []string
		var args []interface{}
		var fields []string
		if projectGiven || !sameID(projectID, oldProject) {
			sets = append(sets, "project_id = ?")
			args = append(args, projectID)
			fields = append(fields, "project_id")
		}
		if areaGiven || !sameID(areaID, oldArea) {
			sets = append(sets, "area_id = ?")
			args = append(args, areaID)
			fields = append(fields, "area_id")
		}
		if headingGiven || !sameID(headingID, oldHeading) {
			sets = append(sets, "heading_id = ?")
			args = append(args, headingID)
			fields = append(fields, "heading_id")
		}
		if whenGiven {
			sets = append(sets, "when_date = ?")
			args = append(args, input.WhenDate)
			fields = append(fields, "when_date")
		}

		if len(sets) > 0 {
			sets = append(sets, "updated_at = datetime('now')")
			args = append(args, id)
			if _, err := db.Exec("UPDATE tasks SET "+strings.Join(sets, ", ")+" WHERE id = ?", args...); err != nil {
				return fmt.Errorf("move task: %w", err)
			}
		}

		if whenGiven {
			if err := br.syncFirstScheduleDate(id, input.WhenDate); err != nil {
				return fmt.Errorf("sync first schedule date: %w", err)
			}
		}

		t, err := br.GetByID(id)
		if err != nil {
			return err
		}
		task = t
		if task != nil && len(fields) > 0 {
			return logChange(br.changeLog, "task", id, "update", fields, task, "", "")
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return task, nil
}

func (r *TaskRepository) Delete(id string) error {
	_, err := r.db.Exec("UPDATE tasks SET deleted_at = datetime('now'), updated_at = datetime('now') WHERE id = ?", id)
	if err == nil {
		// Log the full task snapshot so the pull client sees deleted_at and soft-deletes
		task, getErr := r.GetByID(id)
		if getErr == nil && task != nil {
			// Best-effort log: the delete already committed.
			_ = logChange(r.changeLog, "task", id, "delete", nil, task, "", "")
		} else {
			_ = logChange(r.changeLog, "task", id, "delete", nil, map[string]string{"id": id}, "", "")
		}
	}
	return err
}

func (r *TaskRepository) Restore(id string) (*model.TaskDetail, error) {
	_, err := r.db.Exec("UPDATE tasks SET deleted_at = NULL, updated_at = datetime('now') WHERE id = ?", id)
	if err != nil {
		return nil, err
	}
	task, err := r.GetByID(id)
	if err == nil && task != nil {
		// Best-effort log: the write already committed.
		_ = logChange(r.changeLog, "task", id, "update", []string{"deleted_at"}, task, "", "")
	}
	return task, err
}

func (r *TaskRepository) PermanentDelete(id string) error {
	_, err := r.db.Exec("DELETE FROM tasks WHERE id = ?", id)
	if err == nil {
		// Best-effort log: the write already committed.
		_ = logChange(r.changeLog, "task", id, "delete", nil, map[string]string{"id": id}, "", "")
	}
	return err
}

// SetDoneStatus finishes an open task in one guarded UPDATE: it only fires
// while status = 'open', so completing an already-finished (or unknown) task
// is a no-op. status must be one of completed, canceled, wont_do. changed
// reports whether the row was updated; the change is logged when it was.
func (r *TaskRepository) SetDoneStatus(id, status string) (changed bool, err error) {
	var sets string
	fields := []string{"status"}
	switch status {
	case "completed":
		sets = "status = 'completed', completed_at = datetime('now')"
		fields = append(fields, "completed_at")
	case "canceled":
		sets = "status = 'canceled', canceled_at = datetime('now')"
		fields = append(fields, "canceled_at")
	case "wont_do":
		sets = "status = 'wont_do'"
	default:
		return false, fmt.Errorf("set done status: invalid status %q", status)
	}

	res, err := r.db.Exec(
		"UPDATE tasks SET "+sets+", updated_at = datetime('now') WHERE id = ? AND status = 'open'", id)
	if err != nil {
		return false, fmt.Errorf("set done status: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("set done status: rows affected: %w", err)
	}
	if n == 0 {
		return false, nil
	}
	if task, err := r.GetByID(id); err == nil && task != nil {
		return true, logChange(r.changeLog, "task", id, "update", fields, task, "", "")
	}
	return true, nil
}

func (r *TaskRepository) Reopen(id string) (*model.TaskDetail, error) {
	_, err := r.db.Exec(
		"UPDATE tasks SET status = 'open', completed_at = NULL, canceled_at = NULL, updated_at = datetime('now') WHERE id = ?", id)
	if err != nil {
		return nil, err
	}
	task, err := r.GetByID(id)
	if err == nil && task != nil {
		// Best-effort log: the write already committed.
		_ = logChange(r.changeLog, "task", id, "update", []string{"status", "completed_at", "canceled_at"}, task, "", "")
	}
	return task, err
}

func (r *TaskRepository) MarkReviewed(id string) (*model.TaskDetail, error) {
	_, err := r.db.Exec("UPDATE tasks SET updated_at = datetime('now') WHERE id = ?", id)
	if err != nil {
		return nil, err
	}
	task, err := r.GetByID(id)
	if err == nil && task != nil {
		// Best-effort log: the write already committed.
		_ = logChange(r.changeLog, "task", id, "update", []string{"updated_at"}, task, "", "")
	}
	return task, err
}

func (r *TaskRepository) Reorder(items []model.ReorderItem) error {
	if err := r.inTx(func(db DBTX) error {
		for _, item := range items {
			field := item.SortField
			if field != "sort_order_today" && field != "sort_order_project" && field != "sort_order_heading" {
				return fmt.Errorf("invalid sort field: %s", field)
			}
			_, err := db.Exec("UPDATE tasks SET "+field+" = ? WHERE id = ?", item.SortOrder, item.ID)
			if err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		return err
	}

	for _, item := range items {
		task, err := r.GetByID(item.ID)
		if err == nil && task != nil {
			// Best-effort log: the reorder already committed.
			_ = logChange(r.changeLog, "task", item.ID, "update", []string{item.SortField}, task, "", "")
		}
	}

	return nil
}

func (r *TaskRepository) BulkAction(input model.BulkActionInput) (int, error) {
	affected := 0
	err := r.inTx(func(db DBTX) error {
		now := "datetime('now')"
		br := r.WithTx(db.(*sql.Tx))

		// exec runs one statement of a task's action and reports whether
		// it changed a row, so affected counts real transitions only.
		exec := func(query string, args ...interface{}) (bool, error) {
			res, err := db.Exec(query, args...)
			if err != nil {
				return false, err
			}
			n, err := res.RowsAffected()
			if err != nil {
				return false, err
			}
			return n > 0, nil
		}

		for _, id := range input.TaskIDs {
			var execErr error
			changed := false
			switch input.Action {
			case "delete":
				changed, execErr = exec(
					"UPDATE tasks SET deleted_at = "+now+", updated_at = "+now+" WHERE id = ? AND deleted_at IS NULL", id)
			case "set_when":
				whenDate, _ := input.Params["when_date"].(string)
				var wd *string
				if whenDate != "" {
					wd = &whenDate
				}
				changed, execErr = exec(
					"UPDATE tasks SET when_date = ?, updated_at = "+now+" WHERE id = ? AND deleted_at IS NULL",
					wd, id)
				if changed {
					// The first schedule entry follows the new date — and
					// all entries vanish on a clear — inside the same
					// transaction: a duplicate timeless date aborts the
					// whole bulk action and rolls everything back.
					if err := br.syncFirstScheduleDate(id, wd); err != nil {
						execErr = err
					}
				}
			case "set_deadline":
				deadline, _ := input.Params["deadline"].(string)
				changed, execErr = exec(
					"UPDATE tasks SET deadline = ?, updated_at = "+now+" WHERE id = ? AND deleted_at IS NULL",
					bulkNilIfEmpty(deadline), id)
			case "set_priority":
				// high_priority (bool) is the current param; the legacy
				// integer priority (1 = high) still works. When both are
				// sent, high_priority wins.
				p := 0
				if hp, ok := input.Params["high_priority"].(bool); ok {
					p = boolToInt(hp)
				} else if legacy, ok := input.Params["priority"].(float64); ok {
					p = int(legacy)
				}
				changed, execErr = exec(
					"UPDATE tasks SET high_priority = ?, updated_at = "+now+" WHERE id = ? AND deleted_at IS NULL", p, id)
			case "toggle_priority":
				changed, execErr = exec(
					"UPDATE tasks SET high_priority = CASE WHEN high_priority = 1 THEN 0 ELSE 1 END, updated_at = "+now+" WHERE id = ? AND deleted_at IS NULL", id)
			case "move_project":
				// The params state the target containers (empty = none, as
				// before); D5 normalization then derives the area from the
				// target project and clears a heading left behind by the
				// move. Unknown ids keep their old no-op behavior.
				projectIDParam, _ := input.Params["project_id"].(string)
				areaIDParam, _ := input.Params["area_id"].(string)
				var storedProject, storedArea, storedHeading sql.NullString
				loadErr := db.QueryRow(
					"SELECT project_id, area_id, heading_id FROM tasks WHERE id = ?", id,
				).Scan(&storedProject, &storedArea, &storedHeading)
				if loadErr != nil && !errors.Is(loadErr, sql.ErrNoRows) {
					execErr = fmt.Errorf("bulk move: load task %s: %w", id, loadErr)
				} else if loadErr == nil {
					var projectID, areaID *string
					if p := projectIDParam; p != "" {
						projectID = &p
					}
					if a := areaIDParam; a != "" {
						areaID = &a
					}
					headingID := nullStrPtr(storedHeading)
					projectChanged := !sameID(projectID, nullStrPtr(storedProject))
					if nErr := normalizeParents(db, &projectID, &areaID, &headingID, projectChanged, false); nErr != nil {
						execErr = nErr
					} else {
						changed, execErr = exec(
							"UPDATE tasks SET project_id = ?, area_id = ?, heading_id = ?, updated_at = "+now+" WHERE id = ? AND deleted_at IS NULL",
							projectID, areaID, headingID, id)
					}
				}
			case "add_tags", "remove_tags", "toggle_tags":
				tagIDs, ok := input.Params["tag_ids"].([]interface{})
				if ok {
					for _, rawTagID := range tagIDs {
						tagID, _ := rawTagID.(string)
						if tagID == "" {
							continue
						}
						var c bool
						switch input.Action {
						case "add_tags":
							c, execErr = exec("INSERT OR IGNORE INTO task_tags (task_id, tag_id) VALUES (?, ?)", id, tagID)
						case "remove_tags":
							c, execErr = exec("DELETE FROM task_tags WHERE task_id = ? AND tag_id = ?", id, tagID)
						default: // toggle_tags
							var exists int
							_ = db.QueryRow("SELECT COUNT(*) FROM task_tags WHERE task_id = ? AND tag_id = ?", id, tagID).Scan(&exists)
							if exists > 0 {
								c, execErr = exec("DELETE FROM task_tags WHERE task_id = ? AND tag_id = ?", id, tagID)
							} else {
								c, execErr = exec("INSERT OR IGNORE INTO task_tags (task_id, tag_id) VALUES (?, ?)", id, tagID)
							}
						}
						changed = changed || c
						if execErr != nil {
							break
						}
					}
				}
			case "mark_reviewed":
				changed, execErr = exec(
					"UPDATE tasks SET updated_at = "+now+" WHERE id = ? AND deleted_at IS NULL AND status = 'open'", id)
			default:
				return fmt.Errorf("unknown bulk action: %s", input.Action)
			}
			if execErr != nil {
				return execErr
			}
			if changed {
				affected++
			}
		}
		return nil
	})
	if err != nil {
		return 0, err
	}

	// Log changes for each affected task
	for _, id := range input.TaskIDs {
		task, err := r.GetByID(id)
		if err == nil && task != nil {
			// Best-effort log: the bulk transaction already committed.
			_ = logChange(r.changeLog, "task", id, "update", nil, task, "", "")
		}
	}

	return affected, nil
}

// BackfillAreasFromProjects repairs rows written before a task's area
// followed its project (D5): every task whose area_id differs from its
// project's area — trashed rows included — is updated to it, with one
// change-log entry per row, all in one transaction. It returns the number
// of rows changed; a second run over consistent data changes nothing.
func (r *TaskRepository) BackfillAreasFromProjects() (changed int, err error) {
	err = r.inTx(func(db DBTX) error {
		rows, err := db.Query(`
			SELECT t.id FROM tasks t
			JOIN projects p ON p.id = t.project_id
			WHERE t.area_id IS NOT p.area_id
			ORDER BY t.id`)
		if err != nil {
			return fmt.Errorf("backfill areas: select: %w", err)
		}
		var ids []string
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				_ = rows.Close()
				return fmt.Errorf("backfill areas: scan: %w", err)
			}
			ids = append(ids, id)
		}
		if err := rows.Close(); err != nil {
			return err
		}
		if err := rows.Err(); err != nil {
			return err
		}

		br := r.WithTx(db.(*sql.Tx))
		for _, id := range ids {
			var areaID sql.NullString
			if err := db.QueryRow(
				"SELECT p.area_id FROM tasks t JOIN projects p ON p.id = t.project_id WHERE t.id = ?", id,
			).Scan(&areaID); err != nil {
				return fmt.Errorf("backfill areas: load project area for %s: %w", id, err)
			}
			if _, err := db.Exec(
				"UPDATE tasks SET area_id = ?, updated_at = datetime('now') WHERE id = ?", areaID, id,
			); err != nil {
				return fmt.Errorf("backfill areas: update %s: %w", id, err)
			}
			task, err := br.GetByID(id)
			if err != nil {
				return err
			}
			if err := logChange(br.changeLog, "task", id, "update", []string{"area_id"}, task, "", ""); err != nil {
				return fmt.Errorf("backfill areas: log %s: %w", id, err)
			}
			changed++
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	return changed, nil
}

func bulkNilIfEmpty(s string) interface{} {
	if s == "" {
		return nil
	}
	return s
}

// --- helpers ---

func (r *TaskRepository) getTaskTags(taskID string) ([]model.TagRef, error) {
	rows, err := r.db.Query(
		"SELECT t.id, t.title, t.color FROM tags t JOIN task_tags tt ON t.id = tt.tag_id WHERE tt.task_id = ? ORDER BY t.sort_order", taskID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var tags []model.TagRef
	for rows.Next() {
		var t model.TagRef
		_ = rows.Scan(&t.ID, &t.Title, &t.Color)
		tags = append(tags, t)
	}
	if tags == nil {
		tags = []model.TagRef{}
	}
	return tags, nil
}

func (r *TaskRepository) setTaskTags(taskID string, tagIDs []string) error {
	return r.inTx(func(db DBTX) error {
		if _, err := db.Exec("DELETE FROM task_tags WHERE task_id = ?", taskID); err != nil {
			return fmt.Errorf("delete task tags: %w", err)
		}
		for _, tagID := range tagIDs {
			if _, err := db.Exec("INSERT OR IGNORE INTO task_tags (task_id, tag_id) VALUES (?, ?)", taskID, tagID); err != nil {
				return fmt.Errorf("insert task tag: %w", err)
			}
		}
		return nil
	})
}

func (r *TaskRepository) getChecklist(taskID string) ([]model.ChecklistItem, error) {
	rows, err := r.db.Query(
		"SELECT id, title, completed, sort_order FROM checklist_items WHERE task_id = ? ORDER BY sort_order", taskID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var items []model.ChecklistItem
	for rows.Next() {
		var c model.ChecklistItem
		var completed int
		_ = rows.Scan(&c.ID, &c.Title, &completed, &c.SortOrder)
		c.Completed = completed == 1
		items = append(items, c)
	}
	if items == nil {
		items = []model.ChecklistItem{}
	}
	return items, nil
}

func (r *TaskRepository) getReminders(taskID string) ([]model.Reminder, error) {
	rows, err := r.db.Query(
		"SELECT id, type, value, exact_at, created_at FROM reminders WHERE task_id = ? ORDER BY created_at", taskID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var items []model.Reminder
	for rows.Next() {
		var rm model.Reminder
		_ = rows.Scan(&rm.ID, &rm.Type, &rm.Value, &rm.ExactAt, &rm.CreatedAt)
		items = append(items, rm)
	}
	if items == nil {
		items = []model.Reminder{}
	}
	return items, nil
}

func (r *TaskRepository) getAttachments(taskID string) ([]model.Attachment, error) {
	rows, err := r.db.Query(
		"SELECT id, type, title, url, mime_type, file_size, sort_order, created_at FROM attachments WHERE task_id = ? ORDER BY sort_order", taskID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var items []model.Attachment
	for rows.Next() {
		var a model.Attachment
		_ = rows.Scan(&a.ID, &a.Type, &a.Title, &a.URL, &a.MimeType, &a.FileSize, &a.SortOrder, &a.CreatedAt)
		items = append(items, a)
	}
	if items == nil {
		items = []model.Attachment{}
	}
	return items, nil
}

func (r *TaskRepository) getRepeatRule(taskID string) (*model.RepeatRule, error) {
	var rr model.RepeatRule
	var patternJSON string
	err := r.db.QueryRow(
		"SELECT id, pattern FROM repeat_rules WHERE task_id = ?", taskID,
	).Scan(&rr.ID, &patternJSON)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if patternJSON != "" {
		if err := json.Unmarshal([]byte(patternJSON), &rr.Pattern); err != nil {
			return nil, fmt.Errorf("unmarshal repeat pattern: %w", err)
		}
	}
	// Populate deprecated flat fields for backwards compat
	rr.Mode = string(rr.Pattern.Mode)
	rr.IntervalValue = rr.Pattern.Every
	rr.Frequency = patternToFrequency(rr.Pattern.Type)
	if rr.Pattern.Type == model.PatternWeekly {
		rr.DayConstraints = rr.Pattern.On
	}
	if rr.DayConstraints == nil {
		rr.DayConstraints = []string{}
	}
	return &rr, nil
}

// syncFirstScheduleDate keeps the first task_schedules entry in sync with the task's when_date.
// If when_date is non-null and no schedule entry exists, creates one.
// If when_date is non-null and a schedule entry exists, updates its when_date.
// If when_date is null, deletes all schedule entries.
// Returns an error if the update would create a duplicate timeless date.
func (r *TaskRepository) syncFirstScheduleDate(taskID string, whenDate *string) error {
	if whenDate == nil {
		// Log deletes for each schedule entry before removing them
		rows, _ := r.db.Query("SELECT id FROM task_schedules WHERE task_id = ?", taskID)
		if rows != nil {
			defer func() { _ = rows.Close() }()
			for rows.Next() {
				var sid string
				_ = rows.Scan(&sid)
				if err := logChange(r.changeLog, "schedule", sid, "delete", nil, map[string]string{"id": sid}, "", ""); err != nil {
					return fmt.Errorf("log schedule delete: %w", err)
				}
			}
		}
		// Clear all schedule entries
		_, err := r.db.Exec("DELETE FROM task_schedules WHERE task_id = ?", taskID)
		return err
	}
	// Check if first entry exists
	var existingID string
	var startTime sql.NullString
	err := r.db.QueryRow(
		"SELECT id, start_time FROM task_schedules WHERE task_id = ? ORDER BY sort_order ASC LIMIT 1", taskID,
	).Scan(&existingID, &startTime)
	if err == sql.ErrNoRows {
		// Create first entry
		id := model.NewID()
		_, err = r.db.Exec(
			"INSERT INTO task_schedules (id, task_id, when_date, sort_order) VALUES (?, ?, ?, 0)",
			id, taskID, *whenDate)
		if err == nil {
			// Log the new schedule entry
			var s model.TaskSchedule
			_ = r.db.QueryRow("SELECT id, task_id, when_date, start_time, end_time, completed, sort_order FROM task_schedules WHERE id = ?", id).
				Scan(&s.ID, &s.TaskID, &s.WhenDate, &s.StartTime, &s.EndTime, &s.Completed, &s.SortOrder)
			if logErr := logChange(r.changeLog, "schedule", id, "create", nil, &s, "", ""); logErr != nil {
				return fmt.Errorf("log schedule create: %w", logErr)
			}
		}
		return err
	} else if err != nil {
		return err
	}
	// Block duplicate timeless dates
	if !startTime.Valid && *whenDate != "someday" {
		var count int
		_ = r.db.QueryRow(
			"SELECT COUNT(*) FROM task_schedules WHERE task_id = ? AND when_date = ? AND start_time IS NULL AND id != ?",
			taskID, *whenDate, existingID).Scan(&count)
		if count > 0 {
			return fmt.Errorf("duplicate timeless date")
		}
	}
	// Update existing first entry's date
	_, err = r.db.Exec("UPDATE task_schedules SET when_date = ? WHERE id = ?", *whenDate, existingID)
	if err == nil {
		// Log the schedule update
		var s model.TaskSchedule
		_ = r.db.QueryRow("SELECT id, task_id, when_date, start_time, end_time, completed, sort_order FROM task_schedules WHERE id = ?", existingID).
			Scan(&s.ID, &s.TaskID, &s.WhenDate, &s.StartTime, &s.EndTime, &s.Completed, &s.SortOrder)
		if logErr := logChange(r.changeLog, "schedule", existingID, "update", []string{"when_date"}, &s, "", ""); logErr != nil {
			return fmt.Errorf("log schedule update: %w", logErr)
		}
	}
	return err
}

func (r *TaskRepository) getSchedules(taskID string) ([]model.TaskSchedule, error) {
	rows, err := r.db.Query(
		"SELECT id, when_date, start_time, end_time, completed, sort_order FROM task_schedules WHERE task_id = ? ORDER BY sort_order", taskID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var items []model.TaskSchedule
	for rows.Next() {
		var s model.TaskSchedule
		_ = rows.Scan(&s.ID, &s.WhenDate, &s.StartTime, &s.EndTime, &s.Completed, &s.SortOrder)
		items = append(items, s)
	}
	if items == nil {
		items = []model.TaskSchedule{}
	}
	return items, nil
}

func patternToFrequency(pt model.PatternType) string {
	switch pt {
	case model.PatternDaily, model.PatternDailyWeekday, model.PatternDailyWeekend:
		return "daily"
	case model.PatternWeekly:
		return "weekly"
	case model.PatternMonthlyDOM, model.PatternMonthlyDOW, model.PatternMonthlyWorkday:
		return "monthly"
	case model.PatternYearlyDate, model.PatternYearlyDOW:
		return "yearly"
	default:
		return "daily"
	}
}

func (r *TaskRepository) getRef(table, id string) *model.Ref {
	if !validRefTables[table] {
		return nil
	}
	var ref model.Ref
	err := r.db.QueryRow("SELECT id, title FROM "+table+" WHERE id = ?", id).Scan(&ref.ID, &ref.Title)
	if err != nil {
		return nil
	}
	return &ref
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
