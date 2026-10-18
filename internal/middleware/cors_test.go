package middleware

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/collinjanssen/thingstodo/internal/config"
	"github.com/golang-jwt/jwt/v5"
)

// crossOriginForbiddenEnvelope is the exact JSON body the guard must write.
const crossOriginForbiddenEnvelope = `{"error":"cross-origin session request forbidden","code":"FORBIDDEN"}`

// guardRequest builds a text/plain POST that authenticates as a proxy
// session for Remote-User, targeting Host app.example. An empty origin
// omits the header.
func guardRequest(origin string) *http.Request {
	r := httptest.NewRequest(http.MethodPost, "/api/tasks", strings.NewReader("{}"))
	r.Header.Set("Content-Type", "text/plain")
	r.Header.Set("Remote-User", "proxyuser")
	r.Host = "app.example"
	if origin != "" {
		r.Header.Set("Origin", origin)
	}
	return r
}

func TestAuth_CrossOriginSessionForbidden(t *testing.T) {
	proxyCfg := config.Config{AuthMode: "proxy", AuthProxyHeader: "Remote-User", APIKey: "k"}

	// A cross-origin text/plain POST authenticated only by the proxy
	// session is rejected with 403 FORBIDDEN in the standard envelope —
	// CORS alone cannot stop a browser from attaching a session to a
	// simple no-preflight request.
	rec := runAuth(proxyCfg, firstUserStub, userExistsStub, nil, nil, guardRequest("https://evil.example"))
	assertStatusBody(t, rec, http.StatusForbidden, crossOriginForbiddenEnvelope)

	// Origin naming the same host as the request is allowed; the default
	// port is stripped on both sides before comparing.
	rec = runAuth(proxyCfg, firstUserStub, userExistsStub, nil, nil, guardRequest("https://app.example"))
	assertStatusBody(t, rec, http.StatusOK, "proxyuser")

	rec = runAuth(proxyCfg, firstUserStub, userExistsStub, nil, nil, guardRequest("https://app.example:443"))
	assertStatusBody(t, rec, http.StatusOK, "proxyuser")

	// A different port is still a different origin.
	rec = runAuth(proxyCfg, firstUserStub, userExistsStub, nil, nil, guardRequest("https://app.example:8443"))
	assertStatusBody(t, rec, http.StatusForbidden, crossOriginForbiddenEnvelope)

	// No Origin header (CLI, curl, Hermes) is allowed.
	rec = runAuth(proxyCfg, firstUserStub, userExistsStub, nil, nil, guardRequest(""))
	assertStatusBody(t, rec, http.StatusOK, "proxyuser")

	// Bearer authentication is exempt from the guard: cross-origin
	// widgets are exactly what tokens are for.
	r := guardRequest("https://evil.example")
	r.Header.Set("Authorization", "Bearer k")
	rec = runAuth(proxyCfg, firstUserStub, userExistsStub, nil, nil, r)
	assertStatusBody(t, rec, http.StatusOK, "u1")

	// Cookie sessions (builtin/oidc) are guarded the same way.
	cookieCfg := config.Config{AuthMode: "builtin", JWTSecret: testSecret}
	valid := signToken(t, jwt.SigningMethodHS256, testSecret, jwt.MapClaims{
		"sub": "u1",
		"exp": time.Now().Add(time.Hour).Unix(),
	})

	cross := requestWithSession(valid)
	cross.Host = "app.example"
	cross.Header.Set("Origin", "https://evil.example")
	rec = runAuth(cookieCfg, firstUserStub, userExistsStub, nil, nil, cross)
	assertStatusBody(t, rec, http.StatusForbidden, crossOriginForbiddenEnvelope)

	same := requestWithSession(valid)
	same.Host = "app.example"
	same.Header.Set("Origin", "https://app.example")
	rec = runAuth(cookieCfg, firstUserStub, userExistsStub, nil, nil, same)
	assertStatusBody(t, rec, http.StatusOK, "u1")
}
