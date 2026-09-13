package router

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"maps"
	"reflect"
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

// ContextCacheGenerationMetrics is an optional extension reported when the
// bounded replay-history set is exhausted. Implementations must label only by
// stable provider ID; namespace metadata must never enter metrics.
type ContextCacheGenerationMetrics interface {
	IncGenerationExhausted(providerID string)
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
// Responses are deeply cloned on both write and read so callers can freely
// mutate every pointer, slice, map, and JSON-shaped Signals value without
// corrupting the cached entry.
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
	namespace        [sha256.Size]byte
	evaluation       [sha256.Size]byte
	providerRevision uint64
	generation       uint64
	blocked          bool
	exhausted        bool
	seen             map[[sha256.Size]byte]struct{}
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
// may never be reused after rotation: reuse is rejected without disturbing the
// active generation, preventing both stale resurrection and stale-caller cache
// flushes. The raw inputs are HMACed immediately and never retained.
func (c *ContextCache) Capture(providerID, namespace string, providerEvaluationContext []byte) (ContextCacheScope, bool) {
	return c.captureAtRevision(providerID, namespace, providerEvaluationContext, 0)
}

// captureAtRevision is the router-facing form of Capture. providerRevision is
// monotonic trusted ProviderSet state, so a request holding an older provider
// snapshot cannot rotate the cache back or purge a newer endpoint/config
// generation after the replacement has populated it.
func (c *ContextCache) captureAtRevision(providerID, namespace string, providerEvaluationContext []byte, providerRevision uint64) (ContextCacheScope, bool) {
	if c == nil || !c.secretOK || !validContextCacheNamespace(namespace) {
		return ContextCacheScope{}, false
	}
	digest := c.namespaceDigest(namespace, providerEvaluationContext)
	evaluation := c.evaluationDigest(providerEvaluationContext)

	c.mu.Lock()
	state := c.providers[providerID]
	if state == nil {
		c.generation++
		state = &contextCacheProviderState{
			namespace:        digest,
			evaluation:       evaluation,
			providerRevision: providerRevision,
			generation:       c.generation,
			seen:             map[[sha256.Size]byte]struct{}{digest: {}},
		}
		c.providers[providerID] = state
	} else if providerRevision < state.providerRevision {
		c.mu.Unlock()
		return ContextCacheScope{}, false
	} else if state.namespace != digest {
		if len(state.seen) >= maxContextCacheGenerationsPerProvider {
			// Never discard reuse history: doing so could resurrect an old
			// generation. Fail closed for this provider until process restart
			// rather than let trusted-but-broken rotation grow memory forever.
			firstExhaustion := !state.exhausted
			c.generation++
			state.generation = c.generation
			state.blocked = true
			state.exhausted = true
			c.mu.Unlock()
			if firstExhaustion {
				if metrics, ok := c.metrics.(ContextCacheGenerationMetrics); ok {
					metrics.IncGenerationExhausted(providerID)
				}
			}
			return ContextCacheScope{}, false
		}
		_, reused := state.seen[digest]
		if reused {
			// A delayed request may still hold an older trusted resolver
			// snapshot. Reject that scope without changing the active generation:
			// stale callers must never purge or block a newer warm entry. If the
			// deployment genuinely reused a token, every request using it still
			// bypasses, so no stale generation can be resurrected.
			c.mu.Unlock()
			return ContextCacheScope{}, false
		}
		c.deleteProviderEntriesLocked(providerID)
		c.generation++
		state.namespace = digest
		state.evaluation = evaluation
		state.providerRevision = providerRevision
		state.generation = c.generation
		state.blocked = false
		state.seen[digest] = struct{}{}
	}
	if state.blocked || state.exhausted {
		c.mu.Unlock()
		return ContextCacheScope{}, false
	}
	scope := ContextCacheScope{providerID: providerID, namespace: digest, generation: state.generation}
	c.mu.Unlock()
	return scope, true
}

func (c *ContextCache) namespaceDigest(namespace string, providerEvaluationContext []byte) [sha256.Size]byte {
	mac := hmac.New(sha256.New, c.secret[:])
	writeFramed(mac, []byte("context-cache-namespace-v1"))
	writeFramed(mac, []byte(namespace))
	writeFramed(mac, providerEvaluationContext)
	var digest [sha256.Size]byte
	copy(digest[:], mac.Sum(nil))
	return digest
}

func (c *ContextCache) evaluationDigest(providerEvaluationContext []byte) [sha256.Size]byte {
	mac := hmac.New(sha256.New, c.secret[:])
	writeFramed(mac, []byte("context-cache-provider-evaluation-v1"))
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

// invalidateEvaluation invalidates only the provider evaluation generation
// identified by the caller's trusted snapshot. It is intentionally
// conditional: an old request completing after endpoint/config replacement
// must not purge or block a newer provider revision. Genuine Unknown status
// for the current revision still blocks that generation and rejects its stale
// in-flight insertions.
func (c *ContextCache) invalidateEvaluation(providerID string, providerEvaluationContext []byte, providerRevision uint64) {
	if c == nil || !c.secretOK {
		return
	}
	evaluation := c.evaluationDigest(providerEvaluationContext)
	c.mu.Lock()
	defer c.mu.Unlock()
	state := c.providers[providerID]
	if state == nil || state.providerRevision != providerRevision || state.evaluation != evaluation {
		return
	}
	c.generation++
	state.generation = c.generation
	state.blocked = true
	c.deleteProviderEntriesLocked(providerID)
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
		dst.Signals = cloneSignalMap(src.Signals, make(map[signalCloneVisit]any))
	}
	return &dst
}

type signalCloneVisit struct {
	kind     reflect.Kind
	ptr      uintptr
	length   int
	capacity int
}

// cloneSignalMap recursively clones the shapes produced by encoding/json.
// The seen table both preserves repeated references and safely terminates an
// unexpected cyclic map/slice supplied by an embedding application. Opaque
// scalar or otherwise unexpected values are copied by value without reflection
// or type assertions that could panic.
func cloneSignalMap(src map[string]any, seen map[signalCloneVisit]any) map[string]any {
	if src == nil {
		return nil
	}
	visit := signalCloneVisit{kind: reflect.Map, ptr: reflect.ValueOf(src).Pointer()}
	if prior, ok := seen[visit]; ok {
		return prior.(map[string]any)
	}
	dst := make(map[string]any, len(src))
	seen[visit] = dst
	for key, value := range src {
		dst[key] = cloneSignalValue(value, seen)
	}
	return dst
}

func cloneSignalSlice(src []any, seen map[signalCloneVisit]any) []any {
	if src == nil {
		return nil
	}
	visit := signalCloneVisit{
		kind: reflect.Slice, ptr: reflect.ValueOf(src).Pointer(), length: len(src), capacity: cap(src),
	}
	if prior, ok := seen[visit]; ok {
		return prior.([]any)
	}
	dst := make([]any, len(src))
	seen[visit] = dst
	for i, value := range src {
		dst[i] = cloneSignalValue(value, seen)
	}
	return dst
}

func cloneSignalValue(src any, seen map[signalCloneVisit]any) any {
	switch value := src.(type) {
	case map[string]any:
		return cloneSignalMap(value, seen)
	case []any:
		return cloneSignalSlice(value, seen)
	case json.RawMessage:
		return append(json.RawMessage(nil), value...)
	case []byte:
		return append([]byte(nil), value...)
	case map[string]string:
		dst := make(map[string]string, len(value))
		maps.Copy(dst, value)
		return dst
	case []string:
		return append([]string(nil), value...)
	default:
		return value
	}
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
