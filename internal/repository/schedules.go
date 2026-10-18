package repository

import (
	"database/sql"
	"errors"
	"fmt"

	"github.com/collinjanssen/thingstodo/internal/model"
)

type ScheduleRepository struct {
	db        DBTX
	conn      *sql.DB
	tx        *sql.Tx
	changeLog *ChangeLogRepository
	tasks     *TaskRepository
}

func NewScheduleRepository(db *sql.DB, changeLog *ChangeLogRepository) *ScheduleRepository {
	return &ScheduleRepository{db: db, conn: db, changeLog: changeLog}
}

// WithTasks returns a copy of the repository that can load full task
// snapshots for the change entries SyncPrimary logs. The router chains it
// after construction; without it SyncPrimary still rewrites when_date but
// leaves no task entry in the change log.
func (r *ScheduleRepository) WithTasks(tasks *TaskRepository) *ScheduleRepository {
	cp := *r
	cp.tasks = tasks
	return &cp
}

// bound returns a repository copy whose statements and change-log writes go
// through db, the transaction handle of an inTx callback.
func (r *ScheduleRepository) bound(db DBTX) *ScheduleRepository {
	if tx, ok := db.(*sql.Tx); ok {
		return r.WithTx(tx)
	}
	return r
}

// WithTx returns a shallow copy of the repository bound to tx: its statements
// and nested inTx calls reuse tx instead of opening new transactions. It is
// nil-safe for the change log.
func (r *ScheduleRepository) WithTx(tx *sql.Tx) *ScheduleRepository {
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
func (r *ScheduleRepository) inTx(fn func(db DBTX) error) error {
	if r.tx != nil {
		return fn(r.tx)
	}
	return InTx(r.conn, func(tx *sql.Tx) error { return fn(tx) })
}

func (r *ScheduleRepository) ListByTask(taskID string) ([]model.TaskSchedule, error) {
	rows, err := r.db.Query(
		"SELECT id, task_id, when_date, start_time, end_time, completed, sort_order FROM task_schedules WHERE task_id = ? ORDER BY sort_order", taskID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var items []model.TaskSchedule
	for rows.Next() {
		var s model.TaskSchedule
		if err := rows.Scan(&s.ID, &s.TaskID, &s.WhenDate, &s.StartTime, &s.EndTime, &s.Completed, &s.SortOrder); err != nil {
			return nil, fmt.Errorf("scan schedule: %w", err)
		}
		items = append(items, s)
	}
	if items == nil {
		items = []model.TaskSchedule{}
	}
	return items, rows.Err()
}

// ListAll returns all task schedules across all tasks.
func (r *ScheduleRepository) ListAll() ([]model.TaskSchedule, error) {
	rows, err := r.db.Query(
		"SELECT id, task_id, when_date, start_time, end_time, completed, sort_order FROM task_schedules ORDER BY sort_order")
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var items []model.TaskSchedule
	for rows.Next() {
		var s model.TaskSchedule
		if err := rows.Scan(&s.ID, &s.TaskID, &s.WhenDate, &s.StartTime, &s.EndTime, &s.Completed, &s.SortOrder); err != nil {
			return nil, fmt.Errorf("scan schedule: %w", err)
		}
		items = append(items, s)
	}
	if items == nil {
		items = []model.TaskSchedule{}
	}
	return items, rows.Err()
}

// GetByID returns the schedule entry with the given id, or nil when no
// entry has it.
func (r *ScheduleRepository) GetByID(id string) (*model.TaskSchedule, error) {
	var s model.TaskSchedule
	err := r.db.QueryRow(
		"SELECT id, task_id, when_date, start_time, end_time, completed, sort_order FROM task_schedules WHERE id = ?", id).
		Scan(&s.ID, &s.TaskID, &s.WhenDate, &s.StartTime, &s.EndTime, &s.Completed, &s.SortOrder)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get schedule: %w", err)
	}
	return &s, nil
}

// Create inserts a new schedule entry and re-syncs tasks.when_date from
// the first entry, all in one transaction: a failed sync rolls the insert
// back with it.
func (r *ScheduleRepository) Create(taskID string, input model.CreateTaskScheduleInput) (*model.TaskSchedule, error) {
	var item *model.TaskSchedule
	if err := r.inTx(func(db DBTX) error {
		tr := r.bound(db)
		var err error
		item, err = tr.create(taskID, input)
		if err != nil {
			return err
		}
		return tr.SyncPrimary(taskID)
	}); err != nil {
		return nil, err
	}
	return item, nil
}

func (r *ScheduleRepository) create(taskID string, input model.CreateTaskScheduleInput) (*model.TaskSchedule, error) {
	id := input.ID
	if id == "" {
		id = model.NewID()
	}
	var maxSort float64
	_ = r.db.QueryRow("SELECT COALESCE(MAX(sort_order), 0) FROM task_schedules WHERE task_id = ?", taskID).Scan(&maxSort)

	_, err := r.db.Exec(
		"INSERT INTO task_schedules (id, task_id, when_date, start_time, end_time, sort_order) VALUES (?, ?, ?, ?, ?, ?)",
		id, taskID, input.WhenDate, input.StartTime, input.EndTime, maxSort+1024)
	if err != nil {
		return nil, fmt.Errorf("create schedule: %w", err)
	}

	var s model.TaskSchedule
	_ = r.db.QueryRow("SELECT id, task_id, when_date, start_time, end_time, completed, sort_order FROM task_schedules WHERE id = ?", id).
		Scan(&s.ID, &s.TaskID, &s.WhenDate, &s.StartTime, &s.EndTime, &s.Completed, &s.SortOrder)
	if err := logChange(r.changeLog, "schedule", id, "create", nil, &s, "", ""); err != nil {
		return nil, fmt.Errorf("log schedule create: %w", err)
	}
	return &s, nil
}

// Update applies the patch and re-syncs tasks.when_date from the first
// entry in the same transaction. It returns (nil, nil) when the entry does
// not exist, like before.
func (r *ScheduleRepository) Update(id string, input model.UpdateTaskScheduleInput) (*model.TaskSchedule, error) {
	var item *model.TaskSchedule
	if err := r.inTx(func(db DBTX) error {
		tr := r.bound(db)
		var err error
		item, err = tr.update(id, input)
		if err != nil || item == nil {
			return err
		}
		return tr.SyncPrimary(item.TaskID)
	}); err != nil {
		return nil, err
	}
	return item, nil
}

func (r *ScheduleRepository) update(id string, input model.UpdateTaskScheduleInput) (*model.TaskSchedule, error) {
	if input.WhenDate != nil {
		if _, err := r.db.Exec("UPDATE task_schedules SET when_date = ? WHERE id = ?", *input.WhenDate, id); err != nil {
			return nil, fmt.Errorf("update schedule when_date: %w", err)
		}
	}
	if _, ok := input.Raw["start_time"]; ok {
		if _, err := r.db.Exec("UPDATE task_schedules SET start_time = ? WHERE id = ?", input.StartTime, id); err != nil {
			return nil, fmt.Errorf("update schedule start_time: %w", err)
		}
	}
	if _, ok := input.Raw["end_time"]; ok {
		if _, err := r.db.Exec("UPDATE task_schedules SET end_time = ? WHERE id = ?", input.EndTime, id); err != nil {
			return nil, fmt.Errorf("update schedule end_time: %w", err)
		}
	}
	if input.SortOrder != nil {
		if _, err := r.db.Exec("UPDATE task_schedules SET sort_order = ? WHERE id = ?", *input.SortOrder, id); err != nil {
			return nil, fmt.Errorf("update schedule sort_order: %w", err)
		}
	}
	if input.Completed != nil {
		v := 0
		if *input.Completed {
			v = 1
		}
		if _, err := r.db.Exec("UPDATE task_schedules SET completed = ? WHERE id = ?", v, id); err != nil {
			return nil, fmt.Errorf("update schedule completed: %w", err)
		}
	}

	var s model.TaskSchedule
	err := r.db.QueryRow("SELECT id, task_id, when_date, start_time, end_time, completed, sort_order FROM task_schedules WHERE id = ?", id).
		Scan(&s.ID, &s.TaskID, &s.WhenDate, &s.StartTime, &s.EndTime, &s.Completed, &s.SortOrder)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err == nil {
		if logErr := logChange(r.changeLog, "schedule", id, "update", nil, &s, "", ""); logErr != nil {
			return nil, fmt.Errorf("log schedule update: %w", logErr)
		}
	}
	return &s, err
}

// Delete removes the entry and re-syncs tasks.when_date of the task that
// owned it in the same transaction.
func (r *ScheduleRepository) Delete(id string) error {
	return r.inTx(func(db DBTX) error {
		tr := r.bound(db)
		var taskID sql.NullString
		err := db.QueryRow("SELECT task_id FROM task_schedules WHERE id = ?", id).Scan(&taskID)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if _, err := db.Exec("DELETE FROM task_schedules WHERE id = ?", id); err != nil {
			return err
		}
		if err := logChange(tr.changeLog, "schedule", id, "delete", nil, map[string]string{"id": id}, "", ""); err != nil {
			return fmt.Errorf("log schedule delete: %w", err)
		}
		if !taskID.Valid {
			return nil
		}
		return tr.SyncPrimary(taskID.String)
	})
}

// Reorder rewrites sort_order values, logs every moved entry and re-syncs
// tasks.when_date from the new first entry, all in one transaction.
func (r *ScheduleRepository) Reorder(items []model.SimpleReorderItem) error {
	if len(items) == 0 {
		return nil
	}
	return r.inTx(func(db DBTX) error {
		tr := r.bound(db)
		for _, item := range items {
			if _, err := db.Exec("UPDATE task_schedules SET sort_order = ? WHERE id = ?", item.SortOrder, item.ID); err != nil {
				return err
			}
		}
		for _, item := range items {
			var s model.TaskSchedule
			_ = db.QueryRow("SELECT id, task_id, when_date, start_time, end_time, completed, sort_order FROM task_schedules WHERE id = ?", item.ID).
				Scan(&s.ID, &s.TaskID, &s.WhenDate, &s.StartTime, &s.EndTime, &s.Completed, &s.SortOrder)
			if err := logChange(tr.changeLog, "schedule", item.ID, "update", []string{"sort_order"}, &s, "", ""); err != nil {
				return err
			}
		}
		var taskID string
		if err := db.QueryRow("SELECT task_id FROM task_schedules WHERE id = ?", items[0].ID).Scan(&taskID); err != nil {
			return err
		}
		return tr.SyncPrimary(taskID)
	})
}

// GetTaskIDForSchedule returns the task_id owning the given schedule entry.
func (r *ScheduleRepository) GetTaskIDForSchedule(scheduleID string) (string, error) {
	var taskID string
	err := r.db.QueryRow("SELECT task_id FROM task_schedules WHERE id = ?", scheduleID).Scan(&taskID)
	if err != nil {
		return "", err
	}
	return taskID, nil
}

// CountByTask returns the number of schedule entries for a task.
func (r *ScheduleRepository) CountByTask(taskID string) (int, error) {
	var count int
	err := r.db.QueryRow("SELECT COUNT(*) FROM task_schedules WHERE task_id = ?", taskID).Scan(&count)
	return count, err
}

// SyncPrimary rewrites tasks.when_date from the first schedule entry and
// logs a "task" change when the value actually changes, so pull clients see
// the rewrite. The change entry carries the same full task snapshot the
// TaskRepository logs, loaded through the TaskRepository wired by WithTasks
// (nil in unit setups: the rewrite still happens, just without a log entry).
// It must run inside the same transaction as the schedule write that moved
// the first entry.
func (r *ScheduleRepository) SyncPrimary(taskID string) error {
	var whenDate sql.NullString
	err := r.db.QueryRow(
		"SELECT when_date FROM task_schedules WHERE task_id = ? ORDER BY sort_order ASC LIMIT 1",
		taskID).Scan(&whenDate)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("sync primary: %w", err)
	}

	var current sql.NullString
	err = r.db.QueryRow("SELECT when_date FROM tasks WHERE id = ?", taskID).Scan(&current)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil // unknown task: nothing to sync
		}
		return fmt.Errorf("sync primary: %w", err)
	}

	if current.Valid == whenDate.Valid && (!current.Valid || current.String == whenDate.String) {
		return nil // no rewrite, no change entry
	}

	var newDate interface{}
	if whenDate.Valid {
		newDate = whenDate.String
	}
	if _, err := r.db.Exec(
		"UPDATE tasks SET when_date = ?, updated_at = datetime('now') WHERE id = ?",
		newDate, taskID); err != nil {
		return err
	}

	if r.tasks == nil {
		// No task repository wired (unit setups): rewrite when_date but
		// leave no entry — a partial snapshot would degrade pull clients,
		// which replace whole records from snapshots.
		return nil
	}
	tasks := r.tasks
	if tx, ok := r.db.(*sql.Tx); ok {
		tasks = r.tasks.WithTx(tx)
	}
	detail, err := tasks.GetByID(taskID)
	if err != nil {
		return fmt.Errorf("sync primary: load task snapshot: %w", err)
	}
	if detail == nil {
		return nil
	}
	return logChange(r.changeLog, "task", taskID, "update", []string{"when_date"}, detail, "", "")
}

