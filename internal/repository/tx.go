package repository

import (
	"database/sql"
	"errors"
	"fmt"
)

// DBTX is the subset of *sql.DB and *sql.Tx used by the repositories. A
// repository runs its statements against whichever handle its db field holds:
// the connection pool, or the transaction of a WithTx-bound copy.
type DBTX interface {
	Exec(query string, args ...any) (sql.Result, error)
	Query(query string, args ...any) (*sql.Rows, error)
	QueryRow(query string, args ...any) *sql.Row
}

// InTx runs fn inside a transaction on db. It commits when fn returns nil and
// rolls back when fn returns an error or panics; a panic is re-raised after
// the rollback.
func InTx(db *sql.DB, fn func(tx *sql.Tx) error) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()
	if err := fn(tx); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	committed = true
	return nil
}

// existsTables is the allowlist of tables Exists and HeadingInProject may
// query. Interpolating a table name into SQL is only safe against this list.
var existsTables = map[string]bool{
	"tasks": true, "projects": true, "areas": true, "headings": true, "tags": true,
}

// Exists reports whether the row with the given id exists in table. table
// must be one of tasks, projects, areas, headings, tags; anything else is a
// programming error and returns an error instead of querying.
func Exists(db DBTX, table, id string) (bool, error) {
	if !existsTables[table] {
		return false, fmt.Errorf("repository.Exists: unsupported table %q", table)
	}
	var one int
	err := db.QueryRow("SELECT 1 FROM "+table+" WHERE id = ?", id).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

// HeadingInProject reports whether the heading exists and belongs to the
// given project. Task creates use it to reject a heading_id that points at
// another project's heading.
func HeadingInProject(db DBTX, headingID, projectID string) (bool, error) {
	var one int
	err := db.QueryRow(
		"SELECT 1 FROM headings WHERE id = ? AND project_id = ?", headingID, projectID,
	).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}
