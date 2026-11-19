// Package server wires the HTTP surface: the stdlib (Go 1.22+ method-pattern)
// mux, the global middleware chain, the moderation handlers, and graceful
// shutdown. It is the composition root for a request — it holds the injected
// dependencies (auth, rate limiter, proxy clients, breakers, verdict
// classifier, metrics) and orchestrates them per request; the individual
// resilience pieces stay decoupled and unaware of HTTP.
package server

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"vigil-api/gateway/internal/audit"
	"vigil-api/gateway/internal/auth"
	"vigil-api/gateway/internal/circuit"
	"vigil-api/gateway/internal/config"
	"vigil-api/gateway/internal/middleware"
	"vigil-api/gateway/internal/proxy"
	"vigil-api/gateway/internal/ratelimit"
	"vigil-api/gateway/internal/verdict"
)

// Downstream bundles what the server needs to talk to one inference service
// resiliently: the HTTP client, its circuit breaker, and its backpressure
// semaphore.
type Downstream struct {
	Name    string
	Client  *proxy.Client
	Breaker *circuit.Breaker
	// Sem is a buffered-channel semaphore capping in-flight requests to this
	// downstream. A nil Sem disables backpressure (used by tests that don't
	// exercise it).
	Sem chan struct{}
}

// Deps are the constructor-injected dependencies of the server. main builds
// these; the server never reaches into globals.
type Deps struct {
	Config  config.Config
	Logger  *slog.Logger
	Auth    *auth.Authenticator
	Limiter *ratelimit.Limiter
	Verdict *verdict.Classifier
	Text    *Downstream
	Image   *Downstream

	// Audit is the append-only decision log (nil disables it — see
	// AUDIT_LOG_PATH in docs/api.md and gateway/internal/audit). When set,
	// moderate appends one record per successful (200) decision and Run
	// drains it on graceful shutdown.
	Audit *audit.Logger

	// ObservabilityMiddleware is inserted into the global chain just above the
	// recovery layer (the Prometheus HTTP middleware, wired at the metrics
	// build step). Empty in tests that don't assert on metrics.
	ObservabilityMiddleware []func(http.Handler) http.Handler
	// MetricsHandler serves GET /metrics when set (wired at the metrics step).
	MetricsHandler http.Handler
	// Recorder receives per-request business metrics (verdicts, in-flight).
	// Defaults to a no-op so the handlers stay decoupled from Prometheus until
	// the metrics step provides a real one.
	Recorder Recorder
}

// Server owns the resolved handler and shutdown parameters.
type Server struct {
	cfg    config.Config
	logger *slog.Logger
	mux    *http.ServeMux
	deps   Deps
}

// New builds the mux, registers routes, and returns a ready Server.
func New(deps Deps) *Server {
	if deps.Recorder == nil {
		deps.Recorder = noopRecorder{}
	}
	s := &Server{
		cfg:    deps.Config,
		logger: deps.Logger,
		mux:    http.NewServeMux(),
		deps:   deps,
	}
	s.routes()
	return s
}

func (s *Server) routes() {
	// Normalized 404/405: the stdlib mux answers plaintext by default, but a
	// JSON API should answer JSON (docs/api.md). The catch-all "/" pattern
	// turns any unmatched path into the normalized 404 envelope, and each
	// known path also registers a method-agnostic fallback pattern answering
	// 405 with an Allow header — the method-specific pattern is strictly more
	// specific, so it wins for the right method and the fallback catches the
	// rest (Go 1.22 mux precedence; no response interception needed).
	s.mux.HandleFunc("/", s.handleNotFound)

	// Public, unauthenticated operational endpoints.
	s.mux.HandleFunc("GET /v1/health", s.handleHealth)
	s.mux.HandleFunc("/v1/health", s.methodNotAllowed(http.MethodGet))
	if s.deps.MetricsHandler != nil {
		s.mux.Handle("GET /metrics", s.deps.MetricsHandler)
		s.mux.HandleFunc("/metrics", s.methodNotAllowed(http.MethodGet))
	}

	// Moderation endpoints: auth + per-key rate limit wrap the business logic.
	// Registered only when their dependencies are present so lightweight unit
	// tests can spin up a server without a full graph.
	if s.deps.Auth != nil && s.deps.Limiter != nil {
		guard := func(h http.HandlerFunc) http.Handler {
			return middleware.Chain(h, s.deps.Auth.Middleware, s.deps.Limiter.Middleware)
		}
		if s.deps.Text != nil {
			s.mux.Handle("POST /v1/moderate/text", guard(s.handleText))
			s.mux.HandleFunc("/v1/moderate/text", s.methodNotAllowed(http.MethodPost))
		}
		if s.deps.Image != nil {
			s.mux.Handle("POST /v1/moderate/image", guard(s.handleImage))
			s.mux.HandleFunc("/v1/moderate/image", s.methodNotAllowed(http.MethodPost))
		}
	}
}

