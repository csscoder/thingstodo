package router_test

import (
	"net/http"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/collinjanssen/thingstodo/docs"
	"github.com/collinjanssen/thingstodo/internal/config"
	"github.com/collinjanssen/thingstodo/internal/push"
	"github.com/collinjanssen/thingstodo/internal/repository"
	"github.com/collinjanssen/thingstodo/internal/router"
	"github.com/collinjanssen/thingstodo/internal/scheduler"
	"github.com/collinjanssen/thingstodo/internal/sse"
	"github.com/collinjanssen/thingstodo/internal/testutil"
	"github.com/go-chi/chi/v5"
)

// oidcOnlyRoutes are registered solely in AUTH_MODE=oidc, so a builtin-mode
// router walk never yields them, while the OpenAPI contract documents every
// mode and must always carry them.
var oidcOnlyRoutes = map[string]bool{
	"GET /api/auth/oidc/login":    true,
	"GET /api/auth/oidc/callback": true,
}

// newAPIHandler builds a complete router like newRouterClientForConfig but
// hands back the handler itself, so chi.Walk can introspect the route table.
func newAPIHandler(t *testing.T, cfg config.Config) http.Handler {
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

	return router.New(db, cfg, broker, sched)
}

var pathParamRe = regexp.MustCompile(`\{[^}]+\}`)

// normalizePathPattern collapses every {param} placeholder (chi's {id},
// {id:*}, the YAML's {id}) to {id} so both sides compare verbatim.
func normalizePathPattern(pattern string) string {
	return pathParamRe.ReplaceAllString(pattern, "{id}")
}

// routerAPIOperations walks the router and returns the METHOD /api/...
// operations it serves. Non-/api routes (/health, /mcp, the SPA fallback)
// are outside the OpenAPI contract and excluded.
func routerAPIOperations(t *testing.T, h http.Handler) map[string]bool {
	t.Helper()
	routable, ok := h.(chi.Routes)
	if !ok {
		t.Fatalf("router.New returned %T, which chi.Walk cannot walk", h)
	}
	ops := map[string]bool{}
	err := chi.Walk(routable, func(method, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		if strings.HasPrefix(route, "/api/") {
			ops[method+" "+normalizePathPattern(route)] = true
		}
		return nil
	})
	if err != nil {
		t.Fatalf("chi.Walk failed: %v", err)
	}
	if len(ops) == 0 {
		t.Fatal("chi.Walk collected no /api routes; the walk is broken")
	}
	return ops
}

// openAPIMethods is the known HTTP method set. Requiring method sub-keys to
// be one of these excludes parameters:/servers:/summary: and every other
// path-item key from the scan.
var openAPIMethods = map[string]bool{
	"get": true, "post": true, "put": true, "patch": true,
	"delete": true, "options": true, "head": true, "trace": true,
}

// yamlAPIOperations line-scans the paths: block of the OpenAPI document and
// returns its METHOD /api/... operations. The paths: block indentation is
// authoritative: /...: keys directly under it are paths, and the known
// method names one level deeper are operations. No YAML dependency — the
// drift test only needs path keys and method keys.
func yamlAPIOperations(yamlDoc string) map[string]bool {
	ops := map[string]bool{}
	inPaths := false
	pathsIndent := 0
	currentPath := ""
	for _, raw := range strings.Split(yamlDoc, "\n") {
		line := strings.TrimRight(raw, " \t\r")
		if line == "" {
			continue
		}
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "#") {
			continue
		}
		indent := len(line) - len(strings.TrimLeft(line, " "))
		if !inPaths {
			if trimmed == "paths:" {
				inPaths = true
				pathsIndent = indent
			}
			continue
		}
		if indent <= pathsIndent {
			break // the paths: block ended (components:, tags:, ...)
		}
		if strings.HasPrefix(trimmed, "/") && strings.HasSuffix(trimmed, ":") {
			currentPath = strings.TrimSuffix(trimmed, ":")
			continue
		}
		key := strings.TrimSuffix(trimmed, ":")
		if currentPath != "" && strings.HasPrefix(currentPath, "/api") && openAPIMethods[key] {
			ops[strings.ToUpper(key)+" "+normalizePathPattern(currentPath)] = true
		}
	}
	return ops
}

// TestOpenAPI_MatchesRouter pins docs/openapi.yaml to the route table: every
// /api operation the router serves must be documented, every documented
// operation must be served (except the oidc-mode-conditional pair), so the
// contract cannot silently drift from the code.
func TestOpenAPI_MatchesRouter(t *testing.T) {
	handler := newAPIHandler(t, config.Config{AuthMode: "builtin"})

	routerOps := routerAPIOperations(t, handler)
	yamlOps := yamlAPIOperations(string(docs.OpenAPI))

	if len(yamlOps) == 0 {
		t.Fatal("the line scan found no operations in docs/openapi.yaml; the paths: block is missing or malformed")
	}

	for route := range oidcOnlyRoutes {
		if !yamlOps[route] {
			t.Errorf("OpenAPI document must document mode-conditional route %q", route)
		}
	}
	for op := range routerOps {
		if !yamlOps[op] {
			t.Errorf("router serves %q but docs/openapi.yaml does not document it", op)
		}
	}
	for op := range yamlOps {
		if routerOps[op] || oidcOnlyRoutes[op] {
			continue
		}
		t.Errorf("docs/openapi.yaml documents %q but the router does not serve it", op)
	}
}

// TestOpenAPI_Served pins the public contract endpoint: the embedded YAML is
// served verbatim at GET /api/openapi.yaml without any credentials.
func TestOpenAPI_Served(t *testing.T) {
	handler := newAPIHandler(t, config.Config{AuthMode: "builtin"})
	client := testutil.NewTestClient(t, handler)

	resp := client.Get("/api/openapi.yaml")

	testutil.AssertStatus(t, resp, http.StatusOK)
	if ct := resp.Header.Get("Content-Type"); ct != "application/yaml" {
		t.Errorf("Content-Type: expected application/yaml, got %q", ct)
	}
	if string(resp.Body) != string(docs.OpenAPI) {
		t.Error("GET /api/openapi.yaml must serve the embedded docs.OpenAPI document verbatim")
	}
}
