package router_test

import (
	"bytes"
	"database/sql"
	"io"
	"mime/multipart"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/collinjanssen/thingstodo/internal/config"
	"github.com/collinjanssen/thingstodo/internal/push"
	"github.com/collinjanssen/thingstodo/internal/repository"
	"github.com/collinjanssen/thingstodo/internal/router"
	"github.com/collinjanssen/thingstodo/internal/scheduler"
	"github.com/collinjanssen/thingstodo/internal/sse"
	"github.com/collinjanssen/thingstodo/internal/testutil"
)

func TestHealthEndpoint(t *testing.T) {
	db := testutil.SetupTestDB(t)
	cfg := config.Config{AuthMode: "proxy", AttachmentsPath: t.TempDir()}
	broker := sse.NewBroker()
	taskRepo := repository.NewTaskRepository(db, nil)
	ruleRepo := repository.NewRepeatRuleRepository(db, nil)
	checklistRepo := repository.NewChecklistRepository(db, nil)
	attachRepo := repository.NewAttachmentRepository(db, nil)
	scheduleRepo := repository.NewScheduleRepository(db, nil)
	reminderRepo := repository.NewReminderRepository(db, nil)
	settingsRepo := repository.NewUserSettingsRepository(db)
	userRepo := repository.NewUserRepository(db)
	pushSubRepo := repository.NewPushSubscriptionRepository(db)
	pushSender := push.NewSender(pushSubRepo, "", "", "")
	changeLogRepo := repository.NewChangeLogRepository(db)
	sched := scheduler.New(db, taskRepo, ruleRepo, checklistRepo, attachRepo, scheduleRepo, reminderRepo, settingsRepo, userRepo, changeLogRepo, pushSender, broker, time.UTC)

	handler := router.New(db, cfg, broker, sched)
	client := testutil.NewTestClient(t, handler)

	resp := client.Get("/health")
	testutil.AssertStatus(t, resp, 200)
	testutil.AssertJSONField(t, resp, "status", "ok")
}

// newRouterClient builds a router behind proxy auth, like TestHealthEndpoint
// but with the Remote-User proxy header wired up for /api requests.
func newRouterClient(t *testing.T, cfg config.Config) (*testutil.TestClient, *sql.DB) {
	t.Helper()
	cfg.AuthMode = "proxy"
	cfg.AuthProxyHeader = "Remote-User"
	if cfg.AttachmentsPath == "" {
		cfg.AttachmentsPath = t.TempDir()
	}

	db := testutil.SetupTestDB(t)
	broker := sse.NewBroker()
	taskRepo := repository.NewTaskRepository(db, nil)
	ruleRepo := repository.NewRepeatRuleRepository(db, nil)
	checklistRepo := repository.NewChecklistRepository(db, nil)
	attachRepo := repository.NewAttachmentRepository(db, nil)
	scheduleRepo := repository.NewScheduleRepository(db, nil)
	reminderRepo := repository.NewReminderRepository(db, nil)
	settingsRepo := repository.NewUserSettingsRepository(db)
	userRepo := repository.NewUserRepository(db)
	pushSubRepo := repository.NewPushSubscriptionRepository(db)
	pushSender := push.NewSender(pushSubRepo, "", "", "")
	changeLogRepo := repository.NewChangeLogRepository(db)
	sched := scheduler.New(db, taskRepo, ruleRepo, checklistRepo, attachRepo, scheduleRepo, reminderRepo, settingsRepo, userRepo, changeLogRepo, pushSender, broker, time.UTC)

	return testutil.NewTestClient(t, router.New(db, cfg, broker, sched)), db
}

// doReq sends a request with the proxy-auth Remote-User header and returns
// the response with its body fully read.
func doReq(t *testing.T, client *testutil.TestClient, method, path, userID string, body io.Reader, contentType string) *testutil.TestResponse {
	t.Helper()
	req, err := http.NewRequest(method, client.Server.URL+path, body)
	if err != nil {
		t.Fatalf("failed to create request: %v", err)
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	req.Header.Set("Remote-User", userID)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()
	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("failed to read response body: %v", err)
	}
	return &testutil.TestResponse{Response: resp, Body: respBody}
}

func TestRouter_OversizedJSONIs413(t *testing.T) {
	client, db := newRouterClient(t, config.Config{})

	user, err := repository.NewUserRepository(db).Create("alice", "hash")
	if err != nil {
		t.Fatalf("failed to create user: %v", err)
	}

	// 11 MiB JSON body: over the 10 MiB JSON body limit.
	payload := `{"title":"` + strings.Repeat("a", 11<<20) + `"}`

	resp := doReq(t, client, http.MethodPost, "/api/tasks", user.ID, strings.NewReader(payload), "application/json")

	testutil.AssertStatus(t, resp, http.StatusRequestEntityTooLarge)
	testutil.AssertJSONField(t, resp, "code", "BODY_TOO_LARGE")
}

func TestRouter_UploadAbove10MiB(t *testing.T) {
	client, db := newRouterClient(t, config.Config{MaxUploadSize: 12 << 20})

	user, err := repository.NewUserRepository(db).Create("bob", "hash")
	if err != nil {
		t.Fatalf("failed to create user: %v", err)
	}

	// Create a task to attach to.
	resp := doReq(t, client, http.MethodPost, "/api/tasks", user.ID, strings.NewReader(`{"title":"upload target"}`), "application/json")
	testutil.AssertStatus(t, resp, http.StatusCreated)
	var task struct {
		ID string `json:"id"`
	}
	resp.JSON(t, &task)
	if task.ID == "" {
		t.Fatalf("created task has no id; body: %s", string(resp.Body))
	}

	// Multipart upload just over 10 MiB: allowed while MaxUploadSize is 12 MiB.
	var buf bytes.Buffer
	writer := multipart.NewWriter(&buf)
	part, err := writer.CreateFormFile("file", "big.bin")
	if err != nil {
		t.Fatalf("failed to create form file: %v", err)
	}
	if _, err := part.Write(make([]byte, 10<<20+1024)); err != nil {
		t.Fatalf("failed to write form file: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("failed to close multipart writer: %v", err)
	}

	resp = doReq(t, client, http.MethodPost, "/api/tasks/"+task.ID+"/attachments", user.ID, &buf, writer.FormDataContentType())

	testutil.AssertStatus(t, resp, http.StatusCreated)
}
