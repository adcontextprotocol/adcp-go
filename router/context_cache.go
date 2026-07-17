package router

import (
	"encoding/json"
	"maps"
	"strings"
	"sync"
	"time"

	"github.com/adcontextprotocol/adcp-go/tmproto"
)

// DefaultContextCacheTTL is the router's default cache lifetime for a
// per-provider Context Match response, per spec §Caching. Providers can
// override it via ContextMatchResponse.CacheTTL.
const DefaultContextCacheTTL = 5 * time.Minute

// MaxContextCacheTTL is the schema-enforced ceiling on provider-supplied
// cache_ttl (spec §Caching: "schema-enforced maximum is 86400 seconds").
// The router clamps here as a defense-in-depth in case a future provider
// sends a value that escaped upstream validation.
const MaxContextCacheTTL = 24 * time.Hour

// ContextCacheMetrics is the observability hook for the per-provider
// Context Match cache. Deployments wire this through prommetrics (or
// noop) via WithContextCache. Bounded labels only — providerID is the
// stable configured identifier, not user input.
type ContextCacheMetrics interface {
	IncHit(providerID string)
	IncMiss(providerID string)
}

// noopContextCacheMetrics is used when the caller does not supply one.
type noopContextCacheMetrics struct{}

func (noopContextCacheMetrics) IncHit(string)  {}
func (noopContextCacheMetrics) IncMiss(string) {}

// ContextCache is an in-memory, per-provider cache of Context Match
// responses. Keyed on {property_rid, placement_id, provider_id} per
// spec §Caching. Responses are cloned on read so callers can freely
// overwrite RequestID with the current request's value; that's the
// only field that varies between the cached call and the reuse.
//
// The router is stateless and horizontally scaled, so this cache is
// per-instance — restarts clear it and instances behind a load balancer
// each maintain their own view. That matches how the reference agents
// deploy (no shared cache) and keeps the router deployment story simple
// (no Redis dependency).
type ContextCache struct {
	mu         sync.Mutex
	entries    map[string]contextCacheEntry
	defaultTTL time.Duration
	metrics    ContextCacheMetrics

	// now is time.Now in production; tests substitute a clock so
	// expiration windows can be exercised without sleeping.
	now func() time.Time
}

type contextCacheEntry struct {
	response  *tmproto.ContextMatchResponse
	expiresAt time.Time
}

// ContextCacheOption configures the cache.
type ContextCacheOption func(*ContextCache)

// WithContextCacheMetrics installs a metrics sink. Without this the
// cache runs with a no-op sink.
func WithContextCacheMetrics(m ContextCacheMetrics) ContextCacheOption {
	return func(c *ContextCache) { c.metrics = m }
}

// NewContextCache builds a cache with the given default TTL applied
// whenever a provider response omits or zeroes cache_ttl. TTLs of zero
// or less collapse to DefaultContextCacheTTL — a caller that truly
// wants caching disabled should skip constructing the cache and not
// wire it into the router (or pass WithContextCache(nil)).
func NewContextCache(defaultTTL time.Duration, opts ...ContextCacheOption) *ContextCache {
	if defaultTTL <= 0 {
		defaultTTL = DefaultContextCacheTTL
	}
	c := &ContextCache{
		entries:    make(map[string]contextCacheEntry),
		defaultTTL: defaultTTL,
		metrics:    noopContextCacheMetrics{},
		now:        time.Now,
	}
	for _, o := range opts {
		o(c)
	}
	return c
}

// Get looks up a cached response. Returns (nil, false) on miss or
// expiration; the caller falls back to a live fan-out call. The
// returned response is a defensive copy — callers may overwrite
// RequestID or Signals without corrupting the cached entry.
func (c *ContextCache) Get(propertyRID, placementID, providerID string) (*tmproto.ContextMatchResponse, bool) {
	if c == nil {
		return nil, false
	}
	key := contextCacheKey(propertyRID, placementID, providerID)
	c.mu.Lock()
	entry, ok := c.entries[key]
	if !ok {
		c.mu.Unlock()
		c.metrics.IncMiss(providerID)
		return nil, false
	}
	if c.now().After(entry.expiresAt) {
		delete(c.entries, key)
		c.mu.Unlock()
		c.metrics.IncMiss(providerID)
		return nil, false
	}
	c.mu.Unlock()
	c.metrics.IncHit(providerID)
	return cloneContextResponse(entry.response), true
}

