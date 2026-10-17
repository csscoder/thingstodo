package handler_test

import (
	"database/sql"
	"net/http"
	"testing"

	"github.com/collinjanssen/thingstodo/internal/handler"
	"github.com/collinjanssen/thingstodo/internal/model"
	"github.com/collinjanssen/thingstodo/internal/repository"
	"github.com/collinjanssen/thingstodo/internal/sse"
	"github.com/collinjanssen/thingstodo/internal/testutil"
	"github.com/go-chi/chi/v5"
)

func setupProjectRouter(t *testing.T) (*testutil.TestClient, *sql.DB) {
	t.Helper()
	db := testutil.SetupTestDB(t)
	broker := sse.NewBroker()
	projRepo := repository.NewProjectRepository(db, nil)
	projHandler := handler.NewProjectHandler(projRepo, broker)
	areaRepo := repository.NewAreaRepository(db, nil)
	areaHandler := handler.NewAreaHandler(areaRepo, broker)

	r := chi.NewRouter()
	r.Route("/api/areas", func(r chi.Router) {
		r.Post("/", areaHandler.Create)
	})
	r.Route("/api/projects", func(r chi.Router) {
		r.Get("/", projHandler.List)
		r.Post("/", projHandler.Create)
		r.Route("/{id}", func(r chi.Router) {
			r.Get("/", projHandler.Get)
			r.Patch("/", projHandler.Update)
			r.Delete("/", projHandler.Delete)
			r.Patch("/complete", projHandler.Complete)
		})
	})

	return testutil.NewTestClient(t, r), db
}

// createTestArea creates an area and returns its ID.
func createTestArea(t *testing.T, client *testutil.TestClient) string {
	t.Helper()
	resp := client.Post("/api/areas", map[string]string{"title": "Test Area"})
	testutil.AssertStatus(t, resp, http.StatusCreated)
	var area map[string]interface{}
	resp.JSON(t, &area)
	return area["id"].(string)
}

func TestProjectHandlerCreate(t *testing.T) {
	client, _ := setupProjectRouter(t)
	areaID := createTestArea(t, client)

	resp := client.Post("/api/projects", map[string]string{"title": "My Project", "area_id": areaID})
	testutil.AssertStatus(t, resp, http.StatusCreated)
	testutil.AssertJSONField(t, resp, "title", "My Project")
	testutil.AssertJSONField(t, resp, "status", "open")
}

func TestProjectHandlerCreateMissingTitle(t *testing.T) {
	client, _ := setupProjectRouter(t)
	areaID := createTestArea(t, client)

	resp := client.Post("/api/projects", map[string]string{"area_id": areaID})
	testutil.AssertStatus(t, resp, http.StatusBadRequest)
}

func TestProjectHandlerCreateMissingArea(t *testing.T) {
	client, _ := setupProjectRouter(t)

	resp := client.Post("/api/projects", map[string]string{"title": "No Area"})
	testutil.AssertStatus(t, resp, http.StatusBadRequest)
}

func TestProjectHandlerGet(t *testing.T) {
	client, _ := setupProjectRouter(t)
	areaID := createTestArea(t, client)

	createResp := client.Post("/api/projects", map[string]string{"title": "Test", "area_id": areaID})
	var created map[string]interface{}
	createResp.JSON(t, &created)

	resp := client.Get("/api/projects/" + created["id"].(string))
	testutil.AssertStatus(t, resp, http.StatusOK)
	testutil.AssertJSONField(t, resp, "title", "Test")
}

func TestProjectHandlerGetNotFound(t *testing.T) {
	client, _ := setupProjectRouter(t)

	resp := client.Get("/api/projects/nonexistent")
	testutil.AssertStatus(t, resp, http.StatusNotFound)
}

func TestProjectHandlerUpdate(t *testing.T) {
	client, _ := setupProjectRouter(t)
	areaID := createTestArea(t, client)

	createResp := client.Post("/api/projects", map[string]string{"title": "Original", "area_id": areaID})
	var created map[string]interface{}
	createResp.JSON(t, &created)

	resp := client.Patch("/api/projects/"+created["id"].(string), map[string]string{"title": "Updated"})
	testutil.AssertStatus(t, resp, http.StatusOK)
	testutil.AssertJSONField(t, resp, "title", "Updated")
}

func TestProjectHandlerDelete(t *testing.T) {
	client, _ := setupProjectRouter(t)
	areaID := createTestArea(t, client)

	createResp := client.Post("/api/projects", map[string]string{"title": "To delete", "area_id": areaID})
	var created map[string]interface{}
	createResp.JSON(t, &created)

	resp := client.Delete("/api/projects/" + created["id"].(string))
	testutil.AssertStatus(t, resp, http.StatusNoContent)

	getResp := client.Get("/api/projects/" + created["id"].(string))
	testutil.AssertStatus(t, getResp, http.StatusNotFound)
}

