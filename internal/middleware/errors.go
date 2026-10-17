package middleware

import (
	"encoding/json"
	"log"
	"net/http"
)

// errorEnvelope is the single JSON error body shape returned by the API.
type errorEnvelope struct {
	Error string `json:"error"`
	Code  string `json:"code"`
	Field string `json:"field,omitempty"`
}

// WriteError writes the standard JSON error envelope with
// Content-Type application/json. An empty field is omitted from the body.
// For 5xx responses the message is logged server-side and replaced with the
// generic body so internal details (driver messages, schema details) never
// reach the client.
func WriteError(w http.ResponseWriter, status int, msg, code, field string) {
	if status >= http.StatusInternalServerError {
		log.Printf("http %d: %s", status, msg)
		msg, code, field = "internal error", "INTERNAL", ""
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(errorEnvelope{Error: msg, Code: code, Field: field})
}
