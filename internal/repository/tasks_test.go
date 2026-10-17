package repository_test

import (
	"database/sql"
	"encoding/json"
	"testing"

	"github.com/collinjanssen/thingstodo/internal/model"
	"github.com/collinjanssen/thingstodo/internal/repository"
	"github.com/collinjanssen/thingstodo/internal/testutil"
)

func strPtr(s string) *string { return &s }

func TestTaskCreate(t *testing.T) {
	db := testutil.SetupTestDB(t)
	repo := repository.NewTaskRepository(db, nil)

	task, err := repo.Create(model.CreateTaskInput{
		Title: "Buy groceries",
		Notes: "Milk, eggs, bread",
	})
	if err != nil {
		t.Fatalf("failed to create task: %v", err)
	}
	if task.Title != "Buy groceries" {
		t.Errorf("expected title 'Buy groceries', got %q", task.Title)
	}
	if task.Notes != "Milk, eggs, bread" {
		t.Errorf("expected notes 'Milk, eggs, bread', got %q", task.Notes)
	}
	if task.Status != "open" {
		t.Errorf("expected status 'open', got %q", task.Status)
	}
	if task.ID == "" {
		t.Error("expected non-empty ID")
	}
}

func TestTaskCreateWithProject(t *testing.T) {
	db := testutil.SetupTestDB(t)
	repo := repository.NewTaskRepository(db, nil)

	// Create an area and project first.
	_, _ = db.Exec("INSERT INTO areas (id, title) VALUES ('a1', 'Test Area')")
	_, _ = db.Exec("INSERT INTO projects (id, title, area_id) VALUES ('p1', 'My Project', 'a1')")

	task, err := repo.Create(model.CreateTaskInput{
		Title:     "Project task",
		ProjectID: strPtr("p1"),
	})
	if err != nil {
		t.Fatalf("failed to create task: %v", err)
	}
	if task.ProjectID == nil || *task.ProjectID != "p1" {
		t.Error("expected project_id to be 'p1'")
	}
	if task.Project == nil || task.Project.Title != "My Project" {
		t.Error("expected project ref to be populated")
	}
}

func TestTaskCreateWithTags(t *testing.T) {
	db := testutil.SetupTestDB(t)
	repo := repository.NewTaskRepository(db, nil)

	_, _ = db.Exec("INSERT INTO tags (id, title) VALUES ('tag1', 'urgent')")
	_, _ = db.Exec("INSERT INTO tags (id, title) VALUES ('tag2', 'home')")

	task, err := repo.Create(model.CreateTaskInput{
		Title:  "Tagged task",
		TagIDs: []string{"tag1", "tag2"},
	})
	if err != nil {
		t.Fatalf("failed to create task: %v", err)
	}
	if len(task.Tags) != 2 {
		t.Fatalf("expected 2 tags, got %d", len(task.Tags))
	}
}

func TestTaskGetByID(t *testing.T) {
	db := testutil.SetupTestDB(t)
	repo := repository.NewTaskRepository(db, nil)

	created, err := repo.Create(model.CreateTaskInput{Title: "Test task"})
	if err != nil {
		t.Fatalf("failed to create task: %v", err)
	}

	task, err := repo.GetByID(created.ID)
	if err != nil {
		t.Fatalf("failed to get task: %v", err)
	}
	if task == nil {
		t.Fatal("expected non-nil task")
	}
	if task.Title != "Test task" {
		t.Errorf("expected title 'Test task', got %q", task.Title)
	}
}

func TestTaskGetByIDNotFound(t *testing.T) {
	db := testutil.SetupTestDB(t)
	repo := repository.NewTaskRepository(db, nil)

	task, err := repo.GetByID("nonexistent")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if task != nil {
		t.Error("expected nil task for nonexistent ID")
	}
}

func TestTaskUpdate(t *testing.T) {
	db := testutil.SetupTestDB(t)
	repo := repository.NewTaskRepository(db, nil)

	created, _ := repo.Create(model.CreateTaskInput{Title: "Original"})

	newTitle := "Updated"
	updated, err := repo.Update(created.ID, model.UpdateTaskInput{
		Title: &newTitle,
	})
	if err != nil {
		t.Fatalf("failed to update task: %v", err)
	}
	if updated.Title != "Updated" {
		t.Errorf("expected title 'Updated', got %q", updated.Title)
	}
}

