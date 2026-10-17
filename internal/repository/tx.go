package repository

import (
	"database/sql"
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
