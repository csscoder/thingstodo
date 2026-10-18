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

func setupScheduler(t *testing.T, loc *time.Location) (*Scheduler, *repository.TaskRepository, *repository.RepeatRuleRepository, *sql.DB) {
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
	s := New(db, taskRepo, ruleRepo, checklistRepo, attachRepo, scheduleRepo, reminderRepo, settingsRepo, userRepo, nil, nil, nil, loc)
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
	s, taskRepo, ruleRepo, db := setupScheduler(t, time.UTC)
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
			s, taskRepo, ruleRepo, db := setupScheduler(t, time.UTC)
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
	s, taskRepo, ruleRepo, db := setupScheduler(t, time.UTC)
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
	s, taskRepo, ruleRepo, db := setupScheduler(t, time.UTC)
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
	s, _, _, _ := setupScheduler(t, time.UTC)

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

// mustLoadLocation loads a named IANA zone or fails the test.
func mustLoadLocation(t *testing.T, name string) *time.Location {
	t.Helper()
	loc, err := time.LoadLocation(name)
	if err != nil {
		t.Fatalf("load location %s: %v", name, err)
	}
	return loc
}

func TestComputeFireAt_AllTypes(t *testing.T) {
	msk := mustLoadLocation(t, "Europe/Moscow")

	// at builds the expected fire time as a Moscow wall-clock instant.
	at := func(day string, h, m int) time.Time {
		t.Helper()
		d, err := time.ParseInLocation("2006-01-02", day, msk)
		if err != nil {
			t.Fatalf("parse day %s: %v", day, err)
		}
		return time.Date(d.Year(), d.Month(), d.Day(), h, m, 0, 0, msk)
	}

	start := "14:30"
	cases := []struct {
		name string
		p    model.PendingReminder
		want time.Time
	}{
		{
			name: "at_start uses schedule start_time",
			p:    model.PendingReminder{WhenDate: "2026-10-20", StartTime: &start, Reminder: model.Reminder{Type: model.ReminderAtStart}},
			want: at("2026-10-20", 14, 30),
		},
		{
			name: "at_start falls back to 09:00 without start_time",
			p:    model.PendingReminder{WhenDate: "2026-10-20", Reminder: model.Reminder{Type: model.ReminderAtStart}},
			want: at("2026-10-20", 9, 0),
		},
		{
			name: "on_day fires at 09:00",
			p:    model.PendingReminder{WhenDate: "2026-10-20", Reminder: model.Reminder{Type: model.ReminderOnDay}},
			want: at("2026-10-20", 9, 0),
		},
		{
			name: "minutes_before subtracts from start_time",
			p:    model.PendingReminder{WhenDate: "2026-10-20", StartTime: &start, Reminder: model.Reminder{Type: model.ReminderMinutesBefore, Value: 15}},
			want: at("2026-10-20", 14, 15),
		},
		{
			name: "hours_before subtracts from start_time",
			p:    model.PendingReminder{WhenDate: "2026-10-20", StartTime: &start, Reminder: model.Reminder{Type: model.ReminderHoursBefore, Value: 2}},
			want: at("2026-10-20", 12, 30),
		},
		{
			name: "hours_before falls back to the 09:00 anchor",
			p:    model.PendingReminder{WhenDate: "2026-10-20", Reminder: model.Reminder{Type: model.ReminderHoursBefore, Value: 2}},
			want: at("2026-10-20", 7, 0),
		},
		{
			name: "days_before fires at 09:00 one day earlier",
			p:    model.PendingReminder{WhenDate: "2026-10-20", Reminder: model.Reminder{Type: model.ReminderDaysBefore, Value: 1}},
			want: at("2026-10-19", 9, 0),
		},
		{
			name: "someday when_date has no fire time",
			p:    model.PendingReminder{WhenDate: "someday", Reminder: model.Reminder{Type: model.ReminderAtStart}},
			want: time.Time{},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := computeFireAt(tc.p, msk)
			if !got.Equal(tc.want) {
				t.Errorf("computeFireAt(%s) = %s, want %s",
					tc.p.Reminder.Type, got.Format(time.RFC3339), tc.want.Format(time.RFC3339))
			}
		})
	}
}

func TestComputeFireAt_DST(t *testing.T) {
	berlin := mustLoadLocation(t, "Europe/Berlin")
	// Both transition days leave 09:00 unambiguous, so time.Date pins the
	// 09:00 wall-clock instant directly:
	//   2026-03-29: spring forward 02:00->03:00 CET->CEST, 09:00 = CEST = 07:00Z
	//   2026-10-25: fall back 03:00->02:00 CEST->CET, 09:00 = CET  = 08:00Z
	// The old midnight.Add(9h) construction missed both (10:00 and 08:00).
	cases := []struct {
		day     string
		wantUTC string
	}{
		{"2026-03-29", "2026-03-29T07:00:00Z"},
		{"2026-10-25", "2026-10-25T08:00:00Z"},
	}
	for _, tc := range cases {
		t.Run(tc.day, func(t *testing.T) {
			p := model.PendingReminder{WhenDate: tc.day, Reminder: model.Reminder{Type: model.ReminderAtStart}}
			got := computeFireAt(p, berlin)
			if wall := got.In(berlin).Format("2006-01-02 15:04"); wall != tc.day+" 09:00" {
				t.Errorf("at_start on %s = %s Berlin wall-clock, want %s 09:00", tc.day, wall, tc.day)
			}
			if utc := got.UTC().Format(time.RFC3339); utc != tc.wantUTC {
				t.Errorf("at_start on %s = %s UTC, want %s", tc.day, utc, tc.wantUTC)
			}
		})
	}
}

