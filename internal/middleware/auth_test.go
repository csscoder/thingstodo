package middleware

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/collinjanssen/thingstodo/internal/config"
	"github.com/collinjanssen/thingstodo/internal/repository"
	"github.com/collinjanssen/thingstodo/internal/testutil"
	"github.com/golang-jwt/jwt/v5"
)

const testSecret = "s"

func echoUserID() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, _ := r.Context().Value(UserIDKey).(string)
		_, _ = w.Write([]byte(id))
	})
}

func echoAuthMethod() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(AuthMethod(r.Context())))
	})
}

func signToken(t *testing.T, method jwt.SigningMethod, secret string, claims jwt.MapClaims) string {
	t.Helper()
	signed, err := jwt.NewWithClaims(method, claims).SignedString([]byte(secret))
	if err != nil {
		t.Fatalf("sign token: %v", err)
	}
	return signed
}

func requestWithSession(token string) *http.Request {
	r := httptest.NewRequest(http.MethodGet, "/api/tasks", nil)
	if token != "" {
		r.AddCookie(&http.Cookie{Name: "token", Value: token})
	}
	return r
}

func userExistsStub(id string) (bool, error) { return id == "u1", nil }

func firstUserStub() (string, error) { return "u1", nil }

func runAuth(cfg config.Config, lookup UserLookupFunc, exists UserExistsFunc, tokenLookup TokenLookupFunc, touchToken func(string), r *http.Request) *httptest.ResponseRecorder {
	return runAuthHandler(cfg, lookup, exists, tokenLookup, touchToken, echoUserID(), r)
}

func runAuthHandler(cfg config.Config, lookup UserLookupFunc, exists UserExistsFunc, tokenLookup TokenLookupFunc, touchToken func(string), h http.Handler, r *http.Request) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	Auth(cfg, lookup, exists, tokenLookup, touchToken)(h).ServeHTTP(rec, r)
	return rec
}

func assertStatusBody(t *testing.T, rec *httptest.ResponseRecorder, status int, body string) {
	t.Helper()
	if rec.Code != status {
		t.Errorf("expected status %d, got %d (body %q)", status, rec.Code, rec.Body.String())
	}
	if got := strings.TrimSpace(rec.Body.String()); got != body {
		t.Errorf("expected body %q, got %q", body, got)
	}
}

func builtinCfg() config.Config {
	return config.Config{AuthMode: "builtin", JWTSecret: testSecret}
}

// tokenTestRepo returns an API token repository over a fresh test database.
func tokenTestRepo(t *testing.T) *repository.APITokenRepository {
	t.Helper()
	return repository.NewAPITokenRepository(testutil.SetupTestDB(t))
}

// tokenLookupFunc adapts the repository Lookup to the middleware type,
// failing closed on errors.
func tokenLookupFunc(repo *repository.APITokenRepository) TokenLookupFunc {
	return func(token string) (string, bool) {
		id, ok, err := repo.Lookup(token)
		if err != nil {
			return "", false
		}
		return id, ok
	}
}

func TestAuth_Builtin_ValidSession(t *testing.T) {
	tok := signToken(t, jwt.SigningMethodHS256, testSecret, jwt.MapClaims{
		"sub": "u1",
		"exp": time.Now().Add(time.Hour).Unix(),
	})
	rec := runAuth(builtinCfg(), firstUserStub, userExistsStub, nil, nil, requestWithSession(tok))
	assertStatusBody(t, rec, http.StatusOK, "u1")
}

func TestAuth_Builtin_NoCookie(t *testing.T) {
	rec := runAuth(builtinCfg(), firstUserStub, userExistsStub, nil, nil, requestWithSession(""))
	assertStatusBody(t, rec, http.StatusUnauthorized, `{"error":"unauthorized","code":"UNAUTHORIZED"}`)
}

