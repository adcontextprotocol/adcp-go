package pkgconfigstore_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/adcontextprotocol/adcp-go/targeting"
	"github.com/adcontextprotocol/adcp-go/targeting/pkgconfigstore"
	"github.com/adcontextprotocol/adcp-go/targeting/signalstore"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestService_PutAndGet(t *testing.T) {
	ctx := context.Background()
	store := pkgconfigstore.NewMockStore()
	svc, err := pkgconfigstore.NewService(store)
	require.NoError(t, err)
	r := pkgconfigstore.NewReader(store)

	require.NoError(t, svc.Put(ctx, &targeting.PackageContextConfig{
		PackageID:    "pkg-1",
		TopicTargets: true,
		PropertyRIDs: []string{"1", "2"},
		EmitSegments: []string{"food"},
	}))

	cfg, ok, err := r.Get(ctx, "pkg-1")
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, "pkg-1", cfg.PackageID)
	assert.True(t, cfg.TopicTargets)
	assert.Equal(t, []string{"1", "2"}, cfg.PropertyRIDs)
}

func TestReader_AbsentReturnsOkFalse(t *testing.T) {
	ctx := context.Background()
	r := pkgconfigstore.NewReader(pkgconfigstore.NewMockStore())
	cfg, ok, err := r.Get(ctx, "pkg-missing")
	require.NoError(t, err)
	assert.False(t, ok)
	assert.Nil(t, cfg)
}

func TestCache_HitClonesConfigToIsolateCallers(t *testing.T) {
	ctx := context.Background()
	store := pkgconfigstore.NewMockStore()
	svc, err := pkgconfigstore.NewService(store)
	require.NoError(t, err)
	require.NoError(t, svc.Put(ctx, &targeting.PackageContextConfig{
		PackageID:    "pkg-shared",
		PropertyRIDs: []string{"rid-1"},
		EmitSegments: []string{"food"},
		Offers: []targeting.OfferConfigJSON{
			{DealID: "deal-A", Macros: map[string]string{"k": "v"}},
		},
		Macros: map[string]string{"global": "g1"},
	}))
	r := pkgconfigstore.WithCache(pkgconfigstore.NewReader(store), pkgconfigstore.CacheConfig{Size: 8, TTL: time.Minute})

	first, ok, err := r.Get(ctx, "pkg-shared")
	require.NoError(t, err)
	require.True(t, ok)

	// Mutate every reference-typed field the cache could share.
	first.PropertyRIDs[0] = "MUTATED"
	first.EmitSegments[0] = "MUTATED"
	first.Offers[0].DealID = "MUTATED"
	first.Offers[0].Macros["k"] = "MUTATED"
	first.Macros["global"] = "MUTATED"

	second, ok, err := r.Get(ctx, "pkg-shared")
	require.NoError(t, err)
	require.True(t, ok)

	assert.Equal(t, "rid-1", second.PropertyRIDs[0], "cache hit must not surface caller mutation")
	assert.Equal(t, "food", second.EmitSegments[0])
	assert.Equal(t, "deal-A", second.Offers[0].DealID)
	assert.Equal(t, "v", second.Offers[0].Macros["k"])
	assert.Equal(t, "g1", second.Macros["global"])
}

func TestCache_NegativeCachesMisses(t *testing.T) {
	ctx := context.Background()
	base := pkgconfigstore.NewMockStore()
	counting := &countingStore{Store: base}
	r := pkgconfigstore.WithCache(pkgconfigstore.NewReader(counting), pkgconfigstore.CacheConfig{Size: 8, TTL: time.Minute})

	for range 4 {
		_, ok, err := r.Get(ctx, "pkg-missing")
		require.NoError(t, err)
		assert.False(t, ok)
	}
	assert.Equal(t, 1, counting.getCalls, "absent key cached after first miss")
}

type countingStore struct {
	pkgconfigstore.Store
	getCalls int
}

func (c *countingStore) Get(ctx context.Context, key string) (string, bool, error) {
	c.getCalls++
	return c.Store.Get(ctx, key)
}

func TestService_PutRejectsInvalidContextSignals(t *testing.T) {
	ctx := context.Background()
	svc, err := pkgconfigstore.NewService(pkgconfigstore.NewMockStore())
	require.NoError(t, err)
	err = svc.Put(ctx, &targeting.PackageContextConfig{
		PackageID: "pkg-1",
		ContextSignals: &signalstore.Profile{
			AnyOf: []signalstore.Cfg{
				{SignalOwnerID: 1, KeyTypes: []signalstore.KeyType{"eid"}, SignalID: "x"},
			},
		},
	})
	require.Error(t, err, "identity key_type must be rejected at write time")
	if !errors.Is(err, signalstore.ErrCfgUnsafe) {
		t.Fatalf("expected ErrCfgUnsafe wrap, got %v", err)
	}
}

func TestReader_MGetReturnsConfigsInOrder(t *testing.T) {
	ctx := context.Background()
	store := pkgconfigstore.NewMockStore()
	svc, err := pkgconfigstore.NewService(store)
	require.NoError(t, err)
	require.NoError(t, svc.Put(ctx, &targeting.PackageContextConfig{PackageID: "pkg-a"}))
	require.NoError(t, svc.Put(ctx, &targeting.PackageContextConfig{PackageID: "pkg-c"}))

	r := pkgconfigstore.NewReader(store)
	got, err := r.MGet(ctx, []string{"pkg-a", "pkg-missing", "pkg-c"})
	require.NoError(t, err)
	require.Len(t, got, 3)
	require.NotNil(t, got[0])
	assert.Equal(t, "pkg-a", got[0].PackageID)
	assert.Nil(t, got[1], "missing key must yield nil in-place")
	require.NotNil(t, got[2])
	assert.Equal(t, "pkg-c", got[2].PackageID)
}

func TestCachedReader_MGetBatchesOnlyMisses(t *testing.T) {
	ctx := context.Background()
	store := pkgconfigstore.NewMockStore()
	svc, err := pkgconfigstore.NewService(store)
	require.NoError(t, err)
	require.NoError(t, svc.Put(ctx, &targeting.PackageContextConfig{PackageID: "pkg-a"}))
	require.NoError(t, svc.Put(ctx, &targeting.PackageContextConfig{PackageID: "pkg-b"}))

	counter := &mgetCounter{Store: store}
	r := pkgconfigstore.WithCache(
		pkgconfigstore.NewReader(counter),
		pkgconfigstore.CacheConfig{Size: 8, TTL: time.Minute},
	)

	// Warm the cache for pkg-a.
	_, _, err = r.Get(ctx, "pkg-a")
	require.NoError(t, err)
	counter.calls = 0
	counter.lastKeys = nil

	got, err := r.MGet(ctx, []string{"pkg-a", "pkg-b"})
	require.NoError(t, err)
	require.Len(t, got, 2)
	require.NotNil(t, got[0])
	require.NotNil(t, got[1])
	assert.Equal(t, 1, counter.calls, "cached pkg-a should not be re-fetched")
	require.Len(t, counter.lastKeys, 1)
	assert.Contains(t, counter.lastKeys[0], "pkg-b")
}

type mgetCounter struct {
	pkgconfigstore.Store
	calls    int
	lastKeys []string
}

func (c *mgetCounter) MGet(ctx context.Context, keys ...string) ([]string, error) {
	c.calls++
	c.lastKeys = append([]string(nil), keys...)
	return c.Store.MGet(ctx, keys...)
}
