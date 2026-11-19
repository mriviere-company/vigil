// Command gateway is the vigil public API gateway: API-key auth, per-key
// rate limiting, resilient proxying to the Python inference services (retries,
// circuit breaker, backpressure), verdict aggregation, and Prometheus metrics.
//
// This file is wiring only: load config from the environment, construct the
// dependency graph, and run the server with graceful shutdown. All behavior
// lives in internal packages.
package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"net/http"

	"vigil-api/gateway/internal/audit"
	"vigil-api/gateway/internal/auth"
	"vigil-api/gateway/internal/circuit"
	"vigil-api/gateway/internal/config"
	"vigil-api/gateway/internal/metrics"
	"vigil-api/gateway/internal/proxy"
	"vigil-api/gateway/internal/ratelimit"
	"vigil-api/gateway/internal/server"
	"vigil-api/gateway/internal/verdict"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))

	cfg, err := config.Load()
	if err != nil {
		logger.Error("invalid configuration", "error", err)
		os.Exit(1)
	}

	keys, err := auth.ParseKeys(cfg.APIKeysRaw)
	if err != nil {
		logger.Error("invalid API_KEYS", "error", err)
		os.Exit(1)
	}

	m := metrics.New()

	// Audit is opt-in (AUDIT_LOG_PATH empty => nil, disabled — see
	// docs/api.md). A configured-but-unopenable path fails fast at startup
	// rather than silently running without an audit trail, matching how
	// invalid API_KEYS/config already fail fast above.
	var auditLogger *audit.Logger
	if cfg.AuditLogPath != "" {
		auditLogger, err = audit.NewLogger(cfg.AuditLogPath, logger, m)
		if err != nil {
			logger.Error("could not open audit log", "error", err)
			os.Exit(1)
		}
	}

	deps := server.Deps{
		Config:                  cfg,
		Logger:                  logger,
		Auth:                    auth.New(keys),
		Limiter:                 ratelimit.New(ratelimit.DefaultTierLimits(), ratelimit.DefaultTierQuotas()),
		Verdict:                 verdict.New(cfg.RejectThreshold, cfg.FlagThreshold),
		Text:                    newDownstream("text", cfg.TextServiceURL, cfg, m),
		Image:                   newDownstream("image", cfg.ImageServiceURL, cfg, m),
		Audit:                   auditLogger,
		Recorder:                m,
		MetricsHandler:          m.Handler(),
		ObservabilityMiddleware: []func(http.Handler) http.Handler{m.HTTPMiddleware},
	}

	srv := server.New(deps)

	// A cancelled context (SIGINT/SIGTERM) triggers graceful shutdown.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := srv.Run(ctx); err != nil {
		logger.Error("server error", "error", err)
		os.Exit(1)
	}
}

// newDownstream builds one inference-service pipeline: a retrying HTTP client
// and its circuit breaker (wall clock in production), with metrics hooks wired
// for retries and circuit transitions.
func newDownstream(name, baseURL string, cfg config.Config, m *metrics.Metrics) *server.Downstream {
	client := proxy.New(proxy.Options{
		BaseURL:        baseURL,
		ConnectTimeout: cfg.ConnectTimeout,
		TotalTimeout:   cfg.TotalTimeout,
		MaxRetries:     cfg.MaxRetries,
		OnRetry:        func() { m.IncRetry(name) },
	})
	breaker := circuit.New(circuit.Config{
		FailureThreshold:  cfg.CBFailureThreshold,
		OpenTimeout:       cfg.CBOpenTimeout,
		HalfOpenMaxProbes: cfg.CBHalfOpenProbes,
		OnStateChange: func(from, to circuit.State) {
			m.RecordCircuitTransition(name, from.String(), to.String(), circuitGauge(to))
		},
	})
	m.SetCircuitState(name, circuitGauge(circuit.StateClosed)) // publish initial state
	return &server.Downstream{
		Name:    name,
		Client:  client,
		Breaker: breaker,
		Sem:     make(chan struct{}, cfg.MaxInflight),
	}
}

// circuitGauge maps a breaker state to its /metrics gauge value
// (0=closed, 1=half-open, 2=open).
func circuitGauge(s circuit.State) float64 {
	switch s {
	case circuit.StateHalfOpen:
		return 1
	case circuit.StateOpen:
		return 2
	default:
		return 0
	}
}
