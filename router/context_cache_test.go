package router

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"math"
	"math/big"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/adcontextprotocol/adcp-go/tmproto"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type countingCacheMetrics struct {
	mu                    sync.Mutex
	hits                  map[string]int
	misses                map[string]int
	generationExhaustions map[string]int
}

func newCountingCacheMetrics() *countingCacheMetrics {
	return &countingCacheMetrics{
		hits: map[string]int{}, misses: map[string]int{}, generationExhaustions: map[string]int{},
	}
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
func (m *countingCacheMetrics) IncGenerationExhausted(id string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.generationExhaustions[id]++
}

func cacheScope(t *testing.T, c *ContextCache, provider, namespace string) ContextCacheScope {
	t.Helper()
	scope, ok := c.Capture(provider, namespace, []byte(`{"endpoint":"https://provider.example"}`))
	require.True(t, ok)
	return scope
}

func cacheHash(value string) [sha256.Size]byte { return sha256.Sum256([]byte(value)) }

func ttlPtr(n int) *int { return &n }

type signalCloneCustom struct {
	Counts map[string]int `json:"counts"`
	Values []int          `json:"values"`
}

type signalNamedIntPointer *int

type signalTextMapKey struct {
	Value string
}

func (k *signalTextMapKey) MarshalText() ([]byte, error) { return []byte(k.Value), nil }

type signalHiddenMutable struct {
	Values []int `json:"values"`
}

type signalUnexportedEmbedding struct {
	signalHiddenMutable
}

type signalLockedMarshaler struct {
	mutex sync.Mutex
	Value int
}

func (s *signalLockedMarshaler) MarshalJSON() ([]byte, error) {
	s.mutex.Lock()
	defer s.mutex.Unlock()
	return json.Marshal(struct {
		Value int `json:"value"`
	}{Value: s.Value})
}

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
	cyclic := map[string]any{"value": "original"}
	cyclic["self"] = cyclic
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
		Signals: map[string]any{
			"key":          "original",
			"nested_map":   map[string]any{"value": "original"},
			"nested_array": []any{"original", map[string]any{"value": "original"}},
			"cyclic":       cyclic,
		},
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
	got.Signals["nested_map"].(map[string]any)["value"] = "mutated"
	got.Signals["nested_array"].([]any)[0] = "mutated"
	got.Signals["nested_array"].([]any)[1].(map[string]any)["value"] = "mutated"
	got.Signals["cyclic"].(map[string]any)["self"].(map[string]any)["value"] = "mutated"

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
	assert.Equal(t, "original", again.Signals["nested_map"].(map[string]any)["value"])
	assert.Equal(t, "original", again.Signals["nested_array"].([]any)[0])
	assert.Equal(t, "original", again.Signals["nested_array"].([]any)[1].(map[string]any)["value"])
	assert.Equal(t, "original", again.Signals["cyclic"].(map[string]any)["self"].(map[string]any)["value"])
}

