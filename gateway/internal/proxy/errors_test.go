package proxy

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestErrorStringAndUnwrap(t *testing.T) {
	inner := errors.New("root cause")
	e := &Error{Kind: KindNetwork, Err: inner}
	if e.Error() == "" {
		t.Error("Error() should be non-empty")
	}
	if !errors.Is(e, inner) {
		t.Error("Unwrap should expose the wrapped error")
	}
	// No wrapped error: exercises the status-code branch.
	e2 := &Error{Kind: KindUpstream5xx, StatusCode: 503}
	if e2.Error() == "" {
		t.Error("Error() with status should be non-empty")
	}
}

func TestKindString(t *testing.T) {
	kinds := []Kind{
		KindNetwork, KindTimeout, KindCanceled, KindUpstream5xx,
		KindUpstream4xx, KindDecode, KindEncode, Kind(999),
	}
	seen := map[string]bool{}
	for _, k := range kinds {
		s := k.String()
		if s == "" {
			t.Errorf("Kind(%d).String() empty", k)
		}
		seen[s] = true
	}
	if seen["unknown"] != true {
		t.Error("out-of-range kind should stringify as 'unknown'")
	}
}

func TestPredictEncodeError(t *testing.T) {
	c := newClient("http://example.invalid", 0, time.Second, nil)
	// A channel value cannot be JSON-marshalled.
	_, perr := c.Predict(context.Background(), map[string]any{"bad": make(chan int)})
	if perr == nil || perr.Kind != KindEncode {
		t.Fatalf("want KindEncode, got %v", perr)
	}
	if perr.IsUpstreamFault() {
		t.Error("an encode (our-side) error must not count as an upstream fault")
	}
}

func TestPredictDecodeError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`this is not json`))
	}))
	defer srv.Close()

	c := newClient(srv.URL, 0, time.Second, nil)
	_, perr := c.Predict(context.Background(), TextRequest{Content: "x"})
	if perr == nil || perr.Kind != KindDecode {
		t.Fatalf("want KindDecode, got %v", perr)
	}
	if !perr.IsUpstreamFault() {
		t.Error("a malformed downstream body should count as an upstream fault")
	}
}
