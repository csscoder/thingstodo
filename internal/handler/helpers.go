package handler

import (
	"encoding/json"
	"errors"
	"log"
	"net/http"
)

func writeJSON(w http.ResponseWriter, status int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg, code string) {
	if status >= http.StatusInternalServerError {
		// Never leak internal error text (driver messages, schema details) to
		// clients; log it server-side and send the generic body instead.
		log.Printf("http %d: %s", status, msg)
		writeJSON(w, status, map[string]string{"error": "internal error", "code": "INTERNAL"})
		return
	}
	writeJSON(w, status, map[string]string{"error": msg, "code": code})
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
	defer r.Body.Close()
	return json.NewDecoder(r.Body).Decode(v)
}

func decodeJSONWithRaw(r *http.Request, v interface{}) (map[string]json.RawMessage, error) {
	defer r.Body.Close()
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
