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

func setupAreaRouter(t *testing.T) *testutil.TestClient {
	t.Helper()
	db := testutil.SetupTestDB(t)
	broker := sse.NewBroker()
	areaRepo := repository.NewAreaRepository(db, nil)
	areaHandler := handler.NewAreaHandler(areaRepo, broker)

	r := chi.NewRouter()
	r.Route("/api/areas", func(r chi.Router) {
		r.Get("/", areaHandler.List)
		r.Post("/", areaHandler.Create)
		r.Route("/{id}", func(r chi.Router) {
			r.Get("/", areaHandler.Get)
			r.Patch("/", areaHandler.Update)
			r.Delete("/", areaHandler.Delete)
		})
	})

	return testutil.NewTestClient(t, r)
}

func TestAreaHandlerCreate(t *testing.T) {
	client := setupAreaRouter(t)

	resp := client.Post("/api/areas", map[string]string{"title": "Work"})
	testutil.AssertStatus(t, resp, http.StatusCreated)
	testutil.AssertJSONField(t, resp, "title", "Work")
}

func TestAreaHandlerCreateMissingTitle(t *testing.T) {
	client := setupAreaRouter(t)

	resp := client.Post("/api/areas", map[string]string{})
	testutil.AssertStatus(t, resp, http.StatusBadRequest)
}

func TestAreaHandlerGet(t *testing.T) {
	client := setupAreaRouter(t)

	createResp := client.Post("/api/areas", map[string]string{"title": "Personal"})
	var created map[string]interface{}
	createResp.JSON(t, &created)

	resp := client.Get("/api/areas/" + created["id"].(string))
	testutil.AssertStatus(t, resp, http.StatusOK)
	testutil.AssertJSONField(t, resp, "title", "Personal")
}

func TestAreaHandlerGetNotFound(t *testing.T) {
	client := setupAreaRouter(t)

	resp := client.Get("/api/areas/nonexistent")
	testutil.AssertStatus(t, resp, http.StatusNotFound)
}

func TestAreaHandlerUpdate(t *testing.T) {
	client := setupAreaRouter(t)

	createResp := client.Post("/api/areas", map[string]string{"title": "Old"})
	var created map[string]interface{}
	createResp.JSON(t, &created)

	resp := client.Patch("/api/areas/"+created["id"].(string), map[string]string{"title": "New"})
	testutil.AssertStatus(t, resp, http.StatusOK)
	testutil.AssertJSONField(t, resp, "title", "New")
}

func TestAreaHandlerDelete(t *testing.T) {
	client := setupAreaRouter(t)

	createResp := client.Post("/api/areas", map[string]string{"title": "To delete"})
	var created map[string]interface{}
	createResp.JSON(t, &created)

	resp := client.Delete("/api/areas/" + created["id"].(string))
	testutil.AssertStatus(t, resp, http.StatusNoContent)

	getResp := client.Get("/api/areas/" + created["id"].(string))
	testutil.AssertStatus(t, getResp, http.StatusNotFound)
}

func TestAreaHandlerList(t *testing.T) {
	client := setupAreaRouter(t)

	client.Post("/api/areas", map[string]string{"title": "Work"})
	client.Post("/api/areas", map[string]string{"title": "Personal"})

	resp := client.Get("/api/areas")
	testutil.AssertStatus(t, resp, http.StatusOK)

	var body map[string]interface{}
	resp.JSON(t, &body)
	areas := body["areas"].([]interface{})
	if len(areas) != 2 {
		t.Errorf("expected 2 areas, got %d", len(areas))
	}
}

