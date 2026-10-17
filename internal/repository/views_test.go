package repository_test

import (
	"database/sql"
	"testing"
	"time"

	"github.com/collinjanssen/thingstodo/internal/model"
	"github.com/collinjanssen/thingstodo/internal/repository"
	"github.com/collinjanssen/thingstodo/internal/testutil"
)

func TestViewInboxShowsOnlyUnassignedOpenTasks(t *testing.T) {
	db := testutil.SetupTestDB(t)
	taskRepo := repository.NewTaskRepository(db, nil)
	viewRepo := repository.NewViewRepository(db, time.Local)

	// Inbox task: no project, no area, no when_date, status=open
	_, _ = taskRepo.Create(model.CreateTaskInput{Title: "Inbox task"})

	// Non-inbox: has project
	_, _ = db.Exec("INSERT INTO projects (id, title) VALUES ('p1', 'Project')")
	_, _ = taskRepo.Create(model.CreateTaskInput{Title: "Project task", ProjectID: strPtr("p1")})

	// Non-inbox: has when_date
	_, _ = taskRepo.Create(model.CreateTaskInput{Title: "Scheduled", WhenDate: strPtr("2026-03-01")})

	// Non-inbox: has area
	_, _ = db.Exec("INSERT INTO areas (id, title) VALUES ('a1', 'Work')")
	_, _ = taskRepo.Create(model.CreateTaskInput{Title: "Area task", AreaID: strPtr("a1")})

	view, err := viewRepo.Inbox(nil, true)
	if err != nil {
		t.Fatalf("failed to get inbox: %v", err)
	}
	if len(view.Tasks) != 1 {
		t.Fatalf("expected 1 inbox task, got %d", len(view.Tasks))
	}
	if view.Tasks[0].Title != "Inbox task" {
		t.Errorf("expected 'Inbox task', got %q", view.Tasks[0].Title)
	}
}

func TestViewInboxExcludesCompletedTasks(t *testing.T) {
	db := testutil.SetupTestDB(t)
	taskRepo := repository.NewTaskRepository(db, nil)
	viewRepo := repository.NewViewRepository(db, time.Local)

	task, _ := taskRepo.Create(model.CreateTaskInput{Title: "Done"})
	_, _ = taskRepo.SetDoneStatus(task.ID, "completed")
	_, _ = taskRepo.Create(model.CreateTaskInput{Title: "Open"})

	view, _ := viewRepo.Inbox(nil, true)
	if len(view.Tasks) != 1 {
		t.Fatalf("expected 1 open inbox task, got %d", len(view.Tasks))
	}
}

func TestViewInboxEmpty(t *testing.T) {
	db := testutil.SetupTestDB(t)
	viewRepo := repository.NewViewRepository(db, time.Local)

	view, err := viewRepo.Inbox(nil, true)
	if err != nil {
		t.Fatalf("failed: %v", err)
	}
	if len(view.Tasks) != 0 {
		t.Errorf("expected 0 tasks, got %d", len(view.Tasks))
	}
}

func TestViewTodayStructure(t *testing.T) {
	db := testutil.SetupTestDB(t)
	taskRepo := repository.NewTaskRepository(db, nil)
	viewRepo := repository.NewViewRepository(db, time.Local)

	today := time.Now().Format("2006-01-02")
	_, _ = taskRepo.Create(model.CreateTaskInput{Title: "Today task", WhenDate: &today})

	// Create a task with an evening schedule entry (start_time >= 18:00)
	eveningTask, _ := taskRepo.Create(model.CreateTaskInput{Title: "Evening task", WhenDate: &today})
	scheduleRepo := repository.NewScheduleRepository(db, nil)
	startTime := "19:00"
	endTime := "20:00"
	_, _ = scheduleRepo.Create(eveningTask.ID, model.CreateTaskScheduleInput{
		WhenDate:  today,
		StartTime: &startTime,
		EndTime:   &endTime,
	})

	view, err := viewRepo.Today("18:00")
	if err != nil {
		t.Fatalf("failed: %v", err)
	}
	if len(view.Sections) != 2 {
		t.Fatalf("expected 2 sections, got %d", len(view.Sections))
	}
	if view.Sections[0].Title != "Today" {
		t.Errorf("expected first section 'Today', got %q", view.Sections[0].Title)
	}
	if view.Sections[1].Title != "This Evening" {
		t.Errorf("expected second section 'This Evening', got %q", view.Sections[1].Title)
	}
}

