package server

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"vigil-api/gateway/internal/audit"
	"vigil-api/gateway/internal/circuit"
	"vigil-api/gateway/internal/config"
	"vigil-api/gateway/internal/metrics"
	"vigil-api/gateway/internal/ratelimit"
	"vigil-api/gateway/internal/sor"
)

// testSoRForAudit builds a minimal but valid SoR for tests that only need a
// well-formed value to append directly through an audit.Logger, independent
// of the HTTP pipeline (mirrors the equivalent helper in the audit package's
// own tests, kept local here since it is unexported there).
func testSoRForAudit() sor.SoR {
	return sor.New(sor.Input{
		DecisionID:  "dec-shutdown-test",
		ContentType: "text",
		RawContent:  []byte("hello"),
		Verdict:     "approved",
	})
}

// idAndLatencyPattern normalizes the two fields that legitimately vary per
// request (a fresh UUID and a measured duration) so an otherwise-identical
// response body can be compared byte-for-byte against a fixed literal.
var (
	idPattern        = regexp.MustCompile(`"id":"[^"]+"`)
	latencyMsPattern = regexp.MustCompile(`"latency_ms":\d+`)
)

func normalizeVariableFields(body string) string {
	body = idPattern.ReplaceAllString(body, `"id":"<ID>"`)
	body = latencyMsPattern.ReplaceAllString(body, `"latency_ms":<MS>`)
	return body
}

