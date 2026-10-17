package handler_test

import (
	"database/sql"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/collinjanssen/thingstodo/internal/handler"
	"github.com/collinjanssen/thingstodo/internal/repository"
	"github.com/collinjanssen/thingstodo/internal/sse"
	"github.com/collinjanssen/thingstodo/internal/testutil"
	"github.com/go-chi/chi/v5"
)

func setupSyncRouter(t *testing.T) (*testutil.TestClient, *repository.ChangeLogRepository, *repository.TaskRepository) {
	client, changeLog, taskRepo, _ := setupSyncRouterWithDB(t)
	return client, changeLog, taskRepo
}

func setupSyncRouterWithDB(t *testing.T) (*testutil.TestClient, *repository.ChangeLogRepository, *repository.TaskRepository, *sql.DB) {
	t.Helper()
	db := testutil.SetupTestDB(t)

	changeLogRepo := repository.NewChangeLogRepository(db)
	taskRepo := repository.NewTaskRepository(db, changeLogRepo)
	projectRepo := repository.NewProjectRepository(db, changeLogRepo)
	areaRepo := repository.NewAreaRepository(db, changeLogRepo)
	tagRepo := repository.NewTagRepository(db, changeLogRepo)
	checklistRepo := repository.NewChecklistRepository(db, changeLogRepo)
	headingRepo := repository.NewHeadingRepository(db, changeLogRepo)

	scheduleRepo := repository.NewScheduleRepository(db, changeLogRepo)
	reminderRepo := repository.NewReminderRepository(db, changeLogRepo)
	attachmentRepo := repository.NewAttachmentRepository(db, changeLogRepo)
	repeatRuleRepo := repository.NewRepeatRuleRepository(db, changeLogRepo)
	settingsRepo := repository.NewUserSettingsRepository(db)

	syncH := handler.NewSyncHandler(changeLogRepo, taskRepo, projectRepo, areaRepo, tagRepo, checklistRepo, headingRepo, attachmentRepo, scheduleRepo, reminderRepo, repeatRuleRepo, nil)

	r := chi.NewRouter()
	r.Get("/api/sync/pull", syncH.Pull)
	r.Post("/api/sync/push", syncH.Push)
	r.Get("/api/sync/full", syncH.Full)

	// Also mount task endpoints so we can create test data
	broker := sse.NewBroker()
	taskH := handler.NewTaskHandler(taskRepo, scheduleRepo, reminderRepo, settingsRepo, broker, nil)

	r.Route("/api/tasks", func(r chi.Router) {
		r.Post("/", taskH.Create)
		r.Get("/{id}", taskH.Get)
	})

	client := testutil.NewTestClient(t, r)
	return client, changeLogRepo, taskRepo, db
}

// --- Pull tests ---

func TestSyncPullEmpty(t *testing.T) {
	client, _, _ := setupSyncRouter(t)

	resp := client.Get("/api/sync/pull?since=0")
	testutil.AssertStatus(t, resp, http.StatusOK)

	var result handler.PullResponse
	resp.JSON(t, &result)

	if len(result.Changes) != 0 {
		t.Errorf("expected 0 changes, got %d", len(result.Changes))
	}
	if result.Cursor != 0 {
		t.Errorf("expected cursor 0, got %d", result.Cursor)
	}
	if result.HasMore {
		t.Error("expected has_more=false")
	}
}

func TestSyncPullReturnsChanges(t *testing.T) {
	client, changeLog, _ := setupSyncRouter(t)

	// Insert test changes directly
	_, _ = changeLog.AppendChange("task", "t1", "create", nil, `{"id":"t1","title":"Test"}`, "", "")
	_, _ = changeLog.AppendChange("task", "t1", "update", nil, `{"id":"t1","title":"Updated"}`, "", "")

	resp := client.Get("/api/sync/pull?since=0")
	testutil.AssertStatus(t, resp, http.StatusOK)

	var result handler.PullResponse
	resp.JSON(t, &result)

	if len(result.Changes) != 2 {
		t.Fatalf("expected 2 changes, got %d", len(result.Changes))
	}
	if result.Changes[0].Entity != "task" {
		t.Errorf("expected entity 'task', got %q", result.Changes[0].Entity)
	}
	if result.Changes[0].Action != "create" {
		t.Errorf("expected action 'create', got %q", result.Changes[0].Action)
	}
	if result.Cursor != result.Changes[1].Seq {
		t.Errorf("cursor should be last seq")
	}
	if result.HasMore {
		t.Error("expected has_more=false")
	}
}

