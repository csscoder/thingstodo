package handler_test

import (
	"net/http"
	"strings"
	"testing"
	"time"

	_ "time/tzdata"

	"github.com/collinjanssen/thingstodo/internal/handler"
	"github.com/collinjanssen/thingstodo/internal/repository"
	"github.com/collinjanssen/thingstodo/internal/sse"
	"github.com/collinjanssen/thingstodo/internal/testutil"
	"github.com/go-chi/chi/v5"
)

func setupReminderRouter(t *testing.T) *testutil.TestClient {
	t.Helper()
	db := testutil.SetupTestDB(t)
	broker := sse.NewBroker()
	// Reminders interpret naive exact_at values in the configured location;
	// Moscow (+03, no DST) makes the conversion assertions deterministic.
	msk, err := time.LoadLocation("Europe/Moscow")
	if err != nil {
		t.Fatalf("load Europe/Moscow: %v", err)
	}
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
	repeatRuleRepo := repository.NewRepeatRuleRepository(db, changeLog)
	settingsRepo := repository.NewUserSettingsRepository(db)

	taskH := handler.NewTaskHandler(taskRepo, reminderRepo, settingsRepo, broker, nil)
	reminderH := handler.NewReminderHandler(reminderRepo, broker, msk)
	syncH := handler.NewSyncHandler(changeLog, taskRepo, projectRepo, areaRepo, tagRepo, checklistRepo, headingRepo, attachmentRepo, scheduleRepo, reminderRepo, repeatRuleRepo, nil, msk, t.TempDir())

	r := chi.NewRouter()
	r.Post("/api/tasks", taskH.Create)
	r.Get("/api/tasks/{id}/reminders", reminderH.List)
	r.Post("/api/tasks/{id}/reminders", reminderH.Create)
	r.Post("/api/sync/push", syncH.Push)

	return testutil.NewTestClient(t, r)
}

func createTaskForReminders(t *testing.T, client *testutil.TestClient) string {
	t.Helper()
	resp := client.Post("/api/tasks", map[string]string{"title": "Reminder test task"})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create task failed: %d %s", resp.StatusCode, string(resp.Body))
	}
	var created map[string]interface{}
	resp.JSON(t, &created)
	return created["id"].(string)
}

func TestReminderCreate_Validates(t *testing.T) {
	client := setupReminderRouter(t)
	taskID := createTaskForReminders(t, client)

	cases := []struct {
		name  string
		body  map[string]interface{}
		field string
	}{
		{"unknown type", map[string]interface{}{"type": "telepathy", "value": 1}, "type"},
		{"value 100", map[string]interface{}{"type": "minutes_before", "value": 100}, "value"},
		{"value -1", map[string]interface{}{"type": "minutes_before", "value": -1}, "value"},
		{"exact without exact_at", map[string]interface{}{"type": "exact", "value": 0}, "exact_at"},
		{"exact_at garbage", map[string]interface{}{"type": "exact", "value": 0, "exact_at": "not-a-time"}, "exact_at"},
	}
	for _, tc := range cases {
		resp := client.Post("/api/tasks/"+taskID+"/reminders", tc.body)
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("%s: expected status 400, got %d (body: %s)", tc.name, resp.StatusCode, string(resp.Body))
			continue
		}
		testutil.AssertJSONField(t, resp, "code", "VALIDATION")
		testutil.AssertJSONField(t, resp, "field", tc.field)
	}

	// A valid offset exact_at is stored normalized as UTC RFC 3339, and a
	// naive timestamp is read in the handler's location (Moscow, +03).
	resp := client.Post("/api/tasks/"+taskID+"/reminders", map[string]interface{}{
		"type": "exact", "value": 0, "exact_at": "2026-10-20T09:00:00+03:00",
	})
	testutil.AssertStatus(t, resp, http.StatusCreated)
	resp = client.Post("/api/tasks/"+taskID+"/reminders", map[string]interface{}{
		"type": "exact", "value": 0, "exact_at": "2026-10-22T09:00",
	})
	testutil.AssertStatus(t, resp, http.StatusCreated)

	list := client.Get("/api/tasks/" + taskID + "/reminders")
	testutil.AssertStatus(t, list, http.StatusOK)
	var body struct {
		Items []struct {
			ExactAt string `json:"exact_at"`
		} `json:"items"`
	}
	list.JSON(t, &body)
	stored := make(map[string]bool)
	for _, it := range body.Items {
		stored[it.ExactAt] = true
	}
	for _, want := range []string{"2026-10-20T06:00:00Z", "2026-10-22T06:00:00Z"} {
		if !stored[want] {
			t.Errorf("expected normalized exact_at %s, got %+v", want, body.Items)
		}
	}

	// Sync reminder creates apply the same rules (the Task 4 deferred
	// finding: type, value and exact_at were not validated on the sync path).
	push := client.Post("/api/sync/push", map[string]interface{}{
		"device_id": "dev1",
		"changes": []map[string]interface{}{
			{"entity": "reminder", "entity_id": "rem11111", "action": "create",
				"data": map[string]interface{}{"task_id": taskID, "type": "telepathy", "value": 1}},
			{"entity": "reminder", "entity_id": "rem22222", "action": "create",
				"data": map[string]interface{}{"task_id": taskID, "type": "minutes_before", "value": 100}},
			{"entity": "reminder", "entity_id": "rem33333", "action": "create",
				"data": map[string]interface{}{"task_id": taskID, "type": "exact", "value": 0}},
			{"entity": "reminder", "entity_id": "rem44444", "action": "create",
				"data": map[string]interface{}{"task_id": taskID, "type": "exact", "value": 0, "exact_at": "2026-10-21T09:00:00+03:00"}},
		},
	})
	testutil.AssertStatus(t, push, http.StatusOK)
	var res handler.SyncPushResponse
	push.JSON(t, &res)
	wantErrors := []string{"type:", "value:", "exact_at:"}
	for i, prefix := range wantErrors {
		if res.Results[i].Status != "error" || !strings.HasPrefix(res.Results[i].Error, prefix) {
			t.Errorf("sync change %d: expected error starting with %q, got status=%q error=%q",
				i, prefix, res.Results[i].Status, res.Results[i].Error)
		}
	}
	if res.Results[3].Status != "applied" {
		t.Errorf("sync valid reminder: expected applied, got status=%q error=%q",
			res.Results[3].Status, res.Results[3].Error)
	}

	// The applied sync reminder is stored normalized too.
	list = client.Get("/api/tasks/" + taskID + "/reminders")
	var body2 struct {
		Items []struct {
			ID      string `json:"id"`
			ExactAt string `json:"exact_at"`
		} `json:"items"`
	}
	list.JSON(t, &body2)
	found := false
	for _, it := range body2.Items {
		if it.ID == "rem44444" {
			found = true
			if it.ExactAt != "2026-10-21T06:00:00Z" {
				t.Errorf("sync reminder exact_at: expected 2026-10-21T06:00:00Z, got %q", it.ExactAt)
			}
		}
	}
	if !found {
		t.Errorf("sync reminder rem44444 missing from list: %+v", body2.Items)
	}
}