func TestTaskDelete(t *testing.T) {
	db := testutil.SetupTestDB(t)
	repo := repository.NewTaskRepository(db, nil)

	created, _ := repo.Create(model.CreateTaskInput{Title: "To delete"})

	err := repo.Delete(created.ID)
	if err != nil {
		t.Fatalf("failed to delete task: %v", err)
	}

	// Soft-deleted task is still retrievable by ID
	task, _ := repo.GetByID(created.ID)
	if task == nil {
		t.Fatal("expected soft-deleted task to still be retrievable")
	}

	// But it should not appear in List (filtered by deleted_at IS NULL)
	tasks, _ := repo.List(model.TaskFilters{})
	for _, tl := range tasks {
		if tl.ID == created.ID {
			t.Error("soft-deleted task should not appear in list")
		}
	}

	// Restore should clear deleted_at
	restored, err := repo.Restore(created.ID)
	if err != nil {
		t.Fatalf("failed to restore task: %v", err)
	}
	if restored == nil {
		t.Fatal("expected restored task")
	}

	// Now it should appear in List again
	tasks2, _ := repo.List(model.TaskFilters{})
	found := false
	for _, tl := range tasks2 {
		if tl.ID == created.ID {
			found = true
		}
	}
	if !found {
		t.Error("restored task should appear in list")
	}

	// PermanentDelete should remove it entirely
	err = repo.PermanentDelete(created.ID)
	if err != nil {
		t.Fatalf("failed to permanently delete: %v", err)
	}
	gone, _ := repo.GetByID(created.ID)
	if gone != nil {
		t.Error("expected permanently deleted task to be gone")
	}
}

func TestTaskComplete(t *testing.T) {
	db := testutil.SetupTestDB(t)
	repo := repository.NewTaskRepository(db, nil)

	created, _ := repo.Create(model.CreateTaskInput{Title: "To complete"})

	changed, err := repo.SetDoneStatus(created.ID, "completed")
	if err != nil {
		t.Fatalf("failed to complete task: %v", err)
	}
	if !changed {
		t.Error("expected changed=true on the first complete")
	}
	task, _ := repo.GetByID(created.ID)
	if task == nil || task.Status != "completed" {
		t.Fatalf("expected status 'completed', got %+v", task)
	}
	if task.CompletedAt == nil {
		t.Error("expected completed_at to be set")
	}

	// The guard: finishing an already-finished task changes nothing.
	again, err := repo.SetDoneStatus(created.ID, "completed")
	if err != nil {
		t.Fatalf("second complete: %v", err)
	}
	if again {
		t.Error("expected changed=false on the second complete")
	}
}

func TestTaskCancel(t *testing.T) {
	db := testutil.SetupTestDB(t)
	repo := repository.NewTaskRepository(db, nil)

	created, _ := repo.Create(model.CreateTaskInput{Title: "To cancel"})

	changed, err := repo.SetDoneStatus(created.ID, "canceled")
	if err != nil {
		t.Fatalf("failed to cancel task: %v", err)
	}
	if !changed {
		t.Error("expected changed=true on the first cancel")
	}
	task, _ := repo.GetByID(created.ID)
	if task == nil || task.Status != "canceled" {
		t.Fatalf("expected status 'canceled', got %+v", task)
	}
	if task.CanceledAt == nil {
		t.Error("expected canceled_at to be set")
	}
}

func TestTaskWontDo(t *testing.T) {
	db := testutil.SetupTestDB(t)
	repo := repository.NewTaskRepository(db, nil)

	created, _ := repo.Create(model.CreateTaskInput{Title: "Won't do"})

	changed, err := repo.SetDoneStatus(created.ID, "wont_do")
	if err != nil {
		t.Fatalf("failed to wont_do task: %v", err)
	}
	if !changed {
		t.Error("expected changed=true on the first wont_do")
	}
	task, _ := repo.GetByID(created.ID)
	if task == nil || task.Status != "wont_do" {
		t.Fatalf("expected status 'wont_do', got %+v", task)
	}
}

func TestTaskReopen(t *testing.T) {
	db := testutil.SetupTestDB(t)
	repo := repository.NewTaskRepository(db, nil)

	created, _ := repo.Create(model.CreateTaskInput{Title: "Reopen me"})
	_, _ = repo.SetDoneStatus(created.ID, "completed")

	task, err := repo.Reopen(created.ID)
	if err != nil {
		t.Fatalf("failed to reopen task: %v", err)
	}
	if task.Status != "open" {
		t.Errorf("expected status 'open', got %q", task.Status)
	}
	if task.CompletedAt != nil {
		t.Error("expected completed_at to be cleared")
	}
}

