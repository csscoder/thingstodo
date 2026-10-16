package middleware

import (
	"bytes"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestBodyLimit_CapsJSONRegardlessOfContentType(t *testing.T) {
	var readErr error
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, readErr = io.ReadAll(r.Body)
	})

	req := httptest.NewRequest(http.MethodPost, "/api/tasks", bytes.NewReader(make([]byte, 17)))
	req.Header.Set("Content-Type", "multipart/form-data")
	rec := httptest.NewRecorder()

	BodyLimit(16)(handler).ServeHTTP(rec, req)

	var maxBytes *http.MaxBytesError
	if !errors.As(readErr, &maxBytes) {
		t.Fatalf("expected *http.MaxBytesError from body read, got %T: %v", readErr, readErr)
	}
}

func TestBodyLimit_UploadRouteExempt(t *testing.T) {
	var readErr error
	var readLen int
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var b []byte
		b, readErr = io.ReadAll(r.Body)
		readLen = len(b)
	})

	req := httptest.NewRequest(http.MethodPost, "/api/tasks/abc/attachments", bytes.NewReader(make([]byte, 17)))
	rec := httptest.NewRecorder()

	BodyLimit(16)(handler).ServeHTTP(rec, req)

	if readErr != nil {
		t.Fatalf("expected body read to succeed on exempt route, got %v", readErr)
	}
	if readLen != 17 {
		t.Fatalf("expected 17 bytes read on exempt route, got %d", readLen)
	}
}