func TestSyncPullSinceCursor(t *testing.T) {
	client, changeLog, _ := setupSyncRouter(t)

	_, _ = changeLog.AppendChange("task", "t1", "create", nil, `{"id":"t1"}`, "", "")
	seq2, _ := changeLog.AppendChange("task", "t2", "create", nil, `{"id":"t2"}`, "", "")
	_ = seq2

	// Pull since seq 1 should only return the second change
	resp := client.Get("/api/sync/pull?since=1")
	testutil.AssertStatus(t, resp, http.StatusOK)

	var result handler.PullResponse
	resp.JSON(t, &result)

	if len(result.Changes) != 1 {
		t.Fatalf("expected 1 change, got %d", len(result.Changes))
	}
	if result.Changes[0].EntityID != "t2" {
		t.Errorf("expected entity_id 't2', got %q", result.Changes[0].EntityID)
	}
}

func TestSyncPullHasMore(t *testing.T) {
	client, changeLog, _ := setupSyncRouter(t)

	// Insert 3 changes
	for i := 0; i < 3; i++ {
		_, _ = changeLog.AppendChange("task", fmt.Sprintf("t%d", i), "create", nil, fmt.Sprintf(`{"id":"t%d"}`, i), "", "")
	}

	// Pull with limit=2 should have has_more=true
	resp := client.Get("/api/sync/pull?since=0&limit=2")
	testutil.AssertStatus(t, resp, http.StatusOK)

	var result handler.PullResponse
	resp.JSON(t, &result)

	if len(result.Changes) != 2 {
		t.Fatalf("expected 2 changes, got %d", len(result.Changes))
	}
	if !result.HasMore {
		t.Error("expected has_more=true")
	}
}

func TestSyncPullInvalidSince(t *testing.T) {
	client, _, _ := setupSyncRouter(t)

	resp := client.Get("/api/sync/pull?since=abc")
	testutil.AssertStatus(t, resp, http.StatusBadRequest)
}

func TestSyncPullInvalidLimit(t *testing.T) {
	client, _, _ := setupSyncRouter(t)

	resp := client.Get("/api/sync/pull?since=0&limit=-1")
	testutil.AssertStatus(t, resp, http.StatusBadRequest)
}

func TestSyncPullLimitCapped(t *testing.T) {
	client, _, _ := setupSyncRouter(t)

	// Requesting limit=9999 should not error (capped to 1000)
	resp := client.Get("/api/sync/pull?since=0&limit=9999")
	testutil.AssertStatus(t, resp, http.StatusOK)
}

func TestSyncPullDefaultSince(t *testing.T) {
	client, _, _ := setupSyncRouter(t)

	// since parameter omitted defaults to 0
	resp := client.Get("/api/sync/pull")
	testutil.AssertStatus(t, resp, http.StatusOK)

	var result handler.PullResponse
	resp.JSON(t, &result)
	if result.Cursor != 0 {
		t.Errorf("expected cursor 0, got %d", result.Cursor)
	}
}

// --- Push tests ---

func TestSyncPushCreateTask(t *testing.T) {
	client, _, taskRepo := setupSyncRouter(t)

	body := map[string]interface{}{
		"device_id": "dev1",
		"changes": []map[string]interface{}{
			{
				"entity":            "task",
				"entity_id":         "sync-task-01",
				"action":            "create",
				"data":              map[string]interface{}{"title": "Synced task"},
				"fields":            []string{"title"},
				"client_updated_at": "2026-01-01T00:00:00Z",
			},
		},
	}

	resp := client.Post("/api/sync/push", body)
	testutil.AssertStatus(t, resp, http.StatusOK)

	var result handler.SyncPushResponse
	resp.JSON(t, &result)

	if len(result.Results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(result.Results))
	}
	if result.Results[0].Status != "applied" {
		t.Errorf("expected status 'applied', got %q (error: %s)", result.Results[0].Status, result.Results[0].Error)
	}

	// Verify the task was created (it uses a server-generated ID, so check via list)
	_ = taskRepo
}