func TestViewLogbook(t *testing.T) {
	db := testutil.SetupTestDB(t)
	taskRepo := repository.NewTaskRepository(db, nil)
	viewRepo := repository.NewViewRepository(db, time.Local)

	t1, _ := taskRepo.Create(model.CreateTaskInput{Title: "Completed"})
	_, _ = taskRepo.SetDoneStatus(t1.ID, "completed")
	t2, _ := taskRepo.Create(model.CreateTaskInput{Title: "Canceled"})
	_, _ = taskRepo.SetDoneStatus(t2.ID, "canceled")
	_, _ = taskRepo.Create(model.CreateTaskInput{Title: "Still open"})

	view, err := viewRepo.Logbook(50, 0)
	if err != nil {
		t.Fatalf("failed: %v", err)
	}
	if view.Total != 2 {
		t.Errorf("expected total=2, got %d", view.Total)
	}
	// All groups should have tasks
	totalTasks := 0
	for _, g := range view.Groups {
		totalTasks += len(g.Tasks)
	}
	if totalTasks != 2 {
		t.Errorf("expected 2 tasks in groups, got %d", totalTasks)
	}
}

func TestViewLogbookEmpty(t *testing.T) {
	db := testutil.SetupTestDB(t)
	viewRepo := repository.NewViewRepository(db, time.Local)

	view, err := viewRepo.Logbook(50, 0)
	if err != nil {
		t.Fatalf("failed: %v", err)
	}
	if view.Total != 0 {
		t.Errorf("expected total=0, got %d", view.Total)
	}
	if len(view.Groups) != 0 {
		t.Errorf("expected 0 groups, got %d", len(view.Groups))
	}
}

func TestViewUpcoming(t *testing.T) {
	db := testutil.SetupTestDB(t)
	taskRepo := repository.NewTaskRepository(db, nil)
	viewRepo := repository.NewViewRepository(db, time.Local)

	tomorrow := time.Now().AddDate(0, 0, 1).Format("2006-01-02")
	nextWeek := time.Now().AddDate(0, 0, 7).Format("2006-01-02")
	_, _ = taskRepo.Create(model.CreateTaskInput{Title: "Tomorrow", WhenDate: &tomorrow})
	_, _ = taskRepo.Create(model.CreateTaskInput{Title: "Next week", WhenDate: &nextWeek})

	today := time.Now().Format("2006-01-02")
	view, err := viewRepo.Upcoming(today)
	if err != nil {
		t.Fatalf("failed: %v", err)
	}
	if len(view.Dates) != 2 {
		t.Fatalf("expected 2 date groups, got %d", len(view.Dates))
	}
	// First date should be tomorrow
	if view.Dates[0].Date != tomorrow {
		t.Errorf("expected first date=%q, got %q", tomorrow, view.Dates[0].Date)
	}
}

// --- Task 14: the owner's timezone everywhere ---

// newMoscowViewRepo pins the clock to 2026-10-17 00:30 Europe/Moscow
// (= 2026-10-16 21:30 UTC) and returns a view repository bound to Moscow.
func newMoscowViewRepo(t *testing.T, db *sql.DB) *repository.ViewRepository {
	t.Helper()
	loc, err := time.LoadLocation("Europe/Moscow")
	if err != nil {
		t.Fatalf("load Europe/Moscow: %v", err)
	}
	repo := repository.NewViewRepository(db, loc)
	repo.SetClock(func() time.Time {
		return time.Date(2026, 10, 16, 21, 30, 0, 0, time.UTC)
	})
	return repo
}

// tzHas reports whether the list contains the string.
func tzHas(items []string, want string) bool {
	for _, s := range items {
		if s == want {
			return true
		}
	}
	return false
}

// tzTodayTitles lists the titles of the tasks in the view's first section.
func tzTodayTitles(v *model.TodayView) []string {
	var titles []string
	for _, g := range v.Sections[0].Groups {
		for _, task := range g.Tasks {
			titles = append(titles, task.Title)
		}
	}
	return titles
}

// tzTaskTitles lists the titles of a task slice.
func tzTaskTitles(tasks []model.TaskListItem) []string {
	titles := make([]string, len(tasks))
	for i, t := range tasks {
		titles[i] = t.Title
	}
	return titles
}