// GetScheduleFields reads the current when_date and start_time for a schedule entry.
func (r *ScheduleRepository) GetScheduleFields(id string, whenDate *string, startTime **string) error {
	var wd string
	var st sql.NullString
	err := r.db.QueryRow("SELECT when_date, start_time FROM task_schedules WHERE id = ?", id).Scan(&wd, &st)
	if err != nil {
		return err
	}
	*whenDate = wd
	if st.Valid {
		s := st.String
		*startTime = &s
	} else {
		*startTime = nil
	}
	return nil
}

// HasDuplicateTimelessDate checks whether another timeless entry with the same date exists for this task.
// excludeID is the schedule entry being changed (pass "" for new entries).
func (r *ScheduleRepository) HasDuplicateTimelessDate(taskID, whenDate, excludeID string) (bool, error) {
	if whenDate == "someday" {
		return false, nil
	}
	var count int
	err := r.db.QueryRow(
		"SELECT COUNT(*) FROM task_schedules WHERE task_id = ? AND when_date = ? AND start_time IS NULL AND id != ?",
		taskID, whenDate, excludeID).Scan(&count)
	return count > 0, err
}

// CreateFirstEntry creates a single schedule entry for a task (used by scheduler for repeat instances).
func (r *ScheduleRepository) CreateFirstEntry(taskID, date string) error {
	id := model.NewID()
	_, err := r.db.Exec(
		"INSERT INTO task_schedules (id, task_id, when_date, sort_order) VALUES (?, ?, ?, 0)",
		id, taskID, date)
	if err == nil {
		var s model.TaskSchedule
		_ = r.db.QueryRow("SELECT id, task_id, when_date, start_time, end_time, completed, sort_order FROM task_schedules WHERE id = ?", id).
			Scan(&s.ID, &s.TaskID, &s.WhenDate, &s.StartTime, &s.EndTime, &s.Completed, &s.SortOrder)
		// Best-effort log: the write already committed.
		_ = logChange(r.changeLog, "schedule", id, "create", nil, &s, "", "")
	}
	return err
}