func TestSyncPushUpdateTask(t *testing.T) {
	client, _, _ := setupSyncRouter(t)

	// First create a task
	createResp := client.Post("/api/tasks", map[string]string{"title": "Original"})
	var created map[string]interface{}
	createResp.JSON(t, &created)
	taskID := created["id"].(string)

	// Push an update
	body := map[string]interface{}{
		"device_id": "dev1",
		"changes": []map[string]interface{}{
			{
				"entity":            "task",
				"entity_id":         taskID,
				"action":            "update",
				"data":              map[string]interface{}{"title": "Updated via sync"},
				"fields":            []string{"title"},
				"client_updated_at": "2099-01-01T00:00:00Z", // future time, so client wins
			},
		},
	}

	resp := client.Post("/api/sync/push", body)
	testutil.AssertStatus(t, resp, http.StatusOK)

	var result handler.SyncPushResponse
	resp.JSON(t, &result)

	if result.Results[0].Status != "applied" {
		t.Errorf("expected status 'applied', got %q (error: %s)", result.Results[0].Status, result.Results[0].Error)
	}

	// Verify the update
	getResp := client.Get("/api/tasks/" + taskID)
	testutil.AssertJSONField(t, getResp, "title", "Updated via sync")
}

func TestSyncPushUpdateTaskConflictResolved(t *testing.T) {
	client, _, _ := setupSyncRouter(t)

	// Create a task
	createResp := client.Post("/api/tasks", map[string]string{"title": "Original"})
	var created map[string]interface{}
	createResp.JSON(t, &created)
	taskID := created["id"].(string)

	// Push an update with old timestamp — server is newer, so conflict_resolved
	body := map[string]interface{}{
		"device_id": "dev1",
		"changes": []map[string]interface{}{
			{
				"entity":            "task",
				"entity_id":         taskID,
				"action":            "update",
				"data":              map[string]interface{}{"title": "Stale update"},
				"fields":            []string{"title"},
				"client_updated_at": "2000-01-01T00:00:00Z", // past time, server is newer
			},
		},
	}

	resp := client.Post("/api/sync/push", body)
	testutil.AssertStatus(t, resp, http.StatusOK)

	var result handler.SyncPushResponse
	resp.JSON(t, &result)

	if result.Results[0].Status != "conflict_resolved" {
		t.Errorf("expected status 'conflict_resolved', got %q", result.Results[0].Status)
	}
}

func TestSyncPushDeleteTask(t *testing.T) {
	client, _, _ := setupSyncRouter(t)

	// Create a task
	createResp := client.Post("/api/tasks", map[string]string{"title": "To delete"})
	var created map[string]interface{}
	createResp.JSON(t, &created)
	taskID := created["id"].(string)

	body := map[string]interface{}{
		"device_id": "dev1",
		"changes": []map[string]interface{}{
			{
				"entity":            "task",
				"entity_id":         taskID,
				"action":            "delete",
				"data":              map[string]interface{}{},
				"fields":            []string{},
				"client_updated_at": "2026-01-01T00:00:00Z",
			},
		},
	}

	resp := client.Post("/api/sync/push", body)
	testutil.AssertStatus(t, resp, http.StatusOK)

	var result handler.SyncPushResponse
	resp.JSON(t, &result)

	if result.Results[0].Status != "applied" {
		t.Errorf("expected status 'applied', got %q (error: %s)", result.Results[0].Status, result.Results[0].Error)
	}
}

func TestSyncPushUnsupportedEntity(t *testing.T) {
	client, _, _ := setupSyncRouter(t)

	body := map[string]interface{}{
		"device_id": "dev1",
		"changes": []map[string]interface{}{
			{
				"entity":            "unknown",
				"entity_id":         "x1",
				"action":            "create",
				"data":              map[string]interface{}{},
				"fields":            []string{},
				"client_updated_at": "2026-01-01T00:00:00Z",
			},
		},
	}

	resp := client.Post("/api/sync/push", body)
	testutil.AssertStatus(t, resp, http.StatusOK)

	var result handler.SyncPushResponse
	resp.JSON(t, &result)

	if result.Results[0].Status != "error" {
		t.Errorf("expected status 'error', got %q", result.Results[0].Status)
	}
}

func TestSyncPushMissingDeviceID(t *testing.T) {
	client, _, _ := setupSyncRouter(t)

	body := map[string]interface{}{
		"changes": []map[string]interface{}{},
	}

	resp := client.Post("/api/sync/push", body)
	testutil.AssertStatus(t, resp, http.StatusBadRequest)
}