func TestContextCache_ClonePreservesNilAndEmptyWireShapes(t *testing.T) {
	emptyBytes := make([]byte, 0, 1)
	emptyStrings := make([]string, 0, 1)
	emptyValues := make([]any, 0, 1)
	typedMap := map[string]int{"original": 1}
	typedSlice := []int{1, 2}
	custom := &signalCloneCustom{Counts: map[string]int{"original": 1}, Values: []int{1, 2}}
	namedValue := 1
	namedPointer := signalNamedIntPointer(&namedValue)
	src := &tmproto.ProviderContextMatchResponse{
		Type:   tmproto.TypeContextMatchResponse,
		Offers: make([]tmproto.Offer, 0, 1),
		Signals: map[string]any{
			"bytes":          emptyBytes,
			"strings":        emptyStrings,
			"values":         emptyValues,
			"map":            map[string]any{},
			"string_map":     map[string]string{},
			"nil_bytes":      []byte(nil),
			"nil_strings":    []string(nil),
			"nil_values":     []any(nil),
			"nil_map":        map[string]any(nil),
			"nil_string_map": map[string]string(nil),
			"typed_map":      typedMap,
			"typed_slice":    typedSlice,
			"custom":         custom,
			"named_pointer":  namedPointer,
		},
	}
	wantJSON, err := json.Marshal(src)
	require.NoError(t, err)

	cloned, safe := cloneContextResponse(src)
	require.True(t, safe)
	gotJSON, err := json.Marshal(cloned)
	require.NoError(t, err)
	assert.JSONEq(t, string(wantJSON), string(gotJSON))
	require.NotNil(t, cloned.Offers)
	require.NotNil(t, cloned.Signals)
	require.NotNil(t, cloned.Signals["bytes"].([]byte))
	require.NotNil(t, cloned.Signals["strings"].([]string))
	require.NotNil(t, cloned.Signals["values"].([]any))
	require.NotNil(t, cloned.Signals["map"].(map[string]any))
	require.NotNil(t, cloned.Signals["string_map"].(map[string]string))
	require.NotNil(t, cloned.Signals["typed_map"].(map[string]int))
	require.NotNil(t, cloned.Signals["typed_slice"].([]int))
	require.NotNil(t, cloned.Signals["custom"].(*signalCloneCustom))
	require.IsType(t, signalNamedIntPointer(nil), cloned.Signals["named_pointer"])
	assert.Nil(t, cloned.Signals["nil_bytes"])
	assert.Nil(t, cloned.Signals["nil_strings"])
	assert.Nil(t, cloned.Signals["nil_values"])
	assert.Nil(t, cloned.Signals["nil_map"])
	assert.Nil(t, cloned.Signals["nil_string_map"])

	cloned.Offers = append(cloned.Offers, tmproto.Offer{PackageID: "clone-only"})
	cloned.Signals["new"] = true
	cloned.Signals["bytes"] = append(cloned.Signals["bytes"].([]byte), 1)
	cloned.Signals["strings"] = append(cloned.Signals["strings"].([]string), "clone-only")
	cloned.Signals["values"] = append(cloned.Signals["values"].([]any), "clone-only")
	cloned.Signals["map"].(map[string]any)["new"] = true
	cloned.Signals["string_map"].(map[string]string)["new"] = "clone-only"
	cloned.Signals["typed_map"].(map[string]int)["original"] = 2
	cloned.Signals["typed_slice"].([]int)[0] = 2
	cloned.Signals["custom"].(*signalCloneCustom).Counts["original"] = 2
	cloned.Signals["custom"].(*signalCloneCustom).Values[0] = 2
	*cloned.Signals["named_pointer"].(signalNamedIntPointer) = 2
	assert.Empty(t, src.Offers)
	assert.NotContains(t, src.Signals, "new")
	assert.Equal(t, byte(0), emptyBytes[:cap(emptyBytes)][0])
	assert.Equal(t, "", emptyStrings[:cap(emptyStrings)][0])
	assert.Nil(t, emptyValues[:cap(emptyValues)][0])
	assert.Empty(t, src.Signals["map"])
	assert.Empty(t, src.Signals["string_map"])
	assert.Equal(t, 1, typedMap["original"])
	assert.Equal(t, 1, typedSlice[0])
	assert.Equal(t, 1, custom.Counts["original"])
	assert.Equal(t, 1, custom.Values[0])
	assert.Equal(t, 1, namedValue)

	emptyCreativeData := map[string]string{}
	var nilManifest json.RawMessage
	srcOffer := tmproto.Offer{
		SellerAgent:      json.RawMessage{},
		Brand:            json.RawMessage{},
		CreativeManifest: &nilManifest,
		CreativeData:     emptyCreativeData,
	}
	offer := cloneOffer(srcOffer)
	require.NotNil(t, offer.SellerAgent)
	require.NotNil(t, offer.Brand)
	require.NotNil(t, offer.CreativeManifest)
	assert.Nil(t, *offer.CreativeManifest)
	require.NotNil(t, offer.CreativeData)
	srcOfferJSON, err := json.Marshal(srcOffer)
	require.NoError(t, err)
	clonedOfferJSON, err := json.Marshal(offer)
	require.NoError(t, err)
	assert.JSONEq(t, string(srcOfferJSON), string(clonedOfferJSON))
	offer.CreativeData["new"] = "clone-only"
	assert.Empty(t, emptyCreativeData)

	nilClone, safe := cloneContextResponse(&tmproto.ProviderContextMatchResponse{})
	require.True(t, safe)
	assert.Nil(t, nilClone.Offers)
	assert.Nil(t, nilClone.Signals)
}

