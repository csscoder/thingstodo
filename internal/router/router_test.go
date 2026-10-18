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
	return newRouterClientForConfig(t, cfg)
}

// newRouterClientForConfig builds a router that keeps the auth mode of
// cfg, for tests that need something other than proxy auth.
func newRouterClientForConfig(t *testing.T, cfg config.Config) (*testutil.TestClient, *sql.DB) {
	t.Helper()
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
	defer func() { _ = resp.Body.Close() }()
	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("failed to read response body: %v", err)
	}
	return &testutil.TestResponse{Response: resp, Body: respBody}
}

// doRaw sends req against the test server and returns the response with
// its body fully read, without adding any auth headers.
func doRaw(t *testing.T, client *testutil.TestClient, req *http.Request) *testutil.TestResponse {
	t.Helper()
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("failed to read response body: %v", err)
	}
	return &testutil.TestResponse{Response: resp, Body: respBody}
}

func newOptionsRequest(t *testing.T, client *testutil.TestClient, path string) *http.Request {
	t.Helper()
	req, err := http.NewRequest(http.MethodOptions, client.Server.URL+path, nil)
	if err != nil {
		t.Fatalf("failed to create request: %v", err)
	}
	return req
}

// assertCORSHeaders pins the spec A2 header set verbatim, plus the
// invariant that Access-Control-Allow-Credentials is never sent.
func assertCORSHeaders(t *testing.T, h http.Header) {
	t.Helper()
	for name, want := range map[string]string{
		"Access-Control-Allow-Origin":   "*",
		"Access-Control-Allow-Methods":  "GET, POST, PUT, PATCH, DELETE, OPTIONS",
		"Access-Control-Allow-Headers":  "Authorization, Content-Type, Mcp-Session-Id, Mcp-Protocol-Version, Last-Event-ID",
		"Access-Control-Expose-Headers": "Mcp-Session-Id",
		"Access-Control-Max-Age":        "600",
	} {
		if got := h.Get(name); got != want {
			t.Errorf("header %s: expected %q, got %q", name, want, got)
		}
	}
	assertNoCredentialsHeader(t, h)
}

func assertNoCredentialsHeader(t *testing.T, h http.Header) {
	t.Helper()
	if got := h.Get("Access-Control-Allow-Credentials"); got != "" {
		t.Errorf("Access-Control-Allow-Credentials must never be set, got %q", got)
	}
}

// assertNoCORSHeaders asserts that none of the A2 headers (nor the
// forbidden credentials header) were added.
func assertNoCORSHeaders(t *testing.T, h http.Header) {
	t.Helper()
	for _, name := range []string{
		"Access-Control-Allow-Origin",
		"Access-Control-Allow-Methods",
		"Access-Control-Allow-Headers",
		"Access-Control-Expose-Headers",
		"Access-Control-Max-Age",
		"Access-Control-Allow-Credentials",
	} {
		if got := h.Get(name); got != "" {
			t.Errorf("expected no %s header, got %q", name, got)
		}
	}
}

func TestCORS_Preflight(t *testing.T) {
	client, _ := newRouterClient(t, config.Config{})

	// No Remote-User header is sent: a preflight that reached the auth
	// middleware would answer 401, so a 204 proves OPTIONS is answered
	// before auth runs.
	req := newOptionsRequest(t, client, "/api/tasks")
	req.Header.Set("Origin", "https://widget.example")
	resp := doRaw(t, client, req)

	testutil.AssertStatus(t, resp, http.StatusNoContent)
	assertCORSHeaders(t, resp.Header)

	// The same preflight policy covers the /mcp mount point.
	mcpReq := newOptionsRequest(t, client, "/mcp")
	mcpReq.Header.Set("Origin", "https://widget.example")
	mcpResp := doRaw(t, client, mcpReq)

	testutil.AssertStatus(t, mcpResp, http.StatusNoContent)
	assertCORSHeaders(t, mcpResp.Header)
}

func TestCORS_PreflightLowercaseHeaders(t *testing.T) {
	client, _ := newRouterClient(t, config.Config{})

	req := newOptionsRequest(t, client, "/api/tasks")
	// Sent with a lowercase field name on the wire. Go canonicalizes
	// header names when parsing the request, and http.Header lookups are
	// case-insensitive, so the middleware must not depend on the case
	// any header arrives — or is read — in.
	req.Header["origin"] = []string{"https://widget.example"}
	resp := doRaw(t, client, req)

	testutil.AssertStatus(t, resp, http.StatusNoContent)
	for name, want := range map[string]string{
		"access-control-allow-origin":   "*",
		"access-control-allow-methods":  "GET, POST, PUT, PATCH, DELETE, OPTIONS",
		"access-control-allow-headers":  "Authorization, Content-Type, Mcp-Session-Id, Mcp-Protocol-Version, Last-Event-ID",
		"access-control-expose-headers": "Mcp-Session-Id",
		"access-control-max-age":        "600",
	} {
		if got := resp.Header.Get(name); got != want {
			t.Errorf("header %s: expected %q, got %q", name, want, got)
		}
	}
	assertNoCredentialsHeader(t, resp.Header)
}