func TestTaskListEmpty(t *testing.T) {
	db := testutil.SetupTestDB(t)
	repo := repository.NewTaskRepository(db, nil)

	tasks, err := repo.List(model.TaskFilters{})
	if err != nil {
		t.Fatalf("failed to list tasks: %v", err)
	}
	if len(tasks) != 0 {
		t.Errorf("expected 0 tasks, got %d", len(tasks))
	}
}

func TestTaskListAll(t *testing.T) {
	db := testutil.SetupTestDB(t)
	repo := repository.NewTaskRepository(db, nil)

	_, _ = repo.Create(model.CreateTaskInput{Title: "Task 1"})
	_, _ = repo.Create(model.CreateTaskInput{Title: "Task 2"})
	_, _ = repo.Create(model.CreateTaskInput{Title: "Task 3"})

	tasks, err := repo.List(model.TaskFilters{})
	if err != nil {
		t.Fatalf("failed to list tasks: %v", err)
	}
	if len(tasks) != 3 {
		t.Errorf("expected 3 tasks, got %d", len(tasks))
	}
}

func TestTaskListFilterByStatus(t *testing.T) {
	db := testutil.SetupTestDB(t)
	repo := repository.NewTaskRepository(db, nil)

	t1, _ := repo.Create(model.CreateTaskInput{Title: "Open task"})
	t2, _ := repo.Create(model.CreateTaskInput{Title: "Completed task"})
	_, _ = repo.SetDoneStatus(t2.ID, "completed")
	_ = t1

	tasks, err := repo.List(model.TaskFilters{Status: strPtr("completed")})
	if err != nil {
		t.Fatalf("failed to list tasks: %v", err)
	}
	if len(tasks) != 1 {
		t.Fatalf("expected 1 completed task, got %d", len(tasks))
	}
	if tasks[0].Status != "completed" {
		t.Errorf("expected status 'completed', got %q", tasks[0].Status)
	}
}

func TestTaskListFilterByProject(t *testing.T) {
	db := testutil.SetupTestDB(t)
	repo := repository.NewTaskRepository(db, nil)

	_, _ = db.Exec("INSERT INTO areas (id, title) VALUES ('a1', 'Test Area')")
	_, _ = db.Exec("INSERT INTO projects (id, title, area_id) VALUES ('p1', 'Project 1', 'a1')")
	_, _ = repo.Create(model.CreateTaskInput{Title: "Project task", ProjectID: strPtr("p1")})
	_, _ = repo.Create(model.CreateTaskInput{Title: "No project task"})

	tasks, err := repo.List(model.TaskFilters{ProjectID: strPtr("p1")})
	if err != nil {
		t.Fatalf("failed to list tasks: %v", err)
	}
	if len(tasks) != 1 {
		t.Fatalf("expected 1 task, got %d", len(tasks))
	}
	if tasks[0].Title != "Project task" {
		t.Errorf("expected 'Project task', got %q", tasks[0].Title)
	}
}

func TestTaskListFilterByTag(t *testing.T) {
	db := testutil.SetupTestDB(t)
	repo := repository.NewTaskRepository(db, nil)

	_, _ = db.Exec("INSERT INTO tags (id, title) VALUES ('tag1', 'urgent')")
	_, _ = repo.Create(model.CreateTaskInput{Title: "Tagged", TagIDs: []string{"tag1"}})
	_, _ = repo.Create(model.CreateTaskInput{Title: "Untagged"})

	tasks, err := repo.List(model.TaskFilters{TagIDs: []string{"tag1"}})
	if err != nil {
		t.Fatalf("failed to list tasks: %v", err)
	}
	if len(tasks) != 1 {
		t.Fatalf("expected 1 task, got %d", len(tasks))
	}
	if tasks[0].Title != "Tagged" {
		t.Errorf("expected 'Tagged', got %q", tasks[0].Title)
	}
}

