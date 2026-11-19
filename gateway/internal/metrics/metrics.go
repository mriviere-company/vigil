// Package metrics owns the gateway's Prometheus instrumentation: the collectors
// (registered into an injected registry, so there is no global default-registry
// state), the /metrics scrape handler, an HTTP middleware that records latency
// and request counts, and the hook methods the resilience layers call
// (retries, circuit transitions, in-flight, verdicts).
//
// It is injected as an opaque dependency: the server records verdicts and
// in-flight through the small server.Recorder interface, the proxy calls
// IncRetry via its OnRetry hook, and the breaker calls the transition/state
// methods via OnStateChange — none of those packages import this one, keeping
// the resilience code free of a Prometheus dependency.
package metrics

import (
	"net/http"
	"strconv"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// Metrics holds every collector the gateway exports.
type Metrics struct {
	reg *prometheus.Registry

	requestDuration       *prometheus.HistogramVec
	requestsTotal         *prometheus.CounterVec
	verdictsTotal         *prometheus.CounterVec
	retriesTotal          *prometheus.CounterVec
	circuitTransitions    *prometheus.CounterVec
	inFlight              *prometheus.GaugeVec
	circuitState          *prometheus.GaugeVec
	auditRecordsTotal     prometheus.Counter
	auditDroppedTotal     prometheus.Counter
	auditWriteErrorsTotal prometheus.Counter
}

// New builds and registers the collectors into a fresh registry (plus the Go
// runtime and process collectors for baseline host metrics).
func New() *Metrics {
	reg := prometheus.NewRegistry()
	m := &Metrics{
		reg: reg,
		requestDuration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name: "gateway_request_duration_seconds",
			Help: "End-to-end gateway request latency by endpoint.",
			// Buckets clustered around the 200ms p95 target for useful quantiles.
			Buckets: []float64{0.005, 0.01, 0.025, 0.05, 0.075, 0.1, 0.15, 0.2, 0.3, 0.5, 0.75, 1, 2},
		}, []string{"endpoint"}),
		requestsTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "gateway_requests_total",
			Help: "Total gateway requests by endpoint and HTTP status (429s are status=429).",
		}, []string{"endpoint", "status"}),
		verdictsTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "gateway_verdicts_total",
			Help: "Moderation verdicts by outcome.",
		}, []string{"outcome"}),
		retriesTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "gateway_downstream_retries_total",
			Help: "Downstream request retries by service.",
		}, []string{"downstream"}),
		circuitTransitions: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "gateway_circuit_transitions_total",
			Help: "Circuit breaker state transitions by downstream.",
		}, []string{"downstream", "from", "to"}),
		inFlight: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "gateway_downstream_inflight",
			Help: "In-flight requests per downstream (backpressure semaphore usage).",
		}, []string{"downstream"}),
		circuitState: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "gateway_circuit_state",
			Help: "Circuit breaker state per downstream (0=closed, 1=half-open, 2=open).",
		}, []string{"downstream"}),
		auditRecordsTotal: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "gateway_audit_records_total",
			Help: "Audit log records successfully appended.",
		}),
		auditDroppedTotal: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "gateway_audit_dropped_total",
			Help: "Audit log records dropped because the bounded queue was full (availability over audit completeness; see gateway/internal/audit).",
		}),
		auditWriteErrorsTotal: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "gateway_audit_write_errors_total",
			Help: "Audit log records lost to a write-path failure (hash/marshal/write/flush, e.g. disk full) — distinct from queue-full drops.",
		}),
	}
	reg.MustRegister(
		m.requestDuration, m.requestsTotal, m.verdictsTotal, m.retriesTotal,
		m.circuitTransitions, m.inFlight, m.circuitState,
		m.auditRecordsTotal, m.auditDroppedTotal, m.auditWriteErrorsTotal,
		collectors.NewGoCollector(),
		collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
	)
	return m
}

