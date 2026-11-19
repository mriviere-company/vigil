package ratelimit

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"golang.org/x/time/rate"

	"vigil-api/gateway/internal/auth"
	"vigil-api/gateway/internal/middleware"
)

// harness wires auth identity into the context then runs the rate-limit
// middleware, without a real auth key store.
func harness(l *Limiter, id auth.Identity) http.Handler {
	ok := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	injectID := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			next.ServeHTTP(w, r.WithContext(auth.WithIdentity(r.Context(), id)))
		})
	}
	return middleware.Chain(ok, middleware.RequestID, injectID, l.Middleware)
}

func do(h http.Handler) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/moderate/text", nil))
	return rec
}

func TestBurstExhaustionThen429(t *testing.T) {
	// Free tier: burst 5. The 6th immediate request must be throttled.
	l := New(map[auth.Tier]TierLimit{auth.TierFree: {RPS: 2, Burst: 5}},
		map[auth.Tier]int{auth.TierFree: 1_000_000})
	h := harness(l, auth.Identity{Key: "k1", Tier: auth.TierFree})

	for i := 0; i < 5; i++ {
		if rec := do(h); rec.Code != http.StatusOK {
			t.Fatalf("request %d: status %d, want 200", i+1, rec.Code)
		}
	}
	rec := do(h)
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("6th request: status %d, want 429", rec.Code)
	}
	if ra := rec.Header().Get("Retry-After"); ra == "" {
		t.Fatal("missing Retry-After header on 429")
	}
	var body middleware.ErrorResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("bad json: %v", err)
	}
	if body.Error.Code != middleware.CodeRateLimited {
		t.Errorf("code = %q, want %q", body.Error.Code, middleware.CodeRateLimited)
	}
}

func TestTierIsolationBetweenKeys(t *testing.T) {
	// Two keys share a Limiter; exhausting one must not throttle the other.
	l := New(map[auth.Tier]TierLimit{auth.TierFree: {RPS: 1, Burst: 2}},
		map[auth.Tier]int{auth.TierFree: 1_000_000})

	hot := harness(l, auth.Identity{Key: "hot", Tier: auth.TierFree})
	cold := harness(l, auth.Identity{Key: "cold", Tier: auth.TierFree})

	// Drain "hot".
	do(hot)
	do(hot)
	if rec := do(hot); rec.Code != http.StatusTooManyRequests {
		t.Fatalf("hot key not throttled: %d", rec.Code)
	}
	// "cold" still has its full burst.
	if rec := do(cold); rec.Code != http.StatusOK {
		t.Fatalf("cold key wrongly throttled: %d", rec.Code)
	}
}

func TestMonthlyQuotaExceeded(t *testing.T) {
	// Generous rate, tiny quota: the quota, not the rate, must reject.
	l := New(map[auth.Tier]TierLimit{auth.TierPro: {RPS: rate.Inf, Burst: 1}},
		map[auth.Tier]int{auth.TierPro: 3})
	h := harness(l, auth.Identity{Key: "pk", Tier: auth.TierPro})

	for i := 0; i < 3; i++ {
		if rec := do(h); rec.Code != http.StatusOK {
			t.Fatalf("request %d: status %d, want 200", i+1, rec.Code)
		}
	}
	rec := do(h)
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("4th request: status %d, want 429", rec.Code)
	}
	var body middleware.ErrorResponse
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	if body.Error.Code != middleware.CodeQuotaExceeded {
		t.Errorf("code = %q, want %q", body.Error.Code, middleware.CodeQuotaExceeded)
	}
}

func TestMonthlyQuotaRollsOverAcrossMonths(t *testing.T) {
	l := New(map[auth.Tier]TierLimit{auth.TierPro: {RPS: rate.Inf, Burst: 1}},
		map[auth.Tier]int{auth.TierPro: 1})
	now := time.Date(2026, time.January, 15, 0, 0, 0, 0, time.UTC)
	l.now = func() time.Time { return now }
	h := harness(l, auth.Identity{Key: "pk", Tier: auth.TierPro})

	if rec := do(h); rec.Code != http.StatusOK {
		t.Fatalf("jan req1: %d", rec.Code)
	}
	if rec := do(h); rec.Code != http.StatusTooManyRequests {
		t.Fatalf("jan req2 should exceed quota: %d", rec.Code)
	}
	// Advance a month: the counter resets.
	now = time.Date(2026, time.February, 1, 0, 0, 0, 0, time.UTC)
	if rec := do(h); rec.Code != http.StatusOK {
		t.Fatalf("feb req1 should be allowed after rollover: %d", rec.Code)
	}
}

func TestMissingIdentityFailsClosed(t *testing.T) {
	l := New(DefaultTierLimits(), DefaultTierQuotas())
	ok := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	h := middleware.Chain(ok, middleware.RequestID, l.Middleware)
	if rec := do(h); rec.Code != http.StatusUnauthorized {
		t.Fatalf("missing identity: status %d, want 401", rec.Code)
	}
}
