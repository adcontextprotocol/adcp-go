package router

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"math"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/adcontextprotocol/adcp-go/tmproto"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

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

func cacheScope(t *testing.T, c *ContextCache, provider, namespace string) ContextCacheScope {
	t.Helper()
	scope, ok := c.Capture(provider, namespace, []byte(`{"endpoint":"https://provider.example"}`))
	require.True(t, ok)
	return scope
}

func cacheHash(value string) [sha256.Size]byte { return sha256.Sum256([]byte(value)) }

func ttlPtr(n int) *int { return &n }

func TestContextCache_NilSafe(t *testing.T) {
	var c *ContextCache
	_, ok := c.Capture("prov", "generation-1", nil)
	assert.False(t, ok)
	_, ok = c.GetScoped(ContextCacheScope{}, cacheHash("request"))
	assert.False(t, ok)
	c.PutScoped(ContextCacheScope{}, cacheHash("request"), &tmproto.ProviderContextMatchResponse{})
	assert.Equal(t, 0, c.Size())
}

func TestContextCache_LegacyPlacementAPIAlwaysBypasses(t *testing.T) {
	c := NewContextCache(time.Minute)
	c.Put("rid", "placement", "prov", "https://seller.example", "US", &tmproto.ProviderContextMatchResponse{})
	_, hit := c.Get("rid", "placement", "prov", "https://seller.example", "US")
	assert.False(t, hit)
	assert.Equal(t, 0, c.Size())
}

func TestContextCache_EmptyOrInvalidNamespaceBypasses(t *testing.T) {
	c := NewContextCache(time.Minute)
	for _, namespace := range []string{"", "contains space", "line\nbreak", string(bytes.Repeat([]byte{'x'}, MaxContextCacheNamespaceBytes+1))} {
		_, ok := c.Capture("prov", namespace, nil)
		assert.False(t, ok, "namespace must fail closed")
	}
	assert.Equal(t, 0, c.Size())
}

func TestContextCache_HitReturnsDeepClone(t *testing.T) {
	c := NewContextCache(time.Minute)
	scope := cacheScope(t, c, "prov", "generation-1")
	hash := cacheHash("request")
	price := tmproto.OfferPrice{Amount: 5, Currency: "USD", Model: "cpm"}
	manifest := json.RawMessage(`{"kind":"markdown"}`)
	c.PutScoped(scope, hash, &tmproto.ProviderContextMatchResponse{
		RequestID: "original",
		CacheTTL:  ttlPtr(60),
		Offers: []tmproto.Offer{{
			PackageID:        "pkg-a",
			SellerAgent:      json.RawMessage(`{"agent_url":"https://seller.example"}`),
			Brand:            json.RawMessage(`{"name":"Original"}`),
			Price:            &price,
			CreativeManifest: &manifest,
			CreativeData:     map[string]string{"CID": "original"},
		}},
		Signals: map[string]any{"key": "original"},
	})

	got, ok := c.GetScoped(scope, hash)
	require.True(t, ok)
	got.RequestID = "mutated"
	*got.CacheTTL = 0
	got.Offers[0].PackageID = "mutated"
	got.Offers[0].SellerAgent[0] = 'X'
	got.Offers[0].Brand[0] = 'X'
	got.Offers[0].Price.Amount = 999
	(*got.Offers[0].CreativeManifest)[0] = 'X'
	got.Offers[0].CreativeData["CID"] = "mutated"
	got.Signals["key"] = "mutated"

	again, ok := c.GetScoped(scope, hash)
	require.True(t, ok)
	assert.Equal(t, "original", again.RequestID)
	assert.Equal(t, 60, *again.CacheTTL)
	assert.Equal(t, "pkg-a", again.Offers[0].PackageID)
	assert.Equal(t, byte('{'), again.Offers[0].SellerAgent[0])
	assert.Equal(t, byte('{'), again.Offers[0].Brand[0])
	assert.Equal(t, float64(5), again.Offers[0].Price.Amount)
	assert.Equal(t, byte('{'), (*again.Offers[0].CreativeManifest)[0])
	assert.Equal(t, "original", again.Offers[0].CreativeData["CID"])
	assert.Equal(t, "original", again.Signals["key"])
}

