package config

import (
	"testing"
	"time"
)

func TestLoadDefaults(t *testing.T) {
	// A malformed value must fall back to the default rather than fail Load.
	t.Setenv("MAX_RETRIES", "not-a-number")
	t.Setenv("CONNECT_TIMEOUT", "garbage")

	c, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if c.Addr != ":8080" {
		t.Errorf("Addr = %q, want :8080", c.Addr)
	}
	if c.CBFailureThreshold != 5 {
		t.Errorf("CBFailureThreshold = %d, want 5", c.CBFailureThreshold)
	}
	if c.RejectThreshold != 0.85 {
		t.Errorf("RejectThreshold = %v, want 0.85", c.RejectThreshold)
	}
	if c.MaxRetries != 2 {
		t.Errorf("MaxRetries = %d, want 2 (fallback on bad value)", c.MaxRetries)
	}
	if c.ConnectTimeout != 500*time.Millisecond {
		t.Errorf("ConnectTimeout = %v, want 500ms (fallback)", c.ConnectTimeout)
	}
	if c.BackpressureMode != BackpressureReject {
		t.Errorf("BackpressureMode = %q, want reject", c.BackpressureMode)
	}
}

func TestLoadOverridesAndParsing(t *testing.T) {
	t.Setenv("GATEWAY_ADDR", ":9999")
	t.Setenv("CONNECT_TIMEOUT", "250ms")
	t.Setenv("TOTAL_TIMEOUT", "3s")
	t.Setenv("CB_OPEN_TIMEOUT", "5s")
	t.Setenv("MAX_INFLIGHT", "8")
	t.Setenv("BACKPRESSURE_MODE", "pending")
	t.Setenv("RETRY_AFTER", "45")

	c, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if c.Addr != ":9999" {
		t.Errorf("Addr = %q", c.Addr)
	}
	if c.ConnectTimeout != 250*time.Millisecond {
		t.Errorf("ConnectTimeout = %v", c.ConnectTimeout)
	}
	if c.TotalTimeout != 3*time.Second {
		t.Errorf("TotalTimeout = %v", c.TotalTimeout)
	}
	if c.BackpressureMode != BackpressurePending {
		t.Errorf("BackpressureMode = %q", c.BackpressureMode)
	}
	if c.RetryAfter != 45 {
		t.Errorf("RetryAfter = %d", c.RetryAfter)
	}
}

func TestLoadRejectsInvalid(t *testing.T) {
	t.Setenv("BACKPRESSURE_MODE", "explode")
	if _, err := Load(); err == nil {
		t.Fatal("expected error for invalid BACKPRESSURE_MODE")
	}
}

func TestLoadRejectsFlagAboveReject(t *testing.T) {
	t.Setenv("FLAG_THRESHOLD", "0.9")
	t.Setenv("REJECT_THRESHOLD", "0.85")
	if _, err := Load(); err == nil {
		t.Fatal("expected error when flag threshold exceeds reject threshold")
	}
}

func TestLoadAuditAndSORDefaultOff(t *testing.T) {
	c, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if c.AuditLogPath != "" {
		t.Errorf("AuditLogPath = %q, want empty (disabled) by default", c.AuditLogPath)
	}
	if c.SORIncludeInResponse {
		t.Error("SORIncludeInResponse = true, want false by default (byte-stable public contract)")
	}
}

func TestLoadAuditAndSOROverrides(t *testing.T) {
	t.Setenv("AUDIT_LOG_PATH", "/tmp/audit.jsonl")
	t.Setenv("SOR_INCLUDE_IN_RESPONSE", "true")

	c, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if c.AuditLogPath != "/tmp/audit.jsonl" {
		t.Errorf("AuditLogPath = %q", c.AuditLogPath)
	}
	if !c.SORIncludeInResponse {
		t.Error("SORIncludeInResponse = false, want true")
	}
}

func TestLoadSORIncludeInResponseMalformedFallsBackToDefault(t *testing.T) {
	t.Setenv("SOR_INCLUDE_IN_RESPONSE", "not-a-bool")
	c, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if c.SORIncludeInResponse {
		t.Error("SORIncludeInResponse = true, want false (fallback on malformed value)")
	}
}
