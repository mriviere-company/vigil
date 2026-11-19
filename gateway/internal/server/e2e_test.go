package server

import (
	"bytes"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/time/rate"

	"vigil-api/gateway/internal/audit"
	"vigil-api/gateway/internal/auth"
	"vigil-api/gateway/internal/circuit"
	"vigil-api/gateway/internal/config"
	"vigil-api/gateway/internal/metrics"
	"vigil-api/gateway/internal/middleware"
	"vigil-api/gateway/internal/proxy"
	"vigil-api/gateway/internal/ratelimit"
	"vigil-api/gateway/internal/verdict"
)

type e2eOpts struct {
	textHandler  http.HandlerFunc
	imageHandler http.HandlerFunc
	cbThreshold  int
	maxRetries   int
	totalTimeout time.Duration
	// limiter overrides; zero value => generous (no throttling in most tests).
	limiter *ratelimit.Limiter
	// maxInflight > 0 enables the backpressure semaphore on both downstreams.
	maxInflight      int
	backpressureMode string
	// metrics, when set, wires the Prometheus surface into the server.
	metrics *metrics.Metrics
	// auditLogPath, when set, wires an audit.Logger backed by that file into
	// the server (empty => audit disabled, matching production's default).
	auditLogPath string
	// sorIncludeInResponse mirrors SOR_INCLUDE_IN_RESPONSE.
	sorIncludeInResponse bool
	// rejectThreshold/flagThreshold override the verdict thresholds; zero
	// values fall back to the contract defaults (0.85 / 0.5).
	rejectThreshold float64
	flagThreshold   float64
}

type e2eEnv struct {
	handler http.Handler
	text    *Downstream
	image   *Downstream
	close   func()
	// closeAudit drains and closes the audit logger (no-op if disabled). Call
	// it explicitly, before close(), whenever a test needs to read the audit
	// file: Close() blocks until every already-queued record is flushed, so
	// reading only after it returns is what makes the read deterministic.
	closeAudit func()
}

func mockServer(h http.HandlerFunc) *httptest.Server {
	if h == nil {
		h = func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(`{"scores":{"toxicity":0.05,"spam":0.05},"model_version":"stub-1"}`))
		}
	}
	return httptest.NewServer(h)
}

func newE2E(t *testing.T, o e2eOpts) e2eEnv {
	t.Helper()
	if o.cbThreshold == 0 {
		o.cbThreshold = 5
	}
	if o.totalTimeout == 0 {
		o.totalTimeout = time.Second
	}
	textSrv := mockServer(o.textHandler)
	imageSrv := mockServer(o.imageHandler)

	mkDown := func(name, url string) *Downstream {
		d := &Downstream{
			Name: name,
			Client: proxy.New(proxy.Options{
				BaseURL: url, ConnectTimeout: 200 * time.Millisecond,
				TotalTimeout: o.totalTimeout, MaxRetries: o.maxRetries,
			}),
			Breaker: circuit.New(circuit.Config{
				FailureThreshold: o.cbThreshold, OpenTimeout: time.Minute, HalfOpenMaxProbes: 1,
			}),
		}
		if o.maxInflight > 0 {
			d.Sem = make(chan struct{}, o.maxInflight)
		}
		return d
	}
	text := mkDown("text", textSrv.URL)
	image := mkDown("image", imageSrv.URL)

	lim := o.limiter
	if lim == nil {
		// Generous limits so rate limiting doesn't interfere with other tests.
		lim = ratelimit.New(
			map[auth.Tier]ratelimit.TierLimit{
				auth.TierFree: {RPS: rate.Inf, Burst: 1000},
				auth.TierPro:  {RPS: rate.Inf, Burst: 1000},
			},
			map[auth.Tier]int{auth.TierFree: 1_000_000, auth.TierPro: 1_000_000},
		)
	}

	bpMode := o.backpressureMode
	if bpMode == "" {
		bpMode = config.BackpressureReject
	}
	if o.rejectThreshold == 0 {
		o.rejectThreshold = 0.85
	}
	if o.flagThreshold == 0 {
		o.flagThreshold = 0.5
	}
	deps := Deps{
		Config: config.Config{
			RejectThreshold: o.rejectThreshold, FlagThreshold: o.flagThreshold, RetryAfter: 30,
			BackpressureMode: bpMode, SORIncludeInResponse: o.sorIncludeInResponse,
		},
		Logger:  testLogger(),
		Auth:    auth.New(map[string]auth.Tier{"dev-free": auth.TierFree, "dev-pro": auth.TierPro}),
		Limiter: lim,
		Verdict: verdict.New(o.rejectThreshold, o.flagThreshold),
		Text:    text,
		Image:   image,
	}
	if o.metrics != nil {
		deps.Recorder = o.metrics
		deps.MetricsHandler = o.metrics.Handler()
		deps.ObservabilityMiddleware = []func(http.Handler) http.Handler{o.metrics.HTTPMiddleware}
	}

	closeAudit := func() {}
	if o.auditLogPath != "" {
		var rec audit.MetricsRecorder
		if o.metrics != nil {
			rec = o.metrics
		}
		al, err := audit.NewLogger(o.auditLogPath, testLogger(), rec)
		if err != nil {
			t.Fatalf("audit.NewLogger: %v", err)
		}
		deps.Audit = al
		closeAudit = al.Close
	}

	srv := New(deps)

	return e2eEnv{
		handler:    srv.Handler(),
		text:       text,
		image:      image,
		close:      func() { textSrv.Close(); imageSrv.Close() },
		closeAudit: closeAudit,
	}
}

