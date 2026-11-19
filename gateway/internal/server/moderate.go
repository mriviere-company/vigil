package server

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"vigil-api/gateway/internal/audit"
	"vigil-api/gateway/internal/auth"
	"vigil-api/gateway/internal/circuit"
	"vigil-api/gateway/internal/config"
	"vigil-api/gateway/internal/middleware"
	"vigil-api/gateway/internal/proxy"
	"vigil-api/gateway/internal/sor"
)

// Request body size caps. Text is small; image (base64 or multipart) is bounded
// well above the downstream's 5 MB limit so oversized uploads are rejected here
// rather than streamed to the inference service.
const (
	maxTextBody  = 1 << 20  // 1 MiB
	maxImageBody = 12 << 20 // 12 MiB
)

// Recorder is the business-metrics surface the moderation handler needs. A
// no-op default (noopRecorder) keeps the handler decoupled from Prometheus
// until the metrics build step injects a real implementation.
type Recorder interface {
	IncVerdict(outcome string)
	SetInFlight(downstream string, n int)
}

type noopRecorder struct{}

func (noopRecorder) IncVerdict(string)       {}
func (noopRecorder) SetInFlight(string, int) {}

// moderationResponse is the frozen success shape (docs/api.md).
type moderationResponse struct {
	ID         string             `json:"id"`
	Verdict    string             `json:"verdict"`
	Score      float64            `json:"score"`
	Categories map[string]float64 `json:"categories"`
	LatencyMs  int64              `json:"latency_ms"`
	// StatementOfReasons is attached only when SOR_INCLUDE_IN_RESPONSE=true
	// (default false). omitempty on this pointer is what keeps the default
	// response byte-identical to the pre-existing contract: a nil pointer
	// with omitempty is never emitted, not even as "statement_of_reasons":null
	// — see docs/api.md and TestDefaultResponseContractByteStable.
	StatementOfReasons *sor.SoR `json:"statement_of_reasons,omitempty"`
}

type pendingResponse struct {
	Status     string `json:"status"`
	RetryAfter int    `json:"retry_after"`
}

// textRequestBody is the public request body; language is validated if present
// but not forwarded (the inference contract does not accept it yet).
type textRequestBody struct {
	Content  string `json:"content"`
	Language string `json:"language"`
}

// sorContext carries what moderate needs to build a statement of reasons,
// gathered by the type-specific handler (handleText/handleImage) before the
// shared resilience pipeline runs — moderate itself stays agnostic of how a
// text vs. image request is shaped.
type sorContext struct {
	contentType string // "text" | "image"
	rawContent  []byte // never retained beyond building the SoR — see sor.Content's doc
	language    string // "" => no language (SoR.Content.Language is null)
}

func (s *Server) handleText(w http.ResponseWriter, r *http.Request) {
	var body textRequestBody
	if !decodeJSON(w, r, maxTextBody, &body) {
		return
	}
	if strings.TrimSpace(body.Content) == "" {
		middleware.WriteError(w, r, http.StatusBadRequest, middleware.CodeBadRequest, "content must not be empty")
		return
	}
	if body.Language != "" && body.Language != "fr" && body.Language != "en" {
		middleware.WriteError(w, r, http.StatusBadRequest, middleware.CodeBadRequest, "language must be 'fr' or 'en'")
		return
	}
	s.moderate(w, r, s.deps.Text, proxy.TextRequest{Content: body.Content},
		sorContext{contentType: "text", rawContent: []byte(body.Content), language: body.Language})
}

type imageRequestBody struct {
	ImageBase64 string `json:"image_base64"`
}

