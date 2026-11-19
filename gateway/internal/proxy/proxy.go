// Package proxy holds the HTTP clients that call the Python inference services'
// /predict endpoint. Each client has explicit connect and total timeouts, a
// bounded retry policy (network/5xx only, never 4xx) with exponential backoff
// and full jitter, and propagates the request id so a call is traceable end to
// end. Failures are returned as a typed *Error whose Kind lets the caller map
// to the right client status (4xx→400, 5xx/timeout/network→502) and decide
// whether the circuit breaker should count it.
package proxy

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net"
	"net/http"
	"time"

	"vigil-api/gateway/internal/middleware"
)

// Kind classifies a proxy failure.
type Kind int

const (
	KindNetwork     Kind = iota // connection refused/reset, DNS, etc.
	KindTimeout                 // connect or total timeout elapsed
	KindCanceled                // caller's context was cancelled (client gone)
	KindUpstream5xx             // downstream returned 5xx after retries
	KindUpstream4xx             // downstream returned 4xx (e.g. 422 validation)
	KindDecode                  // 2xx body could not be decoded
	KindEncode                  // request body could not be marshalled (our bug)
)

// Error is a typed proxy failure.
type Error struct {
	Kind       Kind
	StatusCode int // downstream HTTP status for upstream kinds, else 0
	Err        error
}

func (e *Error) Error() string {
	if e.Err != nil {
		return fmt.Sprintf("proxy: %s: %v", e.Kind, e.Err)
	}
	return fmt.Sprintf("proxy: %s (status %d)", e.Kind, e.StatusCode)
}

func (e *Error) Unwrap() error { return e.Err }

// IsUpstreamFault reports whether this failure should count against the circuit
// breaker — i.e. the downstream (or the network to it) misbehaved. Client 4xx,
// caller cancellation, and our own encode bug are explicitly not the
// downstream's fault and must not trip the breaker.
func (e *Error) IsUpstreamFault() bool {
	switch e.Kind {
	case KindNetwork, KindTimeout, KindUpstream5xx, KindDecode:
		return true
	default:
		return false
	}
}

func (k Kind) String() string {
	switch k {
	case KindNetwork:
		return "network"
	case KindTimeout:
		return "timeout"
	case KindCanceled:
		return "canceled"
	case KindUpstream5xx:
		return "upstream_5xx"
	case KindUpstream4xx:
		return "upstream_4xx"
	case KindDecode:
		return "decode"
	case KindEncode:
		return "encode"
	default:
		return "unknown"
	}
}

// Result is the decoded /predict response.
type Result struct {
	Scores       map[string]float64 `json:"scores"`
	ModelVersion string             `json:"model_version"`
}

// TextRequest and ImageRequest are the downstream request bodies.
type TextRequest struct {
	Content string `json:"content"`
}

type ImageRequest struct {
	ImageBase64 string `json:"image_base64"`
}

// Client calls one downstream inference service.
type Client struct {
	http        *http.Client
	baseURL     string
	maxRetries  int
	baseBackoff time.Duration
	onRetry     func() // metrics hook; may be nil
}

// Options configure a Client.
type Options struct {
	BaseURL        string
	ConnectTimeout time.Duration
	TotalTimeout   time.Duration
	MaxRetries     int
	// OnRetry is invoked once per retry attempt (for the retries metric).
	OnRetry func()
}

// New builds a Client. The transport uses a per-connection dial (connect)
// timeout; the http.Client.Timeout bounds each individual attempt end to end.
func New(opts Options) *Client {
	transport := &http.Transport{
		DialContext: (&net.Dialer{Timeout: opts.ConnectTimeout}).DialContext,
		// Modest pooling so bursts reuse connections rather than dialing anew.
		MaxIdleConns:        100,
		MaxIdleConnsPerHost: 100,
		IdleConnTimeout:     90 * time.Second,
	}
	onRetry := opts.OnRetry
	if onRetry == nil {
		onRetry = func() {}
	}
	return &Client{
		http:        &http.Client{Timeout: opts.TotalTimeout, Transport: transport},
		baseURL:     opts.BaseURL,
		maxRetries:  opts.MaxRetries,
		baseBackoff: 50 * time.Millisecond,
		onRetry:     onRetry,
	}
}

