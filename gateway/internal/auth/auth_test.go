package auth

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"vigil-api/gateway/internal/middleware"
)

func TestParseKeys(t *testing.T) {
	tests := []struct {
		name    string
		raw     string
		want    map[string]Tier
		wantErr bool
	}{
		{name: "valid pair", raw: "dev-free:free,dev-pro:pro",
			want: map[string]Tier{"dev-free": TierFree, "dev-pro": TierPro}},
		{name: "whitespace tolerated", raw: " a:free , b:pro ",
			want: map[string]Tier{"a": TierFree, "b": TierPro}},
		{name: "trailing comma", raw: "a:free,",
			want: map[string]Tier{"a": TierFree}},
		{name: "empty", raw: "", wantErr: true},
		{name: "missing tier", raw: "a", wantErr: true},
		{name: "empty key", raw: ":free", wantErr: true},
		{name: "unknown tier", raw: "a:gold", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseKeys(tt.raw)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("expected error for %q", tt.raw)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if len(got) != len(tt.want) {
				t.Fatalf("got %v, want %v", got, tt.want)
			}
			for k, v := range tt.want {
				if got[k] != v {
					t.Errorf("key %q: got %q, want %q", k, got[k], v)
				}
			}
		})
	}
}

func protected() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, ok := IdentityFromContext(r.Context())
		if !ok {
			t := "no identity in context"
			http.Error(w, t, http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]string{"tier": string(id.Tier), "key": id.Key})
	})
}

func TestMiddleware(t *testing.T) {
	a := New(map[string]Tier{"good": TierPro})
	h := middleware.Chain(protected(), middleware.RequestID, a.Middleware)

	tests := []struct {
		name       string
		key        string
		setHeader  bool
		wantStatus int
		wantTier   string
	}{
		{name: "valid key", key: "good", setHeader: true, wantStatus: http.StatusOK, wantTier: "pro"},
		{name: "unknown key", key: "bad", setHeader: true, wantStatus: http.StatusUnauthorized},
		{name: "missing key", setHeader: false, wantStatus: http.StatusUnauthorized},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/v1/moderate/text", nil)
			if tt.setHeader {
				req.Header.Set(APIKeyHeader, tt.key)
			}
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)

			if rec.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d", rec.Code, tt.wantStatus)
			}
			if tt.wantStatus == http.StatusOK {
				var body map[string]string
				if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
					t.Fatalf("bad json: %v", err)
				}
				if body["tier"] != tt.wantTier {
					t.Errorf("tier = %q, want %q", body["tier"], tt.wantTier)
				}
			} else {
				var body middleware.ErrorResponse
				if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
					t.Fatalf("bad error json: %v", err)
				}
				if body.Error.Code != middleware.CodeUnauthorized {
					t.Errorf("code = %q, want %q", body.Error.Code, middleware.CodeUnauthorized)
				}
			}
		})
	}
}
