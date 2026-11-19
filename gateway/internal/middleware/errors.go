package middleware

import (
	"encoding/json"
	"net/http"
)

// Normalized error envelope. Every non-2xx the gateway produces itself (401,
// 400, 429, 500, 502, 503) uses this single shape so clients parse one error
// format and never see a downstream framework's error body leak through. The
// schema is documented in docs/api.md.

// Machine-readable error codes. Kept as a small closed set so clients can
// switch on them.
const (
	CodeUnauthorized   = "unauthorized"
	CodeBadRequest     = "bad_request"
	CodeRateLimited    = "rate_limited"
	CodeQuotaExceeded  = "quota_exceeded"
	CodeUpstreamError  = "upstream_error"
	CodeOverloaded     = "overloaded"
	CodeInternalError  = "internal_error"
	CodeNotFound       = "not_found"
	CodeMethodNotAllow = "method_not_allowed"
)

// ErrorBody is the inner object of the normalized error envelope.
type ErrorBody struct {
	Code      string `json:"code"`
	Message   string `json:"message"`
	RequestID string `json:"request_id"`
}

// ErrorResponse is the top-level normalized error envelope.
type ErrorResponse struct {
	Error ErrorBody `json:"error"`
}

// WriteError renders the normalized error envelope with the given status and
// machine code, pulling the request id from the request context so every error
// is correlatable with the access log and downstream calls.
func WriteError(w http.ResponseWriter, r *http.Request, status int, code, message string) {
	body := ErrorResponse{Error: ErrorBody{
		Code:      code,
		Message:   message,
		RequestID: RequestIDFromContext(r.Context()),
	}}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	// Best-effort: if the client has already gone the encode may fail; there
	// is nothing useful left to do with that error.
	_ = json.NewEncoder(w).Encode(body)
}