func TestContextCache_UnsafeTypedSignalsBypassInsertion(t *testing.T) {
	bigValue := big.NewInt(1)
	hiddenValue := &signalUnexportedEmbedding{signalHiddenMutable: signalHiddenMutable{Values: []int{1}}}
	textKey := &signalTextMapKey{Value: "key"}
	tests := []struct {
		name   string
		value  any
		mutate func()
	}{
		{
			name:  "big.Int has unexported mutable state",
			value: bigValue,
			mutate: func() {
				bigValue.SetInt64(2)
			},
		},
		{
			name:  "unexported embedding contains exported slice",
			value: hiddenValue,
			mutate: func() {
				hiddenValue.Values[0] = 2
			},
		},
		{
			name:  "TextMarshaler pointer map key remains mutable",
			value: map[*signalTextMapKey]int{textKey: 1},
			mutate: func() {
				textKey.Value = "mutated"
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := NewContextCache(time.Minute)
			scope := cacheScope(t, c, "prov", "generation-1")
			hash := cacheHash(tt.name)
			response := &tmproto.ProviderContextMatchResponse{Signals: map[string]any{"value": tt.value}}
			_, safe := cloneContextResponse(response)
			assert.False(t, safe)
			c.PutScoped(scope, hash, response)
			assert.Zero(t, c.Size(), "unsafe Signals must bypass cache insertion")
			tt.mutate()
			_, hit := c.GetScoped(scope, hash)
			assert.False(t, hit)
		})
	}
}

func TestContextCache_LockedUnexportedStateBypassesInsertion(t *testing.T) {
	value := &signalLockedMarshaler{Value: 1}
	value.mutex.Lock()
	defer value.mutex.Unlock()
	if value.mutex.TryLock() {
		value.mutex.Unlock()
		t.Fatal("test mutex unexpectedly unlocked")
	}

	c := NewContextCache(time.Minute)
	scope := cacheScope(t, c, "prov", "generation-1")
	response := &tmproto.ProviderContextMatchResponse{Signals: map[string]any{"value": value}}
	_, safe := cloneContextResponse(response)
	assert.False(t, safe)
	c.PutScoped(scope, cacheHash("locked"), response)
	assert.Zero(t, c.Size())
	if value.mutex.TryLock() {
		value.mutex.Unlock()
		t.Fatal("cache admission unexpectedly changed caller-owned mutex state")
	}
}

func TestContextCache_NamedPointerSignalPreservesTypeAndIsolation(t *testing.T) {
	c := NewContextCache(time.Minute)
	scope := cacheScope(t, c, "prov", "generation-1")
	hash := cacheHash("named-pointer")
	value := 1
	pointer := signalNamedIntPointer(&value)
	c.PutScoped(scope, hash, &tmproto.ProviderContextMatchResponse{
		Signals: map[string]any{"value": pointer},
	})
	value = 2

	got, hit := c.GetScoped(scope, hash)
	require.True(t, hit)
	cloned, ok := got.Signals["value"].(signalNamedIntPointer)
	require.True(t, ok, "clone must preserve the defined pointer type")
	assert.Equal(t, 1, *cloned)
	*cloned = 3
	again, hit := c.GetScoped(scope, hash)
	require.True(t, hit)
	assert.Equal(t, 1, *again.Signals["value"].(signalNamedIntPointer))
}