func TestAuth_Builtin_BadSignature(t *testing.T) {
	tok := signToken(t, jwt.SigningMethodHS256, "other", jwt.MapClaims{
		"sub": "u1",
		"exp": time.Now().Add(time.Hour).Unix(),
	})
	rec := runAuth(builtinCfg(), firstUserStub, userExistsStub, nil, nil, requestWithSession(tok))
	assertStatusBody(t, rec, http.StatusUnauthorized, `{"error":"invalid token","code":"UNAUTHORIZED"}`)
}

func TestAuth_Builtin_WrongAlg(t *testing.T) {
	tok := signToken(t, jwt.SigningMethodHS384, testSecret, jwt.MapClaims{
		"sub": "u1",
		"exp": time.Now().Add(time.Hour).Unix(),
	})
	rec := runAuth(builtinCfg(), firstUserStub, userExistsStub, nil, nil, requestWithSession(tok))
	assertStatusBody(t, rec, http.StatusUnauthorized, `{"error":"invalid token","code":"UNAUTHORIZED"}`)
}

func TestAuth_Builtin_NoExp(t *testing.T) {
	tok := signToken(t, jwt.SigningMethodHS256, testSecret, jwt.MapClaims{"sub": "u1"})
	rec := runAuth(builtinCfg(), firstUserStub, userExistsStub, nil, nil, requestWithSession(tok))
	assertStatusBody(t, rec, http.StatusUnauthorized, `{"error":"invalid token","code":"UNAUTHORIZED"}`)
}

func TestAuth_Builtin_Expired(t *testing.T) {
	tok := signToken(t, jwt.SigningMethodHS256, testSecret, jwt.MapClaims{
		"sub": "u1",
		"exp": time.Now().Add(-time.Hour).Unix(),
	})
	rec := runAuth(builtinCfg(), firstUserStub, userExistsStub, nil, nil, requestWithSession(tok))
	assertStatusBody(t, rec, http.StatusUnauthorized, `{"error":"invalid token","code":"UNAUTHORIZED"}`)
}

func TestAuth_Builtin_NoSub(t *testing.T) {
	tok := signToken(t, jwt.SigningMethodHS256, testSecret, jwt.MapClaims{
		"exp": time.Now().Add(time.Hour).Unix(),
	})
	rec := runAuth(builtinCfg(), firstUserStub, userExistsStub, nil, nil, requestWithSession(tok))
	assertStatusBody(t, rec, http.StatusUnauthorized, `{"error":"invalid claims","code":"UNAUTHORIZED"}`)
}

func TestAuth_Builtin_UnknownSub(t *testing.T) {
	tok := signToken(t, jwt.SigningMethodHS256, testSecret, jwt.MapClaims{
		"sub": "u2",
		"exp": time.Now().Add(time.Hour).Unix(),
	})
	rec := runAuth(builtinCfg(), firstUserStub, userExistsStub, nil, nil, requestWithSession(tok))
	assertStatusBody(t, rec, http.StatusUnauthorized, `{"error":"invalid claims","code":"UNAUTHORIZED"}`)
}

func TestAuth_Builtin_OIDCStateTokenRejected(t *testing.T) {
	// sub is valid and accepted by the stub, so the typ claim is the only
	// rejection path exercised: removing the typ check must fail this test.
	tok := signToken(t, jwt.SigningMethodHS256, testSecret, jwt.MapClaims{
		"state": "x",
		"typ":   "oidc_state",
		"sub":   "u1",
		"exp":   time.Now().Add(time.Hour).Unix(),
	})
	rec := runAuth(builtinCfg(), firstUserStub, userExistsStub, nil, nil, requestWithSession(tok))
	assertStatusBody(t, rec, http.StatusUnauthorized, `{"error":"invalid token","code":"UNAUTHORIZED"}`)
}