// Predict POSTs body to the downstream /predict and returns the decoded scores.
// It retries retryable failures up to maxRetries with backoff+jitter, honoring
// ctx cancellation between attempts.
func (c *Client) Predict(ctx context.Context, body any) (*Result, *Error) {
	payload, err := json.Marshal(body)
	if err != nil {
		return nil, &Error{Kind: KindEncode, Err: fmt.Errorf("marshal request: %w", err)}
	}

	var last *Error
	for attempt := 0; attempt <= c.maxRetries; attempt++ {
		if attempt > 0 {
			c.onRetry()
			if berr := c.backoff(ctx, attempt); berr != nil {
				return nil, berr
			}
		}
		result, perr, retryable := c.attempt(ctx, payload)
		if perr == nil {
			return result, nil
		}
		last = perr
		if !retryable {
			return nil, perr
		}
	}
	return nil, last
}

// attempt performs a single request and classifies the outcome.
func (c *Client) attempt(ctx context.Context, payload []byte) (result *Result, perr *Error, retryable bool) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/predict", bytes.NewReader(payload))
	if err != nil {
		return nil, &Error{Kind: KindNetwork, Err: fmt.Errorf("build request: %w", err)}, false
	}
	req.Header.Set("Content-Type", "application/json")
	if rid := middleware.RequestIDFromContext(ctx); rid != "" {
		req.Header.Set(middleware.RequestIDHeader, rid)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		transportErr, retry := classifyTransportError(err)
		return nil, transportErr, retry
	}
	defer func() { _ = resp.Body.Close() }()

	switch {
	case resp.StatusCode >= 200 && resp.StatusCode < 300:
		var out Result
		if derr := json.NewDecoder(resp.Body).Decode(&out); derr != nil {
			return nil, &Error{Kind: KindDecode, StatusCode: resp.StatusCode,
				Err: fmt.Errorf("decode response: %w", derr)}, false
		}
		return &out, nil, false
	case resp.StatusCode >= 500:
		// Drain a little so the connection can be reused.
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
		return nil, &Error{Kind: KindUpstream5xx, StatusCode: resp.StatusCode,
			Err: fmt.Errorf("downstream status %d", resp.StatusCode)}, true
	default: // 4xx and any non-2xx below 500
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
		return nil, &Error{Kind: KindUpstream4xx, StatusCode: resp.StatusCode,
			Err: fmt.Errorf("downstream status %d", resp.StatusCode)}, false
	}
}

// classifyTransportError maps a Do() error to a typed, non-retryable-or-not
// proxy error.
func classifyTransportError(err error) (*Error, bool) {
	switch {
	case errors.Is(err, context.Canceled):
		return &Error{Kind: KindCanceled, Err: err}, false
	case errors.Is(err, context.DeadlineExceeded):
		return &Error{Kind: KindTimeout, Err: err}, true
	}
	var nerr net.Error
	if errors.As(err, &nerr) && nerr.Timeout() {
		return &Error{Kind: KindTimeout, Err: err}, true
	}
	return &Error{Kind: KindNetwork, Err: err}, true
}

// backoff sleeps for an exponentially growing, fully-jittered interval, or
// returns a KindCanceled error if ctx is cancelled first. Full jitter (a random
// value in [0, base*2^attempt)) spreads retries so a recovering downstream is
// not hit by a synchronized thundering herd.
func (c *Client) backoff(ctx context.Context, attempt int) *Error {
	window := c.baseBackoff << (attempt - 1) // attempt starts at 1 for the first retry
	sleep := time.Duration(rand.Int64N(int64(window) + 1))
	timer := time.NewTimer(sleep)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return &Error{Kind: KindCanceled, Err: ctx.Err()}
	}
}
