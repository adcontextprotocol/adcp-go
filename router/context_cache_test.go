package router

import (
	"encoding/json"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/adcontextprotocol/adcp-go/tmproto"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// countingCacheMetrics tallies hit/miss calls per provider for
// assertions in cache tests.
type countingCacheMetrics struct {
	mu     sync.Mutex
	hits   map[string]int
	misses map[string]int
}

func newCountingCacheMetrics() *countingCacheMetrics {
	return &countingCacheMetrics{hits: map[string]int{}, misses: map[string]int{}}
}
func (m *countingCacheMetrics) IncHit(id string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.hits[id]++
}
func (m *countingCacheMetrics) IncMiss(id string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.misses[id]++
}

// A nil cache is a valid value — Get/Put/Size must all no-op safely so
// callers that omit WithContextCache don't need explicit nil checks.
func TestContextCache_NilSafe(t *testing.T) {
	var c *ContextCache
	resp, ok := c.Get("rid", "pl", "prov")
	assert.False(t, ok)
	assert.Nil(t, resp)
	c.Put("rid", "pl", "prov", &tmproto.ContextMatchResponse{})
	assert.Equal(t, 0, c.Size())
}

// A Put followed by a Get under the same key returns a clone of the
// response — mutating the returned pointer's fields must NOT reach
// back into the cached entry.
func TestContextCache_HitReturnsClone(t *testing.T) {
	c := NewContextCache(time.Minute)
	resp := &tmproto.ContextMatchResponse{
		Type:      tmproto.TypeContextMatchResponse,
		RequestID: "orig",
		Offers:    []tmproto.Offer{{PackageID: "pkg-a"}},
		Signals:   map[string]any{"k": "v"},
	}
	c.Put("rid-1", "sidebar", "prov", resp)
	got, ok := c.Get("rid-1", "sidebar", "prov")
	require.True(t, ok)
	require.NotNil(t, got)
	assert.Equal(t, "orig", got.RequestID)
	assert.Equal(t, "pkg-a", got.Offers[0].PackageID)

	// Mutating the returned response must not corrupt the cache.
	got.RequestID = "mutated"
	got.Offers[0].PackageID = "MUTATED"
	got.Signals["k"] = "MUTATED"

	got2, ok := c.Get("rid-1", "sidebar", "prov")
	require.True(t, ok)
	assert.Equal(t, "orig", got2.RequestID)
	assert.Equal(t, "pkg-a", got2.Offers[0].PackageID)
	assert.Equal(t, "v", got2.Signals["k"])
}

// Deep-clone check: mutating the Offer's inner pointer/slice/map
// fields on the returned response must not corrupt the cached entry.
// Regression guard for the doc note on tmproto/types_gen.go:196 —
// the router MAY stamp SellerAgent from a package→seller map, and a
// shallow clone here would let that stamp leak into the cache.
func TestContextCache_HitDeepClonesOffers(t *testing.T) {
	c := NewContextCache(time.Minute)
	price := tmproto.OfferPrice{Amount: 5.00, Currency: "USD", Model: "cpm"}
	cm := json.RawMessage(`{"kind":"markdown"}`)
	c.Put("rid", "pl", "prov", &tmproto.ContextMatchResponse{
		Offers: []tmproto.Offer{{
			PackageID:        "pkg-a",
			SellerAgent:      json.RawMessage(`{"agent_url":"https://orig.example"}`),
			Brand:            json.RawMessage(`{"name":"Orig"}`),
			Price:            &price,
			CreativeManifest: &cm,
			Macros:           map[string]string{"CID": "orig"},
		}},
	})

	got, ok := c.Get("rid", "pl", "prov")
	require.True(t, ok)
	require.Len(t, got.Offers, 1)

	// Mutate every reference-typed field on the returned Offer.
	o := &got.Offers[0]
	o.SellerAgent[0] = 'X'
	o.Brand[0] = 'X'
	o.Price.Amount = 999
	(*o.CreativeManifest)[0] = 'X'
	o.Macros["CID"] = "MUTATED"

	got2, ok := c.Get("rid", "pl", "prov")
	require.True(t, ok)
	o2 := got2.Offers[0]
	assert.Equal(t, byte('{'), o2.SellerAgent[0], "SellerAgent bytes must be isolated from mutation via a cached hit")
	assert.Equal(t, byte('{'), o2.Brand[0], "Brand bytes must be isolated from mutation via a cached hit")
	assert.Equal(t, 5.00, o2.Price.Amount, "Price must be a distinct allocation")
	assert.Equal(t, byte('{'), (*o2.CreativeManifest)[0], "CreativeManifest bytes must be isolated")
	assert.Equal(t, "orig", o2.Macros["CID"], "Macros map must be a distinct allocation")
}

// Entries expire on TTL. The cache uses an injectable clock so we
// don't have to sleep.
func TestContextCache_TTLExpiration(t *testing.T) {
	c := NewContextCache(500 * time.Millisecond)
	now := time.Unix(1_000_000_000, 0)
	c.now = func() time.Time { return now }

	c.Put("rid", "pl", "prov", &tmproto.ContextMatchResponse{RequestID: "orig"})
	_, ok := c.Get("rid", "pl", "prov")
	require.True(t, ok, "fresh entry must hit")

	// Advance past TTL.
	now = now.Add(600 * time.Millisecond)
	_, ok = c.Get("rid", "pl", "prov")
	assert.False(t, ok, "expired entry must miss")

	// Expired entries are evicted on the miss so Size drops.
	assert.Equal(t, 0, c.Size())
}

// Provider cache_ttl overrides the router's default when present.
func TestContextCache_ProviderTTLOverride(t *testing.T) {
	c := NewContextCache(1 * time.Hour) // long default
	now := time.Unix(1_000_000_000, 0)
	c.now = func() time.Time { return now }

	c.Put("rid", "pl", "prov", &tmproto.ContextMatchResponse{
		RequestID: "orig",
		CacheTTL:  2, // 2 seconds — tighter than the default 1h
	})

	// Just under 2s → still cached.
	now = now.Add(1500 * time.Millisecond)
	_, ok := c.Get("rid", "pl", "prov")
	assert.True(t, ok)

	// Past 2s → expired.
	now = now.Add(1 * time.Second)
	_, ok = c.Get("rid", "pl", "prov")
	assert.False(t, ok, "provider-supplied cache_ttl must be honored over the default")
}

// A cache_ttl above MaxContextCacheTTL (86400s) is clamped, protecting
// the router from a provider (or upstream bug) demanding a
// week-long entry.
func TestContextCache_TTLClampsToMax(t *testing.T) {
	c := NewContextCache(time.Minute)
	now := time.Unix(1_000_000_000, 0)
	c.now = func() time.Time { return now }

	// 30 days — well past the schema-enforced 24h ceiling.
	c.Put("rid", "pl", "prov", &tmproto.ContextMatchResponse{
		CacheTTL: 30 * 24 * 3600,
	})

	// Just past 24h — must be expired regardless of what the provider
	// asked for.
	now = now.Add(MaxContextCacheTTL + time.Minute)
	_, ok := c.Get("rid", "pl", "prov")
	assert.False(t, ok, "provider cache_ttl must be clamped to MaxContextCacheTTL")
}

// cache_ttl == 0 (which Go's omitempty conflates with "field absent")
// falls back to the router's default TTL. The doc on ContextCache.Put
// explains why: providers using the Go type can't emit 0 explicitly,
// so treating received-zero as the default matches the majority use.
func TestContextCache_ZeroTTLUsesDefault(t *testing.T) {
	c := NewContextCache(2 * time.Second)
	now := time.Unix(1_000_000_000, 0)
	c.now = func() time.Time { return now }

	c.Put("rid", "pl", "prov", &tmproto.ContextMatchResponse{CacheTTL: 0})

	// Cached for the default TTL.
	now = now.Add(1500 * time.Millisecond)
	_, ok := c.Get("rid", "pl", "prov")
	assert.True(t, ok)

	// Past default TTL → expired.
	now = now.Add(1 * time.Second)
	_, ok = c.Get("rid", "pl", "prov")
	assert.False(t, ok)
}

// Different providers for the same (property, placement) tuple are
// separate cache entries — the spec key includes provider_id.
func TestContextCache_KeyPartitionedByProvider(t *testing.T) {
	c := NewContextCache(time.Minute)
	c.Put("rid", "pl", "prov-a", &tmproto.ContextMatchResponse{RequestID: "for-a"})
	c.Put("rid", "pl", "prov-b", &tmproto.ContextMatchResponse{RequestID: "for-b"})

	a, okA := c.Get("rid", "pl", "prov-a")
	b, okB := c.Get("rid", "pl", "prov-b")
	require.True(t, okA)
	require.True(t, okB)
	assert.Equal(t, "for-a", a.RequestID)
	assert.Equal(t, "for-b", b.RequestID)
	assert.Equal(t, 2, c.Size())
}

// Metrics fire on every hit and miss.
func TestContextCache_MetricsCounts(t *testing.T) {
	m := newCountingCacheMetrics()
	c := NewContextCache(time.Minute, WithContextCacheMetrics(m))

	// Two misses.
	_, _ = c.Get("rid", "pl", "prov")
	_, _ = c.Get("rid", "pl", "prov")

	// One populated, then two hits.
	c.Put("rid", "pl", "prov", &tmproto.ContextMatchResponse{RequestID: "orig"})
	_, _ = c.Get("rid", "pl", "prov")
	_, _ = c.Get("rid", "pl", "prov")

	assert.Equal(t, 2, m.hits["prov"])
	assert.Equal(t, 2, m.misses["prov"])
}

// Concurrent Get/Put must not race. With the -race detector this
// exercises the mu-protected map.
func TestContextCache_ConcurrentSafe(t *testing.T) {
	c := NewContextCache(time.Minute)
	c.Put("rid", "pl", "prov", &tmproto.ContextMatchResponse{RequestID: "orig"})

	var wg sync.WaitGroup
	var hits atomic.Int64
	for range 32 {
		wg.Go(func() {
			for j := range 200 {
				if _, ok := c.Get("rid", "pl", "prov"); ok {
					hits.Add(1)
				}
				if j%10 == 0 {
					c.Put("rid", "pl", "prov", &tmproto.ContextMatchResponse{RequestID: "orig"})
				}
			}
		})
	}
	wg.Wait()

	// Every iteration should hit — the entry is always fresh.
	assert.Equal(t, int64(32*200), hits.Load())
}