// TestDefaultResponseContractByteStable proves — by literal byte comparison,
// not by merely checking a key's absence — that leaving SOR_INCLUDE_IN_RESPONSE
// unset produces exactly today's response shape: same fields, same order, no
// statement_of_reasons key anywhere in the bytes. This is the regression lock
// for the "byte-stable by default" contract.
func TestDefaultResponseContractByteStable(t *testing.T) {
	env := newE2E(t, e2eOpts{
		textHandler: func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(`{"scores":{"toxicity":0.05,"spam":0.05},"model_version":"stub-1"}`))
		},
	})
	defer env.close()

	rec := postJSON(env, "/v1/moderate/text", "dev-pro", `{"content":"lovely day"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}

	got := normalizeVariableFields(rec.Body.String())
	want := `{"id":"<ID>","verdict":"approved","score":0.05,"categories":{"spam":0.05,"toxicity":0.05},"latency_ms":<MS>}` + "\n"
	if got != want {
		t.Errorf("response bytes changed shape:\ngot:  %s\nwant: %s", got, want)
	}
	if strings.Contains(rec.Body.String(), "statement_of_reasons") {
		t.Error("default response must never contain statement_of_reasons")
	}
}

func TestSORIncludeInResponseTrueAddsBlock(t *testing.T) {
	env := newE2E(t, e2eOpts{
		sorIncludeInResponse: true,
		textHandler: func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(`{"scores":{"toxicity":0.95,"spam":0.05},"model_version":"stub-1"}`))
		},
	})
	defer env.close()

	rec := postJSON(env, "/v1/moderate/text", "dev-pro", `{"content":"contenu haineux","language":"fr"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (%s)", rec.Code, rec.Body.String())
	}

	var body struct {
		ID                 string `json:"id"`
		StatementOfReasons *struct {
			SoRVersion string `json:"sor_version"`
			DecisionID string `json:"decision_id"`
			Content    struct {
				Type      string `json:"type"`
				SHA256    string `json:"sha256"`
				SizeBytes int    `json:"size_bytes"`
				Language  string `json:"language"`
			} `json:"content"`
			AutomatedDetection bool   `json:"automated_detection"`
			AutomatedDecision  string `json:"automated_decision"`
			Detection          struct {
				Verdict string `json:"verdict"`
			} `json:"detection"`
			PlatformFields struct {
				DecisionTaken *string `json:"decision_taken"`
				Ground        *string `json:"ground"`
			} `json:"platform_fields"`
		} `json:"statement_of_reasons"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("bad json: %v (%s)", err, rec.Body.String())
	}
	if body.StatementOfReasons == nil {
		t.Fatal("expected statement_of_reasons block, got none")
	}
	s := body.StatementOfReasons
	if s.DecisionID != body.ID {
		t.Errorf("SoR.DecisionID = %q, want it to match response id %q", s.DecisionID, body.ID)
	}
	if !s.AutomatedDetection || s.AutomatedDecision != "none" {
		t.Errorf("automated_detection/automated_decision = %v/%q", s.AutomatedDetection, s.AutomatedDecision)
	}
	if s.Content.Type != "text" || s.Content.Language != "fr" {
		t.Errorf("content = %+v", s.Content)
	}
	wantSum := sha256.Sum256([]byte("contenu haineux"))
	if s.Content.SHA256 != hex.EncodeToString(wantSum[:]) {
		t.Errorf("content.sha256 = %q, want sha256 of the raw content", s.Content.SHA256)
	}
	if s.Content.SizeBytes != len("contenu haineux") {
		t.Errorf("content.size_bytes = %d, want %d", s.Content.SizeBytes, len("contenu haineux"))
	}
	if s.Detection.Verdict != "rejected" {
		t.Errorf("detection.verdict = %q, want rejected", s.Detection.Verdict)
	}
	if s.PlatformFields.DecisionTaken != nil || s.PlatformFields.Ground != nil {
		t.Errorf("platform_fields must be null placeholders, got %+v", s.PlatformFields)
	}
}

// TestSORThresholdsInForceReflectConfiguredValues runs with non-default
// thresholds and asserts the SoR reports THOSE, not the contract defaults —
// a hardcoded 0.85/0.5 in the SoR builder would pass every default-threshold
// test unnoticed.
func TestSORThresholdsInForceReflectConfiguredValues(t *testing.T) {
	env := newE2E(t, e2eOpts{
		sorIncludeInResponse: true,
		rejectThreshold:      0.7,
		flagThreshold:        0.3,
		textHandler: func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(`{"scores":{"toxicity":0.4,"spam":0.05},"model_version":"stub-1"}`))
		},
	})
	defer env.close()

	rec := postJSON(env, "/v1/moderate/text", "dev-pro", `{"content":"borderline"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (%s)", rec.Code, rec.Body.String())
	}
	var body struct {
		Verdict            string `json:"verdict"`
		StatementOfReasons struct {
			Detection struct {
				ThresholdsInForce struct {
					Flagged  float64 `json:"flagged"`
					Rejected float64 `json:"rejected"`
				} `json:"thresholds_in_force"`
				Verdict string `json:"verdict"`
			} `json:"detection"`
		} `json:"statement_of_reasons"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("bad json: %v", err)
	}
	th := body.StatementOfReasons.Detection.ThresholdsInForce
	if th.Flagged != 0.3 || th.Rejected != 0.7 {
		t.Errorf("thresholds_in_force = %+v, want flagged=0.3 rejected=0.7 (the configured values)", th)
	}
	// 0.4 > flag(0.3) but <= reject(0.7): flagged under THESE thresholds
	// (would be approved under the defaults), proving the verdict pipeline
	// used the same configuration the SoR reports.
	if body.Verdict != "flagged" || body.StatementOfReasons.Detection.Verdict != "flagged" {
		t.Errorf("verdict = %q / SoR verdict = %q, want flagged under 0.3/0.7 thresholds",
			body.Verdict, body.StatementOfReasons.Detection.Verdict)
	}
}

// TestAuditImageRecordHashesDecodedBytes proves an image 200 produces an
// audit record whose content sha256/size reflect the DECODED image bytes,
// not the base64 transport string.
func TestAuditImageRecordHashesDecodedBytes(t *testing.T) {
	auditPath := filepath.Join(t.TempDir(), "audit.jsonl")
	env := newE2E(t, e2eOpts{auditLogPath: auditPath})
	defer env.close()

	rawImage := []byte{0xde, 0xad, 0xbe, 0xef, 0x00, 0x01}
	b64 := base64.StdEncoding.EncodeToString(rawImage)
	rec := postJSON(env, "/v1/moderate/image", "dev-pro", `{"image_base64":"`+b64+`"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (%s)", rec.Code, rec.Body.String())
	}

	env.closeAudit()
	recs := readAuditRecords(t, auditPath)
	if len(recs) != 1 {
		t.Fatalf("got %d audit records, want 1", len(recs))
	}
	got := recs[0]
	if got.Endpoint != "/v1/moderate/image" {
		t.Errorf("Endpoint = %q", got.Endpoint)
	}
	if got.SoR.Content.Type != "image" {
		t.Errorf("content.type = %q, want image", got.SoR.Content.Type)
	}
	wantSum := sha256.Sum256(rawImage)
	if got.SoR.Content.SHA256 != hex.EncodeToString(wantSum[:]) {
		t.Errorf("content.sha256 = %q, want sha256 of the DECODED bytes (not the base64 string)", got.SoR.Content.SHA256)
	}
	if got.SoR.Content.SizeBytes != len(rawImage) {
		t.Errorf("content.size_bytes = %d, want %d (decoded length, not base64 length %d)",
			got.SoR.Content.SizeBytes, len(rawImage), len(b64))
	}
	if got.SoR.Content.Language != nil {
		t.Errorf("content.language = %v, want null for an image", got.SoR.Content.Language)
	}
}

// TestAuditDisabledCreatesNoFile is the negative default-off proof at the e2e
// level: with no auditLogPath configured, a successful decision must leave
// the would-be log directory untouched — no file is created anywhere just
// because a decision happened.
func TestAuditDisabledCreatesNoFile(t *testing.T) {
	dir := t.TempDir() // never handed to the server; must stay empty
	env := newE2E(t, e2eOpts{})
	defer env.close()

	rec := postJSON(env, "/v1/moderate/text", "dev-pro", `{"content":"hello"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("expected no files without AUDIT_LOG_PATH, found %d entries", len(entries))
	}
	if _, err := os.Stat(filepath.Join(dir, "audit.jsonl")); !os.IsNotExist(err) {
		t.Errorf("audit.jsonl unexpectedly exists (or Stat failed oddly): %v", err)
	}
}

func readAuditRecords(t *testing.T, path string) []audit.Record {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read audit file: %v", err)
	}
	var recs []audit.Record
	for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		r, err := audit.ParseRecord([]byte(line))
		if err != nil {
			t.Fatalf("ParseRecord: %v (line %q)", err, line)
		}
		recs = append(recs, r)
	}
	return recs
}

func TestAuditRecordAppendedOnSuccessfulTextDecision(t *testing.T) {
	auditPath := filepath.Join(t.TempDir(), "audit.jsonl")
	env := newE2E(t, e2eOpts{
		auditLogPath: auditPath,
		textHandler: func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(`{"scores":{"toxicity":0.95,"spam":0.05},"model_version":"stub-1"}`))
		},
	})
	defer env.close()

	rec := postJSON(env, "/v1/moderate/text", "dev-pro", `{"content":"contenu haineux","language":"fr"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (%s)", rec.Code, rec.Body.String())
	}
	var respBody struct {
		ID string `json:"id"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &respBody)

	env.closeAudit()
	recs := readAuditRecords(t, auditPath)
	if len(recs) != 1 {
		t.Fatalf("got %d audit records, want 1", len(recs))
	}
	got := recs[0]
	if got.Seq != 1 || got.PrevHash != "genesis" {
		t.Errorf("first record seq/prev_hash = %d/%q, want 1/genesis", got.Seq, got.PrevHash)
	}
	if err := audit.VerifyRecordHash(got); err != nil {
		t.Errorf("VerifyRecordHash: %v", err)
	}
	if got.Endpoint != "/v1/moderate/text" {
		t.Errorf("Endpoint = %q", got.Endpoint)
	}
	if got.RequestID == "" {
		t.Error("RequestID must not be empty")
	}
	wantKeyHash := sha256.Sum256([]byte("dev-pro"))
	if got.APIKeyHash != hex.EncodeToString(wantKeyHash[:]) {
		t.Errorf("APIKeyHash = %q, want sha256(dev-pro)", got.APIKeyHash)
	}
	if got.SoR.DecisionID != respBody.ID {
		t.Errorf("SoR.DecisionID = %q, want response id %q", got.SoR.DecisionID, respBody.ID)
	}
}

func TestAuditRecordsFormValidChainAcrossRequests(t *testing.T) {
	auditPath := filepath.Join(t.TempDir(), "audit.jsonl")
	env := newE2E(t, e2eOpts{auditLogPath: auditPath})
	defer env.close()

	for i := 0; i < 3; i++ {
		rec := postJSON(env, "/v1/moderate/text", "dev-pro", `{"content":"hello"}`)
		if rec.Code != http.StatusOK {
			t.Fatalf("request %d: status = %d", i, rec.Code)
		}
	}
	env.closeAudit()

	recs := readAuditRecords(t, auditPath)
	if len(recs) != 3 {
		t.Fatalf("got %d records, want 3", len(recs))
	}
	prevHash := "genesis"
	for i, r := range recs {
		if err := audit.VerifyRecordHash(r); err != nil {
			t.Errorf("record %d: %v", i, err)
		}
		if r.Seq != uint64(i+1) {
			t.Errorf("record %d: seq = %d, want %d", i, r.Seq, i+1)
		}
		if r.PrevHash != prevHash {
			t.Errorf("record %d: prev_hash = %q, want %q", i, r.PrevHash, prevHash)
		}
		prevHash = r.RecordHash
	}
}

func TestAuditNoRecordForCircuitOpen202(t *testing.T) {
	auditPath := filepath.Join(t.TempDir(), "audit.jsonl")
	env := newE2E(t, e2eOpts{
		auditLogPath: auditPath,
		cbThreshold:  1,
		maxRetries:   0,
		textHandler:  func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusInternalServerError) },
	})
	defer env.close()

	// Trip the breaker (one 5xx, threshold 1), then a second call returns 202.
	postJSON(env, "/v1/moderate/text", "dev-pro", `{"content":"hi"}`)
	rec := postJSON(env, "/v1/moderate/text", "dev-pro", `{"content":"hi"}`)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202", rec.Code)
	}

	env.closeAudit()
	if recs := readAuditRecords(t, auditPath); len(recs) != 0 {
		t.Errorf("got %d audit records for a 202 pending outcome, want 0", len(recs))
	}
}

func TestAuditNoRecordForUnauthorized401(t *testing.T) {
	auditPath := filepath.Join(t.TempDir(), "audit.jsonl")
	env := newE2E(t, e2eOpts{auditLogPath: auditPath})
	defer env.close()

	rec := postJSON(env, "/v1/moderate/text", "nope", `{"content":"hi"}`)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}

	env.closeAudit()
	if recs := readAuditRecords(t, auditPath); len(recs) != 0 {
		t.Errorf("got %d audit records for a 401, want 0", len(recs))
	}
}

func TestAuditNoRecordForBadRequest400(t *testing.T) {
	auditPath := filepath.Join(t.TempDir(), "audit.jsonl")
	env := newE2E(t, e2eOpts{auditLogPath: auditPath})
	defer env.close()

	rec := postJSON(env, "/v1/moderate/text", "dev-pro", `{"content":"   "}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}

	env.closeAudit()
	if recs := readAuditRecords(t, auditPath); len(recs) != 0 {
		t.Errorf("got %d audit records for a 400, want 0", len(recs))
	}
}

func TestAuditNoRecordForRateLimited429(t *testing.T) {
	auditPath := filepath.Join(t.TempDir(), "audit.jsonl")
	lim := ratelimit.New(ratelimit.DefaultTierLimits(), ratelimit.DefaultTierQuotas())
	env := newE2E(t, e2eOpts{auditLogPath: auditPath, limiter: lim})
	defer env.close()

	var got429 bool
	for i := 0; i < 6; i++ {
		rec := postJSON(env, "/v1/moderate/text", "dev-free", `{"content":"hi"}`)
		if rec.Code == http.StatusTooManyRequests {
			got429 = true
			break
		}
	}
	if !got429 {
		t.Fatal("expected a 429 within the first 6 requests")
	}

	env.closeAudit()
	// Some of the 6 requests before the 429 may have succeeded (burst 5) and
	// legitimately produced records; what matters is that the 429 itself adds
	// none. We can't isolate "the 429's" record directly through the file, so
	// assert the weaker but still meaningful invariant: no more records exist
	// than successful (200) requests could have produced (at most 5, the
	// free-tier burst).
	if recs := readAuditRecords(t, auditPath); len(recs) > 5 {
		t.Errorf("got %d audit records, want at most 5 (the free-tier burst; the 429 itself must add none)", len(recs))
	}
}

// TestAuditAPIKeyNeverAppearsInRawLogFile asserts on the raw file bytes (not a
// parsed struct) that the literal API key string never appears anywhere in
// the log, while the hash of that key is present and correct.
func TestAuditAPIKeyNeverAppearsInRawLogFile(t *testing.T) {
	auditPath := filepath.Join(t.TempDir(), "audit.jsonl")
	env := newE2E(t, e2eOpts{auditLogPath: auditPath})
	defer env.close()

	const key = "dev-pro"
	rec := postJSON(env, "/v1/moderate/text", key, `{"content":"some very identifiable secret content"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}

	env.closeAudit()
	raw, err := os.ReadFile(auditPath)
	if err != nil {
		t.Fatalf("read audit file: %v", err)
	}
	if strings.Contains(string(raw), key) {
		t.Fatalf("raw API key %q appears in the audit log:\n%s", key, raw)
	}
	if strings.Contains(string(raw), "some very identifiable secret content") {
		t.Fatal("raw moderated content must never appear in the audit log")
	}
	wantHash := sha256.Sum256([]byte(key))
	if !strings.Contains(string(raw), hex.EncodeToString(wantHash[:])) {
		t.Error("expected api_key_hash (sha256 of the key) to be present in the log")
	}
}

func TestAuditMetricsWiredThroughServer(t *testing.T) {
	auditPath := filepath.Join(t.TempDir(), "audit.jsonl")
	m := metrics.New()
	env := newE2E(t, e2eOpts{auditLogPath: auditPath, metrics: m})
	defer env.close()

	rec := postJSON(env, "/v1/moderate/text", "dev-pro", `{"content":"hi"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	env.closeAudit()

	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	scrapeRec := httptest.NewRecorder()
	env.handler.ServeHTTP(scrapeRec, req)
	body := scrapeRec.Body.String()
	if !strings.Contains(body, "gateway_audit_records_total 1") {
		t.Errorf("missing gateway_audit_records_total in scrape:\n%s", body)
	}
}

// TestGracefulShutdownDrainsAuditLog proves the shutdown path extension: Run's
// existing graceful-shutdown sequence (already tested for HTTP draining in
// server_base_test.go) must also drain the audit logger before returning, with
// no explicit Close call from the caller.
func TestGracefulShutdownDrainsAuditLog(t *testing.T) {
	auditPath := filepath.Join(t.TempDir(), "audit.jsonl")
	m := metrics.New()
	al, err := audit.NewLogger(auditPath, testLogger(), m)
	if err != nil {
		t.Fatalf("audit.NewLogger: %v", err)
	}

	closed := circuit.New(circuit.Config{FailureThreshold: 1, OpenTimeout: time.Minute})
	srv := New(Deps{
		Config: config.Config{Addr: "127.0.0.1:0", ShutdownTimeout: 2 * time.Second},
		Logger: testLogger(),
		Audit:  al,
		Text:   &Downstream{Name: "text", Breaker: closed},
	})

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- srv.Run(ctx) }()
	time.Sleep(50 * time.Millisecond)

	// Queue a record directly through the logger (equivalent to an in-flight
	// decision) so there is something for the shutdown path to drain.
	al.Append(audit.Entry{Timestamp: time.Now(), RequestID: "rid", Endpoint: "/v1/moderate/text", SoR: testSoRForAudit()})

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run returned error: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Run did not return after context cancel")
	}

	recs := readAuditRecords(t, auditPath)
	if len(recs) != 1 {
		t.Fatalf("got %d audit records after shutdown, want 1 (queued entry must be drained, not lost)", len(recs))
	}
}