func postJSON(env e2eEnv, path, key, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader([]byte(body)))
	req.Header.Set("Content-Type", "application/json")
	if key != "" {
		req.Header.Set(auth.APIKeyHeader, key)
	}
	rec := httptest.NewRecorder()
	env.handler.ServeHTTP(rec, req)
	return rec
}

func assertErrorEnvelope(t *testing.T, rec *httptest.ResponseRecorder, wantCode string) {
	t.Helper()
	var body middleware.ErrorResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("body is not a normalized error envelope: %v (%q)", err, rec.Body.String())
	}
	if body.Error.Code != wantCode {
		t.Errorf("error code = %q, want %q", body.Error.Code, wantCode)
	}
	if body.Error.RequestID == "" {
		t.Error("error envelope missing request_id")
	}
	if body.Error.Message == "" {
		t.Error("error envelope missing message")
	}
}

func TestE2ETextRejected(t *testing.T) {
	env := newE2E(t, e2eOpts{
		textHandler: func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(`{"scores":{"toxicity":0.95,"spam":0.05},"model_version":"stub-1"}`))
		},
	})
	defer env.close()

	rec := postJSON(env, "/v1/moderate/text", "dev-pro", `{"content":"you are hateful"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %q)", rec.Code, rec.Body.String())
	}
	var body moderationResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("bad json: %v", err)
	}
	if body.Verdict != "rejected" {
		t.Errorf("verdict = %q, want rejected", body.Verdict)
	}
	if body.Score != 0.95 {
		t.Errorf("score = %v, want 0.95", body.Score)
	}
	if body.Categories["toxicity"] != 0.95 {
		t.Errorf("categories = %v", body.Categories)
	}
	if body.ID == "" {
		t.Error("missing id")
	}
}

func TestE2ETextApproved(t *testing.T) {
	env := newE2E(t, e2eOpts{}) // default handler returns low scores
	defer env.close()

	rec := postJSON(env, "/v1/moderate/text", "dev-free", `{"content":"lovely day","language":"en"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var body moderationResponse
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	if body.Verdict != "approved" {
		t.Errorf("verdict = %q, want approved", body.Verdict)
	}
}

func TestE2EImageJSON(t *testing.T) {
	env := newE2E(t, e2eOpts{
		imageHandler: func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(`{"scores":{"nsfw":0.95,"violence":0.02},"model_version":"stub-1"}`))
		},
	})
	defer env.close()

	rec := postJSON(env, "/v1/moderate/image", "dev-pro", `{"image_base64":"aGVsbG8="}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (%q)", rec.Code, rec.Body.String())
	}
	var body moderationResponse
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	if body.Verdict != "rejected" || body.Categories["nsfw"] != 0.95 {
		t.Errorf("unexpected body: %+v", body)
	}
}

func TestE2EImageMultipart(t *testing.T) {
	var gotBase64 string
	env := newE2E(t, e2eOpts{
		imageHandler: func(w http.ResponseWriter, r *http.Request) {
			var req struct {
				ImageBase64 string `json:"image_base64"`
			}
			_ = json.NewDecoder(r.Body).Decode(&req)
			gotBase64 = req.ImageBase64
			_, _ = w.Write([]byte(`{"scores":{"nsfw":0.1,"violence":0.02},"model_version":"stub-1"}`))
		},
	})
	defer env.close()

	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	fw, _ := mw.CreateFormFile("image", "pic.png")
	_, _ = fw.Write([]byte("raw-image-bytes"))
	_ = mw.Close()

	req := httptest.NewRequest(http.MethodPost, "/v1/moderate/image", &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.Header.Set(auth.APIKeyHeader, "dev-pro")
	rec := httptest.NewRecorder()
	env.handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (%q)", rec.Code, rec.Body.String())
	}
	// "raw-image-bytes" base64-encoded by the gateway.
	if gotBase64 != "cmF3LWltYWdlLWJ5dGVz" {
		t.Errorf("downstream got image_base64 = %q", gotBase64)
	}
}

func TestE2EUnknownKey401(t *testing.T) {
	env := newE2E(t, e2eOpts{})
	defer env.close()
	rec := postJSON(env, "/v1/moderate/text", "nope", `{"content":"hi"}`)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
	assertErrorEnvelope(t, rec, middleware.CodeUnauthorized)
}

func TestE2EEmptyContent400(t *testing.T) {
	env := newE2E(t, e2eOpts{})
	defer env.close()
	rec := postJSON(env, "/v1/moderate/text", "dev-pro", `{"content":"   "}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
	assertErrorEnvelope(t, rec, middleware.CodeBadRequest)
}

