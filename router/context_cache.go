package router

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"maps"
	"sync"
	"time"

	"github.com/adcontextprotocol/adcp-go/tmproto"
)

// DefaultContextCacheTTL is the router's default cache lifetime for a
// per-provider Context Match response, per spec §Caching. Providers can
// override it via ProviderContextMatchResponse.CacheTTL. AdCP 3.2 moved
// cache_ttl from the shared router-hop response to the provider-hop
// response only — the field is emitted by providers and consumed by
// the router; it never appears on the merged router→publisher body.
const DefaultContextCacheTTL = 5 * time.Minute

// MaxContextCacheTTL is the schema-enforced ceiling on provider-supplied
// cache_ttl (spec §Caching: "schema-enforced maximum is 86400 seconds").
// The router clamps here as a defense-in-depth in case a future provider
// sends a value that escaped upstream validation.
const MaxContextCacheTTL = 24 * time.Hour

// MaxContextCacheNamespaceBytes bounds trusted opaque generation tokens. The
// token is never retained or logged, but bounding it prevents a broken dynamic
// resolver or config from turning HMAC input into an unbounded allocation/CPU
// surface.
const MaxContextCacheNamespaceBytes = 256

const maxContextCacheGenerationsPerProvider = 1024

func validContextCacheNamespace(namespace string) bool {
	if namespace == "" || len(namespace) > MaxContextCacheNamespaceBytes {
		return false
	}
	for i := range len(namespace) {
		if namespace[i] < 0x21 || namespace[i] > 0x7e {
			return false
		}
	}
	return true
}

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

// ContextCache is an in-memory, per-provider cache of Context Match responses.
// Its unambiguous struct key is {provider_id, cache_namespace, context_hash}.
// context_hash covers the complete provider-forwarded request except $schema
// and request_id; cache_namespace covers trusted result-affecting state outside
// that request.
//
// Namespace material and request preimages are never retained. The cache uses
// a process-random HMAC key to turn the opaque namespace plus provider
// evaluation config into a non-reversible local digest. Context hashes are
// already SHA-256 digests. A process restart therefore changes internal
// namespace keys and starts with an empty cache.
//
// Responses are deeply cloned on read so callers can freely mutate
// Offer pointer/slice/map members without corrupting the cached
// entry. (Nested any values inside Signals stay shared — see the
// note on cloneContextResponse.)
//
// Spec cache_ttl semantics (see PutScoped for the enforcement code):
//
//   - absent (nil)   → router uses its configured default TTL
//   - explicit 0     → provider is disabling caching; entry not stored
//   - explicit > 0   → override, clamped to MaxContextCacheTTL
//
// The tri-state depends on tmproto.ProviderContextMatchResponse.CacheTTL
// being a pointer type so absent-field is distinguishable from
// present-zero (docs/sdk-typing-policy.md).
//
// The router is stateless and horizontally scaled, so this cache is
// per-instance — restarts clear it and instances behind a load balancer
// each maintain their own view. That matches how the reference agents
// deploy (no shared cache) and keeps the router deployment story simple
// (no Redis dependency).
type ContextCache struct {
	mu         sync.Mutex
	entries    map[contextCacheKey]contextCacheEntry
	defaultTTL time.Duration
	secret     [sha256.Size]byte
	secretOK   bool
	generation uint64
	providers  map[string]*contextCacheProviderState
	// maxEntries caps the number of live entries; 0 disables the cap.
	// When the cap is hit on PutScoped, expired entries are swept; if still
	// full, the entry with the oldest insertedAt is evicted.
	maxEntries int
	metrics    ContextCacheMetrics

	// now is time.Now in production; tests substitute a clock so
	// expiration windows can be exercised without sleeping.
	now func() time.Time
}

type contextCacheEntry struct {
	response   *tmproto.ProviderContextMatchResponse
	expiresAt  time.Time
	insertedAt time.Time
}

type contextCacheKey struct {
	providerID string
	namespace  [sha256.Size]byte
	context    [sha256.Size]byte
}

