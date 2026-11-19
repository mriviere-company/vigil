package middleware

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func discardLogger() *slog.Logger {
	return slog.New(slog.NewJSONHandler(io.Discard, nil))
}

func TestRequestIDGeneratedWhenAbsent(t *testing.T) {
	var seen string
	h := RequestID(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		seen = RequestIDFromContext(r.Context())
	}))

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))

	if seen == "" {
		t.Fatal("expected a generated request id in context")
	}
	if got := rec.Header().Get(RequestIDHeader); got != seen {
		t.Fatalf("response header %q, context %q; want equal", got, seen)
	}
}

func TestRequestIDPropagatedWhenPresent(t *testing.T) {
	const want = "client-supplied-id"
	var seen string
	h := RequestID(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		seen = RequestIDFromContext(r.Context())
	}))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set(RequestIDHeader, want)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if seen != want {
		t.Fatalf("context id = %q, want %q", seen, want)
	}
	if got := rec.Header().Get(RequestIDHeader); got != want {
		t.Fatalf("echoed id = %q, want %q", got, want)
	}
}

func TestRecoveryReturns500NormalizedError(t *testing.T) {
	h := Chain(
		http.HandlerFunc(func(http.ResponseWriter, *http.Request) { panic("boom") }),
		RequestID,
		Recovery(discardLogger()),
	)

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
	var body ErrorResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("body not valid error envelope: %v", err)
	}
	if body.Error.Code != CodeInternalError {
		t.Fatalf("code = %q, want %q", body.Error.Code, CodeInternalError)
	}
	if body.Error.RequestID == "" {
		t.Fatal("expected request_id in error body")
	}
}

func TestWriteErrorShape(t *testing.T) {
	h := RequestID(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		WriteError(w, r, http.StatusBadRequest, CodeBadRequest, "nope")
	}))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))

	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Fatalf("content-type = %q", ct)
	}
	var body ErrorResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("invalid json: %v", err)
	}
	if body.Error.Code != CodeBadRequest || body.Error.Message != "nope" {
		t.Fatalf("unexpected body: %+v", body.Error)
	}
	if body.Error.RequestID == "" {
		t.Fatal("missing request_id")
	}
}

func TestAccessLogEmitsTierAndStatus(t *testing.T) {
	var sink strings.Builder
	logger := slog.New(slog.NewJSONHandler(&sink, nil))

	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		SetTier(r.Context(), "pro")
		w.WriteHeader(http.StatusTeapot)
	})
	h := Chain(inner, RequestID, AccessLog(logger))

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/moderate/text", nil))

	line := sink.String()
	var entry map[string]any
	if err := json.Unmarshal([]byte(line), &entry); err != nil {
		t.Fatalf("log line not json: %v (%q)", err, line)
	}
	if entry["tier"] != "pro" {
		t.Fatalf("tier = %v, want pro", entry["tier"])
	}
	if entry["status"] != float64(http.StatusTeapot) {
		t.Fatalf("status = %v, want 418", entry["status"])
	}
	if entry["method"] != http.MethodPost {
		t.Fatalf("method = %v", entry["method"])
	}
	if entry["request_id"] == "" || entry["request_id"] == nil {
		t.Fatal("missing request_id in access log")
	}
}

func TestNewIDUniqueAndFormatted(t *testing.T) {
	a, b := NewID(), NewID()
	if a == b {
		t.Fatal("NewID returned duplicate ids")
	}
	if len(a) != 36 || strings.Count(a, "-") != 4 {
		t.Fatalf("id %q is not uuid-shaped", a)
	}
	if a[14] != '4' {
		t.Fatalf("id %q is not version 4", a)
	}
}
