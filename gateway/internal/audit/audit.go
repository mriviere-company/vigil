// Package audit is vigil's tamper-evident, append-only decision log.
//
// Hash-chain pattern (interview material, per repo standards) — each record
// carries record_hash = SHA-256(canonical form of every other field,
// including the previous record's hash via prev_hash). A verifier
// (cmd/audit-verify) needs no shared secret or external ledger, just the
// file itself. The precise guarantee: modifying any hashed field, reordering
// records, deleting an interior record, or deleting a prefix (the oldest
// records — caught because the first record must be the seq-1/"genesis"
// anchor) all break a verifiable link. What the file alone can NOT prove is
// tail truncation: cutting records off the END leaves a shorter but
// internally valid chain, and no single-file scheme can distinguish "the log
// simply ends here" from "the newest records were removed." An operator who
// needs that property records the latest record_hash out of band (a
// high-water-mark — even a line in a ticket) and compares it to the file's
// last record. This is the same idea as a blockchain's block-linking without
// any of the distributed-consensus machinery — appropriate here because
// there is exactly one writer (this process) and the threat model is "did
// anyone edit the file after the fact," not "do multiple parties agree on
// order." Note also that the hash covers each record's canonical form, not
// its literal bytes on disk: cosmetically reformatting a line's JSON (or
// adding unknown keys) does not break verification — the guarantee is
// meaningful-field integrity, not byte-for-byte line integrity.
//
// Data-minimization — a record never contains raw request content, only its
// sha256+size (via the embedded SoR). There is deliberately no configuration
// option to log raw content: an audit log that could retain moderated text or
// images would itself become a data-retention liability (the thing DSA
// transparency tooling should reduce, not add).
//
// Concurrency — one bounded channel (Entry) feeds a single writer goroutine
// that owns the running seq counter and prev_hash, so record ordering and
// chain-linking need no lock: only one goroutine ever assigns them. Producers
// call Append, a non-blocking send; when the channel is full, the record is
// dropped (counted and logged) rather than blocking the request path —
// availability of the moderation API wins over completeness of the audit
// trail, and the dropped-record counter (gateway_audit_dropped_total) makes
// that trade-off's cost visible rather than silent.
//
// Rotation is out of scope. Because the log is opened once per process and
// appended to by inode (not by re-resolving the path), an external `mv` of
// the file is transparent to a running process — writes keep landing in the
// moved file, so no signal handling is needed for that step. To actually
// start a fresh file at the configured path, move it and then restart the
// process: a new Logger always begins a fresh chain (seq 1, prev_hash
// "genesis"), so each file is independently self-verifying; there is no
// cross-file link and cmd/audit-verify does not attempt one.
package audit

import (
	"bufio"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"sync"
	"time"

	"vigil-api/gateway/internal/sor"
)

// queueCapacity bounds how many Entry values may be queued before Append
// starts dropping. 1024 comfortably absorbs a burst without letting a slow
// disk turn into unbounded memory growth on the gateway.
const queueCapacity = 1024

// Entry is what a caller submits to be appended. The Logger fills in Seq,
// PrevHash, and RecordHash before writing — a caller never computes those.
type Entry struct {
	Timestamp  time.Time
	RequestID  string
	APIKeyHash string // SHA-256(hex) of the API key — never the raw key
	Endpoint   string
	SoR        sor.SoR
}

// MetricsRecorder is the small surface the audit log needs from the metrics
// package (gateway_audit_records_total / gateway_audit_dropped_total /
// gateway_audit_write_errors_total), mirroring how server.Recorder decouples
// other packages from Prometheus.
type MetricsRecorder interface {
	IncAuditRecord()
	IncAuditDropped()
	// IncAuditWriteError counts a record lost on the write path (hash,
	// marshal, write, or flush failure — e.g. disk full), as opposed to
	// IncAuditDropped's queue-full case. Without it, a failing disk would
	// lose records with only a log line while the dropped counter stayed 0.
	IncAuditWriteError()
}

type noopMetrics struct{}

func (noopMetrics) IncAuditRecord()     {}
func (noopMetrics) IncAuditDropped()    {}
func (noopMetrics) IncAuditWriteError() {}

// Logger appends Entry values to a JSONL file as a hash-chained Record per
// line. Build with NewLogger; do not construct directly outside tests.
type Logger struct {
	ch        chan Entry
	done      chan struct{} // closed once the writer goroutine has exited
	closeOnce sync.Once
	logger    *slog.Logger
	metrics   MetricsRecorder
}

