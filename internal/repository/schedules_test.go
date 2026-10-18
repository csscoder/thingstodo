package repository_test

import (
	"testing"

	"github.com/collinjanssen/thingstodo/internal/model"
	"github.com/collinjanssen/thingstodo/internal/repository"
	"github.com/collinjanssen/thingstodo/internal/testutil"
)

// TestCleanupOnTaskDone_LogsOnlyFlipped pins the change-log contract of the
// done-cleanup: only schedule entries the run flips to completed get an
// update entry. Entries already completed on an earlier day are unchanged,
// so finishing a task with historical entries must not re-log them.
func TestCleanupOnTaskDone_LogsOnlyFlipped(t *testing.T) {
	db := testutil.SetupTestDB(t)
	cl := repository.NewChangeLogRepository(db)
	taskRepo := repository.NewTaskRepository(db, nil)
	scheduleRepo := repository.NewScheduleRepository(db, cl)

	// taskRepo.Create seeds the first schedule row (when_date 2026-10-10);
	// pre-complete it behind the repository's back, like an entry finished
	// on an earlier day. A second open past entry is this run's flip.
	task, err := taskRepo.Create(model.CreateTaskInput{Title: "Cleanup log", WhenDate: strPtr("2026-10-10")})
	if err != nil {
		t.Fatalf("create task: %v", err)
	}
	if _, err := db.Exec(
		"UPDATE task_schedules SET completed = 1 WHERE task_id = ? AND when_date = '2026-10-10'", task.ID,
	); err != nil {
		t.Fatalf("pre-complete first entry: %v", err)
	}
	if _, err := db.Exec(
		"INSERT INTO task_schedules (id, task_id, when_date, sort_order) VALUES ('schedone0', ?, '2026-10-05', 2048)", task.ID,
	); err != nil {
		t.Fatalf("insert open past entry: %v", err)
	}

	if err := scheduleRepo.CleanupOnTaskDone(task.ID, "2026-10-17"); err != nil {
		t.Fatalf("cleanup: %v", err)
	}

	// Exactly one schedule update entry — for the flipped open entry, not
	// for the historical completed one.
	var n int
	var ids string
	if err := db.QueryRow(
		"SELECT COUNT(*), COALESCE(GROUP_CONCAT(entity_id), '') FROM change_log WHERE entity = 'schedule' AND action = 'update'",
	).Scan(&n, &ids); err != nil {
		t.Fatalf("count schedule change log: %v", err)
	}
	if n != 1 || ids != "schedone0" {
		t.Errorf("expected exactly one schedule update entry for schedone0, got %d (%s)", n, ids)
	}

	// Both past entries end up completed.
	var open int
	if err := db.QueryRow(
		"SELECT COUNT(*) FROM task_schedules WHERE task_id = ? AND completed = 0", task.ID,
	).Scan(&open); err != nil {
		t.Fatalf("count open entries: %v", err)
	}
	if open != 0 {
		t.Errorf("expected no open entries after cleanup, got %d", open)
	}
}