// setupAreaDeleteRouter wires areas, projects and the trash view to one
// database and change log, for the area delete tests.
func setupAreaDeleteRouter(t *testing.T) (*testutil.TestClient, *sql.DB, *repository.TaskRepository) {
	t.Helper()
	db := testutil.SetupTestDB(t)
	broker := sse.NewBroker()
	changeLogRepo := repository.NewChangeLogRepository(db)
	areaRepo := repository.NewAreaRepository(db, changeLogRepo)
	areaHandler := handler.NewAreaHandler(areaRepo, broker)
	projRepo := repository.NewProjectRepository(db, changeLogRepo)
	projHandler := handler.NewProjectHandler(projRepo, broker)
	taskRepo := repository.NewTaskRepository(db, changeLogRepo)
	settingsRepo := repository.NewUserSettingsRepository(db)
	viewHandler := handler.NewViewHandler(repository.NewViewRepository(db), settingsRepo)

	r := chi.NewRouter()
	r.Route("/api/areas", func(r chi.Router) {
		r.Post("/", areaHandler.Create)
		r.Route("/{id}", func(r chi.Router) {
			r.Get("/", areaHandler.Get)
			r.Delete("/", areaHandler.Delete)
		})
	})
	r.Post("/api/projects", projHandler.Create)
	r.Get("/api/views/trash", viewHandler.Trash)

	return testutil.NewTestClient(t, r), db, taskRepo
}

func TestDeleteArea_WithProjectsIs409(t *testing.T) {
	client, db, _ := setupAreaDeleteRouter(t)

	createResp := client.Post("/api/areas", map[string]string{"title": "Has projects"})
	var area map[string]interface{}
	createResp.JSON(t, &area)
	areaID := area["id"].(string)

	projResp := client.Post("/api/projects", map[string]string{"title": "Kept project", "area_id": areaID})
	testutil.AssertStatus(t, projResp, http.StatusCreated)
	var project map[string]interface{}
	projResp.JSON(t, &project)

	resp := client.Delete("/api/areas/" + areaID)
	testutil.AssertStatus(t, resp, http.StatusConflict)
	testutil.AssertJSONField(t, resp, "code", "HAS_PROJECTS")

	// Nothing is deleted.
	getResp := client.Get("/api/areas/" + areaID)
	testutil.AssertStatus(t, getResp, http.StatusOK)

	var n int
	if err := db.QueryRow("SELECT COUNT(*) FROM projects WHERE id = ?", project["id"].(string)).Scan(&n); err != nil {
		t.Fatalf("count projects: %v", err)
	}
	if n != 1 {
		t.Errorf("expected project to survive the rejected delete, got %d rows", n)
	}
}

func TestDeleteArea_StandaloneTasksGoToTrash(t *testing.T) {
	client, db, taskRepo := setupAreaDeleteRouter(t)

	createResp := client.Post("/api/areas", map[string]string{"title": "Standalone tasks"})
	var area map[string]interface{}
	createResp.JSON(t, &area)
	areaID := area["id"].(string)

	taskA, err := taskRepo.Create(model.CreateTaskInput{Title: "Standalone A", AreaID: &areaID})
	if err != nil {
		t.Fatalf("create standalone task A: %v", err)
	}
	taskB, err := taskRepo.Create(model.CreateTaskInput{Title: "Standalone B", AreaID: &areaID})
	if err != nil {
		t.Fatalf("create standalone task B: %v", err)
	}

	resp := client.Delete("/api/areas/" + areaID)
	testutil.AssertStatus(t, resp, http.StatusNoContent)

	getResp := client.Get("/api/areas/" + areaID)
	testutil.AssertStatus(t, getResp, http.StatusNotFound)

	trashed := trashViewTasks(t, client)
	for _, task := range []*model.TaskDetail{taskA, taskB} {
		row, ok := trashed[task.ID]
		if !ok {
			t.Errorf("task %s (%q) not listed in trash view", task.ID, task.Title)
			continue
		}
		if aid, present := row["area_id"]; present && aid != nil {
			t.Errorf("trashed task %s still has area_id %v", task.ID, aid)
		}
		if n := countChangeLogEntries(t, db, "task", task.ID, "delete"); n != 1 {
			t.Errorf("expected 1 task delete change for %s, got %d", task.ID, n)
		}
	}
	if n := countChangeLogEntries(t, db, "area", areaID, "delete"); n != 1 {
		t.Errorf("expected 1 area delete change, got %d", n)
	}
}