// NewLogger opens path for appending (creating it if absent) and starts the
// single writer goroutine. metrics may be nil (defaults to a no-op).
func NewLogger(path string, logger *slog.Logger, metrics MetricsRecorder) (*Logger, error) {
	if metrics == nil {
		metrics = noopMetrics{}
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, fmt.Errorf("audit: open log file %s: %w", path, err)
	}

	l := &Logger{
		ch:      make(chan Entry, queueCapacity),
		done:    make(chan struct{}),
		logger:  logger,
		metrics: metrics,
	}
	go l.run(bufio.NewWriter(f), f.Close)
	return l, nil
}

// Append enqueues an entry for the writer goroutine. Non-blocking: if the
// bounded channel is full, the record is dropped — see the package doc's
// availability-over-completeness trade-off.
//
// Append must not be called after Close returns (or concurrently with a call
// to Close): the graceful-shutdown pattern is to stop accepting new requests
// first (http.Server.Shutdown already blocks until every in-flight handler —
// the only callers of Append — has returned) and only then call Close, so in
// practice no Append ever races a Close. See server.Run.
func (l *Logger) Append(e Entry) {
	select {
	case l.ch <- e:
	default:
		l.metrics.IncAuditDropped()
		l.logger.Error("audit log dropped record: queue full", "request_id", e.RequestID, "endpoint", e.Endpoint)
	}
}

// Close stops the writer goroutine after it has flushed every already-queued
// entry (the graceful-shutdown drain) and closes the underlying file. It
// blocks until that drain completes. Safe to call once; a second call is a
// no-op wait on the same completion.
func (l *Logger) Close() {
	l.closeOnce.Do(func() { close(l.ch) })
	<-l.done
}

// run is the single writer goroutine: it owns seq/prevHash (no lock needed —
// nothing else ever touches them) and writes one JSON line per Entry,
// flushing after every record (no fsync — a crash can still lose the last
// buffered-but-unflushed OS write, an accepted trade-off for not paying a
// disk-sync cost on the request-serving path; see the package doc).
//
// w and closeUnderlying are parameterized (rather than run opening the file
// itself) so tests can inject a small-buffer/failing writer to exercise the
// error paths below deterministically; NewLogger supplies the real
// bufio.Writer(file) and file.Close in production.
func (l *Logger) run(w *bufio.Writer, closeUnderlying func() error) {
	defer close(l.done)
	if closeUnderlying != nil {
		defer func() { _ = closeUnderlying() }()
	}

	var seq uint64
	prevHash := "genesis"

	for e := range l.ch {
		seq++
		body := recordBody{
			Seq:        seq,
			PrevHash:   prevHash,
			Timestamp:  e.Timestamp.UTC().Format(time.RFC3339),
			RequestID:  e.RequestID,
			APIKeyHash: e.APIKeyHash,
			Endpoint:   e.Endpoint,
			SoR:        e.SoR,
		}
		hash, err := canonicalHash(body)
		if err != nil {
			// Defensive: not reachable via the documented request path today
			// (scores arrive over JSON, which cannot itself encode NaN/Inf),
			// but a future field could admit a non-JSON-safe value. Do not
			// advance seq/prevHash for a record that was never written, so
			// the chain stays gapless for everything that was.
			seq--
			l.metrics.IncAuditWriteError()
			l.logger.Error("audit: failed to hash record, dropping", "error", err, "request_id", e.RequestID)
			continue
		}
		line, err := json.Marshal(Record{recordBody: body, RecordHash: hash})
		if err != nil {
			// Defensive/effectively unreachable: canonicalHash above already
			// marshaled the same fields successfully; adding the (always
			// valid) hash string cannot newly fail. Guarded for symmetry.
			seq--
			l.metrics.IncAuditWriteError()
			l.logger.Error("audit: failed to marshal record, dropping", "error", err, "request_id", e.RequestID)
			continue
		}
		line = append(line, '\n')

		if _, err := w.Write(line); err != nil {
			seq--
			l.metrics.IncAuditWriteError()
			l.logger.Error("audit: write failed, dropping", "error", err, "request_id", e.RequestID)
			continue
		}
		if err := w.Flush(); err != nil {
			seq--
			l.metrics.IncAuditWriteError()
			l.logger.Error("audit: flush failed, dropping", "error", err, "request_id", e.RequestID)
			continue
		}
		prevHash = hash
		l.metrics.IncAuditRecord()
	}
}