// handleNotFound renders the normalized 404 envelope for any unmatched path.
func (s *Server) handleNotFound(w http.ResponseWriter, r *http.Request) {
	middleware.WriteError(w, r, http.StatusNotFound, middleware.CodeNotFound, "resource not found")
}

// methodNotAllowed renders the normalized 405 envelope with the Allow header
// for a known path hit with the wrong method.
func (s *Server) methodNotAllowed(allow string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Allow", allow)
		middleware.WriteError(w, r, http.StatusMethodNotAllowed, middleware.CodeMethodNotAllow, "method not allowed")
	}
}

// Handler returns the mux wrapped in the global middleware chain. Order
// (outermost first): request-id so everything below can log/propagate it,
// access log to record every request (including recovered panics and metrics),
// the Prometheus HTTP middleware, then recovery nearest the handlers so a panic
// becomes a clean 500 that the outer layers observe.
func (s *Server) Handler() http.Handler {
	mws := []func(http.Handler) http.Handler{
		middleware.RequestID,
		middleware.AccessLog(s.logger),
	}
	mws = append(mws, s.deps.ObservabilityMiddleware...)
	mws = append(mws, middleware.Recovery(s.logger))
	return middleware.Chain(s.mux, mws...)
}

func (s *Server) downstreams() []*Downstream {
	var ds []*Downstream
	if s.deps.Text != nil {
		ds = append(ds, s.deps.Text)
	}
	if s.deps.Image != nil {
		ds = append(ds, s.deps.Image)
	}
	return ds
}

func (s *Server) handleHealth(w http.ResponseWriter, _ *http.Request) {
	circuits := make(map[string]string)
	for _, ds := range s.downstreams() {
		circuits[ds.Name] = ds.Breaker.State().String()
	}
	writeJSON(w, http.StatusOK, healthResponse{Status: "ok", Circuits: circuits})
}

type healthResponse struct {
	Status   string            `json:"status"`
	Circuits map[string]string `json:"circuits"`
}

// Run starts the HTTP server and blocks until ctx is cancelled (signal) or the
// listener fails, then drains in-flight requests within ShutdownTimeout.
//
// Graceful shutdown pattern: a cancelled context (from signal.NotifyContext in
// main) triggers http.Server.Shutdown, which stops accepting new connections
// and waits for active handlers to finish up to the deadline — so a rolling
// deploy or Ctrl-C never severs an in-flight moderation call mid-response.
func (s *Server) Run(ctx context.Context) error {
	httpSrv := &http.Server{
		Addr:    s.cfg.Addr,
		Handler: s.Handler(),
		// ReadHeaderTimeout bounds slow-header (Slowloris) attacks. Read/Write
		// timeouts are left generous: a moderation call may legitimately wait
		// on a downstream retry cycle, and cutting the response short would
		// corrupt it.
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	errCh := make(chan error, 1)
	go func() {
		s.logger.Info("gateway listening", "addr", s.cfg.Addr)
		if err := httpSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
		s.logger.Info("shutdown signal received, draining", "timeout", s.cfg.ShutdownTimeout)
		shutdownCtx, cancel := context.WithTimeout(context.Background(), s.cfg.ShutdownTimeout)
		defer cancel()
		if err := httpSrv.Shutdown(shutdownCtx); err != nil {
			return err
		}
		// Shutdown has returned, so every handler that could call
		// s.deps.Audit.Append has already finished — only now is it safe to
		// close the audit logger, which drains whatever it had queued (see
		// audit.Logger.Close's doc on why ordering matters here).
		if s.deps.Audit != nil {
			s.deps.Audit.Close()
		}
		s.logger.Info("gateway stopped cleanly")
		return nil
	}
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}
