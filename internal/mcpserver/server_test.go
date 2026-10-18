package mcpserver_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sort"
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
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// These tests exercise the MCP server the way Hermes does: a real MCP
// client over Streamable HTTP (go-sdk StreamableClientTransport) against a
// full router.New mounted on an httptest server. Tool calls therefore
// cross the real /mcp transport, auth middleware and in-process dispatch
// path end to end.

// headerRoundTripper injects fixed headers (credentials) into every
// outgoing MCP transport request.
type headerRoundTripper struct {
	headers http.Header
}

func (h *headerRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	for name, values := range h.headers {
		for _, v := range values {
			req.Header.Add(name, v)
		}
	}
	return http.DefaultTransport.RoundTrip(req)
}

// newMCPHTTPServer builds a full router over a fresh test database and
// serves it. The caller sets cfg's auth mode; the database is returned for
// user and token fixtures.
func newMCPHTTPServer(t *testing.T, cfg config.Config) (*httptest.Server, *sql.DB) {
	t.Helper()
	if cfg.AuthMode == "" {
		cfg.AuthMode = "proxy"
		cfg.AuthProxyHeader = "Remote-User"
	}
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

	srv := httptest.NewServer(router.New(db, cfg, broker, sched))
	t.Cleanup(srv.Close)
	return srv, db
}

// newMCPSession connects a real MCP client to the server's /mcp endpoint,
// authenticating every transport request with the given headers.
func newMCPSession(t *testing.T, srv *httptest.Server, headers http.Header) *mcp.ClientSession {
	t.Helper()
	transport := &mcp.StreamableClientTransport{
		Endpoint:             srv.URL + "/mcp",
		HTTPClient:           &http.Client{Transport: &headerRoundTripper{headers: headers}},
		DisableStandaloneSSE: true,
	}
	client := mcp.NewClient(&mcp.Implementation{Name: "mcp-test-client", Version: "1.0"}, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	session, err := client.Connect(ctx, transport, nil)
	if err != nil {
		t.Fatalf("mcp client connect: %v", err)
	}
	t.Cleanup(func() { _ = session.Close() })
	return session
}

// newTokenSession is the standard Hermes-style environment: proxy auth
// mode with a named API token carried as a Bearer header.
func newTokenSession(t *testing.T) *mcp.ClientSession {
	t.Helper()
	srv, db := newMCPHTTPServer(t, config.Config{AuthMode: "proxy", AuthProxyHeader: "Remote-User"})
	if _, err := repository.NewUserRepository(db).Create("mcpuser", "hash"); err != nil {
		t.Fatalf("create user: %v", err)
	}
	_, token, err := repository.NewAPITokenRepository(db).Create("mcp-test")
	if err != nil {
		t.Fatalf("create api token: %v", err)
	}
	return newMCPSession(t, srv, http.Header{"Authorization": {"Bearer " + token}})
}

// callTool invokes a tool and returns the first text content plus the
// IsError flag, failing the test on protocol-level errors.
func callTool(t *testing.T, session *mcp.ClientSession, name string, args map[string]any) (string, bool) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	res, err := session.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("call tool %s: %v", name, err)
	}
	text := ""
	for _, c := range res.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			text = tc.Text
		}
	}
	return text, res.IsError
}

// wantTools is the complete tool list: the 44 tools of the TypeScript
// server (same names) plus the ten new ones.
var wantTools = []string{
	// tasks (TS port)
	"search_tasks", "get_task", "create_task", "update_task",
	"complete_task", "cancel_task", "wontdo_task", "reopen_task",
	"delete_task", "purge_task", "restore_task",
	// projects (TS port)
	"list_projects", "get_project", "create_project", "update_project",
	"delete_project",
	// areas (TS port)
	"list_areas", "get_area", "create_area", "update_area", "delete_area",
	// tags (TS port)
	"list_tags", "create_tag", "update_tag", "delete_tag",
	// headings (TS port)
	"list_headings", "create_heading", "update_heading", "delete_heading",
	// checklist (TS port)
	"add_checklist_item", "update_checklist_item", "delete_checklist_item",
	// attachments (TS port)
	"add_link", "delete_attachment",
	// schedules (TS port)
	"add_schedule", "update_schedule", "delete_schedule",
	// views (TS port)
	"get_today", "get_upcoming", "get_inbox", "get_anytime",
	"get_someday", "get_logbook",
	// bulk (TS port)
	"bulk_action",
	// new tools
	"query_tasks", "get_repeat_rule", "set_repeat_rule", "delete_repeat_rule",
	"list_reminders", "add_reminder", "delete_reminder",
	"get_settings", "update_settings", "get_counts",
}

