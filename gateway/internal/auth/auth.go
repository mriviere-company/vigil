// Package auth authenticates callers by API key and resolves their tier
// (free/pro). Keys live in configuration (env), not a database — a documented
// scope decision for this project, replaceable by a DynamoDB lookup behind the
// same Authenticator interface without touching callers.
package auth

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"vigil-api/gateway/internal/middleware"
)

// Tier is a caller's rate-limit tier.
type Tier string

const (
	TierFree Tier = "free"
	TierPro  Tier = "pro"
)

// APIKeyHeader is the header carrying the caller's key.
const APIKeyHeader = "X-API-Key"

// Identity is the authenticated caller: their key (used to key rate-limit
// buckets) and resolved tier. The key is never logged.
type Identity struct {
	Key  string
	Tier Tier
}

type ctxKey int

const identityKey ctxKey = iota

// WithIdentity returns a context carrying the authenticated identity.
func WithIdentity(ctx context.Context, id Identity) context.Context {
	return context.WithValue(ctx, identityKey, id)
}

// IdentityFromContext returns the authenticated identity and whether one is
// present. Handlers behind the auth middleware can rely on ok == true.
func IdentityFromContext(ctx context.Context) (Identity, bool) {
	id, ok := ctx.Value(identityKey).(Identity)
	return id, ok
}

// ParseKeys parses the API_KEYS env format "key1:free,key2:pro" into a map.
// Whitespace around entries is tolerated; empty input yields an error since a
// gateway with no valid keys would reject every request.
func ParseKeys(raw string) (map[string]Tier, error) {
	keys := make(map[string]Tier)
	for _, entry := range strings.Split(raw, ",") {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		key, tierStr, ok := strings.Cut(entry, ":")
		key = strings.TrimSpace(key)
		tierStr = strings.TrimSpace(tierStr)
		if !ok || key == "" || tierStr == "" {
			return nil, fmt.Errorf("invalid API_KEYS entry %q (want key:tier)", entry)
		}
		tier := Tier(tierStr)
		if tier != TierFree && tier != TierPro {
			return nil, fmt.Errorf("invalid tier %q for key (want free or pro)", tierStr)
		}
		keys[key] = tier
	}
	if len(keys) == 0 {
		return nil, fmt.Errorf("no API keys configured")
	}
	return keys, nil
}

// Authenticator validates keys against an immutable in-memory map.
type Authenticator struct {
	keys map[string]Tier
}

// New builds an Authenticator over a copy of keys so the caller can't mutate
// it afterwards.
func New(keys map[string]Tier) *Authenticator {
	cp := make(map[string]Tier, len(keys))
	for k, v := range keys {
		cp[k] = v
	}
	return &Authenticator{keys: cp}
}

// Resolve returns the tier for a key and whether it is known.
func (a *Authenticator) Resolve(key string) (Tier, bool) {
	tier, ok := a.keys[key]
	return tier, ok
}

// Middleware authenticates the request. A missing or unknown X-API-Key yields
// a 401 with the normalized error body; a valid key attaches the Identity to
// the context and records the tier on the access-log fields.
func (a *Authenticator) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := r.Header.Get(APIKeyHeader)
		if key == "" {
			middleware.WriteError(w, r, http.StatusUnauthorized, middleware.CodeUnauthorized, "missing API key")
			return
		}
		tier, ok := a.Resolve(key)
		if !ok {
			middleware.WriteError(w, r, http.StatusUnauthorized, middleware.CodeUnauthorized, "invalid API key")
			return
		}
		middleware.SetTier(r.Context(), string(tier))
		ctx := WithIdentity(r.Context(), Identity{Key: key, Tier: tier})
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}
