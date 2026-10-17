package repository

import (
	"database/sql"
	"fmt"

	"github.com/collinjanssen/thingstodo/internal/model"
)

type ChecklistRepository struct {
	db        DBTX
	conn      *sql.DB
	tx        *sql.Tx
	changeLog *ChangeLogRepository
}

func NewChecklistRepository(db *sql.DB, changeLog *ChangeLogRepository) *ChecklistRepository {
	return &ChecklistRepository{db: db, conn: db, changeLog: changeLog}
}

// WithTx returns a shallow copy of the repository bound to tx: its statements
// and nested inTx calls reuse tx instead of opening new transactions. It is
// nil-safe for the change log.
func (r *ChecklistRepository) WithTx(tx *sql.Tx) *ChecklistRepository {
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
func (r *ChecklistRepository) inTx(fn func(db DBTX) error) error {
	if r.tx != nil {
		return fn(r.tx)
	}
	return InTx(r.conn, func(tx *sql.Tx) error { return fn(tx) })
}

func (r *ChecklistRepository) ListByTask(taskID string) ([]model.ChecklistItem, error) {
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
		if err := rows.Scan(&c.ID, &c.Title, &completed, &c.SortOrder); err != nil {
			return nil, fmt.Errorf("scan checklist item: %w", err)
		}
		c.Completed = completed == 1
		items = append(items, c)
	}
	if items == nil {
		items = []model.ChecklistItem{}
	}
	return items, rows.Err()
}

// ListAll returns all checklist items across all tasks.
func (r *ChecklistRepository) ListAll() ([]model.ChecklistItem, error) {
	rows, err := r.db.Query(
		"SELECT id, task_id, title, completed, sort_order FROM checklist_items ORDER BY sort_order")
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var items []model.ChecklistItem
	for rows.Next() {
		var c model.ChecklistItem
		var completed int
		if err := rows.Scan(&c.ID, &c.TaskID, &c.Title, &completed, &c.SortOrder); err != nil {
			return nil, fmt.Errorf("scan checklist item: %w", err)
		}
		c.Completed = completed == 1
		items = append(items, c)
	}
	if items == nil {
		items = []model.ChecklistItem{}
	}
	return items, rows.Err()
}

func (r *ChecklistRepository) Create(taskID string, input model.CreateChecklistInput) (*model.ChecklistItem, error) {
	id := input.ID
	if id == "" {
		id = model.NewID()
	}
	var maxSort float64
	_ = r.db.QueryRow("SELECT COALESCE(MAX(sort_order), 0) FROM checklist_items WHERE task_id = ?", taskID).Scan(&maxSort)

	_, err := r.db.Exec("INSERT INTO checklist_items (id, task_id, title, sort_order) VALUES (?, ?, ?, ?)",
		id, taskID, input.Title, maxSort+1024)
	if err != nil {
		return nil, fmt.Errorf("create checklist item: %w", err)
	}

	var c model.ChecklistItem
	var completed int
	_ = r.db.QueryRow("SELECT id, task_id, title, completed, sort_order FROM checklist_items WHERE id = ?", id).
		Scan(&c.ID, &c.TaskID, &c.Title, &completed, &c.SortOrder)
	c.Completed = completed == 1
	logChange(r.changeLog, "checklist_item", id, "create", nil, &c, "", "")
	return &c, nil
}

func (r *ChecklistRepository) Update(id string, input model.UpdateChecklistInput) (*model.ChecklistItem, error) {
	if input.Title != nil {
		_, _ = r.db.Exec("UPDATE checklist_items SET title = ? WHERE id = ?", *input.Title, id)
	}
	if input.Completed != nil {
		_, _ = r.db.Exec("UPDATE checklist_items SET completed = ? WHERE id = ?", boolToInt(*input.Completed), id)
	}
	if input.SortOrder != nil {
		_, _ = r.db.Exec("UPDATE checklist_items SET sort_order = ? WHERE id = ?", *input.SortOrder, id)
	}

	var c model.ChecklistItem
	var completed int
	err := r.db.QueryRow("SELECT id, task_id, title, completed, sort_order FROM checklist_items WHERE id = ?", id).
		Scan(&c.ID, &c.TaskID, &c.Title, &completed, &c.SortOrder)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	c.Completed = completed == 1
	if err == nil {
		logChange(r.changeLog, "checklist_item", id, "update", nil, &c, "", "")
	}
	return &c, err
}

func (r *ChecklistRepository) Delete(id string) error {
	_, err := r.db.Exec("DELETE FROM checklist_items WHERE id = ?", id)
	if err == nil {
		logChange(r.changeLog, "checklist_item", id, "delete", nil, map[string]string{"id": id}, "", "")
	}
	return err
}