func TestProjectHandlerComplete(t *testing.T) {
	client, _ := setupProjectRouter(t)
	areaID := createTestArea(t, client)

	createResp := client.Post("/api/projects", map[string]string{"title": "To complete", "area_id": areaID})
	var created map[string]interface{}
	createResp.JSON(t, &created)

	resp := client.Patch("/api/projects/"+created["id"].(string)+"/complete", nil)
	testutil.AssertStatus(t, resp, http.StatusOK)
	testutil.AssertJSONField(t, resp, "status", "completed")
}

func TestProjectHandlerList(t *testing.T) {
	client, _ := setupProjectRouter(t)
	areaID := createTestArea(t, client)

	client.Post("/api/projects", map[string]string{"title": "Project 1", "area_id": areaID})
	client.Post("/api/projects", map[string]string{"title": "Project 2", "area_id": areaID})

	resp := client.Get("/api/projects")
	testutil.AssertStatus(t, resp, http.StatusOK)

	var body map[string]interface{}
	resp.JSON(t, &body)
	projects := body["projects"].([]interface{})
	if len(projects) != 2 {
		t.Errorf("expected 2 projects, got %d", len(projects))
	}
}

func TestProjectHandlerListEmpty(t *testing.T) {
	client, _ := setupProjectRouter(t)

	resp := client.Get("/api/projects")
	testutil.AssertStatus(t, resp, http.StatusOK)

	var body map[string]interface{}
	resp.JSON(t, &body)
	projects := body["projects"].([]interface{})
	if len(projects) != 0 {
		t.Errorf("expected 0 projects, got %d", len(projects))
	}
}

func TestProjectUpdate_RejectsUnknownStatus(t *testing.T) {
	client, _ := setupProjectRouter(t)
	areaID := createTestArea(t, client)

	createResp := client.Post("/api/projects", map[string]string{"title": "Status test", "area_id": areaID})
	var created map[string]interface{}
	createResp.JSON(t, &created)

	resp := client.Patch("/api/projects/"+created["id"].(string), map[string]string{"status": "archived"})
	testutil.AssertStatus(t, resp, http.StatusBadRequest)
	testutil.AssertJSONField(t, resp, "code", "VALIDATION")
	testutil.AssertJSONField(t, resp, "field", "status")
}

// setupProjectTrashRouter wires areas, projects, task create/restore and the
// trash view to one database and change log, for the delete-to-trash tests.
func setupProjectTrashRouter(t *testing.T) (*testutil.TestClient, *sql.DB, *repository.TaskRepository) {
	t.Helper()
	db := testutil.SetupTestDB(t)
	broker := sse.NewBroker()
	changeLogRepo := repository.NewChangeLogRepository(db)
	projRepo := repository.NewProjectRepository(db, changeLogRepo)
	projHandler := handler.NewProjectHandler(projRepo, broker)
	areaRepo := repository.NewAreaRepository(db, changeLogRepo)
	areaHandler := handler.NewAreaHandler(areaRepo, broker)
	taskRepo := repository.NewTaskRepository(db, changeLogRepo)
	reminderRepo := repository.NewReminderRepository(db, changeLogRepo)
	settingsRepo := repository.NewUserSettingsRepository(db)
	taskHandler := handler.NewTaskHandler(taskRepo, reminderRepo, settingsRepo, broker, nil)
	viewHandler := handler.NewViewHandler(repository.NewViewRepository(db), settingsRepo)

	r := chi.NewRouter()
	r.Route("/api/areas", func(r chi.Router) {
		r.Post("/", areaHandler.Create)
	})
	r.Route("/api/projects", func(r chi.Router) {
		r.Post("/", projHandler.Create)
		r.Route("/{id}", func(r chi.Router) {
			r.Get("/", projHandler.Get)
			r.Delete("/", projHandler.Delete)
		})
	})
	r.Route("/api/tasks", func(r chi.Router) {
		r.Post("/", taskHandler.Create)
		r.Patch("/{id}/restore", taskHandler.Restore)
	})
	r.Get("/api/views/trash", viewHandler.Trash)

	return testutil.NewTestClient(t, r), db, taskRepo
}

