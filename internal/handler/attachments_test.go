package handler_test

import (
	"bytes"
	"database/sql"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
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

const secretDBContent = "top-secret-db-contents"

// setupAttachmentsRouter mounts the attachment and task handlers, mirroring
// setupSyncRouter in sync_test.go. The attachments dir lives at
// <tmp>/attachments so "../secret.db" from it escapes to <tmp>/secret.db.
func setupAttachmentsRouter(t *testing.T) (*testutil.TestClient, *repository.AttachmentRepository, string) {
	t.Helper()
	db := testutil.SetupTestDB(t)

	changeLogRepo := repository.NewChangeLogRepository(db)
	taskRepo := repository.NewTaskRepository(db, changeLogRepo)
	reminderRepo := repository.NewReminderRepository(db, changeLogRepo)
	settingsRepo := repository.NewUserSettingsRepository(db)
	attachmentRepo := repository.NewAttachmentRepository(db, changeLogRepo)

	attachmentsPath := filepath.Join(t.TempDir(), "attachments")
	if err := os.MkdirAll(attachmentsPath, 0o755); err != nil {
		t.Fatalf("failed to create attachments dir: %v", err)
	}

	broker := sse.NewBroker()
	attachmentH := handler.NewAttachmentHandler(attachmentRepo, broker, attachmentsPath, 25<<20)
	taskH := handler.NewTaskHandler(taskRepo, reminderRepo, settingsRepo, broker, nil)

	r := chi.NewRouter()
	r.Post("/api/tasks", taskH.Create)
	r.Post("/api/tasks/{id}/attachments", attachmentH.Create)
	r.Delete("/api/attachments/{id}", attachmentH.Delete)
	r.Get("/api/attachments/{id}/file", attachmentH.Download)

	client := testutil.NewTestClient(t, r)
	return client, attachmentRepo, attachmentsPath
}

func createTaskForAttachment(t *testing.T, client *testutil.TestClient) string {
	t.Helper()
	resp := client.Post("/api/tasks", map[string]string{"title": "Attachment test"})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("expected 201 creating task, got %d: %s", resp.StatusCode, resp.Body)
	}
	var created map[string]interface{}
	resp.JSON(t, &created)
	return created["id"].(string)
}

// writeSecretDB places the sentinel file next to the attachments dir, i.e.
// where "../secret.db" resolves to from inside it.
func writeSecretDB(t *testing.T, attachmentsPath string) string {
	t.Helper()
	secretPath := filepath.Join(filepath.Dir(attachmentsPath), "secret.db")
	if err := os.WriteFile(secretPath, []byte(secretDBContent), 0o600); err != nil {
		t.Fatalf("failed to write sentinel file: %v", err)
	}
	return secretPath
}

func postMultipart(t *testing.T, client *testutil.TestClient, path, filename, content string) *testutil.TestResponse {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	fw, err := mw.CreateFormFile("file", filename)
	if err != nil {
		t.Fatalf("failed to create form file: %v", err)
	}
	if _, err := fw.Write([]byte(content)); err != nil {
		t.Fatalf("failed to write form file: %v", err)
	}
	if err := mw.Close(); err != nil {
		t.Fatalf("failed to close multipart writer: %v", err)
	}

	req, err := http.NewRequest(http.MethodPost, client.Server.URL+path, &buf)
	if err != nil {
		t.Fatalf("failed to create request: %v", err)
	}
	req.Header.Set("Content-Type", mw.FormDataContentType())

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("failed to read response body: %v", err)
	}
	return &testutil.TestResponse{Response: resp, Body: body}
}

func TestCreateAttachment_LinkWithoutType(t *testing.T) {
	client, _, _ := setupAttachmentsRouter(t)
	taskID := createTaskForAttachment(t, client)

	resp := client.Post("/api/tasks/"+taskID+"/attachments", map[string]string{
		"url":   "https://x.test",
		"title": "x",
	})
	testutil.AssertStatus(t, resp, http.StatusCreated)
	testutil.AssertJSONField(t, resp, "type", "link")
	testutil.AssertJSONField(t, resp, "url", "https://x.test")
}

