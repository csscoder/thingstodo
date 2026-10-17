package handler_test

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/collinjanssen/thingstodo/internal/config"
	"github.com/collinjanssen/thingstodo/internal/handler"
	"github.com/collinjanssen/thingstodo/internal/repository"
	"github.com/collinjanssen/thingstodo/internal/testutil"
	"github.com/go-chi/chi/v5"
	"github.com/golang-jwt/jwt/v5"
)

const (
	oidcTestSecret   = "test-secret"
	oidcTestClientID = "client"
	oidcAllowedEmail = "user@example.com"
)

func writeTestJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

// newFakeIdP starts a minimal OIDC provider backed by one RSA-2048 key:
// discovery, /jwks (kid k1), /token issuing RS256 id_tokens that carry the
// per-test claims, and /userinfo returning per-test JSON.
func newFakeIdP(t *testing.T, idClaims, userinfo map[string]any) *httptest.Server {
	t.Helper()

	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate rsa key: %v", err)
	}

	var idpURL string
	mux := http.NewServeMux()

	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, r *http.Request) {
		writeTestJSON(w, map[string]any{
			"issuer":                                idpURL,
			"authorization_endpoint":                idpURL + "/authorize",
			"token_endpoint":                        idpURL + "/token",
			"jwks_uri":                              idpURL + "/jwks",
			"userinfo_endpoint":                     idpURL + "/userinfo",
			"id_token_signing_alg_values_supported": []string{"RS256"},
			"subject_types_supported":               []string{"public"},
			"response_types_supported":              []string{"code"},
		})
	})

	mux.HandleFunc("/jwks", func(w http.ResponseWriter, r *http.Request) {
		writeTestJSON(w, map[string]any{
			"keys": []map[string]string{{
				"kty": "RSA",
				"use": "sig",
				"alg": "RS256",
				"kid": "k1",
				"n":   base64.RawURLEncoding.EncodeToString(key.N.Bytes()),
				"e":   base64.RawURLEncoding.EncodeToString(big.NewInt(int64(key.E)).Bytes()),
			}},
		})
	})

	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		claims := map[string]any{
			"iss": idpURL,
			"aud": oidcTestClientID,
			"sub": "user-1",
			"exp": time.Now().Add(time.Hour).Unix(),
			"iat": time.Now().Unix(),
		}
		for k, v := range idClaims {
			claims[k] = v
		}
		tok := jwt.NewWithClaims(jwt.SigningMethodRS256, jwt.MapClaims(claims))
		tok.Header["kid"] = "k1"
		signed, err := tok.SignedString(key)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeTestJSON(w, map[string]any{
			"access_token": "test-access-token",
			"token_type":   "Bearer",
			"expires_in":   3600,
			"id_token":     signed,
		})
	})

	mux.HandleFunc("/userinfo", func(w http.ResponseWriter, r *http.Request) {
		if userinfo == nil {
			userinfo = map[string]any{}
		}
		writeTestJSON(w, userinfo)
	})

	srv := httptest.NewUnstartedServer(mux)
	srv.Start()
	t.Cleanup(srv.Close)
	idpURL = srv.URL
	return srv
}

// setupOIDCEnv wires an OIDC handler against the fake IdP and a fresh test
// database, optionally seeded with a user of seedUsername.
func setupOIDCEnv(t *testing.T, seedUsername, idpURL string) (*httptest.Server, *repository.UserRepository) {
	t.Helper()

	db := testutil.SetupTestDB(t)
	userRepo := repository.NewUserRepository(db)
	if seedUsername != "" {
		if _, err := userRepo.Create(seedUsername, ""); err != nil {
			t.Fatalf("seed user: %v", err)
		}
	}

	cfg := config.Config{
		AuthMode:         "oidc",
		JWTSecret:        oidcTestSecret,
		OIDCIssuer:       idpURL,
		OIDCClientID:     oidcTestClientID,
		OIDCClientSecret: "client-secret",
		OIDCRedirectURI:  "http://app.example.com/api/auth/oidc/callback",
		OIDCAllowedEmail: oidcAllowedEmail,
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	t.Cleanup(cancel)
	oidcH, err := handler.NewOIDCHandler(ctx, userRepo, cfg)
	if err != nil {
		t.Fatalf("new oidc handler: %v", err)
	}

	r := chi.NewRouter()
	r.Get("/api/auth/oidc/login", oidcH.Login)
	r.Get("/api/auth/oidc/callback", oidcH.Callback)

	srv := httptest.NewServer(r)
	t.Cleanup(srv.Close)
	return srv, userRepo
}

// assertForbidden pins the callback's rejection body: the exact status, the
// "account not allowed" message and the FORBIDDEN code.
func assertForbidden(t *testing.T, resp *http.Response) {
	t.Helper()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("expected 403, got %d (body: %s)", resp.StatusCode, readBody(t, resp))
	}
	var got struct {
		Error string `json:"error"`
		Code  string `json:"code"`
	}
	body := readBody(t, resp)
	if err := json.Unmarshal([]byte(body), &got); err != nil {
		t.Fatalf("failed to decode body: %v\nbody: %s", err, body)
	}
	if got.Error != "account not allowed" || got.Code != "FORBIDDEN" {
		t.Errorf("expected FORBIDDEN account not allowed body, got %s", strings.TrimSpace(body))
	}
}

func readBody(t *testing.T, resp *http.Response) string {
	t.Helper()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	return string(b)
}