func TestTaskListFilterByWhenDate(t *testing.T) {
	db := testutil.SetupTestDB(t)
	repo := repository.NewTaskRepository(db, nil)

	_, _ = repo.Create(model.CreateTaskInput{Title: "Today", WhenDate: strPtr("2026-02-15")})
	_, _ = repo.Create(model.CreateTaskInput{Title: "Tomorrow", WhenDate: strPtr("2026-02-16")})
	_, _ = repo.Create(model.CreateTaskInput{Title: "No date"})

	tasks, err := repo.List(model.TaskFilters{WhenDate: strPtr("2026-02-15")})
	if err != nil {
		t.Fatalf("failed to list tasks: %v", err)
	}
	if len(tasks) != 1 {
		t.Fatalf("expected 1 task, got %d", len(tasks))
	}
	if tasks[0].Title != "Today" {
		t.Errorf("expected 'Today', got %q", tasks[0].Title)
	}
}

func TestTaskReorder(t *testing.T) {
	db := testutil.SetupTestDB(t)
	repo := repository.NewTaskRepository(db, nil)

	t1, _ := repo.Create(model.CreateTaskInput{Title: "Task 1"})
	t2, _ := repo.Create(model.CreateTaskInput{Title: "Task 2"})

	err := repo.Reorder([]model.ReorderItem{
		{ID: t1.ID, SortField: "sort_order_today", SortOrder: 200},
		{ID: t2.ID, SortField: "sort_order_today", SortOrder: 100},
	})
	if err != nil {
		t.Fatalf("failed to reorder: %v", err)
	}

	// After reorder, Task 2 should come first (lower sort_order).
	tasks, _ := repo.List(model.TaskFilters{})
	if len(tasks) != 2 {
		t.Fatalf("expected 2 tasks, got %d", len(tasks))
	}
	if tasks[0].Title != "Task 2" {
		t.Errorf("expected Task 2 first, got %q", tasks[0].Title)
	}
}

func TestTaskReorderInvalidField(t *testing.T) {
	db := testutil.SetupTestDB(t)
	repo := repository.NewTaskRepository(db, nil)

	t1, _ := repo.Create(model.CreateTaskInput{Title: "Task 1"})

	err := repo.Reorder([]model.ReorderItem{
		{ID: t1.ID, SortField: "invalid_field", SortOrder: 100},
	})
	if err == nil {
		t.Error("expected error for invalid sort field")
	}
}

func TestTaskGetByIDWithChecklist(t *testing.T) {
	db := testutil.SetupTestDB(t)
	repo := repository.NewTaskRepository(db, nil)

	created, _ := repo.Create(model.CreateTaskInput{Title: "With checklist"})
	_, _ = db.Exec("INSERT INTO checklist_items (id, task_id, title, sort_order) VALUES ('c1', ?, 'Item 1', 1)", created.ID)
	_, _ = db.Exec("INSERT INTO checklist_items (id, task_id, title, completed, sort_order) VALUES ('c2', ?, 'Item 2', 1, 2)", created.ID)

	task, err := repo.GetByID(created.ID)
	if err != nil {
		t.Fatalf("failed to get task: %v", err)
	}
	if len(task.Checklist) != 2 {
		t.Fatalf("expected 2 checklist items, got %d", len(task.Checklist))
	}
	if task.Checklist[1].Completed != true {
		t.Error("expected second checklist item to be completed")
	}
}

func TestTaskGetByIDWithAttachments(t *testing.T) {
	db := testutil.SetupTestDB(t)
	repo := repository.NewTaskRepository(db, nil)

	created, _ := repo.Create(model.CreateTaskInput{Title: "With attachment"})
	_, _ = db.Exec("INSERT INTO attachments (id, task_id, type, url, sort_order) VALUES ('a1', ?, 'link', 'https://example.com', 1)", created.ID)

	task, err := repo.GetByID(created.ID)
	if err != nil {
		t.Fatalf("failed to get task: %v", err)
	}
	if len(task.Attachments) != 1 {
		t.Fatalf("expected 1 attachment, got %d", len(task.Attachments))
	}
	if task.Attachments[0].URL != "https://example.com" {
		t.Errorf("expected URL 'https://example.com', got %q", task.Attachments[0].URL)
	}
}