// CleanupOnTaskDone completes past and today's uncompleted schedule entries
// and deletes future + someday uncompleted entries for a task. Called when a
// task is completed, canceled, or marked won't do.
func (r *ScheduleRepository) CleanupOnTaskDone(taskID, today string) error {
	var deletedIDs []string
	var flippedIDs []string
	if err := r.inTx(func(db DBTX) error {
		// Collect the ids of past/today entries this run flips to completed
		// before the update clears the completed = 0 flag that selects them.
		rows, err := db.Query(
			"SELECT id FROM task_schedules WHERE task_id = ? AND when_date <= ? AND when_date != 'someday' AND completed = 0",
			taskID, today,
		)
		if err != nil {
			return fmt.Errorf("query flipped schedule ids: %w", err)
		}
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				_ = rows.Close()
				return fmt.Errorf("scan flipped schedule id: %w", err)
			}
			flippedIDs = append(flippedIDs, id)
		}
		_ = rows.Close()

		// Mark past and today's uncompleted entries as completed
		if _, err := db.Exec(
			"UPDATE task_schedules SET completed = 1 WHERE task_id = ? AND when_date <= ? AND when_date != 'someday' AND completed = 0",
			taskID, today,
		); err != nil {
			return fmt.Errorf("complete past/today schedules: %w", err)
		}

		// Collect IDs of future/someday entries before deleting them (for change log)
		rows, err = db.Query(
			"SELECT id FROM task_schedules WHERE task_id = ? AND (when_date > ? OR when_date = 'someday') AND completed = 0",
			taskID, today,
		)
		if err != nil {
			return fmt.Errorf("query future schedule ids: %w", err)
		}
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				_ = rows.Close()
				return fmt.Errorf("scan future schedule id: %w", err)
			}
			deletedIDs = append(deletedIDs, id)
		}
		_ = rows.Close()

		// Delete future (after today) and someday uncompleted entries
		if _, err := db.Exec(
			"DELETE FROM task_schedules WHERE task_id = ? AND (when_date > ? OR when_date = 'someday') AND completed = 0",
			taskID, today,
		); err != nil {
			return fmt.Errorf("delete future schedules: %w", err)
		}
		return nil
	}); err != nil {
		return err
	}

	// Log only the entries this run flipped: rows completed before this
	// call are unchanged and must not produce redundant change-log entries.
	for _, id := range flippedIDs {
		var s model.TaskSchedule
		err := r.db.QueryRow(
			"SELECT id, task_id, when_date, start_time, end_time, completed, sort_order FROM task_schedules WHERE id = ?", id,
		).Scan(&s.ID, &s.TaskID, &s.WhenDate, &s.StartTime, &s.EndTime, &s.Completed, &s.SortOrder)
		if err != nil {
			continue
		}
		// Best-effort log: the transaction already committed.
		_ = logChange(r.changeLog, "schedule", id, "update", []string{"completed"}, &s, "", "")
	}

	// Log deleted entries
	for _, id := range deletedIDs {
		// Best-effort log: the transaction already committed.
		_ = logChange(r.changeLog, "schedule", id, "delete", nil, map[string]string{"id": id}, "", "")
	}

	return nil
}