func TestContextCache_UnsafeResponseRemovesOnlyCurrentExactKey(t *testing.T) {
	tests := []struct {
		name   string
		ttl    int
		unsafe bool
	}{
		{name: "safe explicit zero", ttl: 0},
		{name: "unsafe negative TTL", ttl: -1, unsafe: true},
		{name: "unsafe zero TTL", ttl: 0, unsafe: true},
		{name: "unsafe positive TTL", ttl: 1, unsafe: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := NewContextCache(time.Minute)
			hash := cacheHash("same-key")
			unrelatedHash := cacheHash("unrelated-key")
			current := cacheScope(t, c, "prov", "generation-1")
			otherProvider := cacheScope(t, c, "other", "generation-1")
			c.PutScoped(current, hash, &tmproto.ProviderContextMatchResponse{RequestID: "safe"})
			c.PutScoped(current, unrelatedHash, &tmproto.ProviderContextMatchResponse{RequestID: "unrelated-hash"})
			c.PutScoped(otherProvider, hash, &tmproto.ProviderContextMatchResponse{RequestID: "unrelated-provider"})

			replacement := &tmproto.ProviderContextMatchResponse{RequestID: "replacement", CacheTTL: ttlPtr(tt.ttl)}
			if tt.unsafe {
				replacement.Signals = map[string]any{"value": big.NewInt(1)}
			}
			c.PutScoped(current, hash, replacement)
			_, hit := c.GetScoped(current, hash)
			assert.False(t, hit, "no-cache or unsafe replacement must remove the exact warm key")
			unrelated, hit := c.GetScoped(current, unrelatedHash)
			require.True(t, hit)
			assert.Equal(t, "unrelated-hash", unrelated.RequestID)
			other, hit := c.GetScoped(otherProvider, hash)
			require.True(t, hit)
			assert.Equal(t, "unrelated-provider", other.RequestID)
			assert.Equal(t, 2, c.Size())
		})
	}

	t.Run("stale scope cannot evict current generation", func(t *testing.T) {
		c := NewContextCache(time.Minute)
		hash := cacheHash("same-key")
		old := cacheScope(t, c, "prov", "generation-1")
		current := cacheScope(t, c, "prov", "generation-2")
		c.PutScoped(current, hash, &tmproto.ProviderContextMatchResponse{RequestID: "new-generation"})
		c.PutScoped(old, hash, &tmproto.ProviderContextMatchResponse{
			RequestID: "stale-unsafe",
			CacheTTL:  ttlPtr(0),
			Signals:   map[string]any{"value": big.NewInt(2)},
		})
		got, hit := c.GetScoped(current, hash)
		require.True(t, hit, "no-cache/unsafe stale scope must not evict the current generation")
		assert.Equal(t, "new-generation", got.RequestID)
		assert.Equal(t, 1, c.Size())
	})
}

func TestContextCache_TypedSignalsAreIsolatedOnPutAndGet(t *testing.T) {
	c := NewContextCache(time.Minute)
	scope := cacheScope(t, c, "prov", "generation-1")
	hash := cacheHash("typed-signals")
	typedMap := map[string]int{"value": 1}
	typedSlice := []int{1}
	typedInt := 1
	custom := &signalCloneCustom{Counts: map[string]int{"value": 1}, Values: []int{1}}
	c.PutScoped(scope, hash, &tmproto.ProviderContextMatchResponse{
		Signals: map[string]any{
			"map":    typedMap,
			"slice":  typedSlice,
			"int":    &typedInt,
			"custom": custom,
		},
	})

	// Mutating the caller-owned response after Put must not reach the entry.
	typedMap["value"] = 2
	typedSlice[0] = 2
	typedInt = 2
	custom.Counts["value"] = 2
	custom.Values[0] = 2
	got, hit := c.GetScoped(scope, hash)
	require.True(t, hit)
	assert.Equal(t, 1, got.Signals["map"].(map[string]int)["value"])
	assert.Equal(t, 1, got.Signals["slice"].([]int)[0])
	assert.Equal(t, 1, *got.Signals["int"].(*int))
	assert.Equal(t, 1, got.Signals["custom"].(*signalCloneCustom).Counts["value"])
	assert.Equal(t, 1, got.Signals["custom"].(*signalCloneCustom).Values[0])

	// Mutating one returned hit must likewise not reach a later hit.
	got.Signals["map"].(map[string]int)["value"] = 3
	got.Signals["slice"].([]int)[0] = 3
	*got.Signals["int"].(*int) = 3
	got.Signals["custom"].(*signalCloneCustom).Counts["value"] = 3
	got.Signals["custom"].(*signalCloneCustom).Values[0] = 3
	again, hit := c.GetScoped(scope, hash)
	require.True(t, hit)
	assert.Equal(t, 1, again.Signals["map"].(map[string]int)["value"])
	assert.Equal(t, 1, again.Signals["slice"].([]int)[0])
	assert.Equal(t, 1, *again.Signals["int"].(*int))
	assert.Equal(t, 1, again.Signals["custom"].(*signalCloneCustom).Counts["value"])
	assert.Equal(t, 1, again.Signals["custom"].(*signalCloneCustom).Values[0])
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
	assert.Equal(t, 1, c.Size(), "a stale reused token must not purge the active generation")
	got, hit := c.GetScoped(second, hash)
	require.True(t, hit, "the active generation must survive a stale reused token")
	assert.Equal(t, "new", got.RequestID)
}