type contextCacheProviderState struct {
	namespace  [sha256.Size]byte
	generation uint64
	blocked    bool
	exhausted  bool
	seen       map[[sha256.Size]byte]struct{}
}

// ContextCacheScope is an opaque snapshot of one provider's trusted cache
// namespace generation. Capture it once before lookup and retain that exact
// value through the outbound call and insertion. Its fields are intentionally
// private so callers cannot manufacture a valid generation.
type ContextCacheScope struct {
	providerID string
	namespace  [sha256.Size]byte
	generation uint64
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
// if the cache is still full — bounding memory against callers that
// vary context-hashed request fields to grow the working set forever.
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
		entries:    make(map[contextCacheKey]contextCacheEntry),
		defaultTTL: defaultTTL,
		metrics:    noopContextCacheMetrics{},
		now:        time.Now,
		providers:  make(map[string]*contextCacheProviderState),
	}
	n, err := rand.Read(c.secret[:])
	c.secretOK = err == nil && n == len(c.secret)
	for _, o := range opts {
		o(c)
	}
	return c
}

// Capture establishes a cache scope from an opaque deployment-controlled
// namespace and the trusted provider evaluation configuration. namespace must
// represent every result-affecting condition outside the request body,
// including outbound auth/tenant, authorization and entitlement revision,
// active-package/config generation, deployed model, and targeting rules.
// Callers MUST NOT use credentials, credential hashes, direct principal or
// tenant identifiers, request fields, viewer data, or Identity Match data.
//
// Empty namespaces and random-source failure bypass caching. A namespace value
// may never be reused after rotation: reuse blocks caching for that value and
// deletes the provider's entries, preventing stale generations from being
// resurrected. The raw inputs are HMACed immediately and never retained.
func (c *ContextCache) Capture(providerID, namespace string, providerEvaluationContext []byte) (ContextCacheScope, bool) {
	if c == nil || !c.secretOK || !validContextCacheNamespace(namespace) {
		return ContextCacheScope{}, false
	}
	digest := c.namespaceDigest(namespace, providerEvaluationContext)

	c.mu.Lock()
	defer c.mu.Unlock()
	state := c.providers[providerID]
	if state == nil {
		c.generation++
		state = &contextCacheProviderState{
			namespace:  digest,
			generation: c.generation,
			seen:       map[[sha256.Size]byte]struct{}{digest: {}},
		}
		c.providers[providerID] = state
	} else if state.namespace != digest {
		c.deleteProviderEntriesLocked(providerID)
		c.generation++
		if len(state.seen) >= maxContextCacheGenerationsPerProvider {
			// Never discard reuse history: doing so could resurrect an old
			// generation. Fail closed for this provider until process restart
			// rather than let trusted-but-broken rotation grow memory forever.
			state.generation = c.generation
			state.blocked = true
			state.exhausted = true
			return ContextCacheScope{}, false
		}
		_, reused := state.seen[digest]
		state.namespace = digest
		state.generation = c.generation
		state.blocked = reused
		state.seen[digest] = struct{}{}
	}
	if state.blocked || state.exhausted {
		return ContextCacheScope{}, false
	}
	return ContextCacheScope{providerID: providerID, namespace: digest, generation: state.generation}, true
}

func (c *ContextCache) namespaceDigest(namespace string, providerEvaluationContext []byte) [sha256.Size]byte {
	mac := hmac.New(sha256.New, c.secret[:])
	writeFramed(mac, []byte(namespace))
	writeFramed(mac, providerEvaluationContext)
	var digest [sha256.Size]byte
	copy(digest[:], mac.Sum(nil))
	return digest
}

type byteWriter interface{ Write([]byte) (int, error) }

func writeFramed(w byteWriter, value []byte) {
	var size [8]byte
	binary.BigEndian.PutUint64(size[:], uint64(len(value)))
	_, _ = w.Write(size[:])
	_, _ = w.Write(value)
}

func (c *ContextCache) deleteProviderEntriesLocked(providerID string) {
	for key := range c.entries {
		if key.providerID == providerID {
			delete(c.entries, key)
		}
	}
}