// doOIDCLoginFull runs the browser side of the OIDC flow against the fake
// IdP: GET /api/auth/oidc/login without following redirects, then
// GET /api/auth/oidc/callback with the state cookie and state query value.
func doOIDCLoginFull(t *testing.T, seedUsername string, idClaims, userinfo map[string]any) (*http.Response, *httptest.Server, *repository.UserRepository) {
	t.Helper()

	idp := newFakeIdP(t, idClaims, userinfo)
	srv, userRepo := setupOIDCEnv(t, seedUsername, idp.URL)

	client := &http.Client{
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}

	resp, err := client.Get(srv.URL + "/api/auth/oidc/login")
	if err != nil {
		t.Fatalf("login request: %v", err)
	}
	_ = resp.Body.Close()

	var stateCookie *http.Cookie
	for _, c := range resp.Cookies() {
		if c.Name == "oidc_state" {
			stateCookie = c
		}
	}
	if stateCookie == nil {
		t.Fatal("login response did not set oidc_state cookie")
	}

	loc, err := url.Parse(resp.Header.Get("Location"))
	if err != nil {
		t.Fatalf("parse login redirect: %v", err)
	}
	state := loc.Query().Get("state")
	if state == "" {
		t.Fatal("login redirect has no state parameter")
	}

	req, err := http.NewRequest(http.MethodGet,
		srv.URL+"/api/auth/oidc/callback?state="+url.QueryEscape(state)+"&code=c", nil)
	if err != nil {
		t.Fatalf("build callback request: %v", err)
	}
	req.AddCookie(stateCookie)
	cbResp, err := client.Do(req)
	if err != nil {
		t.Fatalf("callback request: %v", err)
	}
	return cbResp, srv, userRepo
}

func doOIDCLogin(t *testing.T, idClaims, userinfo map[string]any) *http.Response {
	t.Helper()
	resp, _, _ := doOIDCLoginFull(t, "", idClaims, userinfo)
	return resp
}

func TestOIDC_AllowedVerifiedEmailInIDToken(t *testing.T) {
	resp := doOIDCLogin(t, map[string]any{
		"email":          oidcAllowedEmail,
		"email_verified": true,
	}, nil)
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusFound {
		t.Fatalf("expected 302, got %d (body: %s)", resp.StatusCode, readBody(t, resp))
	}
	if loc := resp.Header.Get("Location"); loc != "/inbox" {
		t.Errorf("expected redirect to /inbox, got %q", loc)
	}
	var tokenCookie *http.Cookie
	for _, c := range resp.Cookies() {
		if c.Name == "token" {
			tokenCookie = c
		}
	}
	if tokenCookie == nil || tokenCookie.Value == "" {
		t.Error("callback did not set a token cookie")
	}
}

func TestOIDC_EmailOnlyInUserinfoWithMatchingSub(t *testing.T) {
	resp := doOIDCLogin(t, map[string]any{}, map[string]any{
		"sub":            "user-1",
		"email":          oidcAllowedEmail,
		"email_verified": true,
	})
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusFound {
		t.Fatalf("expected 302, got %d (body: %s)", resp.StatusCode, readBody(t, resp))
	}
	if loc := resp.Header.Get("Location"); loc != "/inbox" {
		t.Errorf("expected redirect to /inbox, got %q", loc)
	}
}

func TestOIDC_UserinfoSubMismatch(t *testing.T) {
	resp := doOIDCLogin(t, map[string]any{}, map[string]any{
		"sub":            "someone-else",
		"email":          oidcAllowedEmail,
		"email_verified": true,
	})
	defer func() { _ = resp.Body.Close() }()

	assertForbidden(t, resp)
}

func TestOIDC_DifferentEmail(t *testing.T) {
	resp, _, userRepo := doOIDCLoginFull(t, "admin", map[string]any{
		"email":          "attacker@evil.example",
		"email_verified": true,
	}, nil)
	defer func() { _ = resp.Body.Close() }()

	assertForbidden(t, resp)

	u, err := userRepo.GetByUsername("admin")
	if err != nil || u == nil {
		t.Fatalf("seeded user disappeared: %v", err)
	}
	if u.Username != "admin" {
		t.Errorf("existing user was renamed to %q", u.Username)
	}
	if other, _ := userRepo.GetByUsername("attacker@evil.example"); other != nil {
		t.Error("attacker email was provisioned")
	}
}

func TestOIDC_EmailVerifiedFalse(t *testing.T) {
	resp := doOIDCLogin(t, map[string]any{
		"email":          oidcAllowedEmail,
		"email_verified": false,
	}, nil)
	defer func() { _ = resp.Body.Close() }()

	assertForbidden(t, resp)
}

func TestOIDC_EmailVerifiedMissing(t *testing.T) {
	resp := doOIDCLogin(t, map[string]any{
		"email": oidcAllowedEmail,
	}, nil)
	defer func() { _ = resp.Body.Close() }()

	assertForbidden(t, resp)
}

func TestOIDC_StateTokenWithoutTypRejected(t *testing.T) {
	idp := newFakeIdP(t, nil, nil)
	srv, _ := setupOIDCEnv(t, "", idp.URL)

	// State cookie signed like the old signState, without the typ claim.
	state := "old-style-state"
	old := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
		"state": state,
		"exp":   time.Now().Add(10 * time.Minute).Unix(),
	})
	signed, err := old.SignedString([]byte(oidcTestSecret))
	if err != nil {
		t.Fatalf("sign old-style state: %v", err)
	}

	client := &http.Client{
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	req, err := http.NewRequest(http.MethodGet,
		srv.URL+"/api/auth/oidc/callback?state="+url.QueryEscape(state)+"&code=c", nil)
	if err != nil {
		t.Fatalf("build callback request: %v", err)
	}
	req.AddCookie(&http.Cookie{Name: "oidc_state", Value: signed})
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("callback request: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d (body: %s)", resp.StatusCode, readBody(t, resp))
	}
	if body := strings.TrimSpace(readBody(t, resp)); !strings.Contains(body, "invalid state") {
		t.Errorf("expected invalid state error, got %s", body)
	}
}