// countChangeLogEntries counts change_log rows for one entity/action.
func countChangeLogEntries(t *testing.T, db *sql.DB, entity, entityID, action string) int {
	t.Helper()
	var n int
	if err := db.QueryRow(
		"SELECT COUNT(*) FROM change_log WHERE entity = ? AND entity_id = ? AND action = ?",
		entity, entityID, action,
	).Scan(&n); err != nil {
		t.Fatalf("count change_log entries: %v", err)
	}
	return n
}

// trashViewTasks reads GET /api/views/trash and maps each listed task id to
// its JSON object.
func trashViewTasks(t *testing.T, client *testutil.TestClient) map[string]map[string]interface{} {
	t.Helper()
	resp := client.Get("/api/views/trash")
	testutil.AssertStatus(t, resp, http.StatusOK)
	var view struct {
		Groups []struct {
			Tasks []map[string]interface{} `json:"tasks"`
		} `json:"groups"`
	}
	resp.JSON(t, &view)
	tasks := make(map[string]map[string]interface{})
	for _, g := range view.Groups {
		for _, task := range g.Tasks {
			id, _ := task["id"].(string)
			tasks[id] = task
		}
	}
	return tasks
}

func TestDeleteProject_TasksGoToTrash(t *testing.T) {
	client, db, taskRepo := setupProjectTrashRouter(t)
	areaID := createTestArea(t, client)

	createResp := client.Post("/api/projects", map[string]string{"title": "Doomed", "area_id": areaID})
	var created map[string]interface{}
	createResp.JSON(t, &created)
	projectID := created["id"].(string)

	taskA, err := taskRepo.Create(model.CreateTaskInput{Title: "Child A", ProjectID: &projectID})
	if err != nil {
		t.Fatalf("create child task A: %v", err)
	}
	taskB, err := taskRepo.Create(model.CreateTaskInput{Title: "Child B", ProjectID: &projectID, AreaID: &areaID})
	if err != nil {
		t.Fatalf("create child task B: %v", err)
	}

	resp := client.Delete("/api/projects/" + projectID)
	testutil.AssertStatus(t, resp, http.StatusNoContent)

	trashed := trashViewTasks(t, client)
	for _, task := range []*model.TaskDetail{taskA, taskB} {
		row, ok := trashed[task.ID]
		if !ok {
			t.Errorf("task %s (%q) not listed in trash view", task.ID, task.Title)
			continue
		}
		if pid, present := row["project_id"]; present && pid != nil {
			t.Errorf("trashed task %s still has project_id %v", task.ID, pid)
		}
		if aid, present := row["area_id"]; present && aid != nil {
			t.Errorf("trashed task %s still has area_id %v", task.ID, aid)
		}
		if n := countChangeLogEntries(t, db, "task", task.ID, "delete"); n != 1 {
			t.Errorf("expected 1 task delete change for %s, got %d", task.ID, n)
		}
	}
	if n := countChangeLogEntries(t, db, "project", projectID, "delete"); n != 1 {
		t.Errorf("expected 1 project delete change, got %d", n)
	}
}

func TestRestoreTrashedProjectTask_IsLoose(t *testing.T) {
	client, db, taskRepo := setupProjectTrashRouter(t)
	areaID := createTestArea(t, client)

	createResp := client.Post("/api/projects", map[string]string{"title": "Vanishing", "area_id": areaID})
	var created map[string]interface{}
	createResp.JSON(t, &created)
	projectID := created["id"].(string)

	task, err := taskRepo.Create(model.CreateTaskInput{Title: "Loose after restore", ProjectID: &projectID, AreaID: &areaID})
	if err != nil {
		t.Fatalf("create task: %v", err)
	}

	delResp := client.Delete("/api/projects/" + projectID)
	testutil.AssertStatus(t, delResp, http.StatusNoContent)

	restoreResp := client.Patch("/api/tasks/"+task.ID+"/restore", nil)
	testutil.AssertStatus(t, restoreResp, http.StatusOK)

	var restored map[string]interface{}
	restoreResp.JSON(t, &restored)
	if restored["status"] != "open" {
		t.Errorf("expected restored task status open, got %v", restored["status"])
	}
	if pid, ok := restored["project_id"]; !ok || pid != nil {
		t.Errorf("expected restored task without project, got %v", pid)
	}
	if aid, ok := restored["area_id"]; !ok || aid != nil {
		t.Errorf("expected restored task without area, got %v", aid)
	}

	var deletedAt sql.NullString
	if err := db.QueryRow("SELECT deleted_at FROM tasks WHERE id = ?", task.ID).Scan(&deletedAt); err != nil {
		t.Fatalf("load task: %v", err)
	}
	if deletedAt.Valid {
		t.Errorf("restored task still has deleted_at %q", deletedAt.String)
	}
}
