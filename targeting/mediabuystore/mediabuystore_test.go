package mediabuystore_test

import (
	"context"
	"testing"
	"time"

	"github.com/adcontextprotocol/adcp-go/targeting/mediabuystore"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const sellerURL = "https://seller.example.com/agent"

func writerAndReader(t *testing.T) (*mediabuystore.Service, mediabuystore.Reader, *mediabuystore.MockStore) {
	t.Helper()
	store := mediabuystore.NewMockStore()
	svc, err := mediabuystore.NewService(store)
	require.NoError(t, err)
	return svc, mediabuystore.NewReader(store), store
}

func TestService_PutAndReadActivePackages(t *testing.T) {
	ctx := context.Background()
	svc, r, _ := writerAndReader(t)

	require.NoError(t, svc.Put(ctx, mediabuystore.MediaBuy{
		MediaBuyID: "mb-1", SellerAgentURL: sellerURL,
		StartDate: "2026-01-01", EndDate: "2026-12-31",
		Countries: []string{"US"}, PropertyIDs: []string{"pub-1"},
		Packages: []mediabuystore.MediaBuyPackage{
			{PackageID: "pkg-a", MediaBuyID: "mb-1"},
			{PackageID: "pkg-b", MediaBuyID: "mb-1"},
		},
	}))

	now := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	pkgs, err := r.ActivePackages(ctx, sellerURL, "pub-1", "US", "", now)
	require.NoError(t, err)
	require.Len(t, pkgs, 2)
	assert.Equal(t, "pkg-a", pkgs[0].PackageID)
	assert.Equal(t, "pkg-b", pkgs[1].PackageID)
}

func TestService_DateFilters(t *testing.T) {
	ctx := context.Background()
	svc, r, _ := writerAndReader(t)

	require.NoError(t, svc.Put(ctx, mediabuystore.MediaBuy{
		MediaBuyID: "mb-expired", SellerAgentURL: sellerURL,
		StartDate: "2025-01-01", EndDate: "2025-12-31",
		Packages: []mediabuystore.MediaBuyPackage{{PackageID: "pkg-old"}},
	}))
	require.NoError(t, svc.Put(ctx, mediabuystore.MediaBuy{
		MediaBuyID: "mb-future", SellerAgentURL: sellerURL,
		StartDate: "2027-01-01", EndDate: "2027-12-31",
		Packages: []mediabuystore.MediaBuyPackage{{PackageID: "pkg-future"}},
	}))
	require.NoError(t, svc.Put(ctx, mediabuystore.MediaBuy{
		MediaBuyID: "mb-current", SellerAgentURL: sellerURL,
		StartDate: "2026-01-01", EndDate: "2026-12-31",
		Packages: []mediabuystore.MediaBuyPackage{{PackageID: "pkg-current"}},
	}))

	now := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	pkgs, err := r.ActivePackages(ctx, sellerURL, "", "", "", now)
	require.NoError(t, err)
	require.Len(t, pkgs, 1)
	assert.Equal(t, "pkg-current", pkgs[0].PackageID)
}

func TestService_GeoAndPropertyFilters(t *testing.T) {
	ctx := context.Background()
	svc, r, _ := writerAndReader(t)

	require.NoError(t, svc.Put(ctx, mediabuystore.MediaBuy{
		MediaBuyID: "mb-us-only", SellerAgentURL: sellerURL,
		StartDate: "2026-01-01", EndDate: "2026-12-31",
		Countries: []string{"US"},
		Packages:  []mediabuystore.MediaBuyPackage{{PackageID: "pkg-us"}},
	}))
	require.NoError(t, svc.Put(ctx, mediabuystore.MediaBuy{
		MediaBuyID: "mb-pub-1-only", SellerAgentURL: sellerURL,
		StartDate: "2026-01-01", EndDate: "2026-12-31",
		PropertyIDs: []string{"pub-1"},
		Packages:    []mediabuystore.MediaBuyPackage{{PackageID: "pkg-pub1"}},
	}))

	now := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)

	pkgs, err := r.ActivePackages(ctx, sellerURL, "pub-1", "GB", "", now)
	require.NoError(t, err)
	require.Len(t, pkgs, 1, "GB filters out the US-only buy; pub-1 keeps the pub-1-only buy")
	assert.Equal(t, "pkg-pub1", pkgs[0].PackageID)

	pkgs, err = r.ActivePackages(ctx, sellerURL, "pub-2", "US", "", now)
	require.NoError(t, err)
	require.Len(t, pkgs, 1, "pub-2 filters out the pub-1-only buy; US keeps the US-only buy")
	assert.Equal(t, "pkg-us", pkgs[0].PackageID)
}

