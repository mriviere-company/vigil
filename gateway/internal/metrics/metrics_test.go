package metrics

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func scrape(t *testing.T, m *Metrics) string {
	t.Helper()
	rec := httptest.NewRecorder()
	m.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("metrics scrape status = %d", rec.Code)
	}
	body, _ := io.ReadAll(rec.Body)
	return string(body)
}

func TestMetricsRecordAndExpose(t *testing.T) {
	m := New()

	m.IncVerdict("rejected")
	m.IncRetry("text")
	m.SetInFlight("image", 3)
	m.RecordCircuitTransition("text", "closed", "open", 2)
	m.SetCircuitState("image", 0)
	m.IncAuditRecord()
	m.IncAuditDropped()
	m.IncAuditWriteError()

	// Exercise the HTTP middleware so request metrics appear.
	h := m.HTTPMiddleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/v1/health", nil))

	out := scrape(t, m)
	wantSubstrings := []string{
		`gateway_verdicts_total{outcome="rejected"} 1`,
		`gateway_downstream_retries_total{downstream="text"} 1`,
		`gateway_downstream_inflight{downstream="image"} 3`,
		`gateway_circuit_transitions_total{downstream="text",from="closed",to="open"} 1`,
		`gateway_circuit_state{downstream="text"} 2`,
		`gateway_requests_total{endpoint="/v1/health",status="200"} 1`,
		"gateway_request_duration_seconds_bucket",
		"gateway_audit_records_total 1",
		"gateway_audit_dropped_total 1",
		"gateway_audit_write_errors_total 1",
	}
	for _, s := range wantSubstrings {
		if !strings.Contains(out, s) {
			t.Errorf("scrape missing %q", s)
		}
	}
}

func TestEndpointLabelCollapsesUnknown(t *testing.T) {
	m := New()
	h := m.HTTPMiddleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {}))
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/random/path", nil))

	out := scrape(t, m)
	if !strings.Contains(out, `endpoint="other"`) {
		t.Errorf("unknown path not collapsed to 'other':\n%s", out)
	}
}