// Invalidate makes the current provider generation unusable and purges its
// entries. A subsequent Capture with the same namespace/evaluation digest is
// rejected as reuse; caching resumes only after a novel trusted generation is
// established. This is used whenever current validity cannot be determined.
func (c *ContextCache) Invalidate(providerID string) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if state := c.providers[providerID]; state != nil {
		c.generation++
		state.generation = c.generation
		state.blocked = true
		c.deleteProviderEntriesLocked(providerID)
	}
}

func (c *ContextCache) scopeCurrentLocked(scope ContextCacheScope) bool {
	state := c.providers[scope.providerID]
	return state != nil && !state.blocked && state.namespace == scope.namespace && state.generation == scope.generation
}

// GetScoped looks up a cached response. Returns (nil, false) on miss or
// expiration; the caller falls back to a live fan-out call. The
// returned response is a defensive copy — callers may overwrite
// RequestID or Signals without corrupting the cached entry.
func (c *ContextCache) GetScoped(scope ContextCacheScope, contextHash [sha256.Size]byte) (*tmproto.ProviderContextMatchResponse, bool) {
	if c == nil {
		return nil, false
	}
	key := contextCacheKey{providerID: scope.providerID, namespace: scope.namespace, context: contextHash}
	c.mu.Lock()
	if !c.scopeCurrentLocked(scope) {
		c.mu.Unlock()
		return nil, false
	}
	entry, ok := c.entries[key]
	if !ok {
		c.mu.Unlock()
		c.metrics.IncMiss(scope.providerID)
		return nil, false
	}
	if c.now().After(entry.expiresAt) {
		delete(c.entries, key)
		c.mu.Unlock()
		c.metrics.IncMiss(scope.providerID)
		return nil, false
	}
	c.mu.Unlock()
	c.metrics.IncHit(scope.providerID)
	return cloneContextResponse(entry.response), true
}

// PutScoped stores a response under the spec's canonical cache key. The TTL
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
func (c *ContextCache) PutScoped(scope ContextCacheScope, contextHash [sha256.Size]byte, resp *tmproto.ProviderContextMatchResponse) {
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
	key := contextCacheKey{providerID: scope.providerID, namespace: scope.namespace, context: contextHash}
	now := c.now()
	c.mu.Lock()
	if !c.scopeCurrentLocked(scope) {
		c.mu.Unlock()
		return
	}
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

// Get preserves source compatibility with the pre-context_hash cache API. Its
// placement-derived arguments cannot establish a conformant namespace or
// context hash, so it deliberately fails closed with a miss.
//
// Deprecated: capture a trusted ContextCacheScope and call GetScoped.
func (c *ContextCache) Get(_, _, _ string, _, _ string) (*tmproto.ProviderContextMatchResponse, bool) {
	return nil, false
}

// Put preserves source compatibility with the pre-context_hash cache API. It
// deliberately does not store because the old arguments omit result-affecting
// request fields and trusted external state.
//
// Deprecated: capture a trusted ContextCacheScope and call PutScoped.
func (c *ContextCache) Put(_, _, _ string, _, _ string, _ *tmproto.ProviderContextMatchResponse) {}

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
	var oldestKey contextCacheKey
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

// cloneContextResponse copies the response deeply enough that the
// merger, and any future seller-agent-stamper or macro-injector code
// path, can freely mutate the returned Offer entries without
// corrupting the cache. tmproto.Offer carries several pointer/slice
// fields that would otherwise be shared:
//
//   - SellerAgent, Brand   (json.RawMessage — byte slice)
//   - CreativeManifest     (*json.RawMessage)
//   - Price                (*OfferPrice)
//   - CreativeData         (map[string]string)
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
func cloneContextResponse(src *tmproto.ProviderContextMatchResponse) *tmproto.ProviderContextMatchResponse {
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
	if len(src.CreativeData) > 0 {
		dst.CreativeData = make(map[string]string, len(src.CreativeData))
		maps.Copy(dst.CreativeData, src.CreativeData)
	}
	return dst
}