func (s *Server) handleImage(w http.ResponseWriter, r *http.Request) {
	contentType := r.Header.Get("Content-Type")
	var imageB64 string
	var raw []byte // decoded image bytes, for the SoR's content hash+size only

	switch {
	case strings.HasPrefix(contentType, "application/json"):
		var body imageRequestBody
		if !decodeJSON(w, r, maxImageBody, &body) {
			return
		}
		if strings.TrimSpace(body.ImageBase64) == "" {
			middleware.WriteError(w, r, http.StatusBadRequest, middleware.CodeBadRequest, "image_base64 must not be empty")
			return
		}
		imageB64 = body.ImageBase64
		if decoded, err := base64.StdEncoding.DecodeString(imageB64); err == nil {
			raw = decoded
		} else {
			// Not valid base64: forwarding is unaffected (the downstream will
			// reject it on its own terms), but there is no decoded content to
			// hash for the SoR — fall back to hashing the given string as-is
			// rather than fail a request the gateway itself never rejected.
			raw = []byte(imageB64)
		}

	case strings.HasPrefix(contentType, "multipart/form-data"):
		b64, rawBytes, ok := s.readMultipartImage(w, r)
		if !ok {
			return
		}
		imageB64 = b64
		raw = rawBytes

	default:
		middleware.WriteError(w, r, http.StatusBadRequest, middleware.CodeBadRequest,
			"Content-Type must be application/json or multipart/form-data")
		return
	}

	s.moderate(w, r, s.deps.Image, proxy.ImageRequest{ImageBase64: imageB64},
		sorContext{contentType: "image", rawContent: raw})
}

// readMultipartImage reads the "image" file field and base64-encodes it (the
// gateway owns the encoding so clients can upload raw bytes). It also returns
// the raw decoded bytes, used only to build the SoR's content hash+size.
func (s *Server) readMultipartImage(w http.ResponseWriter, r *http.Request) (b64 string, raw []byte, ok bool) {
	r.Body = http.MaxBytesReader(w, r.Body, maxImageBody)
	if err := r.ParseMultipartForm(maxImageBody); err != nil {
		middleware.WriteError(w, r, http.StatusBadRequest, middleware.CodeBadRequest, "invalid multipart form")
		return "", nil, false
	}
	file, _, err := r.FormFile("image")
	if err != nil {
		middleware.WriteError(w, r, http.StatusBadRequest, middleware.CodeBadRequest, "missing 'image' file field")
		return "", nil, false
	}
	defer func() { _ = file.Close() }()

	raw, err = io.ReadAll(file)
	if err != nil {
		middleware.WriteError(w, r, http.StatusBadRequest, middleware.CodeBadRequest, "could not read uploaded image")
		return "", nil, false
	}
	return base64.StdEncoding.EncodeToString(raw), raw, true
}

// moderate runs the shared resilience pipeline for one downstream: backpressure
// (semaphore) -> circuit breaker -> retrying proxy -> verdict. The breaker only
// counts genuine upstream faults; a downstream 4xx flows through as a client
// 400 without tripping it.
func (s *Server) moderate(w http.ResponseWriter, r *http.Request, ds *Downstream, reqBody any, sctx sorContext) {
	release, admitted := s.acquire(ds)
	if !admitted {
		s.writeSaturated(w, r)
		return
	}
	defer release()

	ctx := r.Context()
	start := time.Now()

	var result *proxy.Result
	var callErr *proxy.Error
	cbErr := ds.Breaker.Execute(func() error {
		result, callErr = ds.Client.Predict(ctx, reqBody)
		// Only genuine upstream faults count against the breaker; a 4xx or a
		// caller cancellation must not trip it.
		if callErr != nil && callErr.IsUpstreamFault() {
			return callErr
		}
		return nil
	})

	if errors.Is(cbErr, circuit.ErrOpen) {
		writeJSON(w, http.StatusAccepted, pendingResponse{Status: "pending", RetryAfter: s.cfg.RetryAfter})
		return
	}
	if callErr != nil {
		s.writeProxyError(w, r, callErr)
		return
	}

	id := middleware.NewID()
	now := time.Now().UTC()
	v, score := s.deps.Verdict.Classify(result.Scores)
	s.deps.Recorder.IncVerdict(string(v))

	resp := moderationResponse{
		ID:         id,
		Verdict:    string(v),
		Score:      score,
		Categories: result.Scores,
		LatencyMs:  time.Since(start).Milliseconds(),
	}

	// The SoR is only built when something will consume it — either exposure
	// path off (the default) means paying no sha256/allocation cost on the
	// hot path at all, not just suppressing the output.
	if s.cfg.SORIncludeInResponse || s.deps.Audit != nil {
		statement := sor.New(sor.Input{
			DecisionID:   id,
			Now:          now,
			ContentType:  sctx.contentType,
			RawContent:   sctx.rawContent,
			Language:     sctx.language,
			ModelVersion: result.ModelVersion,
			Scores:       result.Scores,
			Reject:       s.cfg.RejectThreshold,
			Flag:         s.cfg.FlagThreshold,
			Verdict:      string(v),
		})
		if s.cfg.SORIncludeInResponse {
			resp.StatementOfReasons = &statement
		}
		if s.deps.Audit != nil {
			s.deps.Audit.Append(audit.Entry{
				Timestamp:  now,
				RequestID:  middleware.RequestIDFromContext(ctx),
				APIKeyHash: apiKeyHash(ctx),
				Endpoint:   r.URL.Path,
				SoR:        statement,
			})
		}
	}

	writeJSON(w, http.StatusOK, resp)
}

