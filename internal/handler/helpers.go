package handler

import (
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"strings"

	mw "github.com/collinjanssen/thingstodo/internal/middleware"
	"github.com/collinjanssen/thingstodo/internal/repository"
)

func writeJSON(w http.ResponseWriter, status int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg, code string) {
	mw.WriteError(w, status, msg, code, "")
}

// writeFieldError writes the JSON error envelope with the name of the
// request field the error is about (e.g. a validation failure).
func writeFieldError(w http.ResponseWriter, status int, msg, code, field string) {
	mw.WriteError(w, status, msg, code, field)
}

// refExists checks that a referenced entity id exists and writes the error
// response when it does not: 422 VALIDATION naming the request field. It
// returns false once the response has been written, so callers write:
//
//	if !refExists(w, db, "projects", "project", "project_id", id) { return }
func refExists(w http.ResponseWriter, db repository.DBTX, table, entity, field, id string) bool {
	ok, err := repository.Exists(db, table, id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error(), "INTERNAL")
		return false
	}
	if !ok {
		writeFieldError(w, http.StatusUnprocessableEntity, entity+" not found", "VALIDATION", field)
		return false
	}
	return true
}

// tagsExist checks every tag id in tagIDs the same way refExists checks one.
func tagsExist(w http.ResponseWriter, db repository.DBTX, field string, tagIDs []string) bool {
	for _, id := range tagIDs {
		if !refExists(w, db, "tags", "tag", field, id) {
			return false
		}
	}
	return true
}

// writeDecodeError maps a request body decode failure to the right client
// error: 413 when the body exceeded the MaxBytesReader limit, 400 otherwise.
func writeDecodeError(w http.ResponseWriter, err error) {
	var maxBytes *http.MaxBytesError
	if errors.As(err, &maxBytes) {
		writeError(w, http.StatusRequestEntityTooLarge, "request body too large", "BODY_TOO_LARGE")
		return
	}
	writeError(w, http.StatusBadRequest, "invalid JSON", "BAD_REQUEST")
}

func decodeJSON(r *http.Request, v interface{}) error {
	defer func() { _ = r.Body.Close() }()
	return json.NewDecoder(r.Body).Decode(v)
}

func decodeJSONWithRaw(r *http.Request, v interface{}) (map[string]json.RawMessage, error) {
	defer func() { _ = r.Body.Close() }()
	var raw map[string]json.RawMessage
	dec := json.NewDecoder(r.Body)
	if err := dec.Decode(&raw); err != nil {
		return nil, err
	}
	// Re-marshal and decode into struct
	b, _ := json.Marshal(raw)
	if err := json.Unmarshal(b, v); err != nil {
		return nil, err
	}
	return raw, nil
}

// isSecureRequest reports whether the request reached this server over TLS,
// either directly or behind a reverse proxy asserting X-Forwarded-Proto.
func isSecureRequest(r *http.Request) bool {
	return r.TLS != nil || strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https")
}

// clientIP resolves the client IP for rate limiting. It starts from the host
// part of RemoteAddr; when that is a loopback or private address (this app
// deploys behind a reverse proxy) and X-Forwarded-For is present, it returns
// the rightmost comma-separated entry — the one appended by the trusted
// proxy for the directly connected client.
func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	if ip := net.ParseIP(host); ip != nil && (ip.IsLoopback() || ip.IsPrivate()) {
		if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
			parts := strings.Split(xff, ",")
			return strings.TrimSpace(parts[len(parts)-1])
		}
	}
	return host
}
