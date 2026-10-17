package repository_test

import (
	"database/sql"
	"errors"
	"testing"

	"github.com/collinjanssen/thingstodo/internal/model"
	"github.com/collinjanssen/thingstodo/internal/repository"
	"github.com/collinjanssen/thingstodo/internal/testutil"
)

func TestInTx_RollsBackOnError(t *testing.T) {
	db := testutil.SetupTestDB(t)

	err := repository.InTx(db, func(tx *sql.Tx) error {
		if _, err := tx.Exec("INSERT INTO areas (id, title, sort_order) VALUES ('area1', 'Rolled back', 0)"); err != nil {
			return err
		}
		return errors.New("boom")
	})
	if err == nil {
		t.Fatal("expected InTx to return the fn error")
	}
	if err.Error() != "boom" {
		t.Errorf("expected the original fn error, got %q", err.Error())
	}

	var count int
	if err := db.QueryRow("SELECT COUNT(*) FROM areas WHERE id = 'area1'").Scan(&count); err != nil {
		t.Fatalf("query areas: %v", err)
	}
	if count != 0 {
		t.Errorf("area must not exist after rollback, got %d rows", count)
	}
}

func TestInTx_RollsBackOnPanic(t *testing.T) {
	db := testutil.SetupTestDB(t)

	defer func() {
		if recover() == nil {
			t.Error("InTx must re-panic when fn panics")
		}
		var count int
		if err := db.QueryRow("SELECT COUNT(*) FROM areas WHERE id = 'area1'").Scan(&count); err != nil {
			t.Errorf("query areas: %v", err)
		}
		if count != 0 {
			t.Errorf("area must not exist after rollback, got %d rows", count)
		}
	}()

	_ = repository.InTx(db, func(tx *sql.Tx) error {
		_, _ = tx.Exec("INSERT INTO areas (id, title, sort_order) VALUES ('area1', 'Panicked', 0)")
		panic("boom")
	})
	t.Error("expected the panic to propagate out of InTx")
}

func TestWithTx_NoNestedBegin(t *testing.T) {
	db := testutil.SetupTestDB(t)
	changeLog := repository.NewChangeLogRepository(db)
	repo := repository.NewTaskRepository(db, changeLog)

	first, err := repo.Create(model.CreateTaskInput{Title: "first"})
	if err != nil {
		t.Fatalf("create first task: %v", err)
	}
	second, err := repo.Create(model.CreateTaskInput{Title: "second"})
	if err != nil {
		t.Fatalf("create second task: %v", err)
	}

	// Commit path: Reorder on a WithTx-bound repository completes inside the
	// outer transaction instead of opening a second one, and commits with it.
	err = repository.InTx(db, func(tx *sql.Tx) error {
		return repo.WithTx(tx).Reorder([]model.ReorderItem{
			{ID: first.ID, SortField: "sort_order_today", SortOrder: 111},
			{ID: second.ID, SortField: "sort_order_today", SortOrder: 222},
		})
	})
	if err != nil {
		t.Fatalf("reorder inside InTx failed (nested Begin?): %v", err)
	}
	assertSortOrderToday(t, db, first.ID, 111)
	assertSortOrderToday(t, db, second.ID, 222)

	// Rollback path: the bound repository's writes roll back with the outer
	// transaction.
	err = repository.InTx(db, func(tx *sql.Tx) error {
		if err := repo.WithTx(tx).Reorder([]model.ReorderItem{
			{ID: first.ID, SortField: "sort_order_today", SortOrder: 999},
		}); err != nil {
			return err
		}
		return errors.New("force rollback")
	})
	if err == nil {
		t.Fatal("expected the outer error to surface")
	}
	assertSortOrderToday(t, db, first.ID, 111)
}

func assertSortOrderToday(t *testing.T, db *sql.DB, id string, want float64) {
	t.Helper()
	var got float64
	if err := db.QueryRow("SELECT sort_order_today FROM tasks WHERE id = ?", id).Scan(&got); err != nil {
		t.Fatalf("query sort order for %s: %v", id, err)
	}
	if got != want {
		t.Errorf("task %s: expected sort_order_today %v, got %v", id, want, got)
	}
}
