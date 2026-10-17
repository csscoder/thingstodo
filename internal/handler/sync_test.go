package handler_test

import (
	"database/sql"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/collinjanssen/thingstodo/internal/handler"
	"github.com/collinjanssen/thingstodo/internal/model"
	"github.com/collinjanssen/thingstodo/internal/repository"
	"github.com/collinjanssen/thingstodo/internal/scheduler"
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

	syncH := handler.NewSyncHandler(changeLogRepo, taskRepo, projectRepo, areaRepo, tagRepo, checklistRepo, headingRepo, attachmentRepo, scheduleRepo, reminderRepo, repeatRuleRepo, nil, time.UTC, t.TempDir())

	r := chi.NewRouter()
	r.Get("/api/sync/pull", syncH.Pull)
	r.Post("/api/sync/push", syncH.Push)
	r.Get("/api/sync/full", syncH.Full)

	// Also mount task endpoints so we can create test data
	broker := sse.NewBroker()
	taskH := handler.NewTaskHandler(taskRepo, reminderRepo, settingsRepo, broker, nil)

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

// setupSyncRouterWithScheduler is setupSyncRouterWithDB plus a real
// scheduler, so sync status pushes run the full finishing path.
func setupSyncRouterWithScheduler(t *testing.T) (*testutil.TestClient, *repository.TaskRepository, *sql.DB) {
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
	userRepo := repository.NewUserRepository(db)

	broker := sse.NewBroker()
	sched := scheduler.New(db, taskRepo, repeatRuleRepo, checklistRepo, attachmentRepo, scheduleRepo, reminderRepo, settingsRepo, userRepo, changeLogRepo, nil, broker, time.UTC)

	syncH := handler.NewSyncHandler(changeLogRepo, taskRepo, projectRepo, areaRepo, tagRepo, checklistRepo, headingRepo, attachmentRepo, scheduleRepo, reminderRepo, repeatRuleRepo, sched, time.UTC, t.TempDir())

	r := chi.NewRouter()
	r.Post("/api/sync/push", syncH.Push)

	client := testutil.NewTestClient(t, r)
	return client, taskRepo, db
}

// TestSyncPush_CompleteRecurringSpawnsOnce (Review Focus 1): pushing
// status=completed for a recurring task in two batches succeeds both times
// and creates exactly one instance.
func TestSyncPush_CompleteRecurringSpawnsOnce(t *testing.T) {
	client, taskRepo, db := setupSyncRouterWithScheduler(t)

	created, err := taskRepo.Create(model.CreateTaskInput{Title: "Sync recurring"})
	if err != nil {
		t.Fatalf("create task: %v", err)
	}
	ruleRepo := repository.NewRepeatRuleRepository(db, nil)
	pattern := model.RecurrencePattern{Type: model.PatternWeekly, Every: 1, Mode: model.RecurrenceModeAfterCompletion}
	if _, err := ruleRepo.Upsert(created.ID, model.CreateRepeatRuleInput{Pattern: &pattern}); err != nil {
		t.Fatalf("upsert rule: %v", err)
	}

	pushOnce := func() {
		t.Helper()
		resp := client.Post("/api/sync/push", map[string]interface{}{
			"device_id": "dev1",
			"changes": []map[string]interface{}{{
				"entity":            "task",
				"entity_id":         created.ID,
				"action":            "update",
				"fields":            []string{"status"},
				"data":              map[string]interface{}{"status": "completed"},
				"client_updated_at": time.Now().Format(time.RFC3339),
			}},
		})
		testutil.AssertStatus(t, resp, http.StatusOK)

		var result handler.SyncPushResponse
		resp.JSON(t, &result)
		if len(result.Results) != 1 {
			t.Fatalf("expected 1 result, got %d (body: %s)", len(result.Results), string(resp.Body))
		}
		if result.Results[0].Status == "error" {
			t.Fatalf("push result errored: %s", result.Results[0].Error)
		}
	}

	pushOnce()
	pushOnce()

	var instances int
	if err := db.QueryRow(
		"SELECT COUNT(*) FROM tasks WHERE title = 'Sync recurring' AND deleted_at IS NULL",
	).Scan(&instances); err != nil {
		t.Fatalf("count tasks: %v", err)
	}
	if instances != 2 { // original + exactly one spawned instance
		t.Errorf("expected original + one instance after two push batches, got %d tasks", instances)
	}
}

func TestSyncDeleteProject_TasksGoToTrash(t *testing.T) {
	client, changeLogRepo, taskRepo, db := setupSyncRouterWithDB(t)

	areaRepo := repository.NewAreaRepository(db, changeLogRepo)
	projRepo := repository.NewProjectRepository(db, changeLogRepo)
	area, err := areaRepo.Create(model.CreateAreaInput{Title: "Sync area"})
	if err != nil {
		t.Fatalf("create area: %v", err)
	}
	project, err := projRepo.Create(model.CreateProjectInput{Title: "Sync project", AreaID: &area.ID})
	if err != nil {
		t.Fatalf("create project: %v", err)
	}
	taskA, err := taskRepo.Create(model.CreateTaskInput{Title: "Sync child A", ProjectID: &project.ID})
	if err != nil {
		t.Fatalf("create child task A: %v", err)
	}
	taskB, err := taskRepo.Create(model.CreateTaskInput{Title: "Sync child B", ProjectID: &project.ID, AreaID: &area.ID})
	if err != nil {
		t.Fatalf("create child task B: %v", err)
	}

	resp := client.Post("/api/sync/push", map[string]interface{}{
		"device_id": "test-device",
		"changes": []map[string]interface{}{{
			"entity": "project", "entity_id": project.ID, "action": "delete",
		}},
	})
	testutil.AssertStatus(t, resp, http.StatusOK)
	var result handler.SyncPushResponse
	resp.JSON(t, &result)
	if len(result.Results) != 1 || result.Results[0].Status != "applied" {
		t.Fatalf("expected one applied change, got %+v", result.Results)
	}

	var n int
	if err := db.QueryRow("SELECT COUNT(*) FROM projects WHERE id = ?", project.ID).Scan(&n); err != nil {
		t.Fatalf("count projects: %v", err)
	}
	if n != 0 {
		t.Errorf("project row survived the sync delete")
	}

	for _, task := range []*model.TaskDetail{taskA, taskB} {
		var deletedAt, projectID, headingID, areaID sql.NullString
		if err := db.QueryRow(
			"SELECT deleted_at, project_id, heading_id, area_id FROM tasks WHERE id = ?", task.ID,
		).Scan(&deletedAt, &projectID, &headingID, &areaID); err != nil {
			t.Fatalf("load task %s: %v", task.ID, err)
		}
		if !deletedAt.Valid {
			t.Errorf("task %s was not sent to trash (deleted_at is NULL)", task.ID)
		}
		if projectID.Valid {
			t.Errorf("trashed task %s still has project_id %q", task.ID, projectID.String)
		}
		if headingID.Valid {
			t.Errorf("trashed task %s still has heading_id %q", task.ID, headingID.String)
		}
		if areaID.Valid {
			t.Errorf("trashed task %s still has area_id %q", task.ID, areaID.String)
		}
		if entries := countChangeLogEntries(t, db, "task", task.ID, "delete"); entries != 1 {
			t.Errorf("expected 1 task delete change for %s, got %d", task.ID, entries)
		}
	}

	// The trashed tasks are visible in the trash view.
	viewRepo := repository.NewViewRepository(db)
	view, err := viewRepo.Trash(50, 0)
	if err != nil {
		t.Fatalf("load trash view: %v", err)
	}
	listed := make(map[string]bool)
	for _, g := range view.Groups {
		for _, task := range g.Tasks {
			listed[task.ID] = true
		}
	}
	for _, task := range []*model.TaskDetail{taskA, taskB} {
		if !listed[task.ID] {
			t.Errorf("task %s not listed in trash view", task.ID)
		}
	}
}

func TestSyncDeleteArea_WithProjectsIsChangeError(t *testing.T) {
	client, changeLogRepo, _, db := setupSyncRouterWithDB(t)

	areaRepo := repository.NewAreaRepository(db, changeLogRepo)
	projRepo := repository.NewProjectRepository(db, changeLogRepo)
	area, err := areaRepo.Create(model.CreateAreaInput{Title: "Sync busy area"})
	if err != nil {
		t.Fatalf("create area: %v", err)
	}
	project, err := projRepo.Create(model.CreateProjectInput{Title: "Blocking project", AreaID: &area.ID})
	if err != nil {
		t.Fatalf("create project: %v", err)
	}

	resp := client.Post("/api/sync/push", map[string]interface{}{
		"device_id": "test-device",
		"changes": []map[string]interface{}{{
			"entity": "area", "entity_id": area.ID, "action": "delete",
		}},
	})
	testutil.AssertStatus(t, resp, http.StatusOK)
	var result handler.SyncPushResponse
	resp.JSON(t, &result)
	if len(result.Results) != 1 {
		t.Fatalf("expected one result, got %d", len(result.Results))
	}
	if result.Results[0].Status != "error" {
		t.Errorf("expected error status, got %q", result.Results[0].Status)
	}
	if !strings.Contains(result.Results[0].Error, "HAS_PROJECTS") {
		t.Errorf("expected change error containing HAS_PROJECTS, got %q", result.Results[0].Error)
	}

	// Nothing is deleted.
	var areaCount, projectCount int
	_ = db.QueryRow("SELECT COUNT(*) FROM areas WHERE id = ?", area.ID).Scan(&areaCount)
	_ = db.QueryRow("SELECT COUNT(*) FROM projects WHERE id = ?", project.ID).Scan(&projectCount)
	if areaCount != 1 || projectCount != 1 {
		t.Errorf("expected area and project to survive, got area=%d project=%d", areaCount, projectCount)
	}
}

// TestSyncPush_UpdateHeadingFromAnotherProjectIsChangeError pins the REST
// PATCH/Move heading rule on the sync push path: a task update carrying a
// heading that does not belong to the effective project is a per-change
// error with the create-path wording, and the task is left unchanged.
func TestSyncPush_UpdateHeadingFromAnotherProjectIsChangeError(t *testing.T) {
	client, changeLogRepo, taskRepo, db := setupSyncRouterWithDB(t)

	areaRepo := repository.NewAreaRepository(db, changeLogRepo)
	projRepo := repository.NewProjectRepository(db, changeLogRepo)
	area, err := areaRepo.Create(model.CreateAreaInput{Title: "Sync area"})
	if err != nil {
		t.Fatalf("create area: %v", err)
	}
	p1, err := projRepo.Create(model.CreateProjectInput{Title: "Sync project one", AreaID: &area.ID})
	if err != nil {
		t.Fatalf("create project one: %v", err)
	}
	p2, err := projRepo.Create(model.CreateProjectInput{Title: "Sync project two", AreaID: &area.ID})
	if err != nil {
		t.Fatalf("create project two: %v", err)
	}
	if _, err := db.Exec(
		"INSERT INTO headings (id, title, project_id, sort_order) VALUES ('h1', 'Heading One', ?, 0)", p1.ID,
	); err != nil {
		t.Fatalf("insert heading: %v", err)
	}
	headingID := "h1"
	task, err := taskRepo.Create(model.CreateTaskInput{
		Title: "Sync heading guard", ProjectID: &p1.ID, HeadingID: &headingID,
	})
	if err != nil {
		t.Fatalf("create task: %v", err)
	}

	resp := client.Post("/api/sync/push", map[string]interface{}{
		"device_id": "test-device",
		"changes": []map[string]interface{}{{
			"entity": "task", "entity_id": task.ID, "action": "update",
			"fields":            []string{"project_id", "heading_id"},
			"data":              map[string]interface{}{"project_id": p2.ID, "heading_id": "h1"},
			"client_updated_at": time.Now().Format(time.RFC3339),
		}},
	})
	testutil.AssertStatus(t, resp, http.StatusOK)
	var result handler.SyncPushResponse
	resp.JSON(t, &result)
	if len(result.Results) != 1 {
		t.Fatalf("expected one result, got %d (body: %s)", len(result.Results), string(resp.Body))
	}
	if result.Results[0].Status != "error" {
		t.Fatalf("expected per-change error, got %q (body: %s)", result.Results[0].Status, string(resp.Body))
	}
	if result.Results[0].Error != "heading_id: heading is not in this project" {
		t.Errorf("expected 'heading_id: heading is not in this project', got %q", result.Results[0].Error)
	}

	// The rejected change must not have been applied.
	var projectID, headingOut sql.NullString
	if err := db.QueryRow(
		"SELECT project_id, heading_id FROM tasks WHERE id = ?", task.ID,
	).Scan(&projectID, &headingOut); err != nil {
		t.Fatalf("load task: %v", err)
	}
	if !projectID.Valid || projectID.String != p1.ID {
		t.Errorf("task project changed: got %v, want %s", projectID, p1.ID)
	}
	if !headingOut.Valid || headingOut.String != "h1" {
		t.Errorf("task heading changed: got %v, want h1", headingOut)
	}
}