func TestCreateAttachment_FileTypeRejectedOverJSON(t *testing.T) {
	client, _, attachmentsPath := setupAttachmentsRouter(t)
	writeSecretDB(t, attachmentsPath)
	taskID := createTaskForAttachment(t, client)

	resp := client.Post("/api/tasks/"+taskID+"/attachments", map[string]string{
		"type": "file",
		"url":  "../secret.db",
	})
	testutil.AssertStatus(t, resp, http.StatusBadRequest)

	var body map[string]string
	resp.JSON(t, &body)
	if body["code"] != "VALIDATION" {
		t.Errorf("expected code VALIDATION, got %q", body["code"])
	}
	if body["error"] != "only link attachments can be created via JSON" {
		t.Errorf("unexpected error message: %q", body["error"])
	}
}

func TestCreateAttachment_ScriptSchemesRejected(t *testing.T) {
	client, _, _ := setupAttachmentsRouter(t)
	taskID := createTaskForAttachment(t, client)

	cases := []string{
		"javascript:alert(1)",
		"JavaScript:alert(1)",
		" data:text/html,x",
		"vbscript:x",
		"file:///etc/passwd",
		"java\tscript:alert(1)",
		"https://x.test/\nfoo",
	}

	for _, rawURL := range cases {
		resp := client.Post("/api/tasks/"+taskID+"/attachments", map[string]string{
			"type": "link",
			"url":  rawURL,
		})
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("url %q: expected 400, got %d\nbody: %s", rawURL, resp.StatusCode, resp.Body)
		}
	}
}

func TestCreateAttachment_AppLinksAllowed(t *testing.T) {
	client, _, _ := setupAttachmentsRouter(t)
	taskID := createTaskForAttachment(t, client)

	for _, rawURL := range []string{"obsidian://open?vault=v", "siyuan://blocks/1"} {
		resp := client.Post("/api/tasks/"+taskID+"/attachments", map[string]string{
			"type": "link",
			"url":  rawURL,
		})
		testutil.AssertStatus(t, resp, http.StatusCreated)
	}
}

// TestCreateAttachment_ReplayWithOtherTaskIs409 pins the parent check of
// the idempotent replay: the same client id replayed against the same task
// returns the stored attachment (200), but against another task it is 409
// CONFLICT, never another task's attachment.
func TestCreateAttachment_ReplayWithOtherTaskIs409(t *testing.T) {
	client, _, _ := setupAttachmentsRouter(t)
	taskA := createTaskForAttachment(t, client)
	taskB := createTaskForAttachment(t, client)

	body := map[string]interface{}{"id": "xparAtt00001", "type": "link", "title": "Spec", "url": "https://example.com"}
	first := client.Post("/api/tasks/"+taskA+"/attachments", body)
	testutil.AssertStatus(t, first, http.StatusCreated)

	replay := client.Post("/api/tasks/"+taskA+"/attachments", body)
	testutil.AssertStatus(t, replay, http.StatusOK)

	conflict := client.Post("/api/tasks/"+taskB+"/attachments", body)
	testutil.AssertStatus(t, conflict, http.StatusConflict)
	testutil.AssertJSONField(t, conflict, "code", "CONFLICT")
}

func TestDownloadAttachment_TraversalBlocked(t *testing.T) {
	client, attachmentRepo, attachmentsPath := setupAttachmentsRouter(t)
	writeSecretDB(t, attachmentsPath)
	taskID := createTaskForAttachment(t, client)

	att, err := attachmentRepo.Create(taskID, model.CreateAttachmentInput{
		Type: "file",
		URL:  "../secret.db",
	})
	if err != nil {
		t.Fatalf("failed to insert attachment row: %v", err)
	}

	resp := client.Get("/api/attachments/" + att.ID + "/file")
	testutil.AssertStatus(t, resp, http.StatusNotFound)
	if strings.Contains(string(resp.Body), secretDBContent) {
		t.Error("response leaked sentinel file contents")
	}
}

func TestDeleteAttachment_TraversalDoesNotRemoveOutsideFile(t *testing.T) {
	client, attachmentRepo, attachmentsPath := setupAttachmentsRouter(t)
	secretPath := writeSecretDB(t, attachmentsPath)
	taskID := createTaskForAttachment(t, client)

	att, err := attachmentRepo.Create(taskID, model.CreateAttachmentInput{
		Type: "file",
		URL:  "../secret.db",
	})
	if err != nil {
		t.Fatalf("failed to insert attachment row: %v", err)
	}

	resp := client.Delete("/api/attachments/" + att.ID)
	testutil.AssertStatus(t, resp, http.StatusNoContent)

	if _, err := os.Stat(secretPath); err != nil {
		t.Errorf("sentinel file was removed through traversal: %v", err)
	}
}