func TestE2EDownstream4xxBecomes400(t *testing.T) {
	env := newE2E(t, e2eOpts{
		textHandler: func(w http.ResponseWriter, _ *http.Request) {
			http.Error(w, `{"detail":[{"msg":"bad"}]}`, http.StatusUnprocessableEntity)
		},
	})
	defer env.close()
	rec := postJSON(env, "/v1/moderate/text", "dev-pro", `{"content":"hi"}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
	assertErrorEnvelope(t, rec, middleware.CodeBadRequest)
}

func TestE2EDownstream5xxBecomes502ThenBreakerOpensTo202(t *testing.T) {
	var calls int32
	env := newE2E(t, e2eOpts{
		cbThreshold: 2,
		maxRetries:  0,
		textHandler: func(w http.ResponseWriter, _ *http.Request) {
			atomic.AddInt32(&calls, 1)
			w.WriteHeader(http.StatusInternalServerError)
		},
	})
	defer env.close()

	// Two 5xx (threshold 2) => two 502s, then the breaker opens.
	for i := 0; i < 2; i++ {
		rec := postJSON(env, "/v1/moderate/text", "dev-pro", `{"content":"hi"}`)
		if rec.Code != http.StatusBadGateway {
			t.Fatalf("request %d: status %d, want 502", i+1, rec.Code)
		}
		assertErrorEnvelope(t, rec, middleware.CodeUpstreamError)
	}
	if env.text.Breaker.State() != circuit.StateOpen {
		t.Fatalf("breaker state = %v, want open", env.text.Breaker.State())
	}
	// Now the circuit is open: degraded mode 202, downstream not called.
	before := atomic.LoadInt32(&calls)
	rec := postJSON(env, "/v1/moderate/text", "dev-pro", `{"content":"hi"}`)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("open-circuit status = %d, want 202", rec.Code)
	}
	var pending pendingResponse
	_ = json.Unmarshal(rec.Body.Bytes(), &pending)
	if pending.Status != "pending" || pending.RetryAfter != 30 {
		t.Errorf("pending body = %+v", pending)
	}
	if atomic.LoadInt32(&calls) != before {
		t.Error("downstream was called while the circuit was open")
	}
}

func TestE2EDownstreamTimeoutBecomes502(t *testing.T) {
	env := newE2E(t, e2eOpts{
		cbThreshold:  5,
		maxRetries:   0,
		totalTimeout: 40 * time.Millisecond,
		textHandler: func(w http.ResponseWriter, _ *http.Request) {
			time.Sleep(200 * time.Millisecond)
			w.WriteHeader(http.StatusOK)
		},
	})
	defer env.close()
	rec := postJSON(env, "/v1/moderate/text", "dev-pro", `{"content":"hi"}`)
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502", rec.Code)
	}
	assertErrorEnvelope(t, rec, middleware.CodeUpstreamError)
}

func TestE2EHealthReflectsCircuit(t *testing.T) {
	env := newE2E(t, e2eOpts{
		cbThreshold: 1,
		maxRetries:  0,
		textHandler: func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusInternalServerError) },
	})
	defer env.close()

	// Trip the text breaker.
	postJSON(env, "/v1/moderate/text", "dev-pro", `{"content":"hi"}`)

	req := httptest.NewRequest(http.MethodGet, "/v1/health", nil)
	rec := httptest.NewRecorder()
	env.handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("health status = %d", rec.Code)
	}
	var body healthResponse
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	if body.Circuits["text"] != "open" {
		t.Errorf("text circuit = %q, want open", body.Circuits["text"])
	}
	if body.Circuits["image"] != "closed" {
		t.Errorf("image circuit = %q, want closed", body.Circuits["image"])
	}
}

func TestE2EBackpressure503(t *testing.T) {
	// Semaphore of 1 + a slow downstream: while one request holds the slot, a
	// second is shed immediately with 503.
	started := make(chan struct{})
	release := make(chan struct{})
	env := newE2E(t, e2eOpts{
		maxInflight: 1,
		textHandler: func(w http.ResponseWriter, _ *http.Request) {
			started <- struct{}{}
			<-release
			_, _ = w.Write([]byte(`{"scores":{"toxicity":0.1},"model_version":"v"}`))
		},
	})
	defer env.close()
	defer close(release)

	go func() { postJSON(env, "/v1/moderate/text", "dev-pro", `{"content":"hi"}`) }()
	<-started // first request now holds the only slot

	rec := postJSON(env, "/v1/moderate/text", "dev-pro", `{"content":"hi"}`)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", rec.Code)
	}
	assertErrorEnvelope(t, rec, middleware.CodeOverloaded)
}

func TestE2EBackpressurePendingMode202(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	env := newE2E(t, e2eOpts{
		maxInflight:      1,
		backpressureMode: config.BackpressurePending,
		textHandler: func(w http.ResponseWriter, _ *http.Request) {
			started <- struct{}{}
			<-release
			_, _ = w.Write([]byte(`{"scores":{"toxicity":0.1},"model_version":"v"}`))
		},
	})
	defer env.close()
	defer close(release)

	go func() { postJSON(env, "/v1/moderate/text", "dev-pro", `{"content":"hi"}`) }()
	<-started

	rec := postJSON(env, "/v1/moderate/text", "dev-pro", `{"content":"hi"}`)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202 in pending mode", rec.Code)
	}
	var pending pendingResponse
	_ = json.Unmarshal(rec.Body.Bytes(), &pending)
	if pending.Status != "pending" {
		t.Errorf("body = %+v", pending)
	}
}

func TestE2EMetricsWiring(t *testing.T) {
	m := metrics.New()
	env := newE2E(t, e2eOpts{
		metrics: m,
		textHandler: func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(`{"scores":{"toxicity":0.95,"spam":0.05},"model_version":"stub-1"}`))
		},
	})
	defer env.close()

	if rec := postJSON(env, "/v1/moderate/text", "dev-pro", `{"content":"hateful"}`); rec.Code != http.StatusOK {
		t.Fatalf("moderation status = %d", rec.Code)
	}

	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	rec := httptest.NewRecorder()
	env.handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("/metrics status = %d", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, `gateway_verdicts_total{outcome="rejected"} 1`) {
		t.Errorf("verdict metric not recorded through the server:\n%s", body)
	}
	if !strings.Contains(body, `gateway_requests_total{endpoint="/v1/moderate/text",status="200"} 1`) {
		t.Errorf("request metric not recorded through the middleware")
	}
}

func TestE2EUnknownPath404Envelope(t *testing.T) {
	env := newE2E(t, e2eOpts{})
	defer env.close()

	req := httptest.NewRequest(http.MethodGet, "/no/such/path", nil)
	req.Header.Set("X-Request-ID", "rid-404")
	rec := httptest.NewRecorder()
	env.handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
	if got := rec.Header().Get("X-Request-ID"); got != "rid-404" {
		t.Errorf("X-Request-ID = %q, want rid-404", got)
	}
	assertErrorEnvelope(t, rec, middleware.CodeNotFound)
	var body middleware.ErrorResponse
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	if body.Error.RequestID != "rid-404" {
		t.Errorf("envelope request_id = %q, want rid-404", body.Error.RequestID)
	}
}

func TestE2EWrongMethod405Envelope(t *testing.T) {
	env := newE2E(t, e2eOpts{})
	defer env.close()

	req := httptest.NewRequest(http.MethodGet, "/v1/moderate/text", nil)
	req.Header.Set("X-Request-ID", "rid-405")
	rec := httptest.NewRecorder()
	env.handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", rec.Code)
	}
	if got := rec.Header().Get("Allow"); got != http.MethodPost {
		t.Errorf("Allow = %q, want POST", got)
	}
	if got := rec.Header().Get("X-Request-ID"); got != "rid-405" {
		t.Errorf("X-Request-ID = %q, want rid-405", got)
	}
	assertErrorEnvelope(t, rec, middleware.CodeMethodNotAllow)
	var body middleware.ErrorResponse
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	if body.Error.RequestID != "rid-405" {
		t.Errorf("envelope request_id = %q, want rid-405", body.Error.RequestID)
	}
}

func TestE2ERateLimit429(t *testing.T) {
	// Free tier default burst 5; the 6th immediate request is throttled.
	lim := ratelimit.New(ratelimit.DefaultTierLimits(), ratelimit.DefaultTierQuotas())
	env := newE2E(t, e2eOpts{limiter: lim})
	defer env.close()

	var got429 *httptest.ResponseRecorder
	for i := 0; i < 6; i++ {
		rec := postJSON(env, "/v1/moderate/text", "dev-free", `{"content":"hi"}`)
		if rec.Code == http.StatusTooManyRequests {
			got429 = rec
			break
		}
	}
	if got429 == nil {
		t.Fatal("expected a 429 within the first 6 requests")
	}
	if got429.Header().Get("Retry-After") == "" {
		t.Error("429 missing Retry-After header")
	}
	assertErrorEnvelope(t, got429, middleware.CodeRateLimited)
}
