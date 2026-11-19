package proxy

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"vigil-api/gateway/internal/middleware"
)

func newClient(url string, maxRetries int, total time.Duration, onRetry func()) *Client {
	return New(Options{
		BaseURL:        url,
		ConnectTimeout: 200 * time.Millisecond,
		TotalTimeout:   total,
		MaxRetries:     maxRetries,
		OnRetry:        onRetry,
	})
}

func TestPredictSuccessPropagatesRequestID(t *testing.T) {
	var gotRID string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotRID = r.Header.Get(middleware.RequestIDHeader)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"scores":{"toxicity":0.95,"spam":0.05},"model_version":"stub-1"}`))
	}))
	defer srv.Close()

	c := newClient(srv.URL, 2, time.Second, nil)
	ctx := middleware.WithRequestID(context.Background(), "rid-abc")

	res, perr := c.Predict(ctx, TextRequest{Content: "hi"})
	if perr != nil {
		t.Fatalf("unexpected error: %v", perr)
	}
	if res.Scores["toxicity"] != 0.95 || res.ModelVersion != "stub-1" {
		t.Fatalf("unexpected result: %+v", res)
	}
	if gotRID != "rid-abc" {
		t.Fatalf("request id not propagated: got %q", gotRID)
	}
}

func TestPredict4xxNotRetried(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&calls, 1)
		http.Error(w, `{"detail":"nope"}`, http.StatusUnprocessableEntity)
	}))
	defer srv.Close()

	c := newClient(srv.URL, 2, time.Second, nil)
	_, perr := c.Predict(context.Background(), TextRequest{Content: "x"})
	if perr == nil || perr.Kind != KindUpstream4xx {
		t.Fatalf("want KindUpstream4xx, got %v", perr)
	}
	if perr.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d", perr.StatusCode)
	}
	if perr.IsUpstreamFault() {
		t.Fatal("4xx must not count as an upstream fault")
	}
	if n := atomic.LoadInt32(&calls); n != 1 {
		t.Fatalf("4xx retried: %d calls, want 1", n)
	}
}

func TestPredict5xxRetriedThenFails(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	var retries int32
	c := newClient(srv.URL, 2, time.Second, func() { atomic.AddInt32(&retries, 1) })
	_, perr := c.Predict(context.Background(), TextRequest{Content: "x"})
	if perr == nil || perr.Kind != KindUpstream5xx {
		t.Fatalf("want KindUpstream5xx, got %v", perr)
	}
	if !perr.IsUpstreamFault() {
		t.Fatal("5xx must count as an upstream fault")
	}
	if n := atomic.LoadInt32(&calls); n != 3 {
		t.Fatalf("calls = %d, want 3 (1 + 2 retries)", n)
	}
	if n := atomic.LoadInt32(&retries); n != 2 {
		t.Fatalf("onRetry calls = %d, want 2", n)
	}
}

func TestPredict5xxThenSuccess(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if atomic.AddInt32(&calls, 1) == 1 {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		_, _ = w.Write([]byte(`{"scores":{"toxicity":0.1},"model_version":"v"}`))
	}))
	defer srv.Close()

	c := newClient(srv.URL, 2, time.Second, nil)
	res, perr := c.Predict(context.Background(), TextRequest{Content: "x"})
	if perr != nil {
		t.Fatalf("expected recovery, got %v", perr)
	}
	if res.Scores["toxicity"] != 0.1 {
		t.Fatalf("unexpected result: %+v", res)
	}
	if n := atomic.LoadInt32(&calls); n != 2 {
		t.Fatalf("calls = %d, want 2", n)
	}
}

func TestPredictTimeout(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(200 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	c := newClient(srv.URL, 1, 40*time.Millisecond, nil)
	_, perr := c.Predict(context.Background(), TextRequest{Content: "x"})
	if perr == nil || perr.Kind != KindTimeout {
		t.Fatalf("want KindTimeout, got %v", perr)
	}
	if !perr.IsUpstreamFault() {
		t.Fatal("timeout must count as an upstream fault")
	}
}

func TestPredictNetworkErrorOnClosedServer(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	url := srv.URL
	srv.Close() // nothing is listening now

	c := newClient(url, 1, time.Second, nil)
	_, perr := c.Predict(context.Background(), TextRequest{Content: "x"})
	if perr == nil || perr.Kind != KindNetwork {
		t.Fatalf("want KindNetwork, got %v", perr)
	}
}

func TestPredictContextCancelNotRetried(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(100 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(20 * time.Millisecond); cancel() }()

	c := newClient(srv.URL, 2, time.Second, nil)
	_, perr := c.Predict(ctx, TextRequest{Content: "x"})
	if perr == nil || perr.Kind != KindCanceled {
		t.Fatalf("want KindCanceled, got %v", perr)
	}
	if perr.IsUpstreamFault() {
		t.Fatal("client cancellation must not count as an upstream fault")
	}
}