func TestFileAttachment_UploadDownloadDelete(t *testing.T) {
	client, _, attachmentsPath := setupAttachmentsRouter(t)
	taskID := createTaskForAttachment(t, client)

	content := "hello attachment bytes"
	resp := postMultipart(t, client, "/api/tasks/"+taskID+"/attachments", "notes.txt", content)
	testutil.AssertStatus(t, resp, http.StatusCreated)

	var att model.Attachment
	resp.JSON(t, &att)
	if att.Type != "file" {
		t.Fatalf("expected type file, got %q", att.Type)
	}

	dl := client.Get("/api/attachments/" + att.ID + "/file")
	testutil.AssertStatus(t, dl, http.StatusOK)
	if string(dl.Body) != content {
		t.Errorf("downloaded bytes differ: got %q", dl.Body)
	}

	del := client.Delete("/api/attachments/" + att.ID)
	testutil.AssertStatus(t, del, http.StatusNoContent)

	if _, err := os.Stat(filepath.Join(attachmentsPath, att.URL)); !os.IsNotExist(err) {
		t.Errorf("stored file still exists after delete (stat err: %v)", err)
	}
}

// setupSharedAttachmentRouter mirrors setupSyncRouterWithScheduler: it wires
// the real scheduler into the task finishing path and the sync handler, and
// mounts the attachment routes over a shared attachments dir.
func setupSharedAttachmentRouter(t *testing.T) (*testutil.TestClient, *repository.AttachmentRepository, *sql.DB, string) {
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

	attachmentsPath := filepath.Join(t.TempDir(), "attachments")
	if err := os.MkdirAll(attachmentsPath, 0o755); err != nil {
		t.Fatalf("failed to create attachments dir: %v", err)
	}

	attachmentH := handler.NewAttachmentHandler(attachmentRepo, broker, attachmentsPath, 25<<20)
	taskH := handler.NewTaskHandler(taskRepo, reminderRepo, settingsRepo, broker, sched)
	syncH := handler.NewSyncHandler(changeLogRepo, taskRepo, projectRepo, areaRepo, tagRepo, checklistRepo, headingRepo, attachmentRepo, scheduleRepo, reminderRepo, repeatRuleRepo, sched, time.UTC, attachmentsPath, broker)

	r := chi.NewRouter()
	r.Post("/api/tasks", taskH.Create)
	r.Patch("/api/tasks/{id}/complete", taskH.Complete)
	r.Post("/api/tasks/{id}/attachments", attachmentH.Create)
	r.Delete("/api/attachments/{id}", attachmentH.Delete)
	r.Get("/api/attachments/{id}/file", attachmentH.Download)
	r.Post("/api/sync/push", syncH.Push)

	client := testutil.NewTestClient(t, r)
	return client, attachmentRepo, db, attachmentsPath
}

// spawnSharedFileCopies runs the data-loss scenario setup: a weekly
// recurring task with an uploaded file is completed through the finishing
// path (Task 8), so the spawned instance carries a copy row pointing at the
// same stored file. It returns the original's and the copy's attachment rows.
func spawnSharedFileCopies(t *testing.T, client *testutil.TestClient, attachmentRepo *repository.AttachmentRepository, db *sql.DB) (model.Attachment, model.Attachment) {
	t.Helper()

	resp := client.Post("/api/tasks", map[string]string{"title": "Recurring with file"})
	testutil.AssertStatus(t, resp, http.StatusCreated)
	var created map[string]interface{}
	resp.JSON(t, &created)
	taskID := created["id"].(string)

	ruleRepo := repository.NewRepeatRuleRepository(db, nil)
	pattern := model.RecurrencePattern{Type: model.PatternWeekly, Every: 1, Mode: model.RecurrenceModeAfterCompletion}
	if _, err := ruleRepo.Upsert(taskID, model.CreateRepeatRuleInput{Pattern: &pattern}); err != nil {
		t.Fatalf("upsert repeat rule: %v", err)
	}

	up := postMultipart(t, client, "/api/tasks/"+taskID+"/attachments", "notes.txt", "shared file bytes")
	testutil.AssertStatus(t, up, http.StatusCreated)
	var original model.Attachment
	up.JSON(t, &original)

	done := client.Patch("/api/tasks/"+taskID+"/complete", nil)
	testutil.AssertStatus(t, done, http.StatusOK)

	// The spawned instance is the remaining open task with the same title.
	var nextID string
	if err := db.QueryRow(
		"SELECT id FROM tasks WHERE title = ? AND status = 'open' AND deleted_at IS NULL AND id != ?",
		"Recurring with file", taskID,
	).Scan(&nextID); err != nil {
		t.Fatalf("find spawned instance: %v", err)
	}

	copies, err := attachmentRepo.ListByTask(nextID)
	if err != nil {
		t.Fatalf("list spawned instance attachments: %v", err)
	}
	if len(copies) != 1 {
		t.Fatalf("expected 1 copied attachment, got %d", len(copies))
	}
	if copies[0].ID == original.ID {
		t.Fatal("expected the copy to be a new row with its own id")
	}
	if copies[0].URL != original.URL {
		t.Fatalf("expected the copy to share the stored file: original %q, copy %q", original.URL, copies[0].URL)
	}
	return original, copies[0]
}