// tzGroupDates lists the group dates of a grouped view.
func tzGroupDates(groups []model.DateGroup) []string {
	dates := make([]string, len(groups))
	for i, g := range groups {
		dates[i] = g.Date
	}
	return dates
}

// TestToday_CompletedTodayLocal pins a task completed at 2026-10-16 21:10:00
// UTC, which is 00:10 on 2026-10-17 in Moscow. With the clock at 00:30 local
// on 2026-10-17, the task belongs to Today's Completed section even though its
// UTC date is still 2026-10-16.
func TestToday_CompletedTodayLocal(t *testing.T) {
	db := testutil.SetupTestDB(t)
	taskRepo := repository.NewTaskRepository(db, nil)
	viewRepo := newMoscowViewRepo(t, db)

	task, err := taskRepo.Create(model.CreateTaskInput{Title: "Done after local midnight"})
	if err != nil {
		t.Fatalf("create task: %v", err)
	}
	if _, err := taskRepo.SetDoneStatus(task.ID, "completed"); err != nil {
		t.Fatalf("complete task: %v", err)
	}
	if _, err := db.Exec(
		"UPDATE tasks SET completed_at = ?, canceled_at = NULL, updated_at = ? WHERE id = ?",
		"2026-10-16 21:10:00", "2026-10-16 21:10:00", task.ID,
	); err != nil {
		t.Fatalf("pin completed_at: %v", err)
	}

	view, err := viewRepo.Today("18:00")
	if err != nil {
		t.Fatalf("today view: %v", err)
	}
	if titles := tzTaskTitles(view.Completed); !tzHas(titles, "Done after local midnight") {
		t.Errorf("task completed at 00:10 local is missing from Today's Completed section; got %v", titles)
	}
}

// TestToday_UsesLocalDate pins a task with when_date 2026-10-17, the local
// (Moscow) today at the pinned clock, while the UTC date is still
// 2026-10-16. The task must be in the Today section and must not be filed as
// Earlier in the Today or Upcoming views.
func TestToday_UsesLocalDate(t *testing.T) {
	db := testutil.SetupTestDB(t)
	taskRepo := repository.NewTaskRepository(db, nil)
	viewRepo := newMoscowViewRepo(t, db)

	when := "2026-10-17"
	if _, err := taskRepo.Create(model.CreateTaskInput{Title: "Local today", WhenDate: &when}); err != nil {
		t.Fatalf("create task: %v", err)
	}

	view, err := viewRepo.Today("18:00")
	if err != nil {
		t.Fatalf("today view: %v", err)
	}
	if titles := tzTodayTitles(view); !tzHas(titles, "Local today") {
		t.Errorf("task with when_date=local today is missing from the Today section; got %v", titles)
	}
	if titles := tzTaskTitles(view.Earlier); tzHas(titles, "Local today") {
		t.Errorf("task with when_date=local today must not be in Today's Earlier section; got %v", titles)
	}

	upcoming, err := viewRepo.Upcoming("")
	if err != nil {
		t.Fatalf("upcoming view: %v", err)
	}
	if titles := tzTaskTitles(upcoming.Earlier); tzHas(titles, "Local today") {
		t.Errorf("task with when_date=local today must not be in Upcoming's Earlier section; got %v", titles)
	}
}

// TestLogbook_GroupsByLocalDate pins a completion at 2026-10-16 21:10:00 UTC
// and expects the Logbook group keyed by the Moscow calendar date
// 2026-10-17, not the UTC date 2026-10-16.
func TestLogbook_GroupsByLocalDate(t *testing.T) {
	db := testutil.SetupTestDB(t)
	taskRepo := repository.NewTaskRepository(db, nil)
	viewRepo := newMoscowViewRepo(t, db)

	task, err := taskRepo.Create(model.CreateTaskInput{Title: "Completed at 00:10 local"})
	if err != nil {
		t.Fatalf("create task: %v", err)
	}
	if _, err := taskRepo.SetDoneStatus(task.ID, "completed"); err != nil {
		t.Fatalf("complete task: %v", err)
	}
	if _, err := db.Exec(
		"UPDATE tasks SET completed_at = ?, canceled_at = NULL, updated_at = ? WHERE id = ?",
		"2026-10-16 21:10:00", "2026-10-16 21:10:00", task.ID,
	); err != nil {
		t.Fatalf("pin completed_at: %v", err)
	}

	view, err := viewRepo.Logbook(50, 0)
	if err != nil {
		t.Fatalf("logbook view: %v", err)
	}
	dates := tzGroupDates(view.Groups)
	if len(dates) != 1 || dates[0] != "2026-10-17" {
		t.Fatalf("expected a single Logbook group dated 2026-10-17 (local date), got %v", dates)
	}
	if titles := tzTaskTitles(view.Groups[0].Tasks); !tzHas(titles, "Completed at 00:10 local") {
		t.Errorf("expected the completed task inside the 2026-10-17 group; got %v", titles)
	}
}

