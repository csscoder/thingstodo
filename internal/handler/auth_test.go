package handler_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/collinjanssen/thingstodo/internal/config"
	"github.com/collinjanssen/thingstodo/internal/handler"
	"github.com/collinjanssen/thingstodo/internal/repository"
	"github.com/collinjanssen/thingstodo/internal/testutil"
	"github.com/go-chi/chi/v5"
	"golang.org/x/crypto/bcrypt"
)

const loginTestUsername = "alice"

// newLoginServer starts a builtin-mode login endpoint with one seeded user
// and returns the server plus that user's correct password. All requests hit
// the server from 127.0.0.1, so they share one limiter bucket per test.
func newLoginServer(t *testing.T) (*httptest.Server, string) {
	t.Helper()

	const password = "correct horse battery staple"
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.MinCost)
	if err != nil {
		t.Fatalf("bcrypt hash: %v", err)
	}

	userRepo := repository.NewUserRepository(testutil.SetupTestDB(t))
	if _, err := userRepo.Create(loginTestUsername, string(hash)); err != nil {
		t.Fatalf("seed user: %v", err)
	}

	authH := handler.NewAuthHandler(userRepo, config.Config{
		AuthMode:  "builtin",
		JWTSecret: "test-secret",
	})

	r := chi.NewRouter()
	r.Post("/api/auth/login", authH.Login)

	srv := httptest.NewServer(r)
	t.Cleanup(srv.Close)
	return srv, password
}

// postLogin posts credentials, optionally setting X-Forwarded-Proto.
func postLogin(t *testing.T, srv *httptest.Server, forwardedProto, password string) *http.Response {
	t.Helper()

	body, err := json.Marshal(map[string]string{"username": loginTestUsername, "password": password})
	if err != nil {
		t.Fatalf("marshal body: %v", err)
	}
	req, err := http.NewRequest(http.MethodPost, srv.URL+"/api/auth/login", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if forwardedProto != "" {
		req.Header.Set("X-Forwarded-Proto", forwardedProto)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("login request: %v", err)
	}
	return resp
}

func TestLogin_EleventhFailureIs429(t *testing.T) {
	srv, password := newLoginServer(t)

	for i := 0; i < 10; i++ {
		resp := postLogin(t, srv, "", "wrong password")
		if resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("attempt %d: expected 401, got %d", i+1, resp.StatusCode)
		}
		_ = resp.Body.Close()
	}

	// The 11th attempt is rejected even with the correct password.
	resp := postLogin(t, srv, "", password)
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("expected 429 on 11th attempt, got %d", resp.StatusCode)
	}

	var got struct {
		Error string `json:"error"`
		Code  string `json:"code"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if got.Error != "too many failed login attempts" || got.Code != "RATE_LIMITED" {
		t.Errorf("unexpected 429 body: %+v", got)
	}
}

func TestLogin_SuccessBeforeLimit(t *testing.T) {
	srv, password := newLoginServer(t)

	for i := 0; i < 3; i++ {
		resp := postLogin(t, srv, "", "wrong password")
		if resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("attempt %d: expected 401, got %d", i+1, resp.StatusCode)
		}
		_ = resp.Body.Close()
	}

	resp := postLogin(t, srv, "", password)
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 on success before the limit, got %d", resp.StatusCode)
	}

	var tokenCookie *http.Cookie
	for _, c := range resp.Cookies() {
		if c.Name == "token" {
			tokenCookie = c
		}
	}
	if tokenCookie == nil {
		t.Fatal("successful login must set a token cookie")
	}
}

func TestLogin_SecureCookieBehindTLSProxy(t *testing.T) {
	srv, password := newLoginServer(t)

	resp := postLogin(t, srv, "https", password)
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	if c := findCookie(resp.Cookies(), "token"); c == nil || !c.Secure {
		t.Error("token cookie must be Secure when X-Forwarded-Proto is https")
	}

	// Plain-HTTP counterpart: local dev keeps a non-Secure cookie.
	plain := postLogin(t, srv, "", password)
	defer func() { _ = plain.Body.Close() }()
	if plain.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", plain.StatusCode)
	}
	if c := findCookie(plain.Cookies(), "token"); c == nil || c.Secure {
		t.Error("token cookie must not be Secure over plain HTTP")
	}
}

func findCookie(cookies []*http.Cookie, name string) *http.Cookie {
	for _, c := range cookies {
		if c.Name == name {
			return c
		}
	}
	return nil
}

func TestAuthConfigIncludesTimezone(t *testing.T) {
	authH := handler.NewAuthHandler(
		repository.NewUserRepository(testutil.SetupTestDB(t)),
		config.Config{
			AuthMode:     "builtin",
			JWTSecret:    "test-secret",
			TimeZoneName: "Europe/Moscow",
		},
	)

	r := chi.NewRouter()
	r.Get("/api/auth/config", authH.AuthConfig)

	srv := httptest.NewServer(r)
	t.Cleanup(srv.Close)

	resp, err := http.Get(srv.URL + "/api/auth/config")
	if err != nil {
		t.Fatalf("auth config request: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	var got struct {
		AuthMode string `json:"auth_mode"`
		Timezone string `json:"timezone"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if got.AuthMode != "builtin" {
		t.Errorf("auth_mode = %q, want builtin", got.AuthMode)
	}
	if got.Timezone != "Europe/Moscow" {
		t.Errorf("timezone = %q, want Europe/Moscow (cfg.TimeZoneName)", got.Timezone)
	}
}