// TestService_PlacementFilter pins the per-package PlacementIDs gating:
// empty list means "any placement"; non-empty means "only these."
func TestService_PlacementFilter(t *testing.T) {
	ctx := context.Background()
	svc, r, _ := writerAndReader(t)

	require.NoError(t, svc.Put(ctx, mediabuystore.MediaBuy{
		MediaBuyID: "mb-mixed", SellerAgentURL: sellerURL,
		StartDate: "2026-01-01", EndDate: "2026-12-31",
		Packages: []mediabuystore.MediaBuyPackage{
			{PackageID: "pkg-anywhere"},
			{PackageID: "pkg-home-only", PlacementIDs: []string{"placement-home"}},
			{PackageID: "pkg-footer-only", PlacementIDs: []string{"placement-footer"}},
		},
	}))

	now := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)

	pkgs, err := r.ActivePackages(ctx, sellerURL, "", "", "placement-home", now)
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{"pkg-anywhere", "pkg-home-only"}, packageIDs(pkgs),
		"placement-home returns the unscoped package and the home-scoped one")

	pkgs, err = r.ActivePackages(ctx, sellerURL, "", "", "placement-footer", now)
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{"pkg-anywhere", "pkg-footer-only"}, packageIDs(pkgs))

	pkgs, err = r.ActivePackages(ctx, sellerURL, "", "", "", now)
	require.NoError(t, err)
	assert.Len(t, pkgs, 3,
		"empty placementID short-circuits the placement check (compatibility for callers that haven't plumbed it through)")
}

func packageIDs(pkgs []mediabuystore.MediaBuyPackage) []string {
	out := make([]string, len(pkgs))
	for i, p := range pkgs {
		out[i] = p.PackageID
	}
	return out
}

func TestService_Remove(t *testing.T) {
	ctx := context.Background()
	svc, r, _ := writerAndReader(t)

	require.NoError(t, svc.Put(ctx, mediabuystore.MediaBuy{
		MediaBuyID: "mb-1", SellerAgentURL: sellerURL,
		StartDate: "2026-01-01", EndDate: "2026-12-31",
		Packages: []mediabuystore.MediaBuyPackage{{PackageID: "pkg-a"}},
	}))

	require.NoError(t, svc.Remove(ctx, sellerURL, "mb-1"))

	now := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	pkgs, err := r.ActivePackages(ctx, sellerURL, "", "", "", now)
	require.NoError(t, err)
	assert.Empty(t, pkgs)
}

// TestCache_NegativeCaching_AvoidsRepeatLookups verifies that a Valkey
// miss (no such seller, no such buy) caches the absence: a second call
// with the same key does not re-issue the underlying store call.
func TestCache_NegativeCaching_AvoidsRepeatLookups(t *testing.T) {
	ctx := context.Background()
	base := mediabuystore.NewMockStore()
	counting := &countingStore{Store: base}
	r := mediabuystore.WithCache(mediabuystore.NewReader(counting), mediabuystore.CacheConfig{
		SellerSetSize: 8, SellerSetTTL: time.Minute,
		MediaBuySize: 8, MediaBuyTTL: time.Minute,
	})

	for range 5 {
		ids, err := r.MediaBuyIDsForSeller(ctx, "https://unknown.example/agent")
		require.NoError(t, err)
		assert.Empty(t, ids)
	}
	assert.Equal(t, 1, counting.setMembersCalls,
		"absent seller key should be cached after the first call")

	for range 5 {
		_, ok, err := r.MediaBuy(ctx, "mb-missing")
		require.NoError(t, err)
		assert.False(t, ok)
	}
	assert.Equal(t, 1, counting.getCalls,
		"absent media buy key should be cached after the first call")
}