func TestTaskListItemMetadata(t *testing.T) {
	db := testutil.SetupTestDB(t)
	repo := repository.NewTaskRepository(db, nil)

	created, _ := repo.Create(model.CreateTaskInput{
		Title: "With metadata",
		Notes: "Has notes",
	})
	_, _ = db.Exec("INSERT INTO checklist_items (id, task_id, title, sort_order) VALUES ('c1', ?, 'Item', 1)", created.ID)
	_, _ = db.Exec("INSERT INTO attachments (id, task_id, type, url, sort_order) VALUES ('a1', ?, 'link', 'https://example.com', 1)", created.ID)

	tasks, err := repo.List(model.TaskFilters{})
	if err != nil {
		t.Fatalf("failed to list tasks: %v", err)
	}
	if len(tasks) != 1 {
		t.Fatalf("expected 1 task, got %d", len(tasks))
	}
	task := tasks[0]
	if !task.HasNotes {
		t.Error("expected has_notes to be true")
	}
	if !task.HasLinks {
		t.Error("expected has_links to be true")
	}
	if task.ChecklistCount != 1 {
		t.Errorf("expected checklist_count=1, got %d", task.ChecklistCount)
	}
}

// TestBulkAction_StatusActionsRejected pins that the repository has no
// status-writing bulk path: finishing a task goes through the scheduler's
// guarded FinishTask (TaskRepository.SetDoneStatus), never BulkAction.
func TestBulkAction_StatusActionsRejected(t *testing.T) {
	db := testutil.SetupTestDB(t)
	repo := repository.NewTaskRepository(db, nil)

	created, _ := repo.Create(model.CreateTaskInput{Title: "Bulk guard"})

	for _, action := range []string{"complete", "cancel", "wontdo"} {
		affected, err := repo.BulkAction(model.BulkActionInput{TaskIDs: []string{created.ID}, Action: action})
		if err == nil {
			t.Errorf("bulk action %q must be rejected by the repository", action)
		}
		if affected != 0 {
			t.Errorf("bulk action %q: expected affected=0, got %d", action, affected)
		}
	}

	// The task must be untouched by the rejected actions.
	task, _ := repo.GetByID(created.ID)
	if task == nil || task.Status != "open" {
		t.Fatalf("task should still be open, got %+v", task)
	}
}

// setupParentFixture inserts two areas, a project in the first area and a
// heading in that project; tests reference the fixed ids directly.
func setupParentFixture(t *testing.T, db *sql.DB) {
	t.Helper()
	_, err := db.Exec("INSERT INTO areas (id, title) VALUES ('a1', 'Area One')")
	if err != nil {
		t.Fatalf("insert area a1: %v", err)
	}
	_, err = db.Exec("INSERT INTO areas (id, title) VALUES ('a2', 'Area Two')")
	if err != nil {
		t.Fatalf("insert area a2: %v", err)
	}
	_, err = db.Exec("INSERT INTO projects (id, title, area_id) VALUES ('p1', 'Project One', 'a1')")
	if err != nil {
		t.Fatalf("insert project p1: %v", err)
	}
	_, err = db.Exec("INSERT INTO headings (id, title, project_id, sort_order) VALUES ('h1', 'Heading One', 'p1', 0)")
	if err != nil {
		t.Fatalf("insert heading h1: %v", err)
	}
}

// countTaskChangeLog returns the number of change_log rows for the given
// task ids with the given action.
func countTaskChangeLog(t *testing.T, db *sql.DB, action string, ids ...string) int {
	t.Helper()
	query := "SELECT COUNT(*) FROM change_log WHERE entity = 'task' AND action = ? AND entity_id IN ("
	args := []interface{}{action}
	for i, id := range ids {
		if i > 0 {
			query += ", "
		}
		query += "?"
		args = append(args, id)
	}
	query += ")"
	var n int
	if err := db.QueryRow(query, args...).Scan(&n); err != nil {
		t.Fatalf("count change log: %v", err)
	}
	return n
}

// TestTaskCreate_AreaFollowsProject pins D5: a task created in a project
// stores the project's area, not the area_id the request carried.
func TestTaskCreate_AreaFollowsProject(t *testing.T) {
	db := testutil.SetupTestDB(t)
	repo := repository.NewTaskRepository(db, nil)
	setupParentFixture(t, db)

	task, err := repo.Create(model.CreateTaskInput{
		Title:     "Area follows project",
		ProjectID: strPtr("p1"),
		AreaID:    strPtr("a2"), // contradicts the project's area
	})
	if err != nil {
		t.Fatalf("failed to create task: %v", err)
	}
	if task.ProjectID == nil || *task.ProjectID != "p1" {
		t.Errorf("expected project_id 'p1', got %v", task.ProjectID)
	}
	if task.AreaID == nil || *task.AreaID != "a1" {
		t.Errorf("expected area_id 'a1' (the project's area), got %v", task.AreaID)
	}
}