func TestAuth_OIDCMode_UsesSameCookieRules(t *testing.T) {
	cfg := config.Config{AuthMode: "oidc", JWTSecret: testSecret}

	valid := signToken(t, jwt.SigningMethodHS256, testSecret, jwt.MapClaims{
		"sub": "u1",
		"exp": time.Now().Add(time.Hour).Unix(),
	})
	rec := runAuth(cfg, firstUserStub, userExistsStub, nil, nil, requestWithSession(valid))
	assertStatusBody(t, rec, http.StatusOK, "u1")

	noSub := signToken(t, jwt.SigningMethodHS256, testSecret, jwt.MapClaims{
		"exp": time.Now().Add(time.Hour).Unix(),
	})
	rec = runAuth(cfg, firstUserStub, userExistsStub, nil, nil, requestWithSession(noSub))
	assertStatusBody(t, rec, http.StatusUnauthorized, `{"error":"invalid claims","code":"UNAUTHORIZED"}`)

	// State tokens occur in oidc mode; with a valid sub the typ claim must
	// still be the rejection path here too.
	stateTok := signToken(t, jwt.SigningMethodHS256, testSecret, jwt.MapClaims{
		"state": "x",
		"typ":   "oidc_state",
		"sub":   "u1",
		"exp":   time.Now().Add(time.Hour).Unix(),
	})
	rec = runAuth(cfg, firstUserStub, userExistsStub, nil, nil, requestWithSession(stateTok))
	assertStatusBody(t, rec, http.StatusUnauthorized, `{"error":"invalid token","code":"UNAUTHORIZED"}`)
}

func TestAuth_Proxy_HeaderSet(t *testing.T) {
	cfg := config.Config{AuthMode: "proxy", AuthProxyHeader: "Remote-User"}
	r := httptest.NewRequest(http.MethodGet, "/api/tasks", nil)
	r.Header.Set("Remote-User", "proxyuser")
	rec := runAuth(cfg, firstUserStub, userExistsStub, nil, nil, r)
	assertStatusBody(t, rec, http.StatusOK, "proxyuser")
}

func TestAuth_Proxy_HeaderMissing(t *testing.T) {
	cfg := config.Config{AuthMode: "proxy", AuthProxyHeader: "Remote-User"}
	r := httptest.NewRequest(http.MethodGet, "/api/tasks", nil)
	rec := runAuth(cfg, firstUserStub, userExistsStub, nil, nil, r)
	assertStatusBody(t, rec, http.StatusUnauthorized, `{"error":"unauthorized","code":"UNAUTHORIZED"}`)
}

func TestAuth_None_UsesFirstUser(t *testing.T) {
	cfg := config.Config{AuthMode: "none"}
	rec := runAuth(cfg, firstUserStub, userExistsStub, nil, nil, httptest.NewRequest(http.MethodGet, "/api/tasks", nil))
	assertStatusBody(t, rec, http.StatusOK, "u1")
}

func TestAuth_APIKey_Valid(t *testing.T) {
	cfg := config.Config{AuthMode: "builtin", JWTSecret: testSecret, APIKey: "k"}
	lookup := func() (string, error) { return "apiuser", nil }
	r := httptest.NewRequest(http.MethodGet, "/api/tasks", nil)
	r.Header.Set("Authorization", "Bearer k")
	rec := runAuth(cfg, lookup, userExistsStub, nil, nil, r)
	assertStatusBody(t, rec, http.StatusOK, "apiuser")
}

func TestAuth_APIKey_Wrong(t *testing.T) {
	cfg := config.Config{AuthMode: "builtin", JWTSecret: testSecret, APIKey: "k"}
	// A valid session cookie must not rescue a wrong API key.
	tok := signToken(t, jwt.SigningMethodHS256, testSecret, jwt.MapClaims{
		"sub": "u1",
		"exp": time.Now().Add(time.Hour).Unix(),
	})
	r := requestWithSession(tok)
	r.Header.Set("Authorization", "Bearer wrong")
	rec := runAuth(cfg, firstUserStub, userExistsStub, nil, nil, r)
	assertStatusBody(t, rec, http.StatusUnauthorized, `{"error":"invalid api key","code":"UNAUTHORIZED"}`)
}

func TestAuth_APIKey_AbsentFallsBackToCookie(t *testing.T) {
	cfg := config.Config{AuthMode: "builtin", JWTSecret: testSecret, APIKey: "k"}
	tok := signToken(t, jwt.SigningMethodHS256, testSecret, jwt.MapClaims{
		"sub": "u1",
		"exp": time.Now().Add(time.Hour).Unix(),
	})
	rec := runAuth(cfg, firstUserStub, userExistsStub, nil, nil, requestWithSession(tok))
	assertStatusBody(t, rec, http.StatusOK, "u1")
}

