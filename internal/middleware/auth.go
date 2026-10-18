package middleware

import (
	"context"
	"crypto/subtle"
	"net"
	"net/http"
	"net/url"
	"strings"

	"github.com/collinjanssen/thingstodo/internal/config"
	"github.com/golang-jwt/jwt/v5"
)

type contextKey string

const (
	UserIDKey     contextKey = "userID"
	AuthMethodKey contextKey = "authMethod"
)

// Authentication methods recorded on the request context by Auth.
const (
	AuthSession   = "session"
	AuthMasterKey = "master_key"
	AuthToken     = "token"
)

// AuthMethod returns the method the request authenticated with —
// AuthSession, AuthMasterKey or AuthToken — or "" when the Auth
// middleware did not record one.
func AuthMethod(ctx context.Context) string {
	m, _ := ctx.Value(AuthMethodKey).(string)
	return m
}

// UserLookupFunc returns the user ID for the API key holder.
type UserLookupFunc func() (string, error)

// UserExistsFunc reports whether a user with the given ID exists.
type UserExistsFunc func(id string) (bool, error)

// TokenLookupFunc resolves a named API token value to its row id. ok is
// false for unknown and revoked tokens.
type TokenLookupFunc func(token string) (id string, ok bool)

// bearerToken extracts the value of an "Authorization: Bearer <value>"
// header, reporting false when the header is absent or not a Bearer.
func bearerToken(r *http.Request) (string, bool) {
	authHeader := r.Header.Get("Authorization")
	if authHeader == "" {
		return "", false
	}
	token, ok := strings.CutPrefix(authHeader, "Bearer ")
	if !ok {
		return "", false
	}
	return strings.TrimSpace(token), true
}

// Auth authenticates each request and records both the user id and the
// authentication method on the context. Bearer precedence: the master API
// key is checked first, then the session (cookie or proxy header), then
// named API tokens — so a valid session is never shadowed by a Bearer
// token. A Bearer value that matches nothing rejects the request without
// falling back to the session. In "none" mode Bearer headers are ignored
// entirely.
func Auth(cfg config.Config, apiKeyUserLookup UserLookupFunc, userExists UserExistsFunc, tokenLookup TokenLookupFunc, touchToken func(id string)) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// No authentication — for development use only. Bearer
			// headers (master key or named token) are ignored.
			if cfg.AuthMode == "none" {
				userID, err := apiKeyUserLookup()
				if err != nil || userID == "" {
					WriteError(w, http.StatusInternalServerError, "no users found", "INTERNAL", "")
					return
				}
				if crossOriginSession(r) {
					rejectCrossOriginSession(w)
					return
				}
				ctx := context.WithValue(r.Context(), UserIDKey, userID)
				ctx = context.WithValue(ctx, AuthMethodKey, AuthSession)
				next.ServeHTTP(w, r.WithContext(ctx))
				return
			}

			// API key check runs in all auth modes (except "none")
			if cfg.APIKey != "" {
				if token, ok := bearerToken(r); ok {
					if subtle.ConstantTimeCompare([]byte(token), []byte(cfg.APIKey)) == 1 {
						userID, err := apiKeyUserLookup()
						if err != nil || userID == "" {
							WriteError(w, http.StatusUnauthorized, "api key user not found", "UNAUTHORIZED", "")
							return
						}
						ctx := context.WithValue(r.Context(), UserIDKey, userID)
						ctx = context.WithValue(ctx, AuthMethodKey, AuthMasterKey)
						next.ServeHTTP(w, r.WithContext(ctx))
						return
					}
				}
			}

			// Session auth (proxy header, or the JWT cookie in builtin
			// and oidc modes). The outcome is held until the named-token
			// check below: an authenticated session stands even when a
			// Bearer token is present, while a Bearer that matches
			// nothing still rejects the request with no fallback.
			sessionCtx, sessionOK, rejectMsg := sessionAuth(cfg, userExists, r)

			if token, ok := bearerToken(r); ok {
				if tokenLookup != nil {
					id, found := tokenLookup(token)
					if found {
						if sessionOK {
							// The session outranks the token, so the
							// effective method is session and the
							// cross-origin guard applies.
							if crossOriginSession(r) {
								rejectCrossOriginSession(w)
								return
							}
							next.ServeHTTP(w, r.WithContext(sessionCtx))
							return
						}
						userID, err := apiKeyUserLookup()
						if err != nil || userID == "" {
							WriteError(w, http.StatusUnauthorized, "api key user not found", "UNAUTHORIZED", "")
							return
						}
						if touchToken != nil {
							touchToken(id)
						}
						ctx := context.WithValue(r.Context(), UserIDKey, userID)
						ctx = context.WithValue(ctx, AuthMethodKey, AuthToken)
						next.ServeHTTP(w, r.WithContext(ctx))
						return
					}
				}
				// Header present but nothing matched — reject immediately
				WriteError(w, http.StatusUnauthorized, "invalid api key", "UNAUTHORIZED", "")
				return
			}

			if sessionOK {
				if crossOriginSession(r) {
					rejectCrossOriginSession(w)
					return
				}
				next.ServeHTTP(w, r.WithContext(sessionCtx))
				return
			}
			WriteError(w, http.StatusUnauthorized, rejectMsg, "UNAUTHORIZED", "")
		})
	}
}

