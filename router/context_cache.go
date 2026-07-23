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
// responses. Keyed on {property_rid, placement_id, provider_id,
// seller_agent_url, country}.
//
// The spec's recommended cache key at §Caching lists only the first
// three components. The additional seller and country dimensions are
// added because this repository's own targeting engine
// (targeting/engine.go: ActivePackages(ctx, canonicalSeller,
// propertyID, country, placementID, ...)) scopes the active package
// set per seller and per country — different sellers or geos on the
// same placement return different offers. Keying only on placement
// would let one seller's cached offers be served to another seller
// on the same placement during the TTL window, disclosing competitor
// brands, pricing, and creative manifests across tenants.
// seller_agent_url is compared using the AdCP URL canonicalization
// rules (urlcanon.Canonicalize), the same normalization the engine
// applies before its lookup.
//
// Responses are deeply cloned on read so callers can freely mutate
// Offer pointer/slice/map members without corrupting the cached
// entry. (Nested any values inside Signals stay shared — see the
// note on cloneContextResponse.)
//
// Spec cache_ttl semantics (see Put for the enforcement code):
//
//   - absent (nil)   → router uses its configured default TTL
//   - explicit 0     → provider is disabling caching; entry not stored
//   - explicit > 0   → override, clamped to MaxContextCacheTTL
//
// The tri-state depends on tmproto.ContextMatchResponse.CacheTTL being
// a pointer type so absent-field is distinguishable from present-zero
// (docs/sdk-typing-policy.md).
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
	// maxEntries caps the number of live entries; 0 disables the cap.
	// When the cap is hit on Put, expired entries are swept; if still
	// full, the entry with the oldest insertedAt is evicted.
	maxEntries int
	metrics    ContextCacheMetrics

	// now is time.Now in production; tests substitute a clock so
	// expiration windows can be exercised without sleeping.
	now func() time.Time
}

type contextCacheEntry struct {
	response   *tmproto.ContextMatchResponse
	expiresAt  time.Time
	insertedAt time.Time
}

// ContextCacheOption configures the cache.
type ContextCacheOption func(*ContextCache)

// WithContextCacheMetrics installs a metrics sink. Without this the
// cache runs with a no-op sink.
func WithContextCacheMetrics(m ContextCacheMetrics) ContextCacheOption {
	return func(c *ContextCache) { c.metrics = m }
}