// Handler serves the Prometheus exposition format for the gateway's registry.
func (m *Metrics) Handler() http.Handler {
	return promhttp.HandlerFor(m.reg, promhttp.HandlerOpts{})
}

// IncVerdict records a moderation outcome (satisfies server.Recorder).
func (m *Metrics) IncVerdict(outcome string) {
	m.verdictsTotal.WithLabelValues(outcome).Inc()
}

// SetInFlight sets the current in-flight gauge for a downstream (satisfies
// server.Recorder).
func (m *Metrics) SetInFlight(downstream string, n int) {
	m.inFlight.WithLabelValues(downstream).Set(float64(n))
}

// IncRetry counts a downstream retry (wired to proxy's OnRetry hook).
func (m *Metrics) IncRetry(downstream string) {
	m.retriesTotal.WithLabelValues(downstream).Inc()
}

// RecordCircuitTransition counts a breaker transition and updates the state
// gauge (wired to the breaker's OnStateChange hook).
func (m *Metrics) RecordCircuitTransition(downstream, from, to string, gaugeValue float64) {
	m.circuitTransitions.WithLabelValues(downstream, from, to).Inc()
	m.circuitState.WithLabelValues(downstream).Set(gaugeValue)
}

// SetCircuitState initializes/sets the state gauge (used at startup to publish
// the initial closed state).
func (m *Metrics) SetCircuitState(downstream string, gaugeValue float64) {
	m.circuitState.WithLabelValues(downstream).Set(gaugeValue)
}

// IncAuditRecord counts one record successfully appended to the audit log
// (satisfies audit.MetricsRecorder).
func (m *Metrics) IncAuditRecord() {
	m.auditRecordsTotal.Inc()
}

// IncAuditDropped counts one record dropped because the audit log's bounded
// queue was full (satisfies audit.MetricsRecorder) — this is the visibility
// half of that documented availability-over-completeness trade-off.
func (m *Metrics) IncAuditDropped() {
	m.auditDroppedTotal.Inc()
}

// IncAuditWriteError counts one record lost on the audit log's write path
// (hash/marshal/write/flush failure, e.g. disk full — satisfies
// audit.MetricsRecorder). Kept separate from IncAuditDropped so a failing
// disk is distinguishable from load-induced queue overflow.
func (m *Metrics) IncAuditWriteError() {
	m.auditWriteErrorsTotal.Inc()
}

// knownEndpoints keeps the endpoint label low-cardinality: unrecognized paths
// collapse to "other" so a scan for random URLs can't explode the series count.
var knownEndpoints = map[string]bool{
	"/v1/moderate/text":  true,
	"/v1/moderate/image": true,
	"/v1/health":         true,
	"/metrics":           true,
}

func endpointLabel(path string) string {
	if knownEndpoints[path] {
		return path
	}
	return "other"
}

// statusRecorder captures the response status for instrumentation.
type statusRecorder struct {
	http.ResponseWriter
	status int
	wrote  bool
}

func (s *statusRecorder) WriteHeader(code int) {
	if !s.wrote {
		s.status = code
		s.wrote = true
	}
	s.ResponseWriter.WriteHeader(code)
}

func (s *statusRecorder) Write(b []byte) (int, error) {
	if !s.wrote {
		s.status = http.StatusOK
		s.wrote = true
	}
	return s.ResponseWriter.Write(b)
}

func (s *statusRecorder) Flush() {
	if f, ok := s.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// HTTPMiddleware records latency and request counts (by endpoint+status) for
// every request. Placed below the access logger and above recovery so it
// observes the final status, including recovered panics.
func (m *Metrics) HTTPMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r)

		endpoint := endpointLabel(r.URL.Path)
		m.requestDuration.WithLabelValues(endpoint).Observe(time.Since(start).Seconds())
		m.requestsTotal.WithLabelValues(endpoint, strconv.Itoa(rec.status)).Inc()
	})
}
