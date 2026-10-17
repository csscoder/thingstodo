package handler_test

import (
	"database/sql"
	"net/http"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/collinjanssen/thingstodo/internal/handler"
	"github.com/collinjanssen/thingstodo/internal/repository"
	"github.com/collinjanssen/thingstodo/internal/sse"
	"github.com/collinjanssen/thingstodo/internal/testutil"
	"github.com/go-chi/chi/v5"
)

// setupIdempotencyRouter mounts every create endpoint in scope for Task 6
// with the change log wired, so tests can assert that a replayed create
// writes no change-log entry.
func setupIdempotencyRouter(t *testing.T) (*testutil.TestClient, *sql.DB) {
	t.Helper()
	db := testutil.SetupTestDB(t)
	broker := sse.NewBroker()
	changeLog := repository.NewChangeLogRepository(db)
	taskRepo := repository.NewTaskRepository(db, changeLog)
	projectRepo := repository.NewProjectRepository(db, changeLog)
	areaRepo := repository.NewAreaRepository(db, changeLog)
	tagRepo := repository.NewTagRepository(db, changeLog)
	checklistRepo := repository.NewChecklistRepository(db, changeLog)
	headingRepo := repository.NewHeadingRepository(db, changeLog)
	attachmentRepo := repository.NewAttachmentRepository(db, changeLog)
	scheduleRepo := repository.NewScheduleRepository(db, changeLog)
	reminderRepo := repository.NewReminderRepository(db, changeLog)
	settingsRepo := repository.NewUserSettingsRepository(db)

	areaH := handler.NewAreaHandler(areaRepo, broker)
	projectH := handler.NewProjectHandler(projectRepo, broker)
	taskH := handler.NewTaskHandler(taskRepo, reminderRepo, settingsRepo, broker, nil)
	tagH := handler.NewTagHandler(tagRepo, broker)
	headingH := handler.NewHeadingHandler(headingRepo, broker)
	checklistH := handler.NewChecklistHandler(checklistRepo, broker)
	scheduleH := handler.NewScheduleHandler(scheduleRepo, taskRepo, broker)
	reminderH := handler.NewReminderHandler(reminderRepo, broker, time.UTC)
	attachmentH := handler.NewAttachmentHandler(attachmentRepo, broker, filepath.Join(t.TempDir(), "attachments"), 25<<20)

	r := chi.NewRouter()
	r.Route("/api", func(r chi.Router) {
		r.Post("/tasks", taskH.Create)
		r.Post("/projects", projectH.Create)
		r.Post("/areas", areaH.Create)
		r.Post("/tags", tagH.Create)
		r.Post("/projects/{id}/headings", headingH.Create)
		r.Post("/tasks/{id}/checklist", checklistH.Create)
		r.Post("/tasks/{id}/schedules", scheduleH.Create)
		r.Post("/tasks/{id}/reminders", reminderH.Create)
		r.Post("/tasks/{id}/attachments", attachmentH.Create)
	})

	return testutil.NewTestClient(t, r), db
}

// requireIdempotentCreate runs the replay contract for one create endpoint:
// the first POST with a fresh client id returns 201, a second POST with the
// same id and one changed field returns 200 with the unchanged stored
// entity, the table still holds exactly one row for that id, and the change
// log did not grow.
func requireIdempotentCreate(t *testing.T, client *testutil.TestClient, db *sql.DB, url, table string, first, second map[string]interface{}) {
	t.Helper()

	resp := client.Post(url, first)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("first POST: expected 201, got %d\nbody: %s", resp.StatusCode, resp.Body)
	}
	var stored map[string]interface{}
	resp.JSON(t, &stored)

	var logBefore int
	if err := db.QueryRow("SELECT COUNT(*) FROM change_log").Scan(&logBefore); err != nil {
		t.Fatalf("count change_log: %v", err)
	}

	replay := client.Post(url, second)
	if replay.StatusCode != http.StatusOK {
		t.Fatalf("replay POST: expected 200, got %d\nbody: %s", replay.StatusCode, replay.Body)
	}
	var replayed map[string]interface{}
	replay.JSON(t, &replayed)
	if !reflect.DeepEqual(stored, replayed) {
		t.Errorf("replay body must equal the stored entity\nstored:  %s\nreplayed: %s", resp.Body, replay.Body)
	}

	var rows int
	if err := db.QueryRow("SELECT COUNT(*) FROM "+table+" WHERE id = ?", first["id"]).Scan(&rows); err != nil {
		t.Fatalf("count %s: %v", table, err)
	}
	if rows != 1 {
		t.Errorf("expected 1 row in %s for id %v, got %d", table, first["id"], rows)
	}

	var logAfter int
	if err := db.QueryRow("SELECT COUNT(*) FROM change_log").Scan(&logAfter); err != nil {
		t.Fatalf("count change_log: %v", err)
	}
	if logAfter != logBefore {
		t.Errorf("replay wrote %d change-log entries", logAfter-logBefore)
	}
}