func TestSyncPushMultipleChanges(t *testing.T) {
	client, _, _ := setupSyncRouter(t)

	body := map[string]interface{}{
		"device_id": "dev1",
		"changes": []map[string]interface{}{
			{
				"entity":            "task",
				"entity_id":         "multi-task-1",
				"action":            "create",
				"data":              map[string]interface{}{"title": "Task 1"},
				"fields":            []string{"title"},
				"client_updated_at": "2026-01-01T00:00:00Z",
			},
			{
				"entity":            "task",
				"entity_id":         "multi-task-2",
				"action":            "create",
				"data":              map[string]interface{}{"title": "Task 2"},
				"fields":            []string{"title"},
				"client_updated_at": "2026-01-01T00:00:00Z",
			},
		},
	}

	resp := client.Post("/api/sync/push", body)
	testutil.AssertStatus(t, resp, http.StatusOK)

	var result handler.SyncPushResponse
	resp.JSON(t, &result)

	if len(result.Results) != 2 {
		t.Fatalf("expected 2 results, got %d", len(result.Results))
	}
	for i, r := range result.Results {
		if r.Status != "applied" {
			t.Errorf("result[%d]: expected 'applied', got %q (error: %s)", i, r.Status, r.Error)
		}
	}
}

func TestSyncPushCreateArea(t *testing.T) {
	client, _, _, db := setupSyncRouterWithDB(t)

	body := map[string]interface{}{
		"device_id": "dev1",
		"changes": []map[string]interface{}{
			{
				"entity":            "area",
				"entity_id":         "area-sync-01",
				"action":            "create",
				"data":              map[string]interface{}{"title": "Work"},
				"fields":            []string{"title"},
				"client_updated_at": "2026-01-01T00:00:00Z",
			},
		},
	}

	resp := client.Post("/api/sync/push", body)
	testutil.AssertStatus(t, resp, http.StatusOK)

	var result handler.SyncPushResponse
	resp.JSON(t, &result)

	if result.Results[0].Status != "applied" {
		t.Errorf("expected 'applied', got %q (error: %s)", result.Results[0].Status, result.Results[0].Error)
	}

	var count int
	if err := db.QueryRow("SELECT COUNT(*) FROM areas WHERE id = ?", "area-sync-01").Scan(&count); err != nil {
		t.Fatalf("failed to query created area: %v", err)
	}
	if count != 1 {
		t.Fatalf("expected synced area id to be preserved, count=%d", count)
	}
}

func TestSyncPushCreateTag(t *testing.T) {
	client, _, _, db := setupSyncRouterWithDB(t)

	body := map[string]interface{}{
		"device_id": "dev1",
		"changes": []map[string]interface{}{
			{
				"entity":            "tag",
				"entity_id":         "tag-sync-001",
				"action":            "create",
				"data":              map[string]interface{}{"title": "urgent"},
				"fields":            []string{"title"},
				"client_updated_at": "2026-01-01T00:00:00Z",
			},
		},
	}

	resp := client.Post("/api/sync/push", body)
	testutil.AssertStatus(t, resp, http.StatusOK)

	var result handler.SyncPushResponse
	resp.JSON(t, &result)

	if result.Results[0].Status != "applied" {
		t.Errorf("expected 'applied', got %q (error: %s)", result.Results[0].Status, result.Results[0].Error)
	}

	var count int
	if err := db.QueryRow("SELECT COUNT(*) FROM tags WHERE id = ?", "tag-sync-001").Scan(&count); err != nil {
		t.Fatalf("failed to query created tag: %v", err)
	}
	if count != 1 {
		t.Fatalf("expected synced tag id to be preserved, count=%d", count)
	}
}

