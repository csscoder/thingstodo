package handler_test

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/collinjanssen/thingstodo/internal/config"
	"github.com/collinjanssen/thingstodo/internal/handler"
	mw "github.com/collinjanssen/thingstodo/internal/middleware"
	"github.com/collinjanssen/thingstodo/internal/repository"
	"github.com/collinjanssen/thingstodo/internal/testutil"
	"github.com/go-chi/chi/v5"
)

// newTokenTestServer builds the /api/tokens routes behind the real Auth
// middleware in proxy mode with a master key configured, so a single
// server can serve proxy-session, master-key and token requests.
func newTokenTestServer(t *testing.T) (*testutil.TestClient, *sql.DB, *repository.APITokenRepository) {
	t.Helper()
	db := testutil.SetupTestDB(t)
	tokenRepo := repository.NewAPITokenRepository(db)
	tokenH := handler.NewAPITokenHandler(tokenRepo)

	cfg := config.Config{
		AuthMode:        "proxy",
		AuthProxyHeader: "Remote-User",
		APIKey:          "master-key",
	}
	r := chi.NewRouter()
	r.Route("/api", func(r chi.Router) {
		r.Use(mw.Auth(cfg,
			func() (string, error) { return "u1", nil },
			func(string) (bool, error) { return true, nil },
			func(token string) (string, bool) {
				id, ok, err := tokenRepo.Lookup(token)
				if err != nil {
					return "", false
				}
				return id, ok
			},
			tokenRepo.TouchLastUsed,
		))
		r.Get("/tokens", tokenH.List)
		r.Post("/tokens", tokenH.Create)
		r.Delete("/tokens/{id}", tokenH.Revoke)
		r.Get("/tasks", func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte(`{"tasks":[]}`))
		})
	})
	return testutil.NewTestClient(t, r), db, tokenRepo
}