func TestMCP_ListsTools(t *testing.T) {
	session := newTokenSession(t)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	res, err := session.ListTools(ctx, nil)
	if err != nil {
		t.Fatalf("list tools: %v", err)
	}
	got := make([]string, 0, len(res.Tools))
	for _, tool := range res.Tools {
		got = append(got, tool.Name)
	}
	sort.Strings(got)
	want := append([]string(nil), wantTools...)
	sort.Strings(want)
	if !reflect.DeepEqual(got, want) {
		t.Errorf("tool list mismatch:\ngot  (%d): %v\nwant (%d): %v", len(got), got, len(want), want)
	}
}

func TestMCP_CreateAndQueryTask(t *testing.T) {
	session := newTokenSession(t)

	text, isErr := callTool(t, session, "create_task", map[string]any{
		"id":            "mcpcreat1",
		"title":         "MCP roundtrip",
		"notes":         "created over MCP",
		"when_date":     "2031-03-04",
		"deadline":      "2031-03-05",
		"high_priority": true,
	})
	if isErr {
		t.Fatalf("create_task returned tool error: %s", text)
	}
	var created struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal([]byte(text), &created); err != nil {
		t.Fatalf("create_task result is not JSON: %v\n%s", err, text)
	}
	if created.ID != "mcpcreat1" {
		t.Errorf("create_task id = %q, want mcpcreat1", created.ID)
	}

	text, isErr = callTool(t, session, "query_tasks", map[string]any{
		"search": "roundtrip",
	})
	if isErr {
		t.Fatalf("query_tasks returned tool error: %s", text)
	}
	var queried struct {
		Tasks []struct {
			ID           string  `json:"id"`
			WhenDate     *string `json:"when_date"`
			Deadline     *string `json:"deadline"`
			HighPriority bool    `json:"high_priority"`
		} `json:"tasks"`
	}
	if err := json.Unmarshal([]byte(text), &queried); err != nil {
		t.Fatalf("query_tasks result is not JSON: %v\n%s", err, text)
	}
	if len(queried.Tasks) != 1 {
		t.Fatalf("query_tasks returned %d tasks, want 1\n%s", len(queried.Tasks), text)
	}
	task := queried.Tasks[0]
	if task.ID != "mcpcreat1" {
		t.Errorf("query_tasks id = %q, want mcpcreat1", task.ID)
	}
	if task.WhenDate == nil || *task.WhenDate != "2031-03-04" {
		t.Errorf("query_tasks when_date = %v, want 2031-03-04", task.WhenDate)
	}
	if task.Deadline == nil || *task.Deadline != "2031-03-05" {
		t.Errorf("query_tasks deadline = %v, want 2031-03-05", task.Deadline)
	}
	if !task.HighPriority {
		t.Error("query_tasks high_priority = false, want true")
	}
}

