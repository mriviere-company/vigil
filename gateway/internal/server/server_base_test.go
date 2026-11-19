package server

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"vigil-api/gateway/internal/circuit"
	"vigil-api/gateway/internal/config"
)

func testLogger() *slog.Logger { return slog.New(slog.NewJSONHandler(io.Discard, nil)) }

func baseServer(t *testing.T) *Server {
	t.Helper()
	// A closed breaker reports "closed"; open one reports "open".
	closed := circuit.New(circuit.Config{FailureThreshold: 1, OpenTimeout: time.Minute})
	open := circuit.New(circuit.Config{FailureThreshold: 1, OpenTimeout: time.Minute})
	_ = open.Execute(func() error { return errBoom }) // trip it to open

	return New(Deps{
		Config: config.Config{ShutdownTimeout: time.Second},
		Logger: testLogger(),
		Text:   &Downstream{Name: "text", Breaker: closed},
		Image:  &Downstream{Name: "image", Breaker: open},
	})
}

var errBoom = errors.New("boom")

func TestHealthEndpoint(t *testing.T) {
	srv := baseServer(t)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/health", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var body healthResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("bad json: %v", err)
	}
	if body.Status != "ok" {
		t.Errorf("status = %q", body.Status)
	}
	if body.Circuits["text"] != "closed" || body.Circuits["image"] != "open" {
		t.Errorf("circuits = %v", body.Circuits)
	}
}

func TestHealthEchoesRequestID(t *testing.T) {
	srv := baseServer(t)
	req := httptest.NewRequest(http.MethodGet, "/v1/health", nil)
	req.Header.Set("X-Request-ID", "trace-123")
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	if got := rec.Header().Get("X-Request-ID"); got != "trace-123" {
		t.Fatalf("X-Request-ID = %q, want trace-123", got)
	}
}

func TestUnknownRouteIs404(t *testing.T) {
	srv := baseServer(t)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/nope", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
}

func TestGracefulShutdownReturnsOnContextCancel(t *testing.T) {
	srv := New(Deps{
		Config: config.Config{Addr: "127.0.0.1:0", ShutdownTimeout: time.Second},
		Logger: testLogger(),
	})
	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan error, 1)
	go func() { done <- srv.Run(ctx) }()

	// Give the listener a moment, then signal shutdown.
	time.Sleep(50 * time.Millisecond)
	cancel()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run returned error: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Run did not return after context cancel")
	}
}
