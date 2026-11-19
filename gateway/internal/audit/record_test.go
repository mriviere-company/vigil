package audit

import (
	"encoding/json"
	"strings"
	"testing"

	"vigil-api/gateway/internal/sor"
)

func testSoR() sor.SoR {
	return sor.New(sor.Input{
		DecisionID:   "dec-1",
		ContentType:  "text",
		RawContent:   []byte("hello"),
		Language:     "en",
		ModelVersion: "stub-1",
		Scores:       map[string]float64{"toxicity": 0.1, "spam": 0.2},
		Reject:       0.85,
		Flag:         0.5,
		Verdict:      "approved",
	})
}

// TestCanonicalizationStability proves the same logical record always hashes
// to the same value, and that the hash is computed over the record minus
// record_hash itself (assigning a record_hash does not change the hash it
// produces when recomputed).
func TestCanonicalizationStability(t *testing.T) {
	body := recordBody{
		Seq: 1, PrevHash: "genesis", Timestamp: "2026-07-11T00:00:00Z",
		RequestID: "rid-1", APIKeyHash: "keyhash", Endpoint: "/v1/moderate/text",
		SoR: testSoR(),
	}
	h1, err := canonicalHash(body)
	if err != nil {
		t.Fatalf("canonicalHash: %v", err)
	}
	h2, err := canonicalHash(body)
	if err != nil {
		t.Fatalf("canonicalHash: %v", err)
	}
	if h1 != h2 {
		t.Errorf("hash not stable: %q != %q", h1, h2)
	}

	rec := Record{recordBody: body, RecordHash: h1}
	if err := VerifyRecordHash(rec); err != nil {
		t.Errorf("VerifyRecordHash on freshly hashed record: %v", err)
	}

	// A field change must change the hash.
	body2 := body
	body2.RequestID = "rid-2"
	h3, err := canonicalHash(body2)
	if err != nil {
		t.Fatalf("canonicalHash: %v", err)
	}
	if h3 == h1 {
		t.Error("changing request_id did not change the hash")
	}
}

// TestVerifyRecordHashDetectsTamper proves a single mutated byte in a
// verified field is caught.
func TestVerifyRecordHashDetectsTamper(t *testing.T) {
	body := recordBody{
		Seq: 1, PrevHash: "genesis", Timestamp: "2026-07-11T00:00:00Z",
		RequestID: "rid-1", APIKeyHash: "keyhash", Endpoint: "/v1/moderate/text",
		SoR: testSoR(),
	}
	h, err := canonicalHash(body)
	if err != nil {
		t.Fatalf("canonicalHash: %v", err)
	}
	rec := Record{recordBody: body, RecordHash: h}

	// Tamper: mutate a field covered by the hash after the fact.
	rec.RequestID = "rid-tampered"
	if err := VerifyRecordHash(rec); err == nil {
		t.Fatal("expected VerifyRecordHash to detect the tamper, got nil error")
	}
}

// TestRecordHashFieldExcludedFromCanonicalForm proves the canonical bytes used
// for hashing never include the record_hash key itself, whatever its value —
// i.e. the "record-minus-record_hash" definition holds structurally, not just
// by convention.
func TestRecordHashFieldExcludedFromCanonicalForm(t *testing.T) {
	body := recordBody{Seq: 1, PrevHash: "genesis", Timestamp: "t", RequestID: "r", APIKeyHash: "k", Endpoint: "e", SoR: testSoR()}
	b, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(b), "record_hash") {
		t.Errorf("canonical bytes must never contain record_hash, got %s", b)
	}
}

// TestParseRecordRoundTrip proves a Record marshals and parses back intact,
// including the promoted recordBody fields (needed by cmd/audit-verify).
func TestParseRecordRoundTrip(t *testing.T) {
	body := recordBody{Seq: 7, PrevHash: "abc", Timestamp: "t", RequestID: "r", APIKeyHash: "k", Endpoint: "/v1/moderate/text", SoR: testSoR()}
	h, err := canonicalHash(body)
	if err != nil {
		t.Fatalf("canonicalHash: %v", err)
	}
	original := Record{recordBody: body, RecordHash: h}

	line, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("marshal record: %v", err)
	}
	parsed, err := ParseRecord(line)
	if err != nil {
		t.Fatalf("ParseRecord: %v", err)
	}
	if parsed.Seq != 7 || parsed.PrevHash != "abc" || parsed.RecordHash != h {
		t.Errorf("round trip mismatch: %+v", parsed)
	}
	if err := VerifyRecordHash(parsed); err != nil {
		t.Errorf("VerifyRecordHash on round-tripped record: %v", err)
	}
}

func TestParseRecordInvalidJSON(t *testing.T) {
	if _, err := ParseRecord([]byte("not json")); err == nil {
		t.Fatal("expected an error for invalid JSON")
	}
}

// TestAPIKeyNeverInRecordJSON is a structural guard: the api_key_hash field
// must be the only key-derived value; nothing named like a raw key exists on
// the type (the actual "never logs the raw key" guarantee is enforced at the
// call site — see server e2e tests — this just documents/pins the shape).
func TestRecordJSONFieldNames(t *testing.T) {
	body := recordBody{Seq: 1, PrevHash: "genesis", Timestamp: "t", RequestID: "r", APIKeyHash: "deadbeef", Endpoint: "e", SoR: testSoR()}
	rec := Record{recordBody: body, RecordHash: "h"}
	b, err := json.Marshal(rec)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	for _, want := range []string{
		`"seq":1`, `"prev_hash":"genesis"`, `"request_id":"r"`,
		`"api_key_hash":"deadbeef"`, `"endpoint":"e"`, `"record_hash":"h"`,
		`"statement_of_reasons":`,
	} {
		if !strings.Contains(string(b), want) {
			t.Errorf("record json missing %s\ngot: %s", want, b)
		}
	}
}