func TestRemindersDue_NeverEarly(t *testing.T) {
	msk := mustLoadLocation(t, "Europe/Moscow")
	s, taskRepo, _, db := setupScheduler(t, msk)
	reminderRepo := repository.NewReminderRepository(db, nil)
	task := mustCreateTask(t, taskRepo, "Standing desk", "2026-10-20")
	if _, err := reminderRepo.Create(task.ID, model.CreateReminderInput{Type: model.ReminderAtStart}); err != nil {
		t.Fatalf("create reminder: %v", err)
	}
	fireAt := time.Date(2026, 10, 20, 9, 0, 0, 0, msk)

	t.Run("tick before fire time is not due", func(t *testing.T) {
		s.lastReminderTick = time.Time{}
		if due := s.remindersDue(fireAt.Add(-30 * time.Second)); len(due) != 0 {
			t.Errorf("reminder due 30s early: %+v", due)
		}
	})

	t.Run("tick at the exact fire second is due", func(t *testing.T) {
		s.lastReminderTick = time.Time{} // fresh scheduler: lastTick = now - 1m
		due := s.remindersDue(fireAt)
		if len(due) != 1 || !due[0].fireAt.Equal(fireAt) {
			t.Fatalf("want exactly the 09:00 reminder, got %+v", due)
		}
	})

	t.Run("tick after fire time is due once, not again", func(t *testing.T) {
		now := fireAt.Add(40 * time.Second) // 09:00:40
		s.lastReminderTick = fireAt.Add(-20 * time.Second)
		due := s.remindersDue(now)
		if len(due) != 1 || !due[0].fireAt.Equal(fireAt) {
			t.Fatalf("want the reminder due once in (08:59:40, 09:00:40], got %+v", due)
		}
		// The tick loop advances lastTick to now after firing; the next tick
		// must not select the same reminder again.
		s.lastReminderTick = now
		if due := s.remindersDue(now.Add(time.Minute)); len(due) != 0 {
			t.Errorf("reminder fired again on the next tick: %+v", due)
		}
	})
}

func TestExactReminder_RFC3339Fires(t *testing.T) {
	msk := mustLoadLocation(t, "Europe/Moscow")

	t.Run("rfc3339 stored value fires at the local wall-clock time", func(t *testing.T) {
		s, taskRepo, _, db := setupScheduler(t, msk)
		reminderRepo := repository.NewReminderRepository(db, nil)
		task := mustCreateTask(t, taskRepo, "Call airport", "2026-10-20")
		exactAt := "2026-10-20T06:00:00Z" // normalized UTC == 09:00 Moscow
		if _, err := reminderRepo.Create(task.ID, model.CreateReminderInput{Type: model.ReminderExact, ExactAt: &exactAt}); err != nil {
			t.Fatalf("create exact reminder: %v", err)
		}

		want := time.Date(2026, 10, 20, 9, 0, 0, 0, msk)
		due := s.remindersDue(want)
		if len(due) != 1 {
			t.Fatalf("want exactly one due reminder, got %d", len(due))
		}
		if !due[0].fireAt.Equal(want) {
			t.Errorf("exact reminder fireAt = %s, want %s",
				due[0].fireAt.UTC().Format(time.RFC3339), want.UTC().Format(time.RFC3339))
		}
		if r := due[0].pending.Reminder; r.Type != model.ReminderExact || r.ID == "" {
			t.Errorf("due reminder is not the exact reminder: %+v", r)
		}
	})

	t.Run("legacy naive stored value still fires", func(t *testing.T) {
		s, taskRepo, _, db := setupScheduler(t, msk)
		reminderRepo := repository.NewReminderRepository(db, nil)
		task := mustCreateTask(t, taskRepo, "Renew passport", "2026-10-20")
		exactAt := "2026-10-20T09:00:00" // legacy naive layout, read in loc
		if _, err := reminderRepo.Create(task.ID, model.CreateReminderInput{Type: model.ReminderExact, ExactAt: &exactAt}); err != nil {
			t.Fatalf("create exact reminder: %v", err)
		}

		want := time.Date(2026, 10, 20, 9, 0, 0, 0, msk)
		due := s.remindersDue(want)
		if len(due) != 1 {
			t.Fatalf("want exactly one due reminder, got %d", len(due))
		}
		if !due[0].fireAt.Equal(want) {
			t.Errorf("naive exact reminder fireAt = %s, want %s",
				due[0].fireAt.UTC().Format(time.RFC3339), want.UTC().Format(time.RFC3339))
		}
	})
}