func TestContextCache_UnknownValidityInvalidatesGeneration(t *testing.T) {
	c := NewContextCache(time.Minute)
	evaluation := []byte(`{"endpoint":"https://provider.example"}`)
	scope, ok := c.Capture("prov", "generation-1", evaluation)
	require.True(t, ok)
	hash := cacheHash("request")
	c.PutScoped(scope, hash, &tmproto.ProviderContextMatchResponse{})
	c.invalidateEvaluation("prov", evaluation, 0)
	assert.Equal(t, 0, c.Size())
	c.PutScoped(scope, hash, &tmproto.ProviderContextMatchResponse{RequestID: "late-inflight"})
	assert.Equal(t, 0, c.Size(), "global uncertainty must reject stale in-flight insertion")
	_, ok = c.Capture("prov", "generation-1", evaluation)
	assert.False(t, ok, "validity recovery requires a novel generation token")
	_, ok = c.Capture("prov", "generation-2", evaluation)
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

func TestContextCache_StaleEvaluationCannotInvalidateCurrentRevision(t *testing.T) {
	c := NewContextCache(time.Minute)
	hash := cacheHash("request")
	oldEvaluation := []byte(`{"endpoint":"https://old.example","revision":1}`)
	newEvaluation := []byte(`{"endpoint":"https://new.example","revision":2}`)
	oldScope, ok := c.captureAtRevision("prov", "generation-1", oldEvaluation, 1)
	require.True(t, ok)
	newScope, ok := c.captureAtRevision("prov", "generation-1", newEvaluation, 2)
	require.True(t, ok)
	c.PutScoped(newScope, hash, &tmproto.ProviderContextMatchResponse{RequestID: "new"})

	c.invalidateEvaluation("prov", oldEvaluation, 1)
	got, hit := c.GetScoped(newScope, hash)
	require.True(t, hit, "a stale evaluation must not purge the replacement revision")
	assert.Equal(t, "new", got.RequestID)
	c.PutScoped(oldScope, hash, &tmproto.ProviderContextMatchResponse{RequestID: "late-old"})
	got, hit = c.GetScoped(newScope, hash)
	require.True(t, hit, "a stale scope must not overwrite the replacement revision")
	assert.Equal(t, "new", got.RequestID)
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

func TestContextCache_GenerationHistoryExhaustionIsObservableAndSafe(t *testing.T) {
	metrics := newCountingCacheMetrics()
	c := NewContextCache(time.Minute, WithContextCacheMetrics(metrics))
	const providerID = "bounded_provider"
	const namespaceMarker = "sensitive-generation-marker"
	evaluation := []byte(`{"endpoint":"https://provider.example"}`)
	for i := range maxContextCacheGenerationsPerProvider {
		namespace := fmt.Sprintf("%s-%04d", namespaceMarker, i)
		_, ok := c.Capture(providerID, namespace, evaluation)
		require.True(t, ok)
	}

	_, ok := c.Capture(providerID, namespaceMarker+"-exhausted", evaluation)
	assert.False(t, ok, "history exhaustion must fail closed")
	_, ok = c.Capture(providerID, namespaceMarker+"-later", evaluation)
	assert.False(t, ok, "an exhausted provider remains bypass-only until restart")
	assert.Zero(t, c.Size())
	metrics.mu.Lock()
	assert.Equal(t, map[string]int{providerID: 1}, metrics.generationExhaustions)
	metrics.mu.Unlock()
	internal := fmt.Sprintf("%#v %#v", c, metrics)
	assert.NotContains(t, internal, namespaceMarker)
}

func TestContextCache_ReplayAtGenerationLimitDoesNotExhaustCurrent(t *testing.T) {
	metrics := newCountingCacheMetrics()
	c := NewContextCache(time.Minute, WithContextCacheMetrics(metrics))
	const providerID = "bounded_provider"
	evaluation := []byte(`{"endpoint":"https://provider.example"}`)
	var current ContextCacheScope
	for i := range maxContextCacheGenerationsPerProvider {
		namespace := fmt.Sprintf("generation-%04d", i)
		var ok bool
		current, ok = c.Capture(providerID, namespace, evaluation)
		require.True(t, ok)
	}
	hash := cacheHash("warm-current")
	c.PutScoped(current, hash, &tmproto.ProviderContextMatchResponse{RequestID: "current"})

	_, ok := c.Capture(providerID, "generation-0000", evaluation)
	assert.False(t, ok, "a replayed generation must bypass")
	got, hit := c.GetScoped(current, hash)
	require.True(t, hit, "replay at the history limit must not block the current generation")
	assert.Equal(t, "current", got.RequestID)
	metrics.mu.Lock()
	assert.Empty(t, metrics.generationExhaustions)
	metrics.mu.Unlock()
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
