package handler_test

import (
	"net/http"
	"testing"
	"time"

	"github.com/collinjanssen/thingstodo/internal/handler"
	"github.com/collinjanssen/thingstodo/internal/repository"
	"github.com/collinjanssen/thingstodo/internal/sse"
	"github.com/collinjanssen/thingstodo/internal/testutil"
	"github.com/go-chi/chi/v5"
)

func setupScheduleRouter(t *testing.T) *testutil.TestClient {
	t.Helper()
	db := testutil.SetupTestDB(t)
	broker := sse.NewBroker()
	changeLog := repository.NewChangeLogRepository(db)
	taskRepo := repository.NewTaskRepository(db, changeLog)
	checklistRepo := repository.NewChecklistRepository(db, changeLog)
	scheduleRepo := repository.NewScheduleRepository(db, changeLog).WithTasks(taskRepo)
	settingsRepo := repository.NewUserSettingsRepository(db)

	taskH := handler.NewTaskHandler(taskRepo, repository.NewReminderRepository(db, changeLog), settingsRepo, broker, nil)
	checklistH := handler.NewChecklistHandler(checklistRepo, broker)
	scheduleH := handler.NewScheduleHandler(scheduleRepo, taskRepo, broker)

	r := chi.NewRouter()
	r.Post("/api/tasks", taskH.Create)
	r.Post("/api/tasks/{id}/schedules", scheduleH.Create)
	r.Get("/api/tasks/{id}/schedules", scheduleH.List)
	r.Patch("/api/tasks/{id}/schedules/reorder", scheduleH.Reorder)
	r.Post("/api/tasks/{id}/checklist", checklistH.Create)

	return testutil.NewTestClient(t, r)
}

func createTaskForSchedule(t *testing.T, client *testutil.TestClient) string {
	t.Helper()
	resp := client.Post("/api/tasks", map[string]string{"title": "Schedule test task"})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create task failed: %d %s", resp.StatusCode, string(resp.Body))
	}
	var created map[string]interface{}
	resp.JSON(t, &created)
	return created["id"].(string)
}

func TestScheduleCreate_RejectsBadTimes(t *testing.T) {
	client := setupScheduleRouter(t)
	taskID := createTaskForSchedule(t, client)

	cases := []struct {
		name string
		body map[string]interface{}
	}{
		{"start_time 9:00", map[string]interface{}{"when_date": "2026-03-01", "start_time": "9:00", "end_time": "10:00"}},
		{"end_time before start_time", map[string]interface{}{"when_date": "2026-03-01", "start_time": "09:00", "end_time": "08:00"}},
		{"end_time equal to start_time", map[string]interface{}{"when_date": "2026-03-01", "start_time": "09:00", "end_time": "09:00"}},
	}
	for _, tc := range cases {
		resp := client.Post("/api/tasks/"+taskID+"/schedules", tc.body)
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("%s: expected status 400, got %d (body: %s)", tc.name, resp.StatusCode, string(resp.Body))
			continue
		}
		testutil.AssertJSONField(t, resp, "code", "VALIDATION")
		if tc.name == "start_time 9:00" {
			testutil.AssertJSONField(t, resp, "field", "start_time")
		} else {
			testutil.AssertJSONField(t, resp, "field", "end_time")
		}
	}
}

func TestScheduleCreate_RejectsBadWhenDate(t *testing.T) {
	client := setupScheduleRouter(t)
	taskID := createTaskForSchedule(t, client)

	resp := client.Post("/api/tasks/"+taskID+"/schedules", map[string]interface{}{"when_date": "March 1"})
	testutil.AssertStatus(t, resp, http.StatusBadRequest)
	testutil.AssertJSONField(t, resp, "code", "VALIDATION")
	testutil.AssertJSONField(t, resp, "field", "when_date")
}

func TestScheduleCreate_UnknownTaskIs422(t *testing.T) {
	client := setupScheduleRouter(t)

	resp := client.Post("/api/tasks/badtask1/schedules", map[string]interface{}{"when_date": "2026-03-01"})
	testutil.AssertStatus(t, resp, http.StatusUnprocessableEntity)
	testutil.AssertJSONField(t, resp, "code", "VALIDATION")
	testutil.AssertJSONField(t, resp, "field", "task_id")
}

