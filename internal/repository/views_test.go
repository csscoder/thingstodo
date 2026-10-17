package repository_test

import (
	"database/sql"
	"os"
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

// --- Task 15: consistent views and shared list predicates (E3, C2) ---

// The fixed clock pins today to 2026-10-17 (UTC), tomorrow 2026-10-18 and
// yesterday 2026-10-16.
var (
	vpToday     = "2026-10-17"
	vpTomorrow  = "2026-10-18"
	vpYesterday = "2026-10-16"
)

func newFixedClockViewRepo(t *testing.T, db *sql.DB) *repository.ViewRepository {
	t.Helper()
	repo := repository.NewViewRepository(db, time.UTC)
	repo.SetClock(func() time.Time {
		return time.Date(2026, 10, 17, 12, 0, 0, 0, time.UTC)
	})
	return repo
}

// vpTaskIDs lists the ids of a task slice.
func vpTaskIDs(tasks []model.TaskListItem) []string {
	ids := make([]string, 0, len(tasks))
	for _, task := range tasks {
		ids = append(ids, task.ID)
	}
	return ids
}

// vpTodayOpenIDs collects the ids of the four open sections of the Today
// view (Today, This Evening, Overdue, Earlier), duplicates included.
func vpTodayOpenIDs(v *model.TodayView) []string {
	var ids []string
	for _, section := range v.Sections {
		for _, group := range section.Groups {
			ids = append(ids, vpTaskIDs(group.Tasks)...)
		}
	}
	ids = append(ids, vpTaskIDs(v.Overdue)...)
	ids = append(ids, vpTaskIDs(v.Earlier)...)
	return ids
}

// vpUpcomingIDs collects the ids of every Upcoming section (Overdue, the
// date groups and Earlier), duplicates included.
func vpUpcomingIDs(v *model.UpcomingView) []string {
	var ids []string
	ids = append(ids, vpTaskIDs(v.Overdue)...)
	for _, group := range v.Dates {
		ids = append(ids, vpTaskIDs(group.Tasks)...)
	}
	ids = append(ids, vpTaskIDs(v.Earlier)...)
	return ids
}

// vpAnytimeIDs collects the ids of an Anytime or Someday view.
func vpAnytimeIDs(v *model.AnytimeView) []string {
	var ids []string
	for _, area := range v.Areas {
		for _, project := range area.Projects {
			ids = append(ids, vpTaskIDs(project.Tasks)...)
		}
		ids = append(ids, vpTaskIDs(area.StandaloneTasks)...)
	}
	for _, project := range v.NoArea.Projects {
		ids = append(ids, vpTaskIDs(project.Tasks)...)
	}
	ids = append(ids, vpTaskIDs(v.NoArea.StandaloneTasks)...)
	return ids
}

// vpAnytimeTitles lists the titles of an Anytime or Someday view.
func vpAnytimeTitles(v *model.AnytimeView) []string {
	titles := make([]string, 0)
	for _, area := range v.Areas {
		for _, project := range area.Projects {
			for _, task := range project.Tasks {
				titles = append(titles, task.Title)
			}
		}
		for _, task := range area.StandaloneTasks {
			titles = append(titles, task.Title)
		}
	}
	for _, project := range v.NoArea.Projects {
		for _, task := range project.Tasks {
			titles = append(titles, task.Title)
		}
	}
	for _, task := range v.NoArea.StandaloneTasks {
		titles = append(titles, task.Title)
	}
	return titles
}

// vpUpcomingDateTitles lists the titles inside the Upcoming date groups.
func vpUpcomingDateTitles(v *model.UpcomingView) []string {
	var titles []string
	for _, group := range v.Dates {
		for _, task := range group.Tasks {
			titles = append(titles, task.Title)
		}
	}
	return titles
}

// vpGroupTaskIDs collects the ids of grouped view tasks (Logbook, Trash).
func vpGroupTaskIDs(groups []model.DateGroup) []string {
	var ids []string
	for _, group := range groups {
		ids = append(ids, vpTaskIDs(group.Tasks)...)
	}
	return ids
}

// vpDistinct removes duplicate ids while keeping order.
func vpDistinct(ids []string) []string {
	seen := make(map[string]bool, len(ids))
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		if !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	return out
}

// vpOccurrences counts how often id appears in ids.
func vpOccurrences(ids []string, id string) int {
	count := 0
	for _, candidate := range ids {
		if candidate == id {
			count++
		}
	}
	return count
}

// vpStripScheduleRows removes every task_schedules row of the task while
// keeping when_date set: the shape pre-019 rows and sync pushes produce, and
// the input for the synthetic-schedule rule.
func vpStripScheduleRows(t *testing.T, db *sql.DB, taskID string) {
	t.Helper()
	if _, err := db.Exec("DELETE FROM task_schedules WHERE task_id = ?", taskID); err != nil {
		t.Fatalf("strip schedule rows of %s: %v", taskID, err)
	}
}

// vpApplyMigration029 runs the zero-padding migration SQL verbatim.
func vpApplyMigration029(t *testing.T, db *sql.DB) {
	t.Helper()
	content, err := os.ReadFile("../database/migrations/029_zero_pad_schedule_times.sql")
	if err != nil {
		t.Fatalf("read migration 029: %v", err)
	}
	if _, err := db.Exec(string(content)); err != nil {
		t.Fatalf("apply migration 029: %v", err)
	}
}

// TestViews_DeadlineOnlyLooseTask pins E3: a task with a deadline but no
// project, area or when_date is in Inbox, never in Anytime, and lands in
// Overdue (deadline yesterday), Today (deadline today) or neither
// (deadline tomorrow).
func TestViews_DeadlineOnlyLooseTask(t *testing.T) {
	db := testutil.SetupTestDB(t)
	taskRepo := repository.NewTaskRepository(db, nil)
	viewRepo := newFixedClockViewRepo(t, db)

	fixtures := []struct{ title, deadline string }{
		{"Deadline yesterday", vpYesterday},
		{"Deadline today", vpToday},
		{"Deadline tomorrow", vpTomorrow},
	}
	for _, f := range fixtures {
		deadline := f.deadline
		if _, err := taskRepo.Create(model.CreateTaskInput{Title: f.title, Deadline: &deadline}); err != nil {
			t.Fatalf("create %s: %v", f.title, err)
		}
	}

	inbox, err := viewRepo.Inbox(nil, true)
	if err != nil {
		t.Fatalf("inbox view: %v", err)
	}
	inboxTitles := tzTaskTitles(inbox.Tasks)
	for _, f := range fixtures {
		if !tzHas(inboxTitles, f.title) {
			t.Errorf("deadline-only loose task %q must be in Inbox; inbox has %v", f.title, inboxTitles)
		}
	}

	anytime, err := viewRepo.Anytime()
	if err != nil {
		t.Fatalf("anytime view: %v", err)
	}
	anytimeTitles := vpAnytimeTitles(anytime)
	for _, f := range fixtures {
		if tzHas(anytimeTitles, f.title) {
			t.Errorf("deadline-only loose task %q must never be in Anytime; anytime has %v", f.title, anytimeTitles)
		}
	}

	tv, err := viewRepo.Today("18:00")
	if err != nil {
		t.Fatalf("today view: %v", err)
	}
	dayTitles := tzTodayTitles(tv)
	overdueTitles := tzTaskTitles(tv.Overdue)
	if !tzHas(overdueTitles, "Deadline yesterday") {
		t.Errorf("deadline yesterday must be in Overdue; got %v", overdueTitles)
	}
	if !tzHas(dayTitles, "Deadline today") {
		t.Errorf("deadline today must be in the Today section; got %v", dayTitles)
	}
	if tzHas(overdueTitles, "Deadline today") || tzHas(overdueTitles, "Deadline tomorrow") {
		t.Errorf("deadline today/tomorrow must not be in Overdue; got %v", overdueTitles)
	}
	if tzHas(dayTitles, "Deadline yesterday") || tzHas(dayTitles, "Deadline tomorrow") {
		t.Errorf("deadline yesterday/tomorrow must not be in the Today section; got %v", dayTitles)
	}

	up, err := viewRepo.Upcoming("")
	if err != nil {
		t.Fatalf("upcoming view: %v", err)
	}
	dateTitles := vpUpcomingDateTitles(up)
	earlierTitles := tzTaskTitles(up.Earlier)
	for _, f := range fixtures {
		if tzHas(dateTitles, f.title) {
			t.Errorf("deadline-only loose task %q must not be in Upcoming dates; got %v", f.title, dateTitles)
		}
		if tzHas(earlierTitles, f.title) {
			t.Errorf("deadline-only loose task %q must not be in Upcoming Earlier; got %v", f.title, earlierTitles)
		}
	}
}

// TestViews_PastDateWithoutSchedulesInEarlier pins E3: Earlier holds every
// open task with when_date < today, not someday, regardless of schedule
// rows — including one with no rows at all.
func TestViews_PastDateWithoutSchedulesInEarlier(t *testing.T) {
	db := testutil.SetupTestDB(t)
	taskRepo := repository.NewTaskRepository(db, nil)
	viewRepo := newFixedClockViewRepo(t, db)

	when := vpYesterday
	task, err := taskRepo.Create(model.CreateTaskInput{Title: "Past without rows", WhenDate: &when})
	if err != nil {
		t.Fatalf("create task: %v", err)
	}
	vpStripScheduleRows(t, db, task.ID)

	tv, err := viewRepo.Today("18:00")
	if err != nil {
		t.Fatalf("today view: %v", err)
	}
	if titles := tzTaskTitles(tv.Earlier); !tzHas(titles, "Past without rows") {
		t.Errorf("rowless task dated %s must be in Today's Earlier; got %v", vpYesterday, titles)
	}
	if titles := tzTodayTitles(tv); tzHas(titles, "Past without rows") {
		t.Errorf("rowless past task must not be in the Today section; got %v", titles)
	}
	if titles := tzTaskTitles(tv.Overdue); tzHas(titles, "Past without rows") {
		t.Errorf("rowless past task without deadline must not be in Overdue; got %v", titles)
	}

	up, err := viewRepo.Upcoming("")
	if err != nil {
		t.Fatalf("upcoming view: %v", err)
	}
	if titles := tzTaskTitles(up.Earlier); !tzHas(titles, "Past without rows") {
		t.Errorf("rowless task dated %s must be in Upcoming's Earlier; got %v", vpYesterday, titles)
	}
	if titles := vpUpcomingDateTitles(up); tzHas(titles, "Past without rows") {
		t.Errorf("rowless past task must not be in Upcoming dates; got %v", titles)
	}
}

// TestViews_FutureDateWithoutScheduleInUpcoming pins E3: a task with a
// when_date and no schedule rows counts as one uncompleted schedule on that
// when_date, so a rowless future task appears in Upcoming.
func TestViews_FutureDateWithoutScheduleInUpcoming(t *testing.T) {
	db := testutil.SetupTestDB(t)
	taskRepo := repository.NewTaskRepository(db, nil)
	viewRepo := newFixedClockViewRepo(t, db)

	when := vpTomorrow
	task, err := taskRepo.Create(model.CreateTaskInput{Title: "Future without rows", WhenDate: &when})
	if err != nil {
		t.Fatalf("create task: %v", err)
	}
	vpStripScheduleRows(t, db, task.ID)

	up, err := viewRepo.Upcoming("")
	if err != nil {
		t.Fatalf("upcoming view: %v", err)
	}
	var found bool
	for _, group := range up.Dates {
		for _, item := range group.Tasks {
			if item.ID == task.ID {
				found = true
				if group.Date != vpTomorrow {
						t.Errorf("rowless future task grouped under %q, want %q", group.Date, vpTomorrow)
				}
			}
		}
	}
	if !found {
		t.Errorf("rowless task dated %s must be in Upcoming dates; got groups %v", vpTomorrow, tzGroupDates(up.Dates))
	}

	tv, err := viewRepo.Today("18:00")
	if err != nil {
		t.Fatalf("today view: %v", err)
	}
	if ids := vpTodayOpenIDs(tv); vpOccurrences(ids, task.ID) != 0 {
		t.Errorf("future task must not be in any Today section")
	}
}

// TestViews_TodayAllSchedulesDoneInNoList pins the single documented
// exception to placement: a task dated today whose schedule rows all exist
// and are completed is in no list at all.
func TestViews_TodayAllSchedulesDoneInNoList(t *testing.T) {
	db := testutil.SetupTestDB(t)
	taskRepo := repository.NewTaskRepository(db, nil)
	viewRepo := newFixedClockViewRepo(t, db)

	when := vpToday
	task, err := taskRepo.Create(model.CreateTaskInput{Title: "Today all schedules done", WhenDate: &when})
	if err != nil {
		t.Fatalf("create task: %v", err)
	}
	if _, err := db.Exec("UPDATE task_schedules SET completed = 1 WHERE task_id = ?", task.ID); err != nil {
		t.Fatalf("complete schedule row: %v", err)
	}

	tv, err := viewRepo.Today("18:00")
	if err != nil {
		t.Fatalf("today view: %v", err)
	}
	if ids := vpTodayOpenIDs(tv); vpOccurrences(ids, task.ID) != 0 {
		t.Errorf("task with all today schedules completed must be in no Today section; got %v", ids)
	}

	inbox, err := viewRepo.Inbox(nil, true)
	if err != nil {
		t.Fatalf("inbox view: %v", err)
	}
	if ids := vpTaskIDs(inbox.Tasks); vpOccurrences(ids, task.ID) != 0 {
		t.Errorf("task with when_date must not be in Inbox; got %v", ids)
	}

	anytime, err := viewRepo.Anytime()
	if err != nil {
		t.Fatalf("anytime view: %v", err)
	}
	if ids := vpAnytimeIDs(anytime); vpOccurrences(ids, task.ID) != 0 {
		t.Errorf("task with when_date must not be in Anytime; got %v", ids)
	}

	someday, err := viewRepo.Someday()
	if err != nil {
		t.Fatalf("someday view: %v", err)
	}
	if ids := vpAnytimeIDs(someday); vpOccurrences(ids, task.ID) != 0 {
		t.Errorf("task dated today must not be in Someday; got %v", ids)
	}

	up, err := viewRepo.Upcoming("")
	if err != nil {
		t.Fatalf("upcoming view: %v", err)
	}
	if ids := vpUpcomingIDs(up); vpOccurrences(ids, task.ID) != 0 {
		t.Errorf("task with all today schedules completed must be in no Upcoming section; got %v", ids)
	}

	counts, err := viewRepo.Counts(nil, true)
	if err != nil {
		t.Fatalf("counts: %v", err)
	}
	if counts.Today != 0 || counts.Inbox != 0 || counts.Anytime != 0 || counts.Someday != 0 {
		t.Errorf("task with all today schedules completed must not be counted; got %+v", counts)
	}
}

// TestViews_EveryOpenTaskPlaced pins E3 placement: every open, non-trashed
// task is in at least one of Inbox, Today, Upcoming, Anytime or Someday —
// except the task whose today schedules are all completed.
func TestViews_EveryOpenTaskPlaced(t *testing.T) {
	db := testutil.SetupTestDB(t)
	taskRepo := repository.NewTaskRepository(db, nil)
	scheduleRepo := repository.NewScheduleRepository(db, nil)
	viewRepo := newFixedClockViewRepo(t, db)

	if _, err := db.Exec("INSERT INTO areas (id, title) VALUES ('vp_a1', 'Area')"); err != nil {
		t.Fatalf("insert area: %v", err)
	}
	if _, err := db.Exec("INSERT INTO projects (id, title, area_id) VALUES ('vp_p1', 'Project', 'vp_a1')"); err != nil {
		t.Fatalf("insert project: %v", err)
	}

	someday := "someday"
	eveningStart := "19:00"
	eveningEnd := "20:00"
	fixtures := []model.CreateTaskInput{
		{Title: "loose undated"},
		{Title: "loose deadline yesterday", Deadline: &vpYesterday},
		{Title: "loose deadline today", Deadline: &vpToday},
		{Title: "loose deadline tomorrow", Deadline: &vpTomorrow},
		{Title: "past without rows", WhenDate: &vpYesterday},
		{Title: "past with row", WhenDate: &vpYesterday},
		{Title: "past all completed", WhenDate: &vpYesterday},
		{Title: "today without rows", WhenDate: &vpToday},
		{Title: "today with row", WhenDate: &vpToday},
		{Title: "today all completed", WhenDate: &vpToday}, // the exception
		{Title: "today day and evening", WhenDate: &vpToday},
		{Title: "future without rows", WhenDate: &vpTomorrow},
		{Title: "future with row", WhenDate: &vpTomorrow},
		{Title: "loose someday", WhenDate: &someday},
		{Title: "project someday", WhenDate: &someday, ProjectID: strPtr("vp_p1")},
		{Title: "project undated", ProjectID: strPtr("vp_p1")},
		{Title: "area standalone undated", AreaID: strPtr("vp_a1")},
		{Title: "multi past and future", WhenDate: &vpYesterday},
		{Title: "overdue and earlier", WhenDate: &vpYesterday, Deadline: &vpYesterday},
	}
	created := make(map[string]string, len(fixtures)) // title -> id
	for _, f := range fixtures {
		task, err := taskRepo.Create(f)
		if err != nil {
			t.Fatalf("create %s: %v", f.Title, err)
		}
		created[f.Title] = task.ID
	}

	// Row-level tweaks the create API cannot express.
	vpStripScheduleRows(t, db, created["past without rows"])
	vpStripScheduleRows(t, db, created["today without rows"])
	vpStripScheduleRows(t, db, created["future without rows"])
	if _, err := db.Exec("UPDATE task_schedules SET completed = 1 WHERE task_id = ?", created["past all completed"]); err != nil {
		t.Fatalf("complete past rows: %v", err)
	}
	if _, err := db.Exec("UPDATE task_schedules SET completed = 1 WHERE task_id = ?", created["today all completed"]); err != nil {
		t.Fatalf("complete today rows: %v", err)
	}
	if _, err := scheduleRepo.Create(created["today day and evening"], model.CreateTaskScheduleInput{
		WhenDate: vpToday, StartTime: &eveningStart, EndTime: &eveningEnd,
	}); err != nil {
		t.Fatalf("add evening schedule: %v", err)
	}
	if _, err := scheduleRepo.Create(created["multi past and future"], model.CreateTaskScheduleInput{
		WhenDate: vpTomorrow,
	}); err != nil {
		t.Fatalf("add future schedule: %v", err)
	}

	placed := make(map[string]bool)
	mark := func(ids []string) {
		for _, id := range ids {
			placed[id] = true
		}
	}
	inbox, err := viewRepo.Inbox(nil, true)
	if err != nil {
		t.Fatalf("inbox view: %v", err)
	}
	mark(vpTaskIDs(inbox.Tasks))
	tv, err := viewRepo.Today("18:00")
	if err != nil {
		t.Fatalf("today view: %v", err)
	}
	mark(vpTodayOpenIDs(tv))
	up, err := viewRepo.Upcoming("")
	if err != nil {
		t.Fatalf("upcoming view: %v", err)
	}
	mark(vpUpcomingIDs(up))
	anytime, err := viewRepo.Anytime()
	if err != nil {
		t.Fatalf("anytime view: %v", err)
	}
	mark(vpAnytimeIDs(anytime))
	somedayView, err := viewRepo.Someday()
	if err != nil {
		t.Fatalf("someday view: %v", err)
	}
	mark(vpAnytimeIDs(somedayView))

	exception := created["today all completed"]
	for title, id := range created {
		if id == exception {
			if placed[id] {
				t.Errorf("exception task %q must be in no list", title)
			}
			continue
		}
		if !placed[id] {
			t.Errorf("open task %q is in no list", title)
		}
	}
}

// TestCounts_DistinctTasks pins C2: every count equals the distinct task ids
// of its view, so a task with a day and an evening schedule today and a task
// in both Overdue and Earlier each count once.
func TestCounts_DistinctTasks(t *testing.T) {
	db := testutil.SetupTestDB(t)
	taskRepo := repository.NewTaskRepository(db, nil)
	scheduleRepo := repository.NewScheduleRepository(db, nil)
	viewRepo := newFixedClockViewRepo(t, db)

	if _, err := db.Exec("INSERT INTO areas (id, title) VALUES ('vp_a1', 'Area')"); err != nil {
		t.Fatalf("insert area: %v", err)
	}
	if _, err := db.Exec("INSERT INTO projects (id, title, area_id) VALUES ('vp_p1', 'Project', 'vp_a1')"); err != nil {
		t.Fatalf("insert project: %v", err)
	}

	dayEvening, err := taskRepo.Create(model.CreateTaskInput{Title: "Day and evening today", WhenDate: &vpToday})
	if err != nil {
		t.Fatalf("create day+evening task: %v", err)
	}
	eveningStart, eveningEnd := "19:00", "20:00"
	if _, err := scheduleRepo.Create(dayEvening.ID, model.CreateTaskScheduleInput{
		WhenDate: vpToday, StartTime: &eveningStart, EndTime: &eveningEnd,
	}); err != nil {
		t.Fatalf("add evening schedule: %v", err)
	}
	overdueEarlier, err := taskRepo.Create(model.CreateTaskInput{
		Title: "Overdue and earlier", WhenDate: &vpYesterday, Deadline: &vpYesterday,
	})
	if err != nil {
		t.Fatalf("create overdue+earlier task: %v", err)
	}
	someday := "someday"
	for _, f := range []model.CreateTaskInput{
		{Title: "Loose"},
		{Title: "Project undated", ProjectID: strPtr("vp_p1")},
		{Title: "Someday", WhenDate: &someday},
		{Title: "Past", WhenDate: &vpYesterday},
		{Title: "Future", WhenDate: &vpTomorrow},
	} {
		if _, err := taskRepo.Create(f); err != nil {
			t.Fatalf("create %s: %v", f.Title, err)
		}
	}
	done, err := taskRepo.Create(model.CreateTaskInput{Title: "Done"})
	if err != nil {
		t.Fatalf("create done task: %v", err)
	}
	if _, err := taskRepo.SetDoneStatus(done.ID, "completed"); err != nil {
		t.Fatalf("complete task: %v", err)
	}
	deleted, err := taskRepo.Create(model.CreateTaskInput{Title: "Deleted"})
	if err != nil {
		t.Fatalf("create deleted task: %v", err)
	}
	if _, err := db.Exec("UPDATE tasks SET deleted_at = datetime('now') WHERE id = ?", deleted.ID); err != nil {
		t.Fatalf("trash task: %v", err)
	}

	counts, err := viewRepo.Counts(nil, true)
	if err != nil {
		t.Fatalf("counts: %v", err)
	}

	inbox, err := viewRepo.Inbox(nil, true)
	if err != nil {
		t.Fatalf("inbox view: %v", err)
	}
	tv, err := viewRepo.Today("18:00")
	if err != nil {
		t.Fatalf("today view: %v", err)
	}
	anytime, err := viewRepo.Anytime()
	if err != nil {
		t.Fatalf("anytime view: %v", err)
	}
	somedayView, err := viewRepo.Someday()
	if err != nil {
		t.Fatalf("someday view: %v", err)
	}
	logbook, err := viewRepo.Logbook(50, 0)
	if err != nil {
		t.Fatalf("logbook view: %v", err)
	}
	trash, err := viewRepo.Trash(50, 0)
	if err != nil {
		t.Fatalf("trash view: %v", err)
	}

	for _, tc := range []struct {
		name string
		got  int
		ids  []string
	}{
		{"inbox", counts.Inbox, vpDistinct(vpTaskIDs(inbox.Tasks))},
		{"today", counts.Today, vpDistinct(vpTodayOpenIDs(tv))},
		{"overdue", counts.Overdue, vpDistinct(vpTaskIDs(tv.Overdue))},
		{"anytime", counts.Anytime, vpDistinct(vpAnytimeIDs(anytime))},
		{"someday", counts.Someday, vpDistinct(vpAnytimeIDs(somedayView))},
		{"logbook", counts.Logbook, vpDistinct(vpGroupTaskIDs(logbook.Groups))},
		{"trash", counts.Trash, vpDistinct(vpGroupTaskIDs(trash.Groups))},
	} {
		if tc.got != len(tc.ids) {
			t.Errorf("count %s = %d, but the view holds %d distinct ids", tc.name, tc.got, len(tc.ids))
		}
	}

	// The day+evening task appears on two Today rows but counts once.
	if raw := vpTodayOpenIDs(tv); vpOccurrences(raw, dayEvening.ID) != 2 {
		t.Errorf("fixture error: day+evening task should appear on two Today rows, got %d", vpOccurrences(raw, dayEvening.ID))
	}
	// The overdue+earlier task is in both sections and counts once in each.
	if ids := vpTaskIDs(tv.Overdue); vpOccurrences(ids, overdueEarlier.ID) != 1 {
		t.Errorf("overdue+earlier task must be in Overdue once")
	}
	if ids := vpTaskIDs(tv.Earlier); vpOccurrences(ids, overdueEarlier.ID) != 1 {
		t.Errorf("overdue+earlier task must be in Earlier once")
	}
}

// TestUpcoming_StartsTomorrowAndEarlierRelativeToToday pins E3: the default
// from is tomorrow, an explicit from still overrides, and the Overdue and
// Earlier sections stay relative to today.
func TestUpcoming_StartsTomorrowAndEarlierRelativeToToday(t *testing.T) {
	db := testutil.SetupTestDB(t)
	taskRepo := repository.NewTaskRepository(db, nil)
	viewRepo := newFixedClockViewRepo(t, db)

	for _, f := range []model.CreateTaskInput{
		{Title: "Scheduled today", WhenDate: &vpToday},
		{Title: "Scheduled tomorrow", WhenDate: &vpTomorrow},
		{Title: "Scheduled past", WhenDate: &vpYesterday},
		{Title: "Deadline past", Deadline: &vpYesterday},
	} {
		if _, err := taskRepo.Create(f); err != nil {
			t.Fatalf("create %s: %v", f.Title, err)
		}
	}

	up, err := viewRepo.Upcoming("")
	if err != nil {
		t.Fatalf("upcoming view: %v", err)
	}
	for _, group := range up.Dates {
		if group.Date == vpToday {
			t.Errorf("default from must skip today; got a group dated %q", vpToday)
		}
	}
	if len(up.Dates) == 0 || up.Dates[0].Date != vpTomorrow {
		t.Fatalf("first Upcoming group must be %q; got %v", vpTomorrow, tzGroupDates(up.Dates))
	}
	if titles := vpUpcomingDateTitles(up); !tzHas(titles, "Scheduled tomorrow") {
		t.Errorf("Scheduled tomorrow must be in Upcoming dates; got %v", titles)
	}
	if titles := vpUpcomingDateTitles(up); tzHas(titles, "Scheduled today") {
		t.Errorf("Scheduled today must not be in Upcoming dates with the default from; got %v", titles)
	}
	if titles := tzTaskTitles(up.Earlier); tzHas(titles, "Scheduled today") {
		t.Errorf("Scheduled today must not be in Upcoming's Earlier; got %v", titles)
	}
	if titles := tzTaskTitles(up.Earlier); !tzHas(titles, "Scheduled past") {
		t.Errorf("Scheduled past must be in Upcoming's Earlier (relative to today); got %v", titles)
	}
	if titles := tzTaskTitles(up.Overdue); !tzHas(titles, "Deadline past") {
		t.Errorf("Deadline past must be in Upcoming's Overdue (relative to today); got %v", titles)
	}

	// An explicit from is still honored.
	fromToday, err := viewRepo.Upcoming(vpToday)
	if err != nil {
		t.Fatalf("upcoming view: %v", err)
	}
	if len(fromToday.Dates) == 0 || fromToday.Dates[0].Date != vpToday {
		t.Fatalf("explicit from=%s must be honored; got %v", vpToday, tzGroupDates(fromToday.Dates))
	}
	if titles := vpUpcomingDateTitles(fromToday); !tzHas(titles, "Scheduled today") {
		t.Errorf("Scheduled today must be in Upcoming dates with explicit from=%s; got %v", vpToday, titles)
	}
	if titles := tzTaskTitles(fromToday.Earlier); tzHas(titles, "Scheduled today") {
		t.Errorf("Earlier must stay relative to today with an explicit from; got %v", titles)
	}
	if titles := tzTaskTitles(fromToday.Earlier); !tzHas(titles, "Scheduled past") {
		t.Errorf("Scheduled past must stay in Earlier with an explicit from; got %v", titles)
	}
}

// TestToday_Legacy900NotEvening pins D1/E3: after migration 029 zero-pads a
// legacy 9:00 start time to 09:00, the entry belongs to the Today section,
// not This Evening.
func TestToday_Legacy900NotEvening(t *testing.T) {
	db := testutil.SetupTestDB(t)
	taskRepo := repository.NewTaskRepository(db, nil)
	viewRepo := newFixedClockViewRepo(t, db)

	when := vpToday
	task, err := taskRepo.Create(model.CreateTaskInput{Title: "Legacy nine o'clock", WhenDate: &when})
	if err != nil {
		t.Fatalf("create task: %v", err)
	}
	if _, err := db.Exec("UPDATE task_schedules SET start_time = '9:00' WHERE task_id = ?", task.ID); err != nil {
		t.Fatalf("plant legacy start_time: %v", err)
	}
	vpApplyMigration029(t, db)

	var start string
	if err := db.QueryRow("SELECT start_time FROM task_schedules WHERE task_id = ?", task.ID).Scan(&start); err != nil {
		t.Fatalf("read start_time: %v", err)
	}
	if start != "09:00" {
		t.Fatalf("migration 029 must store 09:00, got %q", start)
	}

	tv, err := viewRepo.Today("18:00")
	if err != nil {
		t.Fatalf("today view: %v", err)
	}
	if titles := tzTodayTitles(tv); !tzHas(titles, "Legacy nine o'clock") {
		t.Errorf("a 09:00 schedule belongs to the Today section; got %v", titles)
	}
	if len(tv.Sections) < 2 {
		t.Fatalf("expected the This Evening section; got %d sections", len(tv.Sections))
	}
	var eveningTitles []string
	for _, group := range tv.Sections[1].Groups {
		for _, item := range group.Tasks {
				eveningTitles = append(eveningTitles, item.Title)
		}
	}
	if tzHas(eveningTitles, "Legacy nine o'clock") {
		t.Errorf("a 09:00 schedule must not be in This Evening; got %v", eveningTitles)
	}
}
