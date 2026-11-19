// Package middleware holds the cross-cutting HTTP concerns that wrap every
// request: request-id propagation, panic recovery, structured access logging,
// Prometheus instrumentation, and the gateway's normalized error envelope.
//
// It owns the request-scoped observability context (request id + a mutable
// per-request log-fields holder) precisely so lower layers such as auth and
// ratelimit can enrich it (e.g. attach the caller's tier for the access log)
// by importing this package, without this package ever importing them — that
// one-directional dependency is what keeps the import graph acyclic.
package middleware

import "context"

// ctxKey is an unexported type so context values set here can never collide
// with keys from other packages.
type ctxKey int

const (
	requestIDKey ctxKey = iota
	logFieldsKey
)

// logFields is a mutable, per-request holder for values that outer middleware
// (the access logger) wants to report but that only inner middleware knows.
//
// It is written by inner handlers (e.g. auth sets the tier) and read by the
// access logger's deferred call after the handler chain returns. Both happen
// on the single goroutine that serves the request, and the write always
// happens-before the read (it precedes the handler's return), so no mutex is
// needed — this is not shared across goroutines.
type logFields struct {
	tier string
}

// WithRequestID returns a context carrying the request id.
func WithRequestID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, requestIDKey, id)
}

// RequestIDFromContext returns the request id, or "" if none was set.
func RequestIDFromContext(ctx context.Context) string {
	if id, ok := ctx.Value(requestIDKey).(string); ok {
		return id
	}
	return ""
}

func withLogFields(ctx context.Context, lf *logFields) context.Context {
	return context.WithValue(ctx, logFieldsKey, lf)
}

// SetTier records the caller's tier on the request's log-fields holder so the
// access logger can emit it. Safe to call when no holder is present (e.g. in
// tests that exercise a handler in isolation) — it is then a no-op.
func SetTier(ctx context.Context, tier string) {
	if lf, ok := ctx.Value(logFieldsKey).(*logFields); ok {
		lf.tier = tier
	}
}
