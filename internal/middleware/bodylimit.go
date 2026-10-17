package middleware

import (
	"net/http"
	"regexp"
)

// MaxJSONBody is the maximum accepted JSON request body size (10 MiB).
const MaxJSONBody int64 = 10 << 20

// uploadRouteExempt matches the multipart attachment upload route, which is
// capped by its own larger MaxUploadSize limit in the handler instead.
var uploadRouteExempt = regexp.MustCompile(`^/api/tasks/[^/]+/attachments$`)

// BodyLimit wraps every request body in http.MaxBytesReader capped at limit
// bytes, regardless of Content-Type. The only exemption is the multipart
// attachment upload route, which enforces its own MaxUploadSize cap (and
// applies this limit itself to JSON bodies on that route).
func BodyLimit(limit int64) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodPost || !uploadRouteExempt.MatchString(r.URL.Path) {
				r.Body = http.MaxBytesReader(w, r.Body, limit)
			}
			next.ServeHTTP(w, r)
		})
	}
}
