package middleware

import (
	"encoding/json"
	"net/http"
	"testing"
)

// TestAuth_ErrorsAreJSON verifies the auth middleware rejects requests with
// the JSON error envelope (Content-Type application/json), not plain text.
func TestAuth_ErrorsAreJSON(t *testing.T) {
	rec := runAuth(builtinCfg(), firstUserStub, userExistsStub, requestWithSession(""))

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected status %d, got %d (body %q)", http.StatusUnauthorized, rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("expected Content-Type %q, got %q", "application/json", ct)
	}
	var body struct {
		Error string `json:"error"`
		Code  string `json:"code"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("auth error body is not JSON: %v (%q)", err, rec.Body.String())
	}
	if body.Code != "UNAUTHORIZED" {
		t.Errorf("expected code %q, got %q", "UNAUTHORIZED", body.Code)
	}
}