// apiKeyHash returns SHA-256(hex) of the authenticated caller's API key, or ""
// if no identity is on the context (should not happen behind the auth
// middleware, but this keeps the audit path safe if moderate is ever
// exercised without it). The raw key must never reach the audit log — see
// gateway/internal/audit's package doc; this is the one place it is derived
// from, and only ever as a hash.
func apiKeyHash(ctx context.Context) string {
	id, ok := auth.IdentityFromContext(ctx)
	if !ok {
		return ""
	}
	sum := sha256.Sum256([]byte(id.Key))
	return hex.EncodeToString(sum[:])
}

// acquire takes a backpressure slot for the downstream.
//
// Backpressure pattern — a buffered channel used as a counting semaphore caps
// concurrent in-flight requests to a downstream. The acquire is NON-blocking
// (select/default): when the buffer is full the gateway sheds load immediately
// rather than queueing work that would only pile latency onto an already
// struggling service. This protects the inference tier from being overwhelmed
// and keeps the gateway's own memory/goroutine use bounded. A nil semaphore
// disables it. Returns a release func and whether a slot was obtained.
func (s *Server) acquire(ds *Downstream) (release func(), admitted bool) {
	if ds.Sem == nil {
		return func() {}, true
	}
	select {
	case ds.Sem <- struct{}{}:
		s.deps.Recorder.SetInFlight(ds.Name, len(ds.Sem))
		return func() {
			<-ds.Sem
			s.deps.Recorder.SetInFlight(ds.Name, len(ds.Sem))
		}, true
	default:
		return nil, false
	}
}

// writeSaturated sheds load when the semaphore is full: 503 overloaded by
// default, or the 202 pending body when BACKPRESSURE_MODE=pending (so a client
// can poll instead of seeing an error).
func (s *Server) writeSaturated(w http.ResponseWriter, r *http.Request) {
	if s.cfg.BackpressureMode == config.BackpressurePending {
		writeJSON(w, http.StatusAccepted, pendingResponse{Status: "pending", RetryAfter: s.cfg.RetryAfter})
		return
	}
	middleware.WriteError(w, r, http.StatusServiceUnavailable, middleware.CodeOverloaded, "service overloaded, retry shortly")
}

// writeProxyError maps a proxy failure Kind to the client-facing status, always
// in the normalized envelope so no downstream framework error leaks through.
func (s *Server) writeProxyError(w http.ResponseWriter, r *http.Request, e *proxy.Error) {
	switch e.Kind {
	case proxy.KindUpstream4xx:
		middleware.WriteError(w, r, http.StatusBadRequest, middleware.CodeBadRequest, "request rejected by inference service")
	case proxy.KindCanceled:
		// The caller went away; a body is moot. Record an accurate status.
		w.WriteHeader(499)
	case proxy.KindEncode:
		middleware.WriteError(w, r, http.StatusInternalServerError, middleware.CodeInternalError, "internal error")
	default: // Timeout, Network, Upstream5xx, Decode
		middleware.WriteError(w, r, http.StatusBadGateway, middleware.CodeUpstreamError, "inference service unavailable")
	}
}

// decodeJSON reads a size-limited JSON body into v, writing a 400 and returning
// false on any decode failure.
func decodeJSON(w http.ResponseWriter, r *http.Request, maxBytes int64, v any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, maxBytes)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		middleware.WriteError(w, r, http.StatusBadRequest, middleware.CodeBadRequest, "invalid JSON body")
		return false
	}
	return true
}