// WithContextCacheMaxEntries caps the number of live entries. Zero or
// negative values disable the cap. On Put once the cap is hit the
// cache sweeps expired entries first, then evicts the oldest insert
// if the cache is still full — bounding memory against a caller that
// varies placement/seller/country to grow the working set forever.
func WithContextCacheMaxEntries(n int) ContextCacheOption {
	return func(c *ContextCache) {
		if n < 0 {
			n = 0
		}
		c.maxEntries = n
	}
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
//
// sellerAgentURL and country participate in the key so a request from
// one seller never returns a response the router cached for another
// seller (see the ContextCache doc for the rationale). Callers should
// pass sellerAgentURL already normalized via urlcanon.Canonicalize —
// the cache does not canonicalize on the hot path.
func (c *ContextCache) Get(propertyRID, placementID, providerID, sellerAgentURL, country string) (*tmproto.ContextMatchResponse, bool) {
	if c == nil {
		return nil, false
	}
	key := contextCacheKey(propertyRID, placementID, providerID, sellerAgentURL, country)
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
// is derived from the response's cache_ttl per spec §Caching:
//
//   - cache_ttl absent (nil pointer) → use the cache's configured
//     default TTL (5 min out of the box).
//   - cache_ttl == 0                  → provider is disabling caching
//     (e.g. after a targeting-config change). The entry is NOT stored;
//     subsequent requests fan out live until the provider raises the
//     TTL again.
//   - cache_ttl > 0                   → override, clamped to
//     MaxContextCacheTTL. Clamping happens in seconds first to avoid
//     a Duration multiplication overflowing int64 for pathologically
//     large values that escaped upstream schema validation.
func (c *ContextCache) Put(propertyRID, placementID, providerID, sellerAgentURL, country string, resp *tmproto.ContextMatchResponse) {
	if c == nil || resp == nil {
		return
	}
	ttl := c.defaultTTL
	if resp.CacheTTL != nil {
		secs := *resp.CacheTTL
		switch {
		case secs == 0:
			// Explicit disable — do not cache.
			return
		case secs < 0:
			// Nonsensical; fall back to the default rather than store
			// something with a negative TTL that would collapse to
			// already-expired.
		default:
			maxSecs := int(MaxContextCacheTTL / time.Second)
			if secs > maxSecs {
				secs = maxSecs
			}
			ttl = time.Duration(secs) * time.Second
		}
	}
	key := contextCacheKey(propertyRID, placementID, providerID, sellerAgentURL, country)
	now := c.now()
	c.mu.Lock()
	// Bound the map. Only enforce when writing a NEW key — an
	// overwrite doesn't grow the set. Sweep expired first (cheap;
	// removes stale entries the caller has already forgotten about),
	// then evict the oldest insert if still full.
	if c.maxEntries > 0 {
		if _, existing := c.entries[key]; !existing && len(c.entries) >= c.maxEntries {
			c.sweepExpiredLocked(now)
			if len(c.entries) >= c.maxEntries {
				c.evictOldestLocked()
			}
		}
	}
	c.entries[key] = contextCacheEntry{
		response:   cloneContextResponse(resp),
		expiresAt:  now.Add(ttl),
		insertedAt: now,
	}
	c.mu.Unlock()
}

// sweepExpiredLocked removes any entry whose TTL has elapsed. Caller
// holds c.mu.
func (c *ContextCache) sweepExpiredLocked(now time.Time) {
	for k, e := range c.entries {
		if now.After(e.expiresAt) {
			delete(c.entries, k)
		}
	}
}

// evictOldestLocked drops the entry with the oldest insertedAt. O(N)
// but only runs on the cap-hit path, and N is bounded by the
// operator-configured cap. Caller holds c.mu.
func (c *ContextCache) evictOldestLocked() {
	var oldestKey string
	var oldestAt time.Time
	first := true
	for k, e := range c.entries {
		if first || e.insertedAt.Before(oldestAt) {
			oldestKey = k
			oldestAt = e.insertedAt
			first = false
		}
	}
	if !first {
		delete(c.entries, oldestKey)
	}
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
// with it. tmproto request validation rejects control bytes in
// property_rid and placement_id, and every component ultimately
// originates from a JSON string (which cannot contain a raw NUL under
// the JSON grammar), so no component can carry NUL by construction.
func contextCacheKey(propertyRID, placementID, providerID, sellerAgentURL, country string) string {
	var b strings.Builder
	b.Grow(len(propertyRID) + len(placementID) + len(providerID) + len(sellerAgentURL) + len(country) + 4)
	b.WriteString(propertyRID)
	b.WriteByte(0)
	b.WriteString(placementID)
	b.WriteByte(0)
	b.WriteString(providerID)
	b.WriteByte(0)
	b.WriteString(sellerAgentURL)
	b.WriteByte(0)
	b.WriteString(country)
	return b.String()
}

// cloneContextResponse copies the response deeply enough that the
// merger, and any future seller-agent-stamper or macro-injector code
// path, can freely mutate the returned Offer entries without
// corrupting the cache. tmproto.Offer carries several pointer/slice
// fields that would otherwise be shared:
//
//   - SellerAgent, Brand   (json.RawMessage — byte slice)
//   - CreativeManifest     (*json.RawMessage)
//   - Price                (*OfferPrice)
//   - Macros               (map[string]string)
//
// The schema doc on tmproto.Offer.SellerAgent explicitly says the
// router MAY stamp that field from a cached package→seller map —
// once that stamp lands, a shallow clone would silently corrupt cache
// entries. Deep-clone here eliminates that failure mode.
//
// Isolation NOT provided for Signals nested values: the top-level
// map[string]any is a fresh allocation, but nested map/slice values
// stay shared with the cached entry. Nothing in the merger mutates
// them today; a general deep-copy of arbitrary any values would need
// a JSON round-trip (types aren't statically knowable). The
// ContextCache docstring calls this out.
func cloneContextResponse(src *tmproto.ContextMatchResponse) *tmproto.ContextMatchResponse {
	if src == nil {
		return nil
	}
	dst := *src
	// CacheTTL is *int; the shallow struct copy above shares the
	// pointer with the cached entry. Give the caller its own
	// allocation so `*resp.CacheTTL = 0` on a returned hit cannot
	// silently flip the cached entry's disable-caching semantics.
	if src.CacheTTL != nil {
		v := *src.CacheTTL
		dst.CacheTTL = &v
	}
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
