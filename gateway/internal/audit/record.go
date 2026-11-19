package audit

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"

	"vigil-api/gateway/internal/sor"
)

// recordBody holds every field that participates in a Record's hash. It is
// embedded (not duplicated) into Record specifically so the canonical form
// used to compute record_hash — json.Marshal of a recordBody value alone —
// can never drift from Record's own JSON shape minus record_hash: there is
// only one field list, declared once, here.
type recordBody struct {
	// Seq is the record's 1-based position within this log file (monotonic,
	// no gaps; resets to 1 only when a process starts a fresh chain — see the
	// package doc's rotation note).
	Seq uint64 `json:"seq"`
	// PrevHash is the previous record's RecordHash, or "genesis" for the
	// first record written by a given process (see the package doc).
	PrevHash string `json:"prev_hash"`
	// Timestamp is RFC3339, UTC — the same instant used to build the SoR
	// below, not the moment the writer goroutine got around to it.
	Timestamp string `json:"timestamp"`
	// RequestID correlates this record with the gateway's access log and the
	// X-Request-ID propagated to the inference service.
	RequestID string `json:"request_id"`
	// APIKeyHash is SHA-256(hex) of the caller's API key — never the key
	// itself. See the package doc: this is a hard invariant, not a config
	// option.
	APIKeyHash string `json:"api_key_hash"`
	// Endpoint is the request path, e.g. "/v1/moderate/text".
	Endpoint string `json:"endpoint"`
	// SoR is the full statement-of-reasons artifact for this decision.
	SoR sor.SoR `json:"statement_of_reasons"`
}

// Record is one append-only audit-log entry: one JSON line per moderation
// decision. See the package doc for the hash-chain pattern and canonicalHash
// for exactly what "canonical form" means.
type Record struct {
	recordBody
	// RecordHash is hex(SHA-256(canonical form of recordBody)) — see
	// canonicalHash. It is always the last JSON key (recordBody's fields are
	// promoted ahead of it in encoding/json's declaration-order output).
	RecordHash string `json:"record_hash"`
}

// canonicalHash returns the hex SHA-256 digest of a record's canonical form:
// the JSON encoding (via encoding/json) of its recordBody alone.
//
// Determinism relies on two stdlib encoding/json behaviors that this project
// treats as load-bearing and documents rather than assumes:
//  1. struct fields are emitted in declaration order (recordBody's field
//     order above is therefore part of the canonical form and must not be
//     reordered without treating that as a breaking change to existing
//     hashes);
//  2. map keys (sor.Detection.Scores is the one map-typed field reachable
//     here) are emitted in sorted, lexicographic key order — so two records
//     built from maps populated in different insertion orders still hash
//     identically.
//
// record_hash is structurally absent from recordBody (not present-but-zeroed)
// so there is no "empty string vs. absent" ambiguity in what "minus
// record_hash" means.
func canonicalHash(body recordBody) (string, error) {
	b, err := json.Marshal(body)
	if err != nil {
		return "", fmt.Errorf("audit: canonicalize record: %w", err)
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:]), nil
}

// ParseRecord decodes one JSONL line into a Record.
func ParseRecord(line []byte) (Record, error) {
	var r Record
	if err := json.Unmarshal(line, &r); err != nil {
		return Record{}, fmt.Errorf("audit: decode record: %w", err)
	}
	return r, nil
}

// VerifyRecordHash recomputes r's canonical hash and compares it against
// r.RecordHash, returning a descriptive error on mismatch. This is the
// tamper-detection primitive: flipping any byte covered by the hash (which is
// everything except record_hash itself) makes this fail.
func VerifyRecordHash(r Record) error {
	want, err := canonicalHash(r.recordBody)
	if err != nil {
		return err
	}
	if want != r.RecordHash {
		return fmt.Errorf("record_hash mismatch: recomputed %s, stored %s", want, r.RecordHash)
	}
	return nil
}
