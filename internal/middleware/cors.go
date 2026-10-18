package middleware

import "net/http"

// The CORS response headers of spec A2 (agent-widget API design). The
// values are pinned verbatim by the tests and docs/api.md.
const (
	corsAllowOrigin   = "*"
	corsAllowMethods  = "GET, POST, PUT, PATCH, DELETE, OPTIONS"
	corsAllowHeaders  = "Authorization, Content-Type, Mcp-Session-Id, Mcp-Protocol-Version, Last-Event-ID"
	corsExposeHeaders = "Mcp-Session-Id"
	corsMaxAge        = "600"
)

// CORS sets the spec A2 CORS headers on every response and answers
// preflight OPTIONS requests with 204 before the wrapped handler — and
// therefore before auth — runs. It never emits
// Access-Control-Allow-Credentials: cross-origin scripts must
// authenticate with a Bearer token, and the cross-origin session guard
// in Auth rejects requests that would authenticate as a session.
//
// With enabled false the middleware is a no-op: AUTH_MODE=none has no
// auth, and a wildcard policy would let any website the owner visits
// read and write the data.
func CORS(enabled bool) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		if !enabled {
			return next
		}
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			h := w.Header()
			h.Set("Access-Control-Allow-Origin", corsAllowOrigin)
			h.Set("Access-Control-Allow-Methods", corsAllowMethods)
			h.Set("Access-Control-Allow-Headers", corsAllowHeaders)
			h.Set("Access-Control-Expose-Headers", corsExposeHeaders)
			h.Set("Access-Control-Max-Age", corsMaxAge)
			if r.Method == http.MethodOptions {
				w.WriteHeader(http.StatusNoContent)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}
