// Package ratelimit enforces per-API-key request limits.
//
// Design pattern — token bucket (via golang.org/x/time/rate): each key gets its
// own bucket sized by its tier (free: 2 req/s, burst 5; pro: 20 req/s, burst
// 50, per docs/api.md). The bucket refills continuously, so a client may burst
// up to `burst` then is throttled to the steady rate — exactly the behavior an
// API-rate-limit header (Retry-After) describes. Buckets are created lazily and
// kept per key, isolated from one another (one noisy free key cannot spend a
// pro key's allowance).
//
// Documented limitation — monthly quotas are in-memory counters: without a
// datastore they reset when the process restarts, so only the req/s + burst
// rate is actually guaranteed across restarts. This is an accepted scope
// decision (see docs/api.md); a shared counter (Redis/DynamoDB) behind the same
// interface would make quotas durable without changing callers.
package ratelimit

import (
	"math"
	"net/http"
	"strconv"
	"sync"
	"time"

	"golang.org/x/time/rate"

	"vigil-api/gateway/internal/auth"
	"vigil-api/gateway/internal/middleware"
)

// TierLimit is the token-bucket configuration for a tier.
type TierLimit struct {
	RPS   rate.Limit
	Burst int
}

// DefaultTierLimits returns the req/s + burst limits fixed by the API contract.
func DefaultTierLimits() map[auth.Tier]TierLimit {
	return map[auth.Tier]TierLimit{
		auth.TierFree: {RPS: 2, Burst: 5},
		auth.TierPro:  {RPS: 20, Burst: 50},
	}
}

// DefaultTierQuotas returns the monthly request quotas fixed by the contract.
func DefaultTierQuotas() map[auth.Tier]int {
	return map[auth.Tier]int{
		auth.TierFree: 1000,
		auth.TierPro:  100000,
	}
}

// monthCounter is a per-key monotonic counter for the current calendar month.
type monthCounter struct {
	mu    sync.Mutex
	month int // year*12 + month, to detect rollover
	count int
}

// allow reports whether one more request fits under limit this month, and
// consumes a slot if so. It does not consume a slot on rejection.
func (m *monthCounter) allow(now time.Time, limit int) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	cur := int(now.Year())*12 + int(now.Month())
	if cur != m.month {
		m.month = cur
		m.count = 0
	}
	if m.count >= limit {
		return false
	}
	m.count++
	return true
}

// keyState bundles a key's rate bucket and monthly quota counter.
type keyState struct {
	limiter *rate.Limiter
	quota   *monthCounter
}

// Limiter tracks per-key state and enforces tier limits.
type Limiter struct {
	mu     sync.Mutex
	states map[string]*keyState
	limits map[auth.Tier]TierLimit
	quotas map[auth.Tier]int
	now    func() time.Time
}

// New builds a Limiter from tier limits and monthly quotas.
func New(limits map[auth.Tier]TierLimit, quotas map[auth.Tier]int) *Limiter {
	return &Limiter{
		states: make(map[string]*keyState),
		limits: limits,
		quotas: quotas,
		now:    time.Now,
	}
}

// stateFor returns the (lazily created) per-key state for a key/tier.
func (l *Limiter) stateFor(key string, tier auth.Tier) *keyState {
	l.mu.Lock()
	defer l.mu.Unlock()
	if st, ok := l.states[key]; ok {
		return st
	}
	tl := l.limits[tier]
	st := &keyState{
		limiter: rate.NewLimiter(tl.RPS, tl.Burst),
		quota:   &monthCounter{},
	}
	l.states[key] = st
	return st
}

// Middleware enforces the rate and quota for the authenticated caller. It must
// run after the auth middleware; if no identity is present it fails closed with
// a 401.
func (l *Limiter) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, ok := auth.IdentityFromContext(r.Context())
		if !ok {
			middleware.WriteError(w, r, http.StatusUnauthorized, middleware.CodeUnauthorized, "unauthenticated")
			return
		}
		st := l.stateFor(id.Key, id.Tier)

		// Rate check: Reserve so we can compute an accurate Retry-After; cancel
		// the reservation on rejection so a throttled request doesn't burn a
		// future token.
		res := st.limiter.Reserve()
		if delay := res.Delay(); delay > 0 {
			res.Cancel()
			retryAfter := int(math.Ceil(delay.Seconds()))
			if retryAfter < 1 {
				retryAfter = 1
			}
			w.Header().Set("Retry-After", strconv.Itoa(retryAfter))
			middleware.WriteError(w, r, http.StatusTooManyRequests, middleware.CodeRateLimited, "rate limit exceeded")
			return
		}

		// Monthly quota check (in-memory; see package doc).
		if !st.quota.allow(l.now(), l.quotas[id.Tier]) {
			middleware.WriteError(w, r, http.StatusTooManyRequests, middleware.CodeQuotaExceeded, "monthly quota exceeded")
			return
		}

		next.ServeHTTP(w, r)
	})
}