// crossOriginSession reports whether r names an origin other than the
// one it was sent to. When the browser sets Sec-Fetch-Site it decides
// outright: "same-origin" and "none" (user-initiated navigation) count
// as same-origin, any other value ("cross-site", "same-site") as
// cross-origin. Only when the header is absent does the Origin header
// decide: a request whose Origin host — name and, with default ports
// (80 for http, 443 for https) stripped on both sides, port — differs
// from the request Host is cross-origin.
//
// Browsers set Sec-Fetch-Site on every request and scripts cannot set
// or override it (forbidden header name), so a proxy that rewrites Host
// (Vite changeOrigin, nginx $proxy_host) can no longer make a
// same-origin request look foreign. Go's own
// net/http.CrossOriginProtection detects cross-origin requests the same
// way: "with the Sec-Fetch-Site header, available in all browsers since
// 2023, or by comparing the hostname of the Origin header with the Host
// header" (go doc net/http.CrossOriginProtection, go1.27.0, read
// 2026-10-18). We keep this guard instead of adopting that handler
// because it always allows GET/HEAD/OPTIONS, while ours rejects
// cross-origin sessions on every method.
//
// This is the cross-origin session guard of spec A2. Browsers attach
// Origin to every cross-origin request and scripts cannot suppress it,
// so a request that would authenticate as a session (cookie or proxy
// header) and still names a foreign origin cannot come from the trusted
// web UI; it is rejected with 403. A missing or unparseable Origin
// counts as same-origin: browsers always send it cross-origin, so only
// header-less non-browser clients (curl, the CLI, Hermes) omit or mangle
// it. Bearer-authenticated requests (master key or named token) are
// exempt — cross-origin use is exactly what tokens are for.
func crossOriginSession(r *http.Request) bool {
	if sfs := r.Header.Get("Sec-Fetch-Site"); sfs != "" {
		return sfs != "same-origin" && sfs != "none"
	}
	origin := strings.TrimSpace(r.Header.Get("Origin"))
	if origin == "" {
		return false
	}
	u, err := url.Parse(origin)
	if err != nil {
		return false
	}
	originHost, originPort := u.Hostname(), stripDefaultPort(u.Scheme, u.Port())
	if originHost == "" {
		// Opaque origins such as "null" (sandboxed frames) are never
		// same-origin with the target.
		return true
	}
	reqHost, reqPort := r.Host, ""
	if h, p, err := net.SplitHostPort(r.Host); err == nil {
		reqHost, reqPort = h, p
	}
	reqPort = stripDefaultPort(u.Scheme, reqPort)
	return !strings.EqualFold(originHost, reqHost) || originPort != reqPort
}

// stripDefaultPort drops the scheme's default port so that
// "https://app.example:443" and Host "app.example" compare equal.
func stripDefaultPort(scheme, port string) string {
	switch {
	case scheme == "http" && port == "80":
		return ""
	case scheme == "https" && port == "443":
		return ""
	}
	return port
}

// rejectCrossOriginSession answers a session-authenticated request that
// named a foreign Origin with 403 FORBIDDEN in the standard envelope.
func rejectCrossOriginSession(w http.ResponseWriter) {
	WriteError(w, http.StatusForbidden, "cross-origin session request forbidden", "FORBIDDEN", "")
}

// sessionAuth evaluates the mode-specific session (proxy header or JWT
// cookie) without writing a response, so its outcome can be combined with
// the Bearer checks in Auth. It returns the augmented context, whether
// authentication succeeded, and the rejection message for failure.
func sessionAuth(cfg config.Config, userExists UserExistsFunc, r *http.Request) (context.Context, bool, string) {
	ctx := r.Context()

	if cfg.AuthMode == "proxy" {
		// Trust proxy header
		userHeader := r.Header.Get(cfg.AuthProxyHeader)
		if userHeader == "" {
			return ctx, false, "unauthorized"
		}
		ctx = context.WithValue(ctx, UserIDKey, userHeader)
		ctx = context.WithValue(ctx, AuthMethodKey, AuthSession)
		return ctx, true, ""
	}

	// "builtin" and "oidc": JWT cookie auth
	cookie, err := r.Cookie("token")
	if err != nil {
		return ctx, false, "unauthorized"
	}

	jwtToken, err := jwt.Parse(cookie.Value, func(t *jwt.Token) (interface{}, error) {
		return []byte(cfg.JWTSecret), nil
	}, jwt.WithValidMethods([]string{"HS256"}), jwt.WithExpirationRequired())
	if err != nil || !jwtToken.Valid {
		return ctx, false, "invalid token"
	}

	claims, ok := jwtToken.Claims.(jwt.MapClaims)
	if !ok {
		return ctx, false, "invalid claims"
	}

	// OIDC state cookies are signed with the same secret but are
	// not session tokens; never accept them as authentication.
	if typ, _ := claims["typ"].(string); typ == "oidc_state" {
		return ctx, false, "invalid token"
	}

	sub, _ := claims["sub"].(string)
	if sub == "" {
		return ctx, false, "invalid claims"
	}
	exists, err := userExists(sub)
	if err != nil || !exists {
		return ctx, false, "invalid claims"
	}

	ctx = context.WithValue(ctx, UserIDKey, sub)
	ctx = context.WithValue(ctx, AuthMethodKey, AuthSession)
	return ctx, true, ""
}