// createIdempotencyParent posts the parent entity a nested create needs and
// returns its server-generated id.
func createIdempotencyParent(t *testing.T, client *testutil.TestClient, url string, body map[string]interface{}) string {
	t.Helper()
	resp := client.Post(url, body)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create parent %s: expected 201, got %d\nbody: %s", url, resp.StatusCode, resp.Body)
	}
	var created map[string]interface{}
	resp.JSON(t, &created)
	return created["id"].(string)
}

// TestCreate_RepeatWithSameIDReturnsExisting covers every create endpoint
// that accepts a client-supplied id: repeating a create with an id that
// already exists returns 200 with the stored entity instead of duplicating
// or failing. The changed field is the title where one exists, when_date for
// schedules, type for reminders and url for attachments.
func TestCreate_RepeatWithSameIDReturnsExisting(t *testing.T) {
	cases := []struct {
		name string
		run  func(t *testing.T)
	}{
		{"tasks", func(t *testing.T) {
			client, db := setupIdempotencyRouter(t)
			requireIdempotentCreate(t, client, db, "/api/tasks", "tasks",
				map[string]interface{}{"id": "idemTask0001", "title": "Original title"},
				map[string]interface{}{"id": "idemTask0001", "title": "Changed title"})
		}},
		{"projects", func(t *testing.T) {
			client, db := setupIdempotencyRouter(t)
			areaID := createIdempotencyParent(t, client, "/api/areas", map[string]interface{}{"title": "Area"})
			requireIdempotentCreate(t, client, db, "/api/projects", "projects",
				map[string]interface{}{"id": "idemProject01", "title": "Original project", "area_id": areaID},
				map[string]interface{}{"id": "idemProject01", "title": "Changed project", "area_id": areaID})
		}},
		{"areas", func(t *testing.T) {
			client, db := setupIdempotencyRouter(t)
			requireIdempotentCreate(t, client, db, "/api/areas", "areas",
				map[string]interface{}{"id": "idemArea00001", "title": "Original area"},
				map[string]interface{}{"id": "idemArea00001", "title": "Changed area"})
		}},
		{"tags", func(t *testing.T) {
			client, db := setupIdempotencyRouter(t)
			requireIdempotentCreate(t, client, db, "/api/tags", "tags",
				map[string]interface{}{"id": "idemTag00001", "title": "originaltag"},
				map[string]interface{}{"id": "idemTag00001", "title": "changedtag"})
		}},
		{"headings", func(t *testing.T) {
			client, db := setupIdempotencyRouter(t)
			areaID := createIdempotencyParent(t, client, "/api/areas", map[string]interface{}{"title": "Area"})
			projectID := createIdempotencyParent(t, client, "/api/projects", map[string]interface{}{"title": "Project", "area_id": areaID})
			requireIdempotentCreate(t, client, db, "/api/projects/"+projectID+"/headings", "headings",
				map[string]interface{}{"id": "idemHeading01", "title": "Original heading"},
				map[string]interface{}{"id": "idemHeading01", "title": "Changed heading"})
		}},
		{"checklist items", func(t *testing.T) {
			client, db := setupIdempotencyRouter(t)
			taskID := createIdempotencyParent(t, client, "/api/tasks", map[string]interface{}{"title": "Parent task"})
			requireIdempotentCreate(t, client, db, "/api/tasks/"+taskID+"/checklist", "checklist_items",
				map[string]interface{}{"id": "idemCheck0001", "title": "Original item"},
				map[string]interface{}{"id": "idemCheck0001", "title": "Changed item"})
		}},
		{"schedules", func(t *testing.T) {
			client, db := setupIdempotencyRouter(t)
			taskID := createIdempotencyParent(t, client, "/api/tasks", map[string]interface{}{"title": "Parent task"})
			requireIdempotentCreate(t, client, db, "/api/tasks/"+taskID+"/schedules", "task_schedules",
				map[string]interface{}{"id": "idemSched0001", "when_date": "2031-05-10"},
				map[string]interface{}{"id": "idemSched0001", "when_date": "2031-05-11"})
		}},
		{"reminders", func(t *testing.T) {
			client, db := setupIdempotencyRouter(t)
			taskID := createIdempotencyParent(t, client, "/api/tasks", map[string]interface{}{"title": "Parent task"})
			requireIdempotentCreate(t, client, db, "/api/tasks/"+taskID+"/reminders", "reminders",
				map[string]interface{}{"id": "idemReminder01", "type": "minutes_before", "value": 30},
				map[string]interface{}{"id": "idemReminder01", "type": "hours_before", "value": 30})
		}},
		{"link attachments", func(t *testing.T) {
			client, db := setupIdempotencyRouter(t)
			taskID := createIdempotencyParent(t, client, "/api/tasks", map[string]interface{}{"title": "Parent task"})
			requireIdempotentCreate(t, client, db, "/api/tasks/"+taskID+"/attachments", "attachments",
				map[string]interface{}{"id": "idemAttach001", "type": "link", "title": "Spec", "url": "https://example.com/original"},
				map[string]interface{}{"id": "idemAttach001", "type": "link", "title": "Spec", "url": "https://example.com/changed"})
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, tc.run)
	}
}