func TestCORS_TokenGetCrossOrigin(t *testing.T) {
	client, db := newRouterClient(t, config.Config{})

	if _, err := repository.NewUserRepository(db).Create("tokenuser", "hash"); err != nil {
		t.Fatalf("failed to create user: %v", err)
	}
	_, token, err := repository.NewAPITokenRepository(db).Create("cors")
	if err != nil {
		t.Fatalf("failed to create token: %v", err)
	}

	req, err := http.NewRequest(http.MethodGet, client.Server.URL+"/api/tasks", nil)
	if err != nil {
		t.Fatalf("failed to create request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Origin", "https://widget.example")
	resp := doRaw(t, client, req)

	testutil.AssertStatus(t, resp, http.StatusOK)
	if got := resp.Header.Get("Access-Control-Allow-Origin"); got != "*" {
		t.Errorf("Access-Control-Allow-Origin: expected *, got %q", got)
	}
	assertNoCredentialsHeader(t, resp.Header)
}

func TestCORS_OffInNoneMode(t *testing.T) {
	client, db := newRouterClientForConfig(t, config.Config{AuthMode: "none"})

	if _, err := repository.NewUserRepository(db).Create("noneuser", "hash"); err != nil {
		t.Fatalf("failed to create user: %v", err)
	}

	// CORS disabled: the preflight is not answered with 204, the request
	// falls through to chi's 405, and no CORS headers are added.
	req := newOptionsRequest(t, client, "/api/tasks")
	req.Header.Set("Origin", "https://widget.example")
	resp := doRaw(t, client, req)

	testutil.AssertStatus(t, resp, http.StatusMethodNotAllowed)
	assertNoCORSHeaders(t, resp.Header)

	// Plain requests stay header-free as well.
	getReq, err := http.NewRequest(http.MethodGet, client.Server.URL+"/api/tasks", nil)
	if err != nil {
		t.Fatalf("failed to create request: %v", err)
	}
	getResp := doRaw(t, client, getReq)

	testutil.AssertStatus(t, getResp, http.StatusOK)
	assertNoCORSHeaders(t, getResp.Header)

	// The /mcp mount point exists but is empty; with CORS off its
	// preflight is not answered either — the request 404s inside the
	// group instead of falling through to the SPA.
	mcpReq := newOptionsRequest(t, client, "/mcp")
	mcpReq.Header.Set("Origin", "https://widget.example")
	mcpResp := doRaw(t, client, mcpReq)

	testutil.AssertStatus(t, mcpResp, http.StatusNotFound)
	assertNoCORSHeaders(t, mcpResp.Header)
}

func TestRouter_UnknownAPIRouteIsJSON404(t *testing.T) {
	client, _ := newRouterClient(t, config.Config{})

	resp := doReq(t, client, http.MethodGet, "/api/nope", "u1", nil, "")

	testutil.AssertStatus(t, resp, http.StatusNotFound)
	testutil.AssertJSONField(t, resp, "code", "NOT_FOUND")
}

func TestRouter_WrongMethodIsJSON405(t *testing.T) {
	client, _ := newRouterClient(t, config.Config{})

	resp := doReq(t, client, http.MethodPut, "/api/tasks", "u1", nil, "")

	testutil.AssertStatus(t, resp, http.StatusMethodNotAllowed)
	testutil.AssertJSONField(t, resp, "code", "METHOD_NOT_ALLOWED")
	if resp.Header.Get("Allow") == "" {
		t.Error("expected non-empty Allow header on 405 response")
	}
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

func TestRouter_OversizedJSONOnUploadRouteIs413(t *testing.T) {
	client, db := newRouterClient(t, config.Config{})

	user, err := repository.NewUserRepository(db).Create("carol", "hash")
	if err != nil {
		t.Fatalf("failed to create user: %v", err)
	}

	// Create a task to attach to.
	resp := doReq(t, client, http.MethodPost, "/api/tasks", user.ID, strings.NewReader(`{"title":"json cap target"}`), "application/json")
	testutil.AssertStatus(t, resp, http.StatusCreated)
	var task struct {
		ID string `json:"id"`
	}
	resp.JSON(t, &task)
	if task.ID == "" {
		t.Fatalf("created task has no id; body: %s", string(resp.Body))
	}

	// The upload route is exempt from the router-level BodyLimit middleware,
	// so only the handler-side MaxBytesReader caps JSON bodies there. An
	// 11 MiB JSON body must still be rejected with 413 BODY_TOO_LARGE.
	payload := `{"link":"https://example.com/` + strings.Repeat("a", 11<<20) + `"}`

	resp = doReq(t, client, http.MethodPost, "/api/tasks/"+task.ID+"/attachments", user.ID, strings.NewReader(payload), "application/json")

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
