package main

import (
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"vigil-api/gateway/internal/audit"
	"vigil-api/gateway/internal/sor"
)

func writeValidLog(t *testing.T, path string, n int) {
	t.Helper()
	logger := slog.New(slog.NewJSONHandler(os.Stderr, nil))
	l, err := audit.NewLogger(path, logger, nil)
	if err != nil {
		t.Fatalf("NewLogger: %v", err)
	}
	for i := 0; i < n; i++ {
		l.Append(audit.Entry{
			Timestamp:  time.Now(),
			RequestID:  "rid",
			APIKeyHash: "keyhash",
			Endpoint:   "/v1/moderate/text",
			SoR: sor.New(sor.Input{
				DecisionID:  "d",
				ContentType: "text",
				RawContent:  []byte("hi"),
				Verdict:     "approved",
			}),
		})
	}
	l.Close()
}

func TestVerifyOKPath(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.jsonl")
	writeValidLog(t, path, 3)

	res, err := verify(path)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if !res.ok || res.n != 3 {
		t.Errorf("res = %+v, want ok=true n=3", res)
	}
}

func TestVerifyEmptyFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.jsonl")
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatalf("write empty file: %v", err)
	}

	res, err := verify(path)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if !res.ok || res.n != 0 {
		t.Errorf("res = %+v, want ok=true n=0", res)
	}
}

func TestVerifyTamperedRecordDetected(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.jsonl")
	writeValidLog(t, path, 3)

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	// Flip one character inside the second record's request_id value — still
	// valid JSON, but the recomputed hash will no longer match record_hash.
	tampered := strings.Replace(string(raw), `"request_id":"rid"`, `"request_id":"rie"`, 1)
	if tampered == string(raw) {
		t.Fatal("test bug: nothing was replaced")
	}
	if err := os.WriteFile(path, []byte(tampered), 0o600); err != nil {
		t.Fatalf("write tampered file: %v", err)
	}

	res, err := verify(path)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if res.ok {
		t.Fatal("expected tamper detection, got ok=true")
	}
	if !strings.Contains(res.message, "record 1") && !strings.Contains(res.message, "seq 1") {
		t.Errorf("message = %q, want it to name the first broken record", res.message)
	}
}

func TestVerifyBrokenPrevHashChain(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.jsonl")
	writeValidLog(t, path, 2)

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
	if len(lines) != 2 {
		t.Fatalf("got %d lines, want 2", len(lines))
	}
	// Swap the two lines so record 2 (seq 2) comes first: its prev_hash
	// ("genesis") won't match anything, and the seq order breaks too.
	swapped := lines[1] + "\n" + lines[0] + "\n"
	if err := os.WriteFile(path, []byte(swapped), 0o600); err != nil {
		t.Fatalf("write swapped file: %v", err)
	}

	res, err := verify(path)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if res.ok {
		t.Fatal("expected chain break to be detected, got ok=true")
	}
}

func TestVerifyMissingFile(t *testing.T) {
	if _, err := verify(filepath.Join(t.TempDir(), "does-not-exist.jsonl")); err == nil {
		t.Fatal("expected an error for a missing file")
	}
}

// dropLeadingLines rewrites path with its first n lines removed — simulating
// an attacker deleting the oldest records ("prefix deletion").
func dropLeadingLines(t *testing.T, path string, n int) {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
	if len(lines) <= n {
		t.Fatalf("test bug: file has %d lines, cannot drop %d", len(lines), n)
	}
	out := strings.Join(lines[n:], "\n") + "\n"
	if err := os.WriteFile(path, []byte(out), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
}

// TestVerifyPrefixDeletionFirstLineDetected: deleting record 1 leaves a file
// whose remaining records are individually valid and mutually chained, but
// whose first record is not anchored to genesis. verify must reject it.
func TestVerifyPrefixDeletionFirstLineDetected(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.jsonl")
	writeValidLog(t, path, 3)
	dropLeadingLines(t, path, 1)

	res, err := verify(path)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if res.ok {
		t.Fatal("expected prefix deletion (first record removed) to be detected, got ok=true")
	}
	if !strings.Contains(res.message, "genesis") {
		t.Errorf("message = %q, want it to name the missing genesis anchor", res.message)
	}
}

// TestVerifyPrefixDeletionFirstTwoLinesDetected: same, with the two oldest
// records removed.
func TestVerifyPrefixDeletionFirstTwoLinesDetected(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.jsonl")
	writeValidLog(t, path, 3)
	dropLeadingLines(t, path, 2)

	res, err := verify(path)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if res.ok {
		t.Fatal("expected prefix deletion (first two records removed) to be detected, got ok=true")
	}
	if !strings.Contains(res.message, "genesis") {
		t.Errorf("message = %q, want it to name the missing genesis anchor", res.message)
	}
}

// TestVerifyInteriorDeletionDetected: removing a middle record breaks the
// next record's prev_hash link.
func TestVerifyInteriorDeletionDetected(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.jsonl")
	writeValidLog(t, path, 3)

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
	if len(lines) != 3 {
		t.Fatalf("got %d lines, want 3", len(lines))
	}
	// Drop the middle record (seq 2).
	out := lines[0] + "\n" + lines[2] + "\n"
	if err := os.WriteFile(path, []byte(out), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	res, err := verify(path)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if res.ok {
		t.Fatal("expected interior deletion to be detected, got ok=true")
	}
}
