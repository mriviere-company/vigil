// Package config loads all gateway tunables from the environment (12-factor):
// every knob has a sane local default so `go run ./cmd/gateway` works with zero
// setup, and nothing is read from the environment outside Load — the resulting
// Config is passed explicitly to every constructor, so there are no mutable
// globals.
package config

import (
	"fmt"
	"os"
	"strconv"
	"time"
)

// Backpressure modes: how the gateway sheds load when a downstream's in-flight
// semaphore is saturated.
const (
	BackpressureReject  = "reject"  // 503 overloaded (default)
	BackpressurePending = "pending" // 202 pending body
)

// Config is the fully-resolved gateway configuration.
type Config struct {
	Addr            string
	ShutdownTimeout time.Duration

	APIKeysRaw string // "key1:free,key2:pro"

	TextServiceURL  string
	ImageServiceURL string

	ConnectTimeout time.Duration
	TotalTimeout   time.Duration
	MaxRetries     int

	CBFailureThreshold int
	CBOpenTimeout      time.Duration
	CBHalfOpenProbes   int

	RejectThreshold float64
	FlagThreshold   float64

	MaxInflight      int
	BackpressureMode string
	RetryAfter       int // seconds, for the 202 degraded-mode body

	// AuditLogPath enables the append-only audit log when non-empty (default
	// disabled — see gateway/internal/audit and docs/api.md).
	AuditLogPath string
	// SORIncludeInResponse attaches the statement-of-reasons artifact to the
	// public moderation response under "statement_of_reasons" when true.
	// Default false: the public contract stays byte-stable unless an
	// integrator opts in (see docs/api.md).
	SORIncludeInResponse bool
}

// Load reads the environment and returns a validated Config.
func Load() (Config, error) {
	c := Config{
		Addr:               getEnv("GATEWAY_ADDR", ":8080"),
		ShutdownTimeout:    getEnvDuration("SHUTDOWN_TIMEOUT", 10*time.Second),
		APIKeysRaw:         getEnv("API_KEYS", "dev-free:free,dev-pro:pro"),
		TextServiceURL:     getEnv("TEXT_SERVICE_URL", "http://localhost:8001"),
		ImageServiceURL:    getEnv("IMAGE_SERVICE_URL", "http://localhost:8002"),
		ConnectTimeout:     getEnvDuration("CONNECT_TIMEOUT", 500*time.Millisecond),
		TotalTimeout:       getEnvDuration("TOTAL_TIMEOUT", 2*time.Second),
		MaxRetries:         getEnvInt("MAX_RETRIES", 2),
		CBFailureThreshold: getEnvInt("CB_FAILURE_THRESHOLD", 5),
		CBOpenTimeout:      getEnvDuration("CB_OPEN_TIMEOUT", 10*time.Second),
		CBHalfOpenProbes:   getEnvInt("CB_HALFOPEN_PROBES", 1),
		RejectThreshold:    getEnvFloat("REJECT_THRESHOLD", 0.85),
		FlagThreshold:      getEnvFloat("FLAG_THRESHOLD", 0.5),
		MaxInflight:        getEnvInt("MAX_INFLIGHT", 32),
		BackpressureMode:   getEnv("BACKPRESSURE_MODE", BackpressureReject),
		RetryAfter:         getEnvInt("RETRY_AFTER", 30),

		AuditLogPath:         getEnv("AUDIT_LOG_PATH", ""),
		SORIncludeInResponse: getEnvBool("SOR_INCLUDE_IN_RESPONSE", false),
	}
	if err := c.validate(); err != nil {
		return Config{}, err
	}
	return c, nil
}

func (c Config) validate() error {
	if c.BackpressureMode != BackpressureReject && c.BackpressureMode != BackpressurePending {
		return fmt.Errorf("invalid BACKPRESSURE_MODE %q (want %q or %q)",
			c.BackpressureMode, BackpressureReject, BackpressurePending)
	}
	if c.CBFailureThreshold < 1 {
		return fmt.Errorf("CB_FAILURE_THRESHOLD must be >= 1, got %d", c.CBFailureThreshold)
	}
	if c.CBHalfOpenProbes < 1 {
		return fmt.Errorf("CB_HALFOPEN_PROBES must be >= 1, got %d", c.CBHalfOpenProbes)
	}
	if c.MaxInflight < 1 {
		return fmt.Errorf("MAX_INFLIGHT must be >= 1, got %d", c.MaxInflight)
	}
	if c.MaxRetries < 0 {
		return fmt.Errorf("MAX_RETRIES must be >= 0, got %d", c.MaxRetries)
	}
	if c.FlagThreshold > c.RejectThreshold {
		return fmt.Errorf("FLAG_THRESHOLD (%v) must not exceed REJECT_THRESHOLD (%v)",
			c.FlagThreshold, c.RejectThreshold)
	}
	return nil
}

func getEnv(key, def string) string {
	if v, ok := os.LookupEnv(key); ok {
		return v
	}
	return def
}

func getEnvInt(key string, def int) int {
	if v, ok := os.LookupEnv(key); ok {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}

func getEnvFloat(key string, def float64) float64 {
	if v, ok := os.LookupEnv(key); ok {
		if f, err := strconv.ParseFloat(v, 64); err == nil {
			return f
		}
	}
	return def
}

func getEnvDuration(key string, def time.Duration) time.Duration {
	if v, ok := os.LookupEnv(key); ok {
		if d, err := time.ParseDuration(v); err == nil {
			return d
		}
	}
	return def
}

func getEnvBool(key string, def bool) bool {
	if v, ok := os.LookupEnv(key); ok {
		if b, err := strconv.ParseBool(v); err == nil {
			return b
		}
	}
	return def
}
