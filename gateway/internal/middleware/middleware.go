package middleware

import (
	"crypto/rand"
	"fmt"
	"log/slog"
	"net/http"
	"runtime/debug"
	"time"
)

// RequestIDHeader is the canonical header the gateway accepts and echoes, and
// forwards to the inference services so a request is traceable end to end.
const RequestIDHeader = "X-Request-ID"

// NewID returns a random UUIDv4 string. It is the gateway's single id source:
// used for the request id (when a client sends none) and for the moderation
// record id in verdict responses. Hand-rolled on crypto/rand to stay
// stdlib-only (no google/uuid dependency).
func NewID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		// crypto/rand failing is catastrophic and essentially never happens;
		// fall back to a time-derived value so we still return something unique.
		return fmt.Sprintf("00000000-0000-4000-8000-%012x", time.Now().UnixNano())
	}
	b[6] = (b[6] & 0x0f) | 0x40 // version 4
	b[8] = (b[8] & 0x3f) | 0x80 // RFC 4122 variant
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

// RequestID accepts an inbound X-Request-ID or mints a fresh UUIDv4, stashes it
// on the context for downstream use, and echoes it on the response so callers
// can correlate. Outermost useful middleware — everything below can rely on the
// id being present.
func RequestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.Header.Get(RequestIDHeader)
		if id == "" {
			id = NewID()
		}
		w.Header().Set(RequestIDHeader, id)
		ctx := WithRequestID(r.Context(), id)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// Recovery turns a panic in any downstream handler into a 500 with the
// normalized error body instead of crashing the process or leaking a half
// written response. The stack is logged (never sent to the client). Pattern:
// a single top-level recover keeps one bad request from taking down a shared
// server process.
func Recovery(logger *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			defer func() {
				if rec := recover(); rec != nil {
					logger.Error("panic recovered",
						"error", fmt.Sprint(rec),
						"request_id", RequestIDFromContext(r.Context()),
						"stack", string(debug.Stack()),
					)
					WriteError(w, r, http.StatusInternalServerError, CodeInternalError, "internal server error")
				}
			}()
			next.ServeHTTP(w, r)
		})
	}
}

// statusRecorder captures the status code (and whether one was written) so the
// access logger can report it. Defaults to 200, matching net/http's behavior
// when a handler writes a body without calling WriteHeader.
type statusRecorder struct {
	http.ResponseWriter
	status int
	wrote  bool
}

func (s *statusRecorder) WriteHeader(code int) {
	if !s.wrote {
		s.status = code
		s.wrote = true
	}
	s.ResponseWriter.WriteHeader(code)
}

func (s *statusRecorder) Write(b []byte) (int, error) {
	if !s.wrote {
		s.status = http.StatusOK
		s.wrote = true
	}
	return s.ResponseWriter.Write(b)
}

// Flush forwards to the underlying writer if it supports flushing, so the
// wrapper stays transparent to streaming handlers.
func (s *statusRecorder) Flush() {
	if f, ok := s.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// AccessLog emits exactly one structured JSON line per request after it
// completes: method, path, status, duration_ms, request_id and the caller's
// tier. It deliberately never logs the API key itself. It installs the mutable
// logFields holder so inner middleware (auth) can attach the tier.
func AccessLog(logger *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			lf := &logFields{}
			ctx := withLogFields(r.Context(), lf)
			rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}

			next.ServeHTTP(rec, r.WithContext(ctx))

			logger.Info("request",
				"method", r.Method,
				"path", r.URL.Path,
				"status", rec.status,
				"duration_ms", time.Since(start).Milliseconds(),
				"request_id", RequestIDFromContext(ctx),
				"tier", lf.tier,
			)
		})
	}
}

// Chain applies middlewares so the first listed is the outermost wrapper
// (runs first on the way in, last on the way out).
func Chain(h http.Handler, mws ...func(http.Handler) http.Handler) http.Handler {
	for i := len(mws) - 1; i >= 0; i-- {
		h = mws[i](h)
	}
	return h
}