func TestMCP_UpdateTaskClearsWhenDate(t *testing.T) {
	session := newTokenSession(t)

	text, isErr := callTool(t, session, "create_task", map[string]any{
		"id":        "mcpclr001",
		"title":     "Clear my dates",
		"when_date": "2031-04-01",
		"deadline":  "2031-04-02",
	})
	if isErr {
		t.Fatalf("create_task returned tool error: %s", text)
	}

	// Explicit JSON null must clear; absent fields must stay untouched.
	text, isErr = callTool(t, session, "update_task", map[string]any{
		"id":        "mcpclr001",
		"when_date": nil,
		"deadline":  nil,
	})
	if isErr {
		t.Fatalf("update_task returned tool error: %s", text)
	}

	text, isErr = callTool(t, session, "get_task", map[string]any{"id": "mcpclr001"})
	if isErr {
		t.Fatalf("get_task returned tool error: %s", text)
	}
	var task struct {
		WhenDate  *string `json:"when_date"`
		Deadline  *string `json:"deadline"`
		Schedules []struct {
			WhenDate string `json:"when_date"`
		} `json:"schedules"`
	}
	if err := json.Unmarshal([]byte(text), &task); err != nil {
		t.Fatalf("get_task result is not JSON: %v\n%s", err, text)
	}
	if task.WhenDate != nil {
		t.Errorf("when_date = %q after null update, want cleared", *task.WhenDate)
	}
	if task.Deadline != nil {
		t.Errorf("deadline = %q after null update, want cleared", *task.Deadline)
	}
	if len(task.Schedules) != 0 {
		t.Errorf("schedules not cleared with when_date: %d entries remain\n%s", len(task.Schedules), text)
	}
}

func TestMCP_ValidationIsToolError(t *testing.T) {
	session := newTokenSession(t)

	text, isErr := callTool(t, session, "create_task", map[string]any{
		"title":     "Bad date",
		"when_date": "tomorrow",
	})
	if !isErr {
		t.Fatalf("create_task with when_date=tomorrow should be a tool error, got result: %s", text)
	}
	if !strings.Contains(text, "VALIDATION") {
		t.Errorf("tool error text must contain the API error code VALIDATION, got: %s", text)
	}
	if !strings.Contains(text, "when_date") {
		t.Errorf("tool error text should name the failing field, got: %s", text)
	}
}

// TestMCP_MalformedIDIsValidationError pins the dispatch path guard: an id
// carrying path syntax (here a "/" that would turn delete_task into a purge
// of a different task) must fail as a VALIDATION tool error before any
// request is built, and the addressed task must survive untouched.
func TestMCP_MalformedIDIsValidationError(t *testing.T) {
	session := newTokenSession(t)

	text, isErr := callTool(t, session, "create_task", map[string]any{
		"id":    "mcpsafe01",
		"title": "Survives malformed delete",
	})
	if isErr {
		t.Fatalf("create_task returned tool error: %s", text)
	}

	text, isErr = callTool(t, session, "delete_task", map[string]any{"id": "mcpsafe01/purge"})
	if !isErr {
		t.Fatalf("delete_task with a path-bearing id should be a tool error, got result: %s", text)
	}
	if !strings.Contains(text, "VALIDATION") {
		t.Errorf("tool error text must contain the API error code VALIDATION, got: %s", text)
	}
	if !strings.Contains(text, "id") {
		t.Errorf("tool error text should name the failing field id, got: %s", text)
	}

	// The task must still exist: the malformed id must not have dispatched
	// DELETE /api/tasks/mcpsafe01/purge and purged it.
	text, isErr = callTool(t, session, "get_task", map[string]any{"id": "mcpsafe01"})
	if isErr {
		t.Fatalf("get_task after malformed delete returned tool error: %s", text)
	}
	if !strings.Contains(text, "Survives malformed delete") {
		t.Errorf("task was lost to the malformed delete call: %s", text)
	}
}

func TestMCP_CreateRetryWithSameID(t *testing.T) {
	session := newTokenSession(t)

	args := map[string]any{"id": "mcpretry01", "title": "Retry creates one"}
	for call := 1; call <= 2; call++ {
		text, isErr := callTool(t, session, "create_task", args)
		if isErr {
			t.Fatalf("create_task call %d returned tool error: %s", call, text)
		}
		var created struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal([]byte(text), &created); err != nil {
			t.Fatalf("create_task call %d result is not JSON: %v\n%s", call, err, text)
		}
		if created.ID != "mcpretry01" {
			t.Errorf("create_task call %d id = %q, want mcpretry01", call, created.ID)
		}
	}

	text, isErr := callTool(t, session, "query_tasks", map[string]any{"search": "Retry creates one"})
	if isErr {
		t.Fatalf("query_tasks returned tool error: %s", text)
	}
	var queried struct {
		Tasks []struct {
			ID string `json:"id"`
		} `json:"tasks"`
	}
	if err := json.Unmarshal([]byte(text), &queried); err != nil {
		t.Fatalf("query_tasks result is not JSON: %v\n%s", err, text)
	}
	if len(queried.Tasks) != 1 {
		t.Errorf("retry with same id created %d tasks, want exactly 1\n%s", len(queried.Tasks), text)
	}
}