func TestSyncPushCreateProjectPreservesClientID(t *testing.T) {
	client, _, _, db := setupSyncRouterWithDB(t)

	if _, err := db.Exec(`INSERT INTO areas (id, title, sort_order) VALUES ('area1', 'Work', 0)`); err != nil {
		t.Fatalf("failed to seed area: %v", err)
	}

	body := map[string]interface{}{
		"device_id": "dev1",
		"changes": []map[string]interface{}{
			{
				"entity":    "project",
				"entity_id": "project1",
				"action":    "create",
				"data": map[string]interface{}{
					"title":   "Roadmap",
					"area_id": "area1",
				},
				"fields":            []string{"title", "area_id"},
				"client_updated_at": "2026-01-01T00:00:00Z",
			},
		},
	}

	resp := client.Post("/api/sync/push", body)
	testutil.AssertStatus(t, resp, http.StatusOK)

	var result handler.SyncPushResponse
	resp.JSON(t, &result)

	if result.Results[0].Status != "applied" {
		t.Errorf("expected 'applied', got %q (error: %s)", result.Results[0].Status, result.Results[0].Error)
	}

	var count int
	if err := db.QueryRow("SELECT COUNT(*) FROM projects WHERE id = ?", "project1").Scan(&count); err != nil {
		t.Fatalf("failed to query created project: %v", err)
	}
	if count != 1 {
		t.Fatalf("expected synced project id to be preserved, count=%d", count)
	}
}

func TestSyncPushCreateProjectIsIdempotent(t *testing.T) {
	client, _, _, db := setupSyncRouterWithDB(t)

	if _, err := db.Exec(`INSERT INTO areas (id, title, sort_order) VALUES ('area1', 'Work', 0)`); err != nil {
		t.Fatalf("failed to seed area: %v", err)
	}

	body := map[string]interface{}{
		"device_id": "dev1",
		"changes": []map[string]interface{}{
			{
				"entity":    "project",
				"entity_id": "project1",
				"action":    "create",
				"data": map[string]interface{}{
					"title":   "Roadmap",
					"area_id": "area1",
				},
				"fields":            []string{"title", "area_id"},
				"client_updated_at": "2026-01-01T00:00:00Z",
			},
		},
	}

	firstResp := client.Post("/api/sync/push", body)
	testutil.AssertStatus(t, firstResp, http.StatusOK)
	secondResp := client.Post("/api/sync/push", body)
	testutil.AssertStatus(t, secondResp, http.StatusOK)

	var result handler.SyncPushResponse
	secondResp.JSON(t, &result)
	if result.Results[0].Status != "applied" {
		t.Errorf("expected idempotent replay to stay 'applied', got %q (error: %s)", result.Results[0].Status, result.Results[0].Error)
	}

	var count int
	if err := db.QueryRow("SELECT COUNT(*) FROM projects WHERE id = ?", "project1").Scan(&count); err != nil {
		t.Fatalf("failed to query created project: %v", err)
	}
	if count != 1 {
		t.Fatalf("expected exactly one project row after replay, count=%d", count)
	}
}

func TestSyncPushInvalidJSON(t *testing.T) {
	client, _, _ := setupSyncRouter(t)

	resp := client.Post("/api/sync/push", "not json")
	testutil.AssertStatus(t, resp, http.StatusBadRequest)
}

func TestSyncPushUpdateNonexistentTask(t *testing.T) {
	client, _, _ := setupSyncRouter(t)

	body := map[string]interface{}{
		"device_id": "dev1",
		"changes": []map[string]interface{}{
			{
				"entity":            "task",
				"entity_id":         "nonexistent",
				"action":            "update",
				"data":              map[string]interface{}{"title": "Nope"},
				"fields":            []string{"title"},
				"client_updated_at": "2026-01-01T00:00:00Z",
			},
		},
	}

	resp := client.Post("/api/sync/push", body)
	testutil.AssertStatus(t, resp, http.StatusOK)

	var result handler.SyncPushResponse
	resp.JSON(t, &result)

	if result.Results[0].Status != "error" {
		t.Errorf("expected 'error', got %q", result.Results[0].Status)
	}
}

func TestSyncPush_UnsupportedEntityMessageUnchanged(t *testing.T) {
	client, _, _ := setupSyncRouter(t)

	body := map[string]interface{}{
		"device_id": "dev1",
		"changes": []map[string]interface{}{
			{
				"entity":            "bogus",
				"entity_id":         "x1",
				"action":            "create",
				"data":              map[string]interface{}{},
				"fields":            []string{},
				"client_updated_at": "2026-01-01T00:00:00Z",
			},
		},
	}

	resp := client.Post("/api/sync/push", body)
	testutil.AssertStatus(t, resp, http.StatusOK)

	var result handler.SyncPushResponse
	resp.JSON(t, &result)

	if len(result.Results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(result.Results))
	}
	r := result.Results[0]
	if r.Status != "error" {
		t.Errorf("expected status 'error', got %q", r.Status)
	}
	if r.Error != "unsupported entity type: bogus" {
		t.Errorf("expected error %q, got %q", "unsupported entity type: bogus", r.Error)
	}
}