// TestTrash_GroupsByLocalDate pins a deletion at 2026-10-16 21:10:00 UTC and
// expects the Trash group keyed by the Moscow calendar date 2026-10-17.
func TestTrash_GroupsByLocalDate(t *testing.T) {
	db := testutil.SetupTestDB(t)
	taskRepo := repository.NewTaskRepository(db, nil)
	viewRepo := newMoscowViewRepo(t, db)

	task, err := taskRepo.Create(model.CreateTaskInput{Title: "Deleted at 00:10 local"})
	if err != nil {
		t.Fatalf("create task: %v", err)
	}
	if _, err := db.Exec(
		"UPDATE tasks SET deleted_at = ?, updated_at = ? WHERE id = ?",
		"2026-10-16 21:10:00", "2026-10-16 21:10:00", task.ID,
	); err != nil {
		t.Fatalf("pin deleted_at: %v", err)
	}

	view, err := viewRepo.Trash(50, 0)
	if err != nil {
		t.Fatalf("trash view: %v", err)
	}
	dates := tzGroupDates(view.Groups)
	if len(dates) != 1 || dates[0] != "2026-10-17" {
		t.Fatalf("expected a single Trash group dated 2026-10-17 (local date), got %v", dates)
	}
	if titles := tzTaskTitles(view.Groups[0].Tasks); !tzHas(titles, "Deleted at 00:10 local") {
		t.Errorf("expected the deleted task inside the 2026-10-17 group; got %v", titles)
	}
}

// TestInbox_ReviewCutoffLocal pins the clock at 2026-10-17 00:30 Moscow, so a
// 1-day review cutoff falls on 2026-10-16 local. A task last updated on
// 2026-10-15 UTC is stale; one updated on 2026-10-16 UTC is not.
func TestInbox_ReviewCutoffLocal(t *testing.T) {
	db := testutil.SetupTestDB(t)
	taskRepo := repository.NewTaskRepository(db, nil)
	viewRepo := newMoscowViewRepo(t, db)

	staleWhen, freshWhen := "2026-12-01", "2026-12-02"
	stale, err := taskRepo.Create(model.CreateTaskInput{Title: "Stale", WhenDate: &staleWhen})
	if err != nil {
		t.Fatalf("create stale task: %v", err)
	}
	fresh, err := taskRepo.Create(model.CreateTaskInput{Title: "Fresh", WhenDate: &freshWhen})
	if err != nil {
		t.Fatalf("create fresh task: %v", err)
	}
	if _, err := db.Exec(
		"UPDATE tasks SET updated_at = ? WHERE id = ?", "2026-10-15 12:00:00", stale.ID,
	); err != nil {
		t.Fatalf("pin stale updated_at: %v", err)
	}
	if _, err := db.Exec(
		"UPDATE tasks SET updated_at = ? WHERE id = ?", "2026-10-16 12:00:00", fresh.ID,
	); err != nil {
		t.Fatalf("pin fresh updated_at: %v", err)
	}

	days := 1
	view, err := viewRepo.Inbox(&days, true)
	if err != nil {
		t.Fatalf("inbox view: %v", err)
	}
	review := tzTaskTitles(view.Review)
	if !tzHas(review, "Stale") {
		t.Errorf("task updated 2026-10-15 UTC must be in Review with a local cutoff of 2026-10-16; got %v", review)
	}
	if tzHas(review, "Fresh") {
		t.Errorf("task updated 2026-10-16 UTC must not be in Review with a local cutoff of 2026-10-16; got %v", review)
	}
}
