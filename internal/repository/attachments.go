package repository

import (
	"database/sql"
	"fmt"

	"github.com/collinjanssen/thingstodo/internal/model"
)

type AttachmentRepository struct {
	db        DBTX
	conn      *sql.DB
	tx        *sql.Tx
	changeLog *ChangeLogRepository
}

func NewAttachmentRepository(db *sql.DB, changeLog *ChangeLogRepository) *AttachmentRepository {
	return &AttachmentRepository{db: db, conn: db, changeLog: changeLog}
}

// Conn returns the underlying connection pool, for boundary validation
// lookups (repository.Exists) outside repository transactions.
func (r *AttachmentRepository) Conn() *sql.DB {
	return r.conn
}

// WithTx returns a shallow copy of the repository bound to tx: its statements
// and nested inTx calls reuse tx instead of opening new transactions. It is
// nil-safe for the change log.
func (r *AttachmentRepository) WithTx(tx *sql.Tx) *AttachmentRepository {
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
func (r *AttachmentRepository) inTx(fn func(tx *sql.Tx) error) error {
	if r.tx != nil {
		return fn(r.tx)
	}
	return InTx(r.conn, fn)
}

func (r *AttachmentRepository) ListByTask(taskID string) ([]model.Attachment, error) {
	rows, err := r.db.Query(
		"SELECT id, type, title, url, mime_type, file_size, sort_order, created_at FROM attachments WHERE task_id = ? ORDER BY sort_order", taskID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var items []model.Attachment
	for rows.Next() {
		var a model.Attachment
		if err := rows.Scan(&a.ID, &a.Type, &a.Title, &a.URL, &a.MimeType, &a.FileSize, &a.SortOrder, &a.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan attachment: %w", err)
		}
		items = append(items, a)
	}
	if items == nil {
		items = []model.Attachment{}
	}
	return items, rows.Err()
}

// ListAll returns all attachments across all tasks.
func (r *AttachmentRepository) ListAll() ([]model.Attachment, error) {
	rows, err := r.db.Query(
		"SELECT id, task_id, type, title, url, mime_type, file_size, sort_order, created_at FROM attachments ORDER BY sort_order")
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var items []model.Attachment
	for rows.Next() {
		var a model.Attachment
		if err := rows.Scan(&a.ID, &a.TaskID, &a.Type, &a.Title, &a.URL, &a.MimeType, &a.FileSize, &a.SortOrder, &a.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan attachment: %w", err)
		}
		items = append(items, a)
	}
	if items == nil {
		items = []model.Attachment{}
	}
	return items, rows.Err()
}

func (r *AttachmentRepository) GetByID(id string) (*model.Attachment, error) {
	var a model.Attachment
	err := r.db.QueryRow(
		"SELECT id, task_id, type, title, url, mime_type, file_size, sort_order, created_at FROM attachments WHERE id = ?", id,
	).Scan(&a.ID, &a.TaskID, &a.Type, &a.Title, &a.URL, &a.MimeType, &a.FileSize, &a.SortOrder, &a.CreatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &a, nil
}

func (r *AttachmentRepository) Create(taskID string, input model.CreateAttachmentInput) (*model.Attachment, error) {
	id := input.ID
	if id == "" {
		id = model.NewID()
	}
	var maxSort float64
	_ = r.db.QueryRow("SELECT COALESCE(MAX(sort_order), 0) FROM attachments WHERE task_id = ?", taskID).Scan(&maxSort)

	_, err := r.db.Exec(`INSERT INTO attachments (id, task_id, type, title, url, mime_type, file_size, sort_order)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		id, taskID, input.Type, input.Title, input.URL, input.MimeType, input.FileSize, maxSort+1024)
	if err != nil {
		return nil, fmt.Errorf("create attachment: %w", err)
	}

	attachment, err := r.GetByID(id)
	if err == nil && attachment != nil {
		logChange(r.changeLog, "attachment", id, "create", nil, attachment, "", "")
	}
	return attachment, err
}

func (r *AttachmentRepository) Update(id string, input model.UpdateAttachmentInput) (*model.Attachment, error) {
	if input.Title != nil {
		if _, err := r.db.Exec("UPDATE attachments SET title = ? WHERE id = ?", *input.Title, id); err != nil {
			return nil, fmt.Errorf("update attachment title: %w", err)
		}
	}
	if input.SortOrder != nil {
		if _, err := r.db.Exec("UPDATE attachments SET sort_order = ? WHERE id = ?", *input.SortOrder, id); err != nil {
			return nil, fmt.Errorf("update attachment sort_order: %w", err)
		}
	}
	attachment, err := r.GetByID(id)
	if err == nil && attachment != nil {
		logChange(r.changeLog, "attachment", id, "update", nil, attachment, "", "")
	}
	return attachment, err
}

func (r *AttachmentRepository) Delete(id string) error {
	_, err := r.db.Exec("DELETE FROM attachments WHERE id = ?", id)
	if err == nil {
		logChange(r.changeLog, "attachment", id, "delete", nil, map[string]string{"id": id}, "", "")
	}
	return err
}

// ErrAttachmentNotFound is returned by DeleteOne when no attachment row
// carries the given id. Handlers map it to 404.
var ErrAttachmentNotFound = fmt.Errorf("attachment not found")

// DeleteOne deletes exactly the attachment row with the given id, in one
// transaction together with its change-log entry. Recurring instances copy
// file attachments as rows sharing one stored file, so for a file row it
// then counts the remaining rows referencing the same URL (inside the same
// transaction, i.e. excluding the row just deleted) and returns that stored
// file name only when none remain — the caller removes it from disk after
// the transaction committed. Every other case returns "".
func (r *AttachmentRepository) DeleteOne(id string) (orphanedFile string, err error) {
	err = r.inTx(func(tx *sql.Tx) error {
		var attType, url string
		if err := tx.QueryRow("SELECT type, url FROM attachments WHERE id = ?", id).Scan(&attType, &url); err != nil {
			if err == sql.ErrNoRows {
				return ErrAttachmentNotFound
			}
			return fmt.Errorf("lookup attachment %s: %w", id, err)
		}

		if _, err := tx.Exec("DELETE FROM attachments WHERE id = ?", id); err != nil {
			return fmt.Errorf("delete attachment %s: %w", id, err)
		}

		// The change log joins the same transaction (WithTx is nil-safe for
		// a missing change log; logChange propagates its error so the whole
		// delete rolls back).
		if err := logChange(r.WithTx(tx).changeLog, "attachment", id, "delete", nil, map[string]string{"id": id}, "", ""); err != nil {
			return fmt.Errorf("log attachment delete: %w", err)
		}

		if attType != "file" || url == "" {
			return nil // link rows (and file rows without a usable name) orphan nothing
		}
		count, err := r.WithTx(tx).CountByURL(url)
		if err != nil {
			return fmt.Errorf("count attachments by url: %w", err)
		}
		if count == 0 {
			orphanedFile = url
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	return orphanedFile, nil
}

// CountByURL returns how many file attachments reference the given stored file URL.
func (r *AttachmentRepository) CountByURL(url string) (int, error) {
	var count int
	err := r.db.QueryRow("SELECT COUNT(*) FROM attachments WHERE type = 'file' AND url = ?", url).Scan(&count)
	return count, err
}