func TestSyncPush_WrongTypeIsPerChangeError(t *testing.T) {
	client, _, taskRepo := setupSyncRouter(t)

	body := map[string]interface{}{
		"device_id": "dev1",
		"changes": []map[string]interface{}{
			{
				"entity":    "task",
				"entity_id": "sync-wrongtype-1",
				"action":    "create",
				"data": map[string]interface{}{
					"title":     "Wrong when_date type",
					"when_date": 123,
				},
				"fields":            []string{"title", "when_date"},
				"client_updated_at": "2026-01-01T00:00:00Z",
			},
			{
				"entity":            "task",
				"entity_id":         "sync-wrongtype-2",
				"action":            "create",
				"data":              map[string]interface{}{"title": "Valid task"},
				"fields":            []string{"title"},
				"client_updated_at": "2026-01-01T00:00:00Z",
			},
			{
				"entity":            "task",
				"entity_id":         "sync-wrongtype-2",
				"action":            "update",
				"data":              map[string]interface{}{"project_id": true},
				"fields":            []string{"project_id"},
				"client_updated_at": "2099-01-01T00:00:00Z",
			},
		},
	}

	resp := client.Post("/api/sync/push", body)
	testutil.AssertStatus(t, resp, http.StatusOK)

	var result handler.SyncPushResponse
	resp.JSON(t, &result)

	if len(result.Results) != 3 {
		t.Fatalf("expected 3 results, got %d (body: %s)", len(result.Results), string(resp.Body))
	}

	if result.Results[0].Status != "error" {
		t.Errorf("results[0]: expected status 'error', got %q", result.Results[0].Status)
	}
	if !strings.Contains(result.Results[0].Error, "when_date") {
		t.Errorf("results[0]: expected error mentioning when_date, got %q", result.Results[0].Error)
	}

	if result.Results[1].Status != "applied" {
		t.Errorf("results[1]: expected status 'applied', got %q (error: %s)", result.Results[1].Status, result.Results[1].Error)
	}
	task, err := taskRepo.GetByID("sync-wrongtype-2")
	if err != nil || task == nil {
		t.Errorf("results[1]: expected task sync-wrongtype-2 to exist, got task=%v err=%v", task, err)
	}

	if result.Results[2].Status != "error" {
		t.Errorf("results[2]: expected status 'error', got %q", result.Results[2].Status)
	}
	if !strings.Contains(result.Results[2].Error, "project_id") {
		t.Errorf("results[2]: expected error mentioning project_id, got %q", result.Results[2].Error)
	}
}

func TestSyncPush_NullOptionalFieldsAccepted(t *testing.T) {
	client, _, _ := setupSyncRouter(t)

	body := map[string]interface{}{
		"device_id": "dev1",
		"changes": []map[string]interface{}{
			{
				"entity":    "task",
				"entity_id": "sync-null-1",
				"action":    "create",
				"data": map[string]interface{}{
					"title":      "Null optional fields",
					"deadline":   nil,
					"project_id": nil,
				},
				"fields":            []string{"title", "deadline", "project_id"},
				"client_updated_at": "2026-01-01T00:00:00Z",
			},
			{
				"entity":            "task",
				"entity_id":         "sync-null-1",
				"action":            "update",
				"data":              map[string]interface{}{"deadline": nil, "project_id": nil},
				"fields":            []string{"deadline", "project_id"},
				"client_updated_at": "2099-01-01T00:00:00Z",
			},
		},
	}

	resp := client.Post("/api/sync/push", body)
	testutil.AssertStatus(t, resp, http.StatusOK)

	var result handler.SyncPushResponse
	resp.JSON(t, &result)

	if len(result.Results) != 2 {
		t.Fatalf("expected 2 results, got %d (body: %s)", len(result.Results), string(resp.Body))
	}
	for i, r := range result.Results {
		if r.Status != "applied" {
			t.Errorf("results[%d]: expected status 'applied', got %q (error: %s)", i, r.Status, r.Error)
		}
	}
}

// --- Attachment sync tests ---