// SyncFirstScheduleDate updates the first schedule entry's when_date to match tasks.when_date.
// This is called when when_date is updated via PATCH /tasks/{id}.
func (r *ScheduleRepository) SyncFirstScheduleDate(taskID string, date *string) error {
	if date == nil {
		// when_date set to NULL — delete all schedules
		_, err := r.db.Exec("DELETE FROM task_schedules WHERE task_id = ?", taskID)
		return err
	}
	// Check if there's a first schedule entry
	var schedID string
	var startTime sql.NullString
	err := r.db.QueryRow(
		"SELECT id, start_time FROM task_schedules WHERE task_id = ? ORDER BY sort_order ASC LIMIT 1",
		taskID).Scan(&schedID, &startTime)
	if err == sql.ErrNoRows {
		// Create one
		return r.CreateFirstEntry(taskID, *date)
	}
	if err != nil {
		return err
	}
	// Block duplicate timeless dates
	if !startTime.Valid && *date != "someday" {
		dup, err := r.HasDuplicateTimelessDate(taskID, *date, schedID)
		if err != nil {
			return err
		}
		if dup {
			return fmt.Errorf("duplicate timeless date")
		}
	}
	// Update existing first entry
	_, err = r.db.Exec("UPDATE task_schedules SET when_date = ? WHERE id = ?", *date, schedID)
	return err
}