// TestCache_PositiveCaching verifies that a populated entry serves
// subsequent calls from cache.
func TestCache_PositiveCaching(t *testing.T) {
	ctx := context.Background()
	base := mediabuystore.NewMockStore()
	svc, err := mediabuystore.NewService(base)
	require.NoError(t, err)
	require.NoError(t, svc.Put(ctx, mediabuystore.MediaBuy{
		MediaBuyID: "mb-1", SellerAgentURL: sellerURL,
		StartDate: "2026-01-01", EndDate: "2026-12-31",
		Packages: []mediabuystore.MediaBuyPackage{{PackageID: "pkg-a"}},
	}))

	counting := &countingStore{Store: base}
	r := mediabuystore.WithCache(mediabuystore.NewReader(counting), mediabuystore.CacheConfig{
		SellerSetSize: 8, SellerSetTTL: time.Minute,
		MediaBuySize: 8, MediaBuyTTL: time.Minute,
	})

	now := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	for range 4 {
		pkgs, err := r.ActivePackages(ctx, sellerURL, "", "", "", now)
		require.NoError(t, err)
		require.Len(t, pkgs, 1)
	}
	assert.Equal(t, 1, counting.setMembersCalls, "seller set fetched once")
	assert.Equal(t, 1, counting.mgetCalls+counting.getCalls,
		"media buy fetched once (via either MGet or Get path)")
}

// TestCorruptPayload_AlignedSkipAcrossPaths pins the contract that one
// corrupt JSON record skips one media buy in BOTH the direct and
// cached reader's ActivePackages — not abort the seller in one path
// and skip in the other. Reader.MediaBuy still returns
// ErrCorruptPayload for direct single-buy lookups; only the iteration
// path coalesces it to "skip".
func TestCorruptPayload_AlignedSkipAcrossPaths(t *testing.T) {
	ctx := context.Background()
	base := mediabuystore.NewMockStore()
	svc, err := mediabuystore.NewService(base)
	require.NoError(t, err)
	require.NoError(t, svc.Put(ctx, mediabuystore.MediaBuy{
		MediaBuyID: "mb-good", SellerAgentURL: sellerURL,
		StartDate: "2026-01-01", EndDate: "2026-12-31",
		Packages: []mediabuystore.MediaBuyPackage{{PackageID: "pkg-good"}},
	}))
	// Plant a corrupt payload that the seller-set still references.
	require.NoError(t, base.Set(ctx, mediabuystore.MediaBuyKey("mb-bad"), "{not-json", 0))
	require.NoError(t, base.SetAdd(ctx, mediabuystore.SellerSetKey(sellerURL), "mb-bad"))

	now := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)

	t.Run("direct_reader_skips_corrupt", func(t *testing.T) {
		r := mediabuystore.NewReader(base)
		pkgs, err := r.ActivePackages(ctx, sellerURL, "", "", "", now)
		require.NoError(t, err, "one bad payload must not sink the whole seller")
		require.Len(t, pkgs, 1)
		assert.Equal(t, "pkg-good", pkgs[0].PackageID)
	})

	t.Run("cached_reader_skips_corrupt", func(t *testing.T) {
		r := mediabuystore.WithCache(mediabuystore.NewReader(base), mediabuystore.CacheConfig{
			SellerSetSize: 8, SellerSetTTL: time.Minute,
			MediaBuySize: 8, MediaBuyTTL: time.Minute,
		})
		pkgs, err := r.ActivePackages(ctx, sellerURL, "", "", "", now)
		require.NoError(t, err, "cached path must align with direct: skip, not abort")
		require.Len(t, pkgs, 1)
		assert.Equal(t, "pkg-good", pkgs[0].PackageID)
	})

	t.Run("single_buy_lookup_surfaces_ErrCorruptPayload", func(t *testing.T) {
		r := mediabuystore.NewReader(base)
		_, _, err := r.MediaBuy(ctx, "mb-bad")
		require.Error(t, err)
		assert.ErrorIs(t, err, mediabuystore.ErrCorruptPayload,
			"single-buy lookup must surface the sentinel so callers can classify")
	})
}

// countingStore wraps a mediabuystore.Store and tallies calls; used to
// assert how the cache wrapper interacts with the underlying store.
type countingStore struct {
	mediabuystore.Store
	setMembersCalls int
	mgetCalls       int
	getCalls        int
}

func (c *countingStore) SetMembers(ctx context.Context, key string) ([]string, error) {
	c.setMembersCalls++
	return c.Store.SetMembers(ctx, key)
}
func (c *countingStore) MGet(ctx context.Context, keys ...string) ([]string, error) {
	c.mgetCalls++
	return c.Store.MGet(ctx, keys...)
}
func (c *countingStore) Get(ctx context.Context, key string) (string, bool, error) {
	c.getCalls++
	return c.Store.Get(ctx, key)
}