func TestContextCache_TTLSemantics(t *testing.T) {
	tests := []struct {
		name       string
		configured time.Duration
		provider   *int
		advance    time.Duration
		wantHit    bool
	}{
		{"default alive", 2 * time.Second, nil, 1500 * time.Millisecond, true},
		{"default expired", 2 * time.Second, nil, 3 * time.Second, false},
		{"provider override alive", time.Hour, ttlPtr(2), 1500 * time.Millisecond, true},
		{"provider override expired", time.Hour, ttlPtr(2), 3 * time.Second, false},
		{"explicit zero", time.Hour, ttlPtr(0), 0, false},
		{"negative falls back", 2 * time.Second, ttlPtr(-1), 1500 * time.Millisecond, true},
		{"maximum clamp", time.Minute, ttlPtr(math.MaxInt), MaxContextCacheTTL + time.Second, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := NewContextCache(tt.configured)
			now := time.Unix(1_000_000_000, 0)
			c.now = func() time.Time { return now }
			scope := cacheScope(t, c, "prov", "generation-1")
			hash := cacheHash("request")
			c.PutScoped(scope, hash, &tmproto.ProviderContextMatchResponse{CacheTTL: tt.provider})
			now = now.Add(tt.advance)
			_, hit := c.GetScoped(scope, hash)
			assert.Equal(t, tt.wantHit, hit)
		})
	}
}

func TestContextCache_KeyPartitionsProviderNamespaceAndContext(t *testing.T) {
	c := NewContextCache(time.Minute)
	a1 := cacheScope(t, c, "provider-a", "generation-1")
	b1 := cacheScope(t, c, "provider-b", "generation-1")
	h1, h2 := cacheHash("request-one"), cacheHash("request-two")
	c.PutScoped(a1, h1, &tmproto.ProviderContextMatchResponse{RequestID: "a-one"})
	c.PutScoped(b1, h1, &tmproto.ProviderContextMatchResponse{RequestID: "b-one"})
	c.PutScoped(a1, h2, &tmproto.ProviderContextMatchResponse{RequestID: "a-two"})

	gotA1, ok := c.GetScoped(a1, h1)
	require.True(t, ok)
	gotB1, ok := c.GetScoped(b1, h1)
	require.True(t, ok)
	gotA2, ok := c.GetScoped(a1, h2)
	require.True(t, ok)
	assert.Equal(t, "a-one", gotA1.RequestID)
	assert.Equal(t, "b-one", gotB1.RequestID)
	assert.Equal(t, "a-two", gotA2.RequestID)
	assert.Equal(t, 3, c.Size())
}

func TestContextCache_NamespaceRotationPurgesAndRejectsReuse(t *testing.T) {
	c := NewContextCache(time.Minute)
	hash := cacheHash("request")
	first := cacheScope(t, c, "prov", "generation-1")
	c.PutScoped(first, hash, &tmproto.ProviderContextMatchResponse{RequestID: "old"})

	second := cacheScope(t, c, "prov", "generation-2")
	assert.Equal(t, 0, c.Size(), "rotation must make old entries unreachable immediately")
	_, hit := c.GetScoped(first, hash)
	assert.False(t, hit)
	c.PutScoped(first, hash, &tmproto.ProviderContextMatchResponse{RequestID: "late-old"})
	assert.Equal(t, 0, c.Size(), "an old in-flight response must not populate the new generation")

	c.PutScoped(second, hash, &tmproto.ProviderContextMatchResponse{RequestID: "new"})
	_, hit = c.GetScoped(second, hash)
	require.True(t, hit)

	_, ok := c.Capture("prov", "generation-1", []byte(`{"endpoint":"https://provider.example"}`))
	assert.False(t, ok, "generation-token reuse must fail closed")
	assert.Equal(t, 0, c.Size(), "reuse must not resurrect the first generation's entries")
}

