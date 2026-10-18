package repository_test

import (
	"testing"
	"time"

	"github.com/collinjanssen/thingstodo/internal/model"
	"github.com/collinjanssen/thingstodo/internal/repository"
	"github.com/collinjanssen/thingstodo/internal/testutil"
)

func TestAreaCreate(t *testing.T) {
	db := testutil.SetupTestDB(t)
	repo := repository.NewAreaRepository(db, nil)

	area, err := repo.Create(model.CreateAreaInput{Title: "Work"})
	if err != nil {
		t.Fatalf("failed to create area: %v", err)
	}
	if area.Title != "Work" {
		t.Errorf("expected title 'Work', got %q", area.Title)
	}
	if area.ID == "" {
		t.Error("expected non-empty ID")
	}
}

func TestAreaGetByID(t *testing.T) {
	db := testutil.SetupTestDB(t)
	repo := repository.NewAreaRepository(db, nil)

	created, _ := repo.Create(model.CreateAreaInput{Title: "Personal"})
	area, err := repo.GetByID(created.ID)
	if err != nil {
		t.Fatalf("failed to get area: %v", err)
	}
	if area == nil {
		t.Fatal("expected non-nil area")
	}
	if area.Title != "Personal" {
		t.Errorf("expected 'Personal', got %q", area.Title)
	}
}

func TestAreaGetByIDNotFound(t *testing.T) {
	db := testutil.SetupTestDB(t)
	repo := repository.NewAreaRepository(db, nil)

	area, err := repo.GetByID("nonexistent")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if area != nil {
		t.Error("expected nil for nonexistent area")
	}
}

func TestAreaGetByIDWithProjects(t *testing.T) {
	db := testutil.SetupTestDB(t)
	areaRepo := repository.NewAreaRepository(db, nil)
	projRepo := repository.NewProjectRepository(db, nil)

	area, _ := areaRepo.Create(model.CreateAreaInput{Title: "Work"})
	_, _ = projRepo.Create(model.CreateProjectInput{Title: "Project 1", AreaID: &area.ID})
	_, _ = projRepo.Create(model.CreateProjectInput{Title: "Project 2", AreaID: &area.ID})

	detail, _ := areaRepo.GetByID(area.ID)
	if len(detail.Projects) != 2 {
		t.Errorf("expected 2 projects, got %d", len(detail.Projects))
	}
}

func TestAreaGetByIDWithTasks(t *testing.T) {
	db := testutil.SetupTestDB(t)
	areaRepo := repository.NewAreaRepository(db, nil)
	taskRepo := repository.NewTaskRepository(db, nil)

	area, _ := areaRepo.Create(model.CreateAreaInput{Title: "Home"})
	_, _ = taskRepo.Create(model.CreateTaskInput{Title: "Standalone task", AreaID: &area.ID})

	detail, _ := areaRepo.GetByID(area.ID)
	if len(detail.Tasks) != 1 {
		t.Errorf("expected 1 standalone task, got %d", len(detail.Tasks))
	}
}

func TestAreaUpdate(t *testing.T) {
	db := testutil.SetupTestDB(t)
	repo := repository.NewAreaRepository(db, nil)

	created, _ := repo.Create(model.CreateAreaInput{Title: "Original"})
	newTitle := "Updated"
	updated, err := repo.Update(created.ID, model.UpdateAreaInput{Title: &newTitle})
	if err != nil {
		t.Fatalf("failed to update: %v", err)
	}
	if updated.Title != "Updated" {
		t.Errorf("expected 'Updated', got %q", updated.Title)
	}
}

func TestAreaDelete(t *testing.T) {
	db := testutil.SetupTestDB(t)
	repo := repository.NewAreaRepository(db, nil)

	created, _ := repo.Create(model.CreateAreaInput{Title: "To delete"})
	err := repo.Delete(created.ID)
	if err != nil {
		t.Fatalf("failed to delete: %v", err)
	}
	area, _ := repo.GetByID(created.ID)
	if area != nil {
		t.Error("expected area to be deleted")
	}
}

func TestAreaDeleteBlockedByProjects(t *testing.T) {
	db := testutil.SetupTestDB(t)
	areaRepo := repository.NewAreaRepository(db, nil)
	projRepo := repository.NewProjectRepository(db, nil)

	area, _ := areaRepo.Create(model.CreateAreaInput{Title: "To delete"})
	_, _ = projRepo.Create(model.CreateProjectInput{Title: "Project", AreaID: &area.ID})

	err := areaRepo.Delete(area.ID)
	if err == nil {
		t.Error("expected error when deleting area with projects")
	}
}

func TestAreaListEmpty(t *testing.T) {
	db := testutil.SetupTestDB(t)
	repo := repository.NewAreaRepository(db, nil)

	areas, err := repo.List()
	if err != nil {
		t.Fatalf("failed to list: %v", err)
	}
	if len(areas) != 0 {
		t.Errorf("expected 0 areas, got %d", len(areas))
	}
}

func TestAreaListWithCounts(t *testing.T) {
	db := testutil.SetupTestDB(t)
	areaRepo := repository.NewAreaRepository(db, nil)
	projRepo := repository.NewProjectRepository(db, nil)
	taskRepo := repository.NewTaskRepository(db, nil)

	area, _ := areaRepo.Create(model.CreateAreaInput{Title: "Work"})
	_, _ = projRepo.Create(model.CreateProjectInput{Title: "Proj", AreaID: &area.ID})
	_, _ = taskRepo.Create(model.CreateTaskInput{Title: "Task", AreaID: &area.ID})

	areas, _ := areaRepo.List()
	if len(areas) != 1 {
		t.Fatalf("expected 1 area, got %d", len(areas))
	}
	if areas[0].ProjectCount != 1 {
		t.Errorf("expected project_count=1, got %d", areas[0].ProjectCount)
	}
	if areas[0].TaskCount != 1 {
		t.Errorf("expected task_count=1, got %d", areas[0].TaskCount)
	}
}

// TestAreaCompletedToday_UsesLocation pins a standalone task completed at
// 2026-10-16 21:10:00 UTC, which is 00:10 on 2026-10-17 in Moscow. With the
// repository clock pinned to 2026-10-16 21:30 UTC (= 00:30 local on
// 2026-10-17) and the repository bound to Europe/Moscow, the task belongs to
// the area's completed-today list even though its UTC date is 2026-10-16.
func TestAreaCompletedToday_UsesLocation(t *testing.T) {
	db := testutil.SetupTestDB(t)
	loc, err := time.LoadLocation("Europe/Moscow")
	if err != nil {
		t.Fatalf("load Europe/Moscow: %v", err)
	}
	areaRepo := repository.NewAreaRepository(db, nil).WithLocation(loc)
	areaRepo.SetClock(func() time.Time {
		return time.Date(2026, 10, 16, 21, 30, 0, 0, time.UTC)
	})
	taskRepo := repository.NewTaskRepository(db, nil)

	area, err := areaRepo.Create(model.CreateAreaInput{Title: "TZ Area"})
	if err != nil {
		t.Fatalf("create area: %v", err)
	}
	task, err := taskRepo.Create(model.CreateTaskInput{Title: "Done after local midnight", AreaID: &area.ID})
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

	detail, err := areaRepo.GetByID(area.ID)
	if err != nil {
		t.Fatalf("get area: %v", err)
	}
	found := false
	for _, ct := range detail.CompletedTasks {
		if ct.ID == task.ID {
			found = true
		}
	}
	if !found {
		t.Errorf("task completed at 00:10 local is missing from the area's completed-today list; got %d entries", len(detail.CompletedTasks))
	}
}

