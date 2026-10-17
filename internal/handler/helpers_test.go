package handler

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/collinjanssen/thingstodo/internal/repository"
)

type errorBody struct {
	Error string `json:"error"`
	Code  string `json:"code"`
}

func TestWriteError_5xxIsGeneric(t *testing.T) {
	rec := httptest.NewRecorder()
	writeError(rec, http.StatusInternalServerError, "sql: no such column x", "INTERNAL")

	body := rec.Body.String()
	if strings.Contains(body, "sql:") {
		t.Errorf("5xx body must not leak internal error text, got: %s", body)
	}

	var got errorBody
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("failed to decode body: %v\nbody: %s", err, body)
	}
	if got.Error != "internal error" {
		t.Errorf("expected error %q, got %q", "internal error", got.Error)
	}
	if got.Code != "INTERNAL" {
		t.Errorf("expected code %q, got %q", "INTERNAL", got.Code)
	}
}

func TestWriteError_4xxPassesThrough(t *testing.T) {
	rec := httptest.NewRecorder()
	writeError(rec, http.StatusBadRequest, "title is required", "VALIDATION")

	var got errorBody
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("failed to decode body: %v\nbody: %s", err, rec.Body.String())
	}
	if got.Error != "title is required" {
		t.Errorf("expected error %q, got %q", "title is required", got.Error)
	}
	if got.Code != "VALIDATION" {
		t.Errorf("expected code %q, got %q", "VALIDATION", got.Code)
	}
}

func TestWriteError_FieldIncluded(t *testing.T) {
	rec := httptest.NewRecorder()
	writeFieldError(rec, http.StatusBadRequest, "bad", "VALIDATION", "when_date")

	want := `{"error":"bad","code":"VALIDATION","field":"when_date"}`
	if got := strings.TrimSpace(rec.Body.String()); got != want {
		t.Errorf("expected body %s, got %s", want, got)
	}
}

func TestSyncErrorMessage(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want string
	}{
		{
			name: "duplicate project name passes through",
			err:  repository.ErrDuplicateProjectName,
			want: "duplicate project name",
		},
		{
			name: "duplicate area name passes through",
			err:  repository.ErrDuplicateAreaName,
			want: "duplicate area name",
		},
		{
			name: "duplicate tag name passes through",
			err:  repository.ErrDuplicateTagName,
			want: "duplicate tag name",
		},
		{
			name: "wrapped duplicate sentinel passes through",
			err:  fmt.Errorf("create project: %w", repository.ErrDuplicateProjectName),
			want: "create project: duplicate project name",
		},
		{
			name: "database error is genericized",
			err:  fmt.Errorf("insert task: %w", errors.New("UNIQUE constraint")),
			want: "internal error",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := syncErrorMessage(tt.err); got != tt.want {
				t.Errorf("syncErrorMessage(%v) = %q, want %q", tt.err, got, tt.want)
			}
		})
	}
}