func TestMCP_CompleteRecurringOnce(t *testing.T) {
	session := newTokenSession(t)

	text, isErr := callTool(t, session, "create_task", map[string]any{
		"id":        "mcprecur1",
		"title":     "MCP Recur",
		"when_date": "2031-01-06",
	})
	if isErr {
		t.Fatalf("create_task returned tool error: %s", text)
	}

	_, isErr = callTool(t, session, "set_repeat_rule", map[string]any{
		"task_id": "mcprecur1",
		"pattern": map[string]any{
			"type":  "weekly",
			"every": 1,
			"mode":  "after_completion",
		},
	})
	if isErr {
		t.Fatalf("set_repeat_rule returned tool error: %s", text)
	}

	text, isErr = callTool(t, session, "complete_task", map[string]any{"id": "mcprecur1"})
	if isErr {
		t.Fatalf("complete_task returned tool error: %s", text)
	}
	// A second completion of the same task must not spawn another instance.
	_, isErr = callTool(t, session, "complete_task", map[string]any{"id": "mcprecur1"})
	if isErr {
		t.Fatalf("second complete_task returned tool error: %s", text)
	}

	countWithStatus := func(status string) int {
		t.Helper()
		text, isErr := callTool(t, session, "query_tasks", map[string]any{
			"search": "MCP Recur",
			"status": status,
		})
		if isErr {
			t.Fatalf("query_tasks(%s) returned tool error: %s", status, text)
		}
		var queried struct {
			Tasks []struct {
				ID string `json:"id"`
			} `json:"tasks"`
		}
		if err := json.Unmarshal([]byte(text), &queried); err != nil {
			t.Fatalf("query_tasks(%s) result is not JSON: %v\n%s", status, err, text)
		}
		return len(queried.Tasks)
	}
	if open := countWithStatus("open"); open != 1 {
		t.Errorf("open instances = %d, want exactly 1", open)
	}
	if done := countWithStatus("completed"); done != 1 {
		t.Errorf("completed instances = %d, want exactly 1", done)
	}
}

func TestMCP_Unauthenticated401(t *testing.T) {
	srv, _ := newMCPHTTPServer(t, config.Config{AuthMode: "proxy", AuthProxyHeader: "Remote-User"})

	// No credentials at all: the auth middleware wrapping /mcp must
	// reject the request before any MCP processing.
	resp, err := http.Post(srv.URL+"/mcp", "application/json", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"x","version":"1"}}}`))
	if err != nil {
		t.Fatalf("post /mcp: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("unauthenticated POST /mcp status = %d, want 401\nbody: %s", resp.StatusCode, body)
	}
	if !strings.Contains(string(body), "UNAUTHORIZED") {
		t.Errorf("unauthenticated POST /mcp body must carry code UNAUTHORIZED, got: %s", body)
	}
}

func TestMCP_ProxySessionWorks(t *testing.T) {
	srv, _ := newMCPHTTPServer(t, config.Config{AuthMode: "proxy", AuthProxyHeader: "Remote-User"})

	// Only the proxy header authenticates: the internal dispatch must
	// copy it so the tool's in-process API call succeeds.
	session := newMCPSession(t, srv, http.Header{"Remote-User": {"proxymcp"}})

	text, isErr := callTool(t, session, "get_counts", map[string]any{})
	if isErr {
		t.Fatalf("get_counts with only Remote-User returned tool error: %s", text)
	}
	var counts map[string]any
	if err := json.Unmarshal([]byte(text), &counts); err != nil {
		t.Fatalf("get_counts result is not JSON: %v\n%s", err, text)
	}
	if len(counts) == 0 {
		t.Errorf("get_counts returned an empty object: %s", text)
	}
}
