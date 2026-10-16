package middleware

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRecover_PanicReturnsJSON500(t *testing.T) {
	handler := Recover(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		panic("boom")
	}))

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/tasks", nil))

	if rec.Code != http.StatusInternalServerError {
		t.Errorf("expected status %d, got %d", http.StatusInternalServerError, rec.Code)
	}
	if got := rec.Header().Get("Content-Type"); got != "application/json" {
		t.Errorf("expected Content-Type %q, got %q", "application/json", got)
	}
	if body := strings.TrimSpace(rec.Body.String()); body != `{"error":"internal error","code":"INTERNAL"}` {
		t.Errorf("expected generic 5xx body %q, got %q", `{"error":"internal error","code":"INTERNAL"}`, body)
	}
}
