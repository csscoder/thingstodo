package middleware

import (
	"encoding/json"
	"log"
	"net/http"
	"runtime/debug"
)

// Recover converts a panic in a downstream handler into a 500 response with
// the generic JSON body, so clients get a clean error instead of a dropped
// connection mid-request (e.g. mid sync batch).
func Recover(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if rec := recover(); rec != nil {
				log.Printf("panic recovered: %v\n%s", rec, debug.Stack())
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusInternalServerError)
				_ = json.NewEncoder(w).Encode(struct {
					Error string `json:"error"`
					Code  string `json:"code"`
				}{Error: "internal error", Code: "INTERNAL"})
			}
		}()
		next.ServeHTTP(w, r)
	})
}