func TestTokenAuth_ValidRevokedWrong(t *testing.T) {
	repo := tokenTestRepo(t)
	id, token, err := repo.Create("ci")
	if err != nil {
		t.Fatalf("create token: %v", err)
	}
	touchCount := 0
	touch := func(string) { touchCount++ }

	// A valid token authenticates as the admin user, records the token
	// auth method and touches last_used_at exactly once.
	r := httptest.NewRequest(http.MethodGet, "/api/tasks", nil)
	r.Header.Set("Authorization", "Bearer "+token)
	rec := runAuth(builtinCfg(), firstUserStub, userExistsStub, tokenLookupFunc(repo), touch, r)
	assertStatusBody(t, rec, http.StatusOK, "u1")

	rec = runAuthHandler(builtinCfg(), firstUserStub, userExistsStub, tokenLookupFunc(repo), touch, echoAuthMethod(), r)
	assertStatusBody(t, rec, http.StatusOK, AuthToken)
	if touchCount != 2 {
		t.Errorf("expected TouchLastUsed called once per request, got %d", touchCount)
	}

	// After revocation the same token is rejected.
	if err := repo.Revoke(id); err != nil {
		t.Fatalf("revoke token: %v", err)
	}
	rec = runAuth(builtinCfg(), firstUserStub, userExistsStub, tokenLookupFunc(repo), touch, r)
	assertStatusBody(t, rec, http.StatusUnauthorized, `{"error":"invalid api key","code":"UNAUTHORIZED"}`)
	if touchCount != 2 {
		t.Errorf("revoked token must not touch last_used_at, got %d calls", touchCount)
	}

	// A wrong Bearer is rejected with no cookie fallback.
	valid := signToken(t, jwt.SigningMethodHS256, testSecret, jwt.MapClaims{
		"sub": "u1",
		"exp": time.Now().Add(time.Hour).Unix(),
	})
	rWrong := requestWithSession(valid)
	rWrong.Header.Set("Authorization", "Bearer "+token+"wrong")
	rec = runAuth(builtinCfg(), firstUserStub, userExistsStub, tokenLookupFunc(repo), touch, rWrong)
	assertStatusBody(t, rec, http.StatusUnauthorized, `{"error":"invalid api key","code":"UNAUTHORIZED"}`)
}

func TestTokenAuth_NoneModeIgnoresBearer(t *testing.T) {
	repo := tokenTestRepo(t)
	_, token, err := repo.Create("ignored")
	if err != nil {
		t.Fatalf("create token: %v", err)
	}
	touchCount := 0
	touch := func(string) { touchCount++ }
	cfg := config.Config{AuthMode: "none"}

	// Even a Bearer holding a real token is ignored in none mode: the
	// first user authenticates the request, the method is a session and
	// last_used_at is never touched.
	r := httptest.NewRequest(http.MethodGet, "/api/tasks", nil)
	r.Header.Set("Authorization", "Bearer "+token)
	rec := runAuthHandler(cfg, firstUserStub, userExistsStub, tokenLookupFunc(repo), touch, echoAuthMethod(), r)
	assertStatusBody(t, rec, http.StatusOK, AuthSession)
	if touchCount != 0 {
		t.Errorf("none mode must not touch last_used_at, got %d calls", touchCount)
	}

	// Garbage bearers are ignored the same way.
	rGarbage := httptest.NewRequest(http.MethodGet, "/api/tasks", nil)
	rGarbage.Header.Set("Authorization", "Bearer ttd_nope")
	rec = runAuth(cfg, firstUserStub, userExistsStub, tokenLookupFunc(repo), touch, rGarbage)
	assertStatusBody(t, rec, http.StatusOK, "u1")
	if touchCount != 0 {
		t.Errorf("none mode must not touch last_used_at, got %d calls", touchCount)
	}
}