// TestScheduleReorder_ScopedToTask pins that reordering accepts only
// schedule entries of the task in the URL: a foreign id rejects the whole
// request (400 VALIDATION, field items) and nothing is applied, not even
// the entries that do belong to the task.
func TestScheduleReorder_ScopedToTask(t *testing.T) {
	client := setupScheduleRouter(t)
	taskA := createTaskForSchedule(t, client)
	taskB := createTaskForSchedule(t, client)

	createSchedule := func(taskID, whenDate string) string {
		t.Helper()
		resp := client.Post("/api/tasks/"+taskID+"/schedules", map[string]interface{}{"when_date": whenDate})
		if resp.StatusCode != http.StatusCreated {
			t.Fatalf("create schedule: %d %s", resp.StatusCode, string(resp.Body))
		}
		var m map[string]interface{}
		resp.JSON(t, &m)
		return m["id"].(string)
	}
	a1 := createSchedule(taskA, "2026-03-01")
	a2 := createSchedule(taskA, "2026-03-02")
	b1 := createSchedule(taskB, "2026-03-03")

	resp := client.Patch("/api/tasks/"+taskA+"/schedules/reorder", []map[string]interface{}{
		{"id": a1, "sort_order": 4096},
		{"id": b1, "sort_order": 8192},
	})
	testutil.AssertStatus(t, resp, http.StatusBadRequest)
	testutil.AssertJSONField(t, resp, "code", "VALIDATION")
	testutil.AssertJSONField(t, resp, "field", "items")

	sortOrder := func(taskID, scheduleID string) float64 {
		t.Helper()
		listResp := client.Get("/api/tasks/" + taskID + "/schedules")
		testutil.AssertStatus(t, listResp, http.StatusOK)
		var list struct {
			Items []struct {
				ID        string  `json:"id"`
				SortOrder float64 `json:"sort_order"`
			} `json:"items"`
		}
		listResp.JSON(t, &list)
		for _, it := range list.Items {
			if it.ID == scheduleID {
				return it.SortOrder
			}
		}
		t.Fatalf("schedule %s not found under task %s", scheduleID, taskID)
		return 0
	}
	if got := sortOrder(taskB, b1); got != 1024 {
		t.Errorf("foreign schedule sort_order: expected 1024, got %v", got)
	}
	if got := sortOrder(taskA, a1); got != 1024 {
		t.Errorf("task A entry sort_order must stay unchanged on rejection, expected 1024, got %v", got)
	}
	if got := sortOrder(taskA, a2); got != 2048 {
		t.Errorf("task A second entry sort_order: expected 2048, got %v", got)
	}
}

// TestChecklistUpdate_EventIDs pins the SSE envelope for a checklist item
// update: the event identifies the changed entity (entity "checklist_item",
// the item id) and carries the owning task as task_id context.
func TestChecklistUpdate_EventIDs(t *testing.T) {
	db := testutil.SetupTestDB(t)
	broker := sse.NewBroker()
	changeLog := repository.NewChangeLogRepository(db)
	taskRepo := repository.NewTaskRepository(db, changeLog)
	checklistRepo := repository.NewChecklistRepository(db, changeLog)
	checklistH := handler.NewChecklistHandler(checklistRepo, broker)
	taskH := handler.NewTaskHandler(taskRepo, repository.NewReminderRepository(db, changeLog), repository.NewUserSettingsRepository(db), broker, nil)

	r := chi.NewRouter()
	r.Post("/api/tasks", taskH.Create)
	r.Post("/api/tasks/{id}/checklist", checklistH.Create)
	r.Patch("/api/checklist/{id}", checklistH.Update)
	client := testutil.NewTestClient(t, r)

	taskID := createTaskForSchedule(t, client)
	itemResp := client.Post("/api/tasks/"+taskID+"/checklist", map[string]string{"title": "Item"})
	if itemResp.StatusCode != http.StatusCreated {
		t.Fatalf("create checklist item: %d %s", itemResp.StatusCode, string(itemResp.Body))
	}
	var item map[string]interface{}
	itemResp.JSON(t, &item)
	itemID := item["id"].(string)

	events := broker.Subscribe("checklist-test")

	resp := client.Patch("/api/checklist/"+itemID, map[string]interface{}{"completed": true})
	testutil.AssertStatus(t, resp, http.StatusOK)

	select {
	case e := <-events:
		if e.Type != "task_updated" {
			t.Errorf("event type: expected task_updated, got %s", e.Type)
		}
		data, ok := e.Data.(map[string]interface{})
		if !ok {
			t.Fatalf("event data: expected map, got %T", e.Data)
		}
		if data["entity"] != "checklist_item" {
			t.Errorf("entity: expected checklist_item, got %v", data["entity"])
		}
		if data["id"] != itemID {
			t.Errorf("id: expected %s, got %v", itemID, data["id"])
		}
		if data["task_id"] != taskID {
			t.Errorf("task_id: expected %s, got %v", taskID, data["task_id"])
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for checklist update event")
	}
}

func TestChecklistCreate_UnknownTaskIs422(t *testing.T) {
	client := setupScheduleRouter(t)

	resp := client.Post("/api/tasks/badtask1/checklist", map[string]interface{}{"title": "Item"})
	testutil.AssertStatus(t, resp, http.StatusUnprocessableEntity)
	testutil.AssertJSONField(t, resp, "code", "VALIDATION")
	testutil.AssertJSONField(t, resp, "field", "task_id")
}

// TestScheduleCreate_ReplayWithOtherTaskIs409 pins the parent check of the
// idempotent replay: the same client id replayed against the same task
// returns the stored entry (200), but against another task it is 409
// CONFLICT, never another task's entry.
func TestScheduleCreate_ReplayWithOtherTaskIs409(t *testing.T) {
	client := setupScheduleRouter(t)
	taskA := createTaskForSchedule(t, client)
	taskB := createTaskForSchedule(t, client)

	body := map[string]interface{}{"id": "xparSched001", "when_date": "2031-06-01"}
	first := client.Post("/api/tasks/"+taskA+"/schedules", body)
	testutil.AssertStatus(t, first, http.StatusCreated)

	replay := client.Post("/api/tasks/"+taskA+"/schedules", body)
	testutil.AssertStatus(t, replay, http.StatusOK)

	conflict := client.Post("/api/tasks/"+taskB+"/schedules", body)
	testutil.AssertStatus(t, conflict, http.StatusConflict)
	testutil.AssertJSONField(t, conflict, "code", "CONFLICT")
}