func TestContextCache_UnknownValidityInvalidatesGeneration(t *testing.T) {
	c := NewContextCache(time.Minute)
	scope := cacheScope(t, c, "prov", "generation-1")
	hash := cacheHash("request")
	c.PutScoped(scope, hash, &tmproto.ProviderContextMatchResponse{})
	c.Invalidate("prov")
	assert.Equal(t, 0, c.Size())
	_, ok := c.Capture("prov", "generation-1", []byte(`{"endpoint":"https://provider.example"}`))
	assert.False(t, ok, "validity recovery requires a novel generation token")
	_, ok = c.Capture("prov", "generation-2", []byte(`{"endpoint":"https://provider.example"}`))
	assert.True(t, ok)
}

func TestContextCache_ProviderEvaluationContextRotation(t *testing.T) {
	c := NewContextCache(time.Minute)
	hash := cacheHash("request")
	oldScope, ok := c.Capture("prov", "generation-1", []byte(`{"endpoint":"https://old.example"}`))
	require.True(t, ok)
	c.PutScoped(oldScope, hash, &tmproto.ProviderContextMatchResponse{RequestID: "old"})
	newScope, ok := c.Capture("prov", "generation-1", []byte(`{"endpoint":"https://new.example"}`))
	require.True(t, ok)
	assert.NotEqual(t, oldScope, newScope)
	assert.Equal(t, 0, c.Size())
}

func TestContextCache_DoesNotRetainNamespaceOrRequestPreimage(t *testing.T) {
	c := NewContextCache(time.Minute)
	namespace := "opaque-sensitive-generation-marker"
	preimage := "private-request-preimage-marker"
	scope := cacheScope(t, c, "prov", namespace)
	c.PutScoped(scope, cacheHash(preimage), &tmproto.ProviderContextMatchResponse{RequestID: "response"})

	internal := fmt.Sprintf("%#v", c)
	assert.NotContains(t, internal, namespace)
	assert.NotContains(t, internal, preimage)
}

func TestContextCache_NamespaceDigestUsesUnambiguousFraming(t *testing.T) {
	c := NewContextCache(time.Minute)
	assert.NotEqual(t, c.namespaceDigest("ab", []byte("c")), c.namespaceDigest("a", []byte("bc")))
}

func TestContextCache_MaxEntriesEvictsOldest(t *testing.T) {
	c := NewContextCache(time.Minute, WithContextCacheMaxEntries(2))
	now := time.Unix(1_000_000_000, 0)
	c.now = func() time.Time { return now }
	scope := cacheScope(t, c, "prov", "generation-1")
	for _, value := range []string{"one", "two", "three"} {
		c.PutScoped(scope, cacheHash(value), &tmproto.ProviderContextMatchResponse{RequestID: value})
		now = now.Add(time.Second)
	}
	assert.Equal(t, 2, c.Size())
	_, hit := c.GetScoped(scope, cacheHash("one"))
	assert.False(t, hit)
	_, hit = c.GetScoped(scope, cacheHash("two"))
	assert.True(t, hit)
	_, hit = c.GetScoped(scope, cacheHash("three"))
	assert.True(t, hit)
}

func TestContextCache_MetricsCounts(t *testing.T) {
	metrics := newCountingCacheMetrics()
	c := NewContextCache(time.Minute, WithContextCacheMetrics(metrics))
	scope := cacheScope(t, c, "prov", "generation-1")
	hash := cacheHash("request")
	_, _ = c.GetScoped(scope, hash)
	c.PutScoped(scope, hash, &tmproto.ProviderContextMatchResponse{})
	_, _ = c.GetScoped(scope, hash)
	assert.Equal(t, 1, metrics.hits["prov"])
	assert.Equal(t, 1, metrics.misses["prov"])
}

func TestContextCache_ConcurrentSafe(t *testing.T) {
	c := NewContextCache(time.Minute)
	scope := cacheScope(t, c, "prov", "generation-1")
	hash := cacheHash("request")
	c.PutScoped(scope, hash, &tmproto.ProviderContextMatchResponse{RequestID: "original"})

	var wg sync.WaitGroup
	var hits atomic.Int64
	for range 32 {
		wg.Go(func() {
			for i := range 200 {
				if _, ok := c.GetScoped(scope, hash); ok {
					hits.Add(1)
				}
				if i%10 == 0 {
					c.PutScoped(scope, hash, &tmproto.ProviderContextMatchResponse{RequestID: "original"})
				}
			}
		})
	}
	wg.Wait()
	assert.Equal(t, int64(32*200), hits.Load())
}