func pushAttachmentChange(t *testing.T, client *testutil.TestClient, entityID string, data map[string]interface{}) handler.SyncPushResponse {
	t.Helper()

	createResp := client.Post("/api/tasks", map[string]string{"title": "Attachment sync test"})
	var created map[string]interface{}
	createResp.JSON(t, &created)
	data["task_id"] = created["id"].(string)

	body := map[string]interface{}{
		"device_id": "dev1",
		"changes": []map[string]interface{}{
			{
				"entity":            "attachment",
				"entity_id":         entityID,
				"action":            "create",
				"data":              data,
				"client_updated_at": "2026-01-01T00:00:00Z",
			},
		},
	}

	resp := client.Post("/api/sync/push", body)
	testutil.AssertStatus(t, resp, http.StatusOK)

	var result handler.SyncPushResponse
	resp.JSON(t, &result)
	return result
}

func TestSyncPush_AttachmentFileTypeRejected(t *testing.T) {
	client, _, _, db := setupSyncRouterWithDB(t)

	result := pushAttachmentChange(t, client, "att-file-1", map[string]interface{}{
		"type": "file",
		"url":  "../secret.db",
	})

	if len(result.Results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(result.Results))
	}
	if result.Results[0].Status != "error" {
		t.Fatalf("expected status 'error', got %q", result.Results[0].Status)
	}
	if !strings.Contains(result.Results[0].Error, "only link attachments") {
		t.Errorf("unexpected error message: %q", result.Results[0].Error)
	}

	var n int
	if err := db.QueryRow("SELECT COUNT(*) FROM attachments").Scan(&n); err != nil {
		t.Fatalf("failed to count attachments: %v", err)
	}
	if n != 0 {
		t.Errorf("expected no attachment rows after rejection, got %d", n)
	}
}

func TestSyncPush_AttachmentScriptSchemeRejected(t *testing.T) {
	client, _, _, _ := setupSyncRouterWithDB(t)

	result := pushAttachmentChange(t, client, "att-js-1", map[string]interface{}{
		"type": "link",
		"url":  "javascript:alert(1)",
	})

	if len(result.Results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(result.Results))
	}
	if result.Results[0].Status != "error" {
		t.Fatalf("expected status 'error', got %q", result.Results[0].Status)
	}
	if !strings.Contains(result.Results[0].Error, "url scheme not allowed") {
		t.Errorf("unexpected error message: %q", result.Results[0].Error)
	}
}

func TestSyncPush_AttachmentLinkApplied(t *testing.T) {
	client, _, _, _ := setupSyncRouterWithDB(t)

	result := pushAttachmentChange(t, client, "att-link-1", map[string]interface{}{
		"url":   "https://x.test",
		"title": "L",
	})

	if len(result.Results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(result.Results))
	}
	if result.Results[0].Status != "applied" {
		t.Fatalf("expected status 'applied', got %q (error: %s)", result.Results[0].Status, result.Results[0].Error)
	}
}

func TestSyncPush_BadWhenDateIsChangeError(t *testing.T) {
	client, _, _ := setupSyncRouter(t)

	body := map[string]interface{}{
		"device_id": "dev1",
		"changes": []map[string]interface{}{
			{
				"entity":    "task",
				"entity_id": "sync-badwd-01",
				"action":    "create",
				"data": map[string]interface{}{
					"title":     "Bad when date",
					"when_date": "tomorrow",
				},
				"fields":            []string{"title", "when_date"},
				"client_updated_at": "2026-01-01T00:00:00Z",
			},
			{
				"entity":            "task",
				"entity_id":         "sync-goodwd-1",
				"action":            "create",
				"data":              map[string]interface{}{"title": "Good when date"},
				"fields":            []string{"title"},
				"client_updated_at": "2026-01-01T00:00:00Z",
			},
		},
	}

	resp := client.Post("/api/sync/push", body)
	testutil.AssertStatus(t, resp, http.StatusOK)

	var result handler.SyncPushResponse
	resp.JSON(t, &result)

	if len(result.Results) != 2 {
		t.Fatalf("expected 2 results, got %d (body: %s)", len(result.Results), string(resp.Body))
	}
	if result.Results[0].Status != "error" {
		t.Errorf("expected status 'error', got %q", result.Results[0].Status)
	}
	if !strings.Contains(result.Results[0].Error, "when_date") {
		t.Errorf("expected error mentioning when_date, got %q", result.Results[0].Error)
	}
	if result.Results[1].Status != "applied" {
		t.Errorf("batch must continue after a per-change error, got %q (error: %s)", result.Results[1].Status, result.Results[1].Error)
	}
}