// TestProjectAreaChange_UpdatesTasks pins D5: changing a project's area
// updates its tasks' area_id in the same transaction, one change-log entry
// per affected task.
func TestProjectAreaChange_UpdatesTasks(t *testing.T) {
	db := testutil.SetupTestDB(t)
	cl := repository.NewChangeLogRepository(db)
	projRepo := repository.NewProjectRepository(db, cl)
	taskRepo := repository.NewTaskRepository(db, cl)
	setupParentFixture(t, db)

	t1, err := taskRepo.Create(model.CreateTaskInput{Title: "T1", ProjectID: strPtr("p1")})
	if err != nil {
		t.Fatalf("create t1: %v", err)
	}
	t2, err := taskRepo.Create(model.CreateTaskInput{Title: "T2", ProjectID: strPtr("p1")})
	if err != nil {
		t.Fatalf("create t2: %v", err)
	}

	if _, err := projRepo.Update("p1", model.UpdateProjectInput{
		AreaID: strPtr("a2"),
		Raw:    map[string]json.RawMessage{"area_id": []byte(`"a2"`)},
	}); err != nil {
		t.Fatalf("update project area: %v", err)
	}

	for _, id := range []string{t1.ID, t2.ID} {
		task, err := taskRepo.GetByID(id)
		if err != nil {
			t.Fatalf("get task %s: %v", id, err)
		}
		if task.AreaID == nil || *task.AreaID != "a2" {
			t.Errorf("task %s: expected area_id 'a2' after project change, got %v", id, task.AreaID)
		}
	}
	if n := countTaskChangeLog(t, db, "update", t1.ID, t2.ID); n != 2 {
		t.Errorf("expected exactly 2 task change-log entries, got %d", n)
	}
}

// TestBackfillAreas_IdempotentAndLogged pins the startup backfill: corrupted
// rows are aligned with their project's area, each change is logged, and a
// second run changes nothing.
func TestBackfillAreas_IdempotentAndLogged(t *testing.T) {
	db := testutil.SetupTestDB(t)
	cl := repository.NewChangeLogRepository(db)
	repo := repository.NewTaskRepository(db, cl)
	setupParentFixture(t, db)

	t1, err := repo.Create(model.CreateTaskInput{Title: "T1", ProjectID: strPtr("p1")})
	if err != nil {
		t.Fatalf("create t1: %v", err)
	}
	t2, err := repo.Create(model.CreateTaskInput{Title: "T2", ProjectID: strPtr("p1")})
	if err != nil {
		t.Fatalf("create t2: %v", err)
	}

	// Corrupt both rows behind the repository's back.
	if _, err := db.Exec("UPDATE tasks SET area_id = 'a2' WHERE id IN (?, ?)", t1.ID, t2.ID); err != nil {
		t.Fatalf("corrupt rows: %v", err)
	}

	changed, err := repo.BackfillAreasFromProjects()
	if err != nil {
		t.Fatalf("backfill: %v", err)
	}
	if changed != 2 {
		t.Errorf("expected 2 changed rows on first run, got %d", changed)
	}
	for _, id := range []string{t1.ID, t2.ID} {
		task, err := repo.GetByID(id)
		if err != nil {
			t.Fatalf("get task %s: %v", id, err)
		}
		if task.AreaID == nil || *task.AreaID != "a1" {
			t.Errorf("task %s: expected area_id 'a1' after backfill, got %v", id, task.AreaID)
		}
	}
	if n := countTaskChangeLog(t, db, "update", t1.ID, t2.ID); n != 2 {
		t.Errorf("expected exactly 2 task change-log entries, got %d", n)
	}

	changed, err = repo.BackfillAreasFromProjects()
	if err != nil {
		t.Fatalf("second backfill: %v", err)
	}
	if changed != 0 {
		t.Errorf("expected 0 changed rows on second run, got %d", changed)
	}
	if n := countTaskChangeLog(t, db, "update", t1.ID, t2.ID); n != 2 {
		t.Errorf("second run must not log: expected 2 entries, got %d", n)
	}
}
