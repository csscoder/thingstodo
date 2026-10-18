package middleware

import (
	"context"
	"crypto/subtle"
	"net/http"
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
				next.ServeHTTP(w, r.WithContext(sessionCtx))
				return
			}
			WriteError(w, http.StatusUnauthorized, rejectMsg, "UNAUTHORIZED", "")
		})
	}
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
