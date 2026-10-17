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

const UserIDKey contextKey = "userID"

// UserLookupFunc returns the user ID for the API key holder.
type UserLookupFunc func() (string, error)

// UserExistsFunc reports whether a user with the given ID exists.
type UserExistsFunc func(id string) (bool, error)

func Auth(cfg config.Config, apiKeyUserLookup UserLookupFunc, userExists UserExistsFunc) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// API key check runs in all auth modes (except "none")
			if cfg.APIKey != "" && cfg.AuthMode != "none" {
				if authHeader := r.Header.Get("Authorization"); authHeader != "" {
					if token, ok := strings.CutPrefix(authHeader, "Bearer "); ok {
						token = strings.TrimSpace(token)
						if subtle.ConstantTimeCompare([]byte(token), []byte(cfg.APIKey)) == 1 {
							userID, err := apiKeyUserLookup()
							if err != nil || userID == "" {
								WriteError(w, http.StatusUnauthorized, "api key user not found", "UNAUTHORIZED", "")
								return
							}
							ctx := context.WithValue(r.Context(), UserIDKey, userID)
							next.ServeHTTP(w, r.WithContext(ctx))
							return
						}
						// Header present but key doesn't match — reject immediately
						WriteError(w, http.StatusUnauthorized, "invalid api key", "UNAUTHORIZED", "")
						return
					}
				}
			}

			switch cfg.AuthMode {
			case "none":
				// No authentication — for development use only
				userID, err := apiKeyUserLookup()
				if err != nil || userID == "" {
					WriteError(w, http.StatusInternalServerError, "no users found", "INTERNAL", "")
					return
				}
				ctx := context.WithValue(r.Context(), UserIDKey, userID)
				next.ServeHTTP(w, r.WithContext(ctx))
				return

			case "proxy":
				// Trust proxy header
				userHeader := r.Header.Get(cfg.AuthProxyHeader)
				if userHeader == "" {
					WriteError(w, http.StatusUnauthorized, "unauthorized", "UNAUTHORIZED", "")
					return
				}
				ctx := context.WithValue(r.Context(), UserIDKey, userHeader)
				next.ServeHTTP(w, r.WithContext(ctx))

			default: // "builtin" and "oidc": JWT cookie auth
				cookie, err := r.Cookie("token")
				if err != nil {
					WriteError(w, http.StatusUnauthorized, "unauthorized", "UNAUTHORIZED", "")
					return
				}

				jwtToken, err := jwt.Parse(cookie.Value, func(t *jwt.Token) (interface{}, error) {
					return []byte(cfg.JWTSecret), nil
				}, jwt.WithValidMethods([]string{"HS256"}), jwt.WithExpirationRequired())
				if err != nil || !jwtToken.Valid {
					WriteError(w, http.StatusUnauthorized, "invalid token", "UNAUTHORIZED", "")
					return
				}

				claims, ok := jwtToken.Claims.(jwt.MapClaims)
				if !ok {
					WriteError(w, http.StatusUnauthorized, "invalid claims", "UNAUTHORIZED", "")
					return
				}

				// OIDC state cookies are signed with the same secret but are
				// not session tokens; never accept them as authentication.
				if typ, _ := claims["typ"].(string); typ == "oidc_state" {
					WriteError(w, http.StatusUnauthorized, "invalid token", "UNAUTHORIZED", "")
					return
				}

				sub, _ := claims["sub"].(string)
				if sub == "" {
					WriteError(w, http.StatusUnauthorized, "invalid claims", "UNAUTHORIZED", "")
					return
				}
				exists, err := userExists(sub)
				if err != nil || !exists {
					WriteError(w, http.StatusUnauthorized, "invalid claims", "UNAUTHORIZED", "")
					return
				}

				ctx := context.WithValue(r.Context(), UserIDKey, sub)
				next.ServeHTTP(w, r.WithContext(ctx))
			}
		})
	}
}