func TestDeleteAttachment_KeepsSharedFile(t *testing.T) {
	client, attachmentRepo, db, attachmentsPath := setupSharedAttachmentRouter(t)
	originalAtt, copyAtt := spawnSharedFileCopies(t, client, attachmentRepo, db)

	storedPath := filepath.Join(attachmentsPath, copyAtt.URL)

	// Deleting the original's copy must leave the shared file and the new
	// instance's row usable.
	del := client.Delete("/api/attachments/" + originalAtt.ID)
	testutil.AssertStatus(t, del, http.StatusNoContent)

	dl := client.Get("/api/attachments/" + copyAtt.ID + "/file")
	testutil.AssertStatus(t, dl, http.StatusOK)
	if string(dl.Body) != "shared file bytes" {
		t.Errorf("downloaded bytes differ: got %q", dl.Body)
	}
	if _, err := os.Stat(storedPath); err != nil {
		t.Errorf("shared stored file must survive deleting one copy: %v", err)
	}

	// Deleting the last remaining copy removes the stored file.
	last := client.Delete("/api/attachments/" + copyAtt.ID)
	testutil.AssertStatus(t, last, http.StatusNoContent)
	if _, err := os.Stat(storedPath); !os.IsNotExist(err) {
		t.Errorf("stored file must be removed with the last copy (stat err: %v)", err)
	}
}

func TestSyncDeleteAttachment_KeepsSharedFile(t *testing.T) {
	client, attachmentRepo, db, attachmentsPath := setupSharedAttachmentRouter(t)
	originalAtt, copyAtt := spawnSharedFileCopies(t, client, attachmentRepo, db)

	storedPath := filepath.Join(attachmentsPath, copyAtt.URL)

	pushDelete := func(id string) {
		t.Helper()
		resp := client.Post("/api/sync/push", map[string]interface{}{
			"device_id": "dev1",
			"changes": []map[string]interface{}{{
				"entity":            "attachment",
				"entity_id":         id,
				"action":            "delete",
				"data":              map[string]interface{}{},
				"client_updated_at": time.Now().Format(time.RFC3339),
			}},
		})
		testutil.AssertStatus(t, resp, http.StatusOK)
		var result handler.SyncPushResponse
		resp.JSON(t, &result)
		if len(result.Results) != 1 {
			t.Fatalf("expected 1 result, got %d (body: %s)", len(result.Results), string(resp.Body))
		}
		if result.Results[0].Status != "applied" {
			t.Fatalf("sync delete not applied: %s", result.Results[0].Error)
		}
	}

	pushDelete(originalAtt.ID)

	dl := client.Get("/api/attachments/" + copyAtt.ID + "/file")
	testutil.AssertStatus(t, dl, http.StatusOK)
	if _, err := os.Stat(storedPath); err != nil {
		t.Errorf("shared stored file must survive deleting one copy: %v", err)
	}

	pushDelete(copyAtt.ID)
	if _, err := os.Stat(storedPath); !os.IsNotExist(err) {
		t.Errorf("stored file must be removed with the last copy (stat err: %v)", err)
	}
}