// tokenRequest sends a request authenticating either as the proxy session
// ("session"), the master key ("master") or with the given token value.
func tokenRequest(t *testing.T, client *testutil.TestClient, method, path, auth string, body io.Reader) *testutil.TestResponse {
	t.Helper()
	req, err := http.NewRequest(method, client.Server.URL+path, body)
	if err != nil {
		t.Fatalf("failed to create request: %v", err)
	}
	switch auth {
	case "session":
		req.Header.Set("Remote-User", "u1")
	case "master":
		req.Header.Set("Authorization", "Bearer master-key")
	default:
		req.Header.Set("Authorization", "Bearer "+auth)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
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

func TestTokenCreate_PlaintextOnceHashStored(t *testing.T) {
	client, db, _ := newTokenTestServer(t)

	resp := tokenRequest(t, client, http.MethodPost, "/api/tokens", "session", strings.NewReader(`{"name":"ci"}`))
	testutil.AssertStatus(t, resp, http.StatusCreated)

	var created struct {
		ID     string `json:"id"`
		Name   string `json:"name"`
		Prefix string `json:"prefix"`
		Token  string `json:"token"`
	}
	resp.JSON(t, &created)
	if !strings.HasPrefix(created.Token, "ttd_") {
		t.Errorf("token must start with ttd_, got %q", created.Token)
	}
	if created.Name != "ci" {
		t.Errorf("expected name ci, got %q", created.Name)
	}
	if created.ID == "" {
		t.Fatal("created token has no id")
	}
	if len(created.Token) < 8 || created.Prefix != created.Token[:8] {
		t.Errorf("expected prefix to be the first 8 characters of the token, got %q", created.Prefix)
	}

	// The row stores sha256(token) and no plaintext anywhere.
	sum := sha256.Sum256([]byte(created.Token))
	var rowID, rowName, rowHash, rowPrefix, rowCreated string
	var lastUsed, revoked sql.NullString
	err := db.QueryRow(
		`SELECT id, name, token_hash, prefix, created_at, last_used_at, revoked_at FROM api_tokens WHERE id = ?`,
		created.ID,
	).Scan(&rowID, &rowName, &rowHash, &rowPrefix, &rowCreated, &lastUsed, &revoked)
	if err != nil {
		t.Fatalf("read api_tokens row: %v", err)
	}
	if rowHash != hex.EncodeToString(sum[:]) {
		t.Errorf("expected sha256(token) stored, got %q", rowHash)
	}
	allCols := strings.Join([]string{rowID, rowName, rowHash, rowPrefix, rowCreated, lastUsed.String, revoked.String}, " ")
	if strings.Contains(allCols, created.Token) {
		t.Errorf("plaintext token stored in the row: %s", allCols)
	}
	if rowPrefix != created.Prefix {
		t.Errorf("expected stored prefix %q, got %q", created.Prefix, rowPrefix)
	}

	// GET shows id, name and prefix — never the hash or the token.
	list := tokenRequest(t, client, http.MethodGet, "/api/tokens", "session", nil)
	testutil.AssertStatus(t, list, http.StatusOK)
	if strings.Contains(string(list.Body), created.Token) {
		t.Errorf("GET /api/tokens leaked the plaintext token: %s", list.Body)
	}
	if strings.Contains(string(list.Body), rowHash) {
		t.Errorf("GET /api/tokens leaked the token hash: %s", list.Body)
	}
	var listed struct {
		Tokens []map[string]any `json:"tokens"`
	}
	list.JSON(t, &listed)
	if len(listed.Tokens) != 1 {
		t.Fatalf("expected 1 token listed, got %d", len(listed.Tokens))
	}
	got := listed.Tokens[0]
	if got["id"] != created.ID || got["name"] != "ci" || got["prefix"] != created.Prefix {
		t.Errorf("listed token mismatch: %v", got)
	}
	if _, has := got["token_hash"]; has {
		t.Error("list must not expose token_hash")
	}
	if _, has := got["token"]; has {
		t.Error("list must not expose the token value")
	}
}

func TestTokenManage_ForbiddenWithToken(t *testing.T) {
	client, _, tokenRepo := newTokenTestServer(t)
	id, token, err := tokenRepo.Create("managed")
	if err != nil {
		t.Fatalf("create token: %v", err)
	}

	// A token cannot manage tokens: POST, GET and DELETE all answer 403.
	resp := tokenRequest(t, client, http.MethodPost, "/api/tokens", token, strings.NewReader(`{"name":"escalate"}`))
	testutil.AssertStatus(t, resp, http.StatusForbidden)
	testutil.AssertJSONField(t, resp, "code", "FORBIDDEN")
	testutil.AssertJSONField(t, resp, "error", "token authentication cannot manage tokens")

	list := tokenRequest(t, client, http.MethodGet, "/api/tokens", token, nil)
	testutil.AssertStatus(t, list, http.StatusForbidden)

	del := tokenRequest(t, client, http.MethodDelete, "/api/tokens/"+id, token, nil)
	testutil.AssertStatus(t, del, http.StatusForbidden)

	// The token still authenticates ordinary API routes.
	tasks := tokenRequest(t, client, http.MethodGet, "/api/tasks", token, nil)
	testutil.AssertStatus(t, tasks, http.StatusOK)

	// The master key may manage tokens.
	master := tokenRequest(t, client, http.MethodPost, "/api/tokens", "master", strings.NewReader(`{"name":"by-master"}`))
	testutil.AssertStatus(t, master, http.StatusCreated)

	// A proxy session may manage tokens.
	session := tokenRequest(t, client, http.MethodPost, "/api/tokens", "session", strings.NewReader(`{"name":"by-session"}`))
	testutil.AssertStatus(t, session, http.StatusCreated)
}

func TestTokenCreate_DuplicateNameIs409(t *testing.T) {
	client, _, _ := newTokenTestServer(t)

	first := tokenRequest(t, client, http.MethodPost, "/api/tokens", "session", strings.NewReader(`{"name":"dup"}`))
	testutil.AssertStatus(t, first, http.StatusCreated)

	second := tokenRequest(t, client, http.MethodPost, "/api/tokens", "session", strings.NewReader(`{"name":"dup"}`))
	testutil.AssertStatus(t, second, http.StatusConflict)
	testutil.AssertJSONField(t, second, "code", "DUPLICATE_NAME")
}

func TestTokenCreate_IgnoresClientID(t *testing.T) {
	client, db, _ := newTokenTestServer(t)

	// A body id is ignored: tokens always get a server-generated id and
	// token creates are not idempotent.
	resp := tokenRequest(t, client, http.MethodPost, "/api/tokens", "session", strings.NewReader(`{"id":"clientid42","name":"x"}`))
	testutil.AssertStatus(t, resp, http.StatusCreated)

	var created struct {
		ID string `json:"id"`
	}
	resp.JSON(t, &created)
	if created.ID == "" {
		t.Fatal("created token has no id")
	}
	if created.ID == "clientid42" {
		t.Error("server must generate its own id, not use the body id")
	}

	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM api_tokens WHERE id = ?`, "clientid42").Scan(&n); err != nil {
		t.Fatalf("count client-id rows: %v", err)
	}
	if n != 0 {
		t.Errorf("client-supplied id must not be stored, found %d rows", n)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM api_tokens WHERE id = ?`, created.ID).Scan(&n); err != nil {
		t.Fatalf("count server-id rows: %v", err)
	}
	if n != 1 {
		t.Errorf("expected the generated id row to exist, found %d", n)
	}
}