// Put stores a response under the spec's canonical cache key. The TTL
// is derived from the response's cache_ttl (clamped to
// [1s, MaxContextCacheTTL]) when positive, otherwise from the cache's
// configured default.
//
// A cache_ttl of zero from a provider means either (a) the field was
// truly absent (Go's omitempty conflates absent with zero) or (b) the
// provider explicitly disabled caching (spec §Caching: "0 disables
// caching"). Because the Go type can't distinguish these two cases and
// the majority use is (a), we treat received-zero as "use default".
// Providers that need to force cache invalidation should return
// cache_ttl=1 (nearly-disabled) until their config change propagates.
func (c *ContextCache) Put(propertyRID, placementID, providerID string, resp *tmproto.ContextMatchResponse) {
	if c == nil || resp == nil {
		return
	}
	ttl := c.defaultTTL
	if resp.CacheTTL > 0 {
		ttl = min(time.Duration(resp.CacheTTL)*time.Second, MaxContextCacheTTL)
	}
	key := contextCacheKey(propertyRID, placementID, providerID)
	c.mu.Lock()
	c.entries[key] = contextCacheEntry{
		response:  cloneContextResponse(resp),
		expiresAt: c.now().Add(ttl),
	}
	c.mu.Unlock()
}

// Size returns the number of live entries. Includes entries whose TTL
// has expired but which have not been evicted yet — used mostly by
// tests and operational metrics, not by hot-path logic.
func (c *ContextCache) Size() int {
	if c == nil {
		return 0
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.entries)
}

// contextCacheKey assembles the canonical cache key. NUL is used as
// the separator so a component containing "|" or "/" cannot collide
// with the separator (property_rid is a UUID and placement_id is a
// bounded string — neither can contain NUL by construction).
func contextCacheKey(propertyRID, placementID, providerID string) string {
	var b strings.Builder
	b.Grow(len(propertyRID) + len(placementID) + len(providerID) + 2)
	b.WriteString(propertyRID)
	b.WriteByte(0)
	b.WriteString(placementID)
	b.WriteByte(0)
	b.WriteString(providerID)
	return b.String()
}

// cloneContextResponse copies the response deeply enough that any
// downstream code path (merger, seller-agent stamper, macro injector)
// can freely mutate the returned value without corrupting the cached
// entry. tmproto.Offer carries several pointer/slice fields that would
// otherwise be shared:
//
//   - SellerAgent, Brand   (json.RawMessage — byte slice)
//   - CreativeManifest     (*json.RawMessage)
//   - Price                (*OfferPrice)
//   - Macros               (map[string]string)
//
// tmproto/types_gen.go:196 explicitly says "the router MAY stamp
// SellerAgent from its cached package→seller map" — the moment a
// caller wires that stamp, a shallow clone would silently corrupt
// cache entries. Deep-clone eliminates that failure mode upfront.
func cloneContextResponse(src *tmproto.ContextMatchResponse) *tmproto.ContextMatchResponse {
	if src == nil {
		return nil
	}
	dst := *src
	if len(src.Offers) > 0 {
		dst.Offers = make([]tmproto.Offer, len(src.Offers))
		for i := range src.Offers {
			dst.Offers[i] = cloneOffer(src.Offers[i])
		}
	}
	if len(src.Signals) > 0 {
		dst.Signals = make(map[string]any, len(src.Signals))
		maps.Copy(dst.Signals, src.Signals)
	}
	return &dst
}

// cloneOffer duplicates every pointer/slice/map on Offer so mutation
// through a cache-hit copy cannot reach the cached entry.
func cloneOffer(src tmproto.Offer) tmproto.Offer {
	dst := src // scalar fields (PackageID, Summary) copy by value
	if src.SellerAgent != nil {
		dst.SellerAgent = append(json.RawMessage(nil), src.SellerAgent...)
	}
	if src.Brand != nil {
		dst.Brand = append(json.RawMessage(nil), src.Brand...)
	}
	if src.Price != nil {
		p := *src.Price
		dst.Price = &p
	}
	if src.CreativeManifest != nil {
		cm := append(json.RawMessage(nil), *src.CreativeManifest...)
		dst.CreativeManifest = &cm
	}
	if len(src.Macros) > 0 {
		dst.Macros = make(map[string]string, len(src.Macros))
		maps.Copy(dst.Macros, src.Macros)
	}
	return dst
}
