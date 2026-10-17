package scheduler

import (
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/collinjanssen/thingstodo/internal/database"
	"github.com/collinjanssen/thingstodo/internal/model"
	"github.com/collinjanssen/thingstodo/internal/repository"
)

// setupTestDB opens a migrated SQLite database. Same-package tests cannot
// import internal/testutil here, so this mirrors testutil.SetupTestDB with
// database.Open.
func setupTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := database.Open(t.TempDir() + "/test.db")
	if err != nil {
		t.Fatalf("open test database: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func setupScheduler(t *testing.T) (*Scheduler, *repository.TaskRepository, *repository.RepeatRuleRepository, *sql.DB) {
	t.Helper()
	db := setupTestDB(t)
	taskRepo := repository.NewTaskRepository(db, nil)
	ruleRepo := repository.NewRepeatRuleRepository(db, nil)
	checklistRepo := repository.NewChecklistRepository(db, nil)
	attachRepo := repository.NewAttachmentRepository(db, nil)
	scheduleRepo := repository.NewScheduleRepository(db, nil)
	reminderRepo := repository.NewReminderRepository(db, nil)
	settingsRepo := repository.NewUserSettingsRepository(db)
	userRepo := repository.NewUserRepository(db)
	s := New(db, taskRepo, ruleRepo, checklistRepo, attachRepo, scheduleRepo, reminderRepo, settingsRepo, userRepo, nil, nil, nil, time.UTC)
	return s, taskRepo, ruleRepo, db
}

func mustCreateTask(t *testing.T, repo *repository.TaskRepository, title, whenDate string) *model.TaskDetail {
	t.Helper()
	var wd *string
	if whenDate != "" {
		wd = &whenDate
	}
	task, err := repo.Create(model.CreateTaskInput{Title: title, WhenDate: wd})
	if err != nil {
		t.Fatalf("create task: %v", err)
	}
	return task
}

func mustUpsertRule(t *testing.T, repo *repository.RepeatRuleRepository, taskID string, pattern model.RecurrencePattern) {
	t.Helper()
	if _, err := repo.Upsert(taskID, model.CreateRepeatRuleInput{Pattern: &pattern}); err != nil {
		t.Fatalf("upsert rule: %v", err)
	}
}

// setClock pins the scheduler to a fixed moment in UTC.
func setClock(s *Scheduler, y, m, d int) {
	s.SetClock(func() time.Time {
		return time.Date(y, time.Month(m), d, 9, 0, 0, 0, time.UTC)
	})
}

// countTasks returns the number of non-deleted tasks sharing the title.
func countTasks(t *testing.T, db *sql.DB, title string) int {
	t.Helper()
	var n int
	if err := db.QueryRow(
		"SELECT COUNT(*) FROM tasks WHERE title = ? AND deleted_at IS NULL", title,
	).Scan(&n); err != nil {
		t.Fatalf("count tasks: %v", err)
	}
	return n
}

// openWhenDate returns the when_date of the single open task with the title.
func openWhenDate(t *testing.T, db *sql.DB, title string) string {
	t.Helper()
	var wd sql.NullString
	if err := db.QueryRow(
		"SELECT when_date FROM tasks WHERE title = ? AND status = 'open' AND deleted_at IS NULL", title,
	).Scan(&wd); err != nil {
		t.Fatalf("load open instance: %v", err)
	}
	return wd.String
}

func TestFinishTask_DoubleCompleteSpawnsOnce(t *testing.T) {
	s, taskRepo, ruleRepo, db := setupScheduler(t)
	task := mustCreateTask(t, taskRepo, "Recur", "2026-10-14")
	mustUpsertRule(t, ruleRepo, task.ID, model.RecurrencePattern{
		Type: model.PatternWeekly, Every: 1, Mode: model.RecurrenceModeAfterCompletion,
	})
	setClock(s, 2026, 10, 17) // Saturday

	got, changed, err := s.FinishTask(task.ID, "completed")
	if err != nil {
		t.Fatalf("first FinishTask: %v", err)
	}
	if !changed {
		t.Error("first FinishTask should report changed=true")
	}
	if got == nil || got.Status != "completed" {
		t.Errorf("first FinishTask should return the completed task, got %+v", got)
	}

	got2, changed2, err := s.FinishTask(task.ID, "completed")
	if err != nil {
		t.Fatalf("second FinishTask: %v", err)
	}
	if changed2 {
		t.Error("second FinishTask should report changed=false")
	}
	if got2 == nil || got2.Status != "completed" {
		t.Errorf("second FinishTask should still return the task, got %+v", got2)
	}

	if n := countTasks(t, db, "Recur"); n != 2 {
		t.Errorf("expected original + exactly one instance, got %d tasks", n)
	}
}

func TestFinishTask_AfterCompletionUsesCompletionDate(t *testing.T) {
	cases := []struct {
		name string
		on   []string
		want string
	}{
		{"no on constraint", nil, "2026-10-24"},
		{"on wed", []string{"wed"}, "2026-10-21"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s, taskRepo, ruleRepo, db := setupScheduler(t)
			// when_date is Wed 2026-10-14, completion happens Sat 2026-10-17:
			// the next instance must be computed from the completion date,
			// not the original when_date.
			task := mustCreateTask(t, taskRepo, "Weekly", "2026-10-14")
			mustUpsertRule(t, ruleRepo, task.ID, model.RecurrencePattern{
				Type: model.PatternWeekly, Every: 1, Mode: model.RecurrenceModeAfterCompletion, On: tc.on,
			})
			setClock(s, 2026, 10, 17)

			if _, _, err := s.FinishTask(task.ID, "completed"); err != nil {
				t.Fatalf("FinishTask: %v", err)
			}
			if got := openWhenDate(t, db, "Weekly"); got != tc.want {
				t.Errorf("next instance when_date = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestFinishTask_FixedRollsForward(t *testing.T) {
	s, taskRepo, ruleRepo, db := setupScheduler(t)
	// Daily fixed rule anchored at 2026-10-01, finished on 2026-10-17:
	// the next instance rolls forward to today instead of landing in the past.
	task := mustCreateTask(t, taskRepo, "Daily", "2026-10-01")
	mustUpsertRule(t, ruleRepo, task.ID, model.RecurrencePattern{
		Type: model.PatternDaily, Every: 1, Mode: model.RecurrenceModeFixed,
	})
	setClock(s, 2026, 10, 17)

	if _, _, err := s.FinishTask(task.ID, "completed"); err != nil {
		t.Fatalf("FinishTask: %v", err)
	}
	if got := openWhenDate(t, db, "Daily"); got != "2026-10-17" {
		t.Errorf("next instance when_date = %q, want 2026-10-17", got)
	}
}

func TestFinishTask_InstanceFailureRollsBack(t *testing.T) {
	s, taskRepo, ruleRepo, db := setupScheduler(t)
	task := mustCreateTask(t, taskRepo, "Rollback", "2026-10-14")
	mustUpsertRule(t, ruleRepo, task.ID, model.RecurrencePattern{
		Type: model.PatternWeekly, Every: 1, Mode: model.RecurrenceModeAfterCompletion,
	})
	setClock(s, 2026, 10, 17)

	hookErr := errors.New("forced instance failure")
	s.testHookAfterInstance = func() error { return hookErr }
	defer func() { s.testHookAfterInstance = nil }()

	_, _, err := s.FinishTask(task.ID, "completed")
	if !errors.Is(err, hookErr) {
		t.Fatalf("FinishTask should return the hook error, got %v", err)
	}

	after, getErr := taskRepo.GetByID(task.ID)
	if getErr != nil || after == nil {
		t.Fatalf("load task after rollback: %v %v", after, getErr)
	}
	if after.Status != "open" {
		t.Errorf("task should still be open after rollback, got status %q", after.Status)
	}
	if n := countTasks(t, db, "Rollback"); n != 1 {
		t.Errorf("expected only the original task after rollback, got %d", n)
	}
	rule, ruleErr := ruleRepo.GetByTask(task.ID)
	if ruleErr != nil || rule == nil {
		t.Fatalf("repeat rule should still be on the original task after rollback: %v %v", rule, ruleErr)
	}
}

func TestFinishTask_UnknownTaskReturnsNilFalseNil(t *testing.T) {
	s, _, _, _ := setupScheduler(t)

	task, changed, err := s.FinishTask("nope0000", "completed")
	if err != nil {
		t.Fatalf("unknown task must not error, got %v", err)
	}
	if changed {
		t.Error("unknown task must report changed=false")
	}
	if task != nil {
		t.Errorf("unknown task must return nil task, got %+v", task)
	}
}
