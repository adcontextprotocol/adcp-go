package targeting

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestResolver_NoMediaBuys(t *testing.T) {
	store := NewMockStore()
	pkgs, err := ResolvePackages(context.Background(), store, "seller-1", "pub-1", "US", time.Now())
	require.NoError(t, err)
	assert.Empty(t, pkgs)
}

func TestResolver_ActiveMediaBuy(t *testing.T) {
	store := NewMockStore()
	store.SetMediaBuy(MediaBuy{
		MediaBuyID:  "mb-1",
		SellerID:    "seller-1",
		StartDate:   "2026-01-01",
		EndDate:     "2026-12-31",
		Countries:   []string{"US", "GB"},
		PropertyIDs: []string{"pub-oakwood"},
		Packages: []MediaBuyPackage{
			{PackageID: "pkg-food", MediaBuyID: "mb-1", FormatIDs: []string{"banner-300x250"}},
			{PackageID: "pkg-tech", MediaBuyID: "mb-1"},
		},
	})

	now := time.Date(2026, 6, 15, 0, 0, 0, 0, time.UTC)
	pkgs, err := ResolvePackages(context.Background(), store, "seller-1", "pub-oakwood", "US", now)
	require.NoError(t, err)
	require.Len(t, pkgs, 2)
	assert.Equal(t, "pkg-food", pkgs[0].PackageID)
	assert.Equal(t, "pkg-tech", pkgs[1].PackageID)
	assert.Equal(t, "mb-1", pkgs[0].MediaBuyID)
}

func TestResolver_ExpiredMediaBuy(t *testing.T) {
	store := NewMockStore()
	store.SetMediaBuy(MediaBuy{
		MediaBuyID: "mb-expired",
		SellerID:   "seller-1",
		StartDate:  "2025-01-01",
		EndDate:    "2025-12-31",
		Packages:   []MediaBuyPackage{{PackageID: "pkg-old", MediaBuyID: "mb-expired"}},
	})

	now := time.Date(2026, 6, 15, 0, 0, 0, 0, time.UTC)
	pkgs, err := ResolvePackages(context.Background(), store, "seller-1", "pub-1", "US", now)
	require.NoError(t, err)
	assert.Empty(t, pkgs, "expected 0 packages (expired)")
}

func TestResolver_FutureMediaBuy(t *testing.T) {
	store := NewMockStore()
	store.SetMediaBuy(MediaBuy{
		MediaBuyID: "mb-future",
		SellerID:   "seller-1",
		StartDate:  "2027-01-01",
		EndDate:    "2027-12-31",
		Packages:   []MediaBuyPackage{{PackageID: "pkg-future", MediaBuyID: "mb-future"}},
	})

	now := time.Date(2026, 6, 15, 0, 0, 0, 0, time.UTC)
	pkgs, err := ResolvePackages(context.Background(), store, "seller-1", "pub-1", "US", now)
	require.NoError(t, err)
	assert.Empty(t, pkgs, "expected 0 packages (future)")
}

func TestResolver_GeoMismatch(t *testing.T) {
	store := NewMockStore()
	store.SetMediaBuy(MediaBuy{
		MediaBuyID: "mb-uk",
		SellerID:   "seller-1",
		StartDate:  "2026-01-01",
		EndDate:    "2026-12-31",
		Countries:  []string{"GB"},
		Packages:   []MediaBuyPackage{{PackageID: "pkg-uk", MediaBuyID: "mb-uk"}},
	})

	now := time.Date(2026, 6, 15, 0, 0, 0, 0, time.UTC)
	pkgs, err := ResolvePackages(context.Background(), store, "seller-1", "pub-1", "US", now)
	require.NoError(t, err)
	assert.Empty(t, pkgs, "expected 0 packages (geo mismatch)")
}

func TestResolver_PropertyMismatch(t *testing.T) {
	store := NewMockStore()
	store.SetMediaBuy(MediaBuy{
		MediaBuyID:  "mb-specific",
		SellerID:    "seller-1",
		StartDate:   "2026-01-01",
		EndDate:     "2026-12-31",
		PropertyIDs: []string{"pub-premium"},
		Packages:    []MediaBuyPackage{{PackageID: "pkg-premium", MediaBuyID: "mb-specific"}},
	})

	now := time.Date(2026, 6, 15, 0, 0, 0, 0, time.UTC)
	pkgs, err := ResolvePackages(context.Background(), store, "seller-1", "pub-other", "US", now)
	require.NoError(t, err)
	assert.Empty(t, pkgs, "expected 0 packages (property mismatch)")
}

func TestResolver_EmptyCountries_AllGeos(t *testing.T) {
	store := NewMockStore()
	store.SetMediaBuy(MediaBuy{
		MediaBuyID: "mb-global",
		SellerID:   "seller-1",
		StartDate:  "2026-01-01",
		EndDate:    "2026-12-31",
		Countries:  nil, // empty = all geos
		Packages:   []MediaBuyPackage{{PackageID: "pkg-global", MediaBuyID: "mb-global"}},
	})

	now := time.Date(2026, 6, 15, 0, 0, 0, 0, time.UTC)
	pkgs, err := ResolvePackages(context.Background(), store, "seller-1", "pub-1", "JP", now)
	require.NoError(t, err)
	assert.Len(t, pkgs, 1, "expected 1 package (all geos)")
}

func TestResolver_EmptyPropertyIDs_AllProperties(t *testing.T) {
	store := NewMockStore()
	store.SetMediaBuy(MediaBuy{
		MediaBuyID:  "mb-all-props",
		SellerID:    "seller-1",
		StartDate:   "2026-01-01",
		EndDate:     "2026-12-31",
		PropertyIDs: nil, // empty = all properties
		Packages:    []MediaBuyPackage{{PackageID: "pkg-all", MediaBuyID: "mb-all-props"}},
	})

	now := time.Date(2026, 6, 15, 0, 0, 0, 0, time.UTC)
	pkgs, err := ResolvePackages(context.Background(), store, "seller-1", "any-property", "US", now)
	require.NoError(t, err)
	assert.Len(t, pkgs, 1, "expected 1 package (all properties)")
}

func TestResolve_BuildsIndexes(t *testing.T) {
	store := NewMockStore()
	now := time.Date(2026, 6, 15, 0, 0, 0, 0, time.UTC)

	store.SetMediaBuy(MediaBuy{
		MediaBuyID:  "mb-1",
		SellerID:    "seller-1",
		StartDate:   "2026-01-01",
		EndDate:     "2026-12-31",
		Countries:   []string{"US"},
		PropertyIDs: []string{"pub-1"},
		Packages: []MediaBuyPackage{
			{PackageID: "pkg-food", MediaBuyID: "mb-1"},
			{PackageID: "pkg-tech", MediaBuyID: "mb-1"},
		},
	})

	// Context configs.
	store.SetPackageContextConfig("pkg-food", PackageContextConfig{
		PackageID:    "pkg-food",
		TopicTargets: true,
		URLBlocklist: true,
		PropertyRIDs: []uint64{1, 2, 3},
	})
	store.SetPackageContextConfig("pkg-tech", PackageContextConfig{
		PackageID:    "pkg-tech",
		TopicTargets: true,
		PropertyRIDs: []uint64{1, 4},
	})

	// Topic data.
	store.SetAdd("topics:package:pkg-food", "food.cooking", "food.recipes")
	store.SetAdd("topics:package:pkg-tech", "tech.gadgets", "tech.reviews")

	// URL blocklist.
	store.SetAdd("url:blocklist:pkg-food", HashURL("article:bad"))

	// Identity configs.
	store.SetPackageIdentityConfig("pkg-food", PackageIdentityConfig{
		CampaignID:     "campaign-1",
		TargetSegments: []string{"cooking_fans"},
		FrequencyRules: []FrequencyRuleJSON{{MaxCount: 5, WindowSeconds: 86400}},
	})
	store.SetPackageIdentityConfig("pkg-tech", PackageIdentityConfig{
		TargetSegments: []string{"tech_enthusiasts", "cooking_fans"},
	})
	store.SetCampaignFreqConfig("campaign-1", CampaignFreqConfig{
		FrequencyRules: []FrequencyRuleJSON{{MaxCount: 10, WindowSeconds: 604800}},
	})

	resolved, err := Resolve(context.Background(), store, "seller-1", "pub-1", "US", now)
	require.NoError(t, err)

	// Check packages resolved.
	require.Len(t, resolved.Packages, 2)

	// PropertyIndex: RID 1 should map to both packages.
	assert.Len(t, resolved.PropertyIndex[1], 2, "PropertyIndex[1]: expected 2 packages")
	// RID 4 should map to pkg-tech only.
	require.Len(t, resolved.PropertyIndex[4], 1)
	assert.Equal(t, "pkg-tech", resolved.PropertyIndex[4][0])

	// TopicIndex.
	require.Len(t, resolved.TopicIndex["food.cooking"], 1)
	assert.Equal(t, "pkg-food", resolved.TopicIndex["food.cooking"][0])
	require.Len(t, resolved.TopicIndex["tech.gadgets"], 1)
	assert.Equal(t, "pkg-tech", resolved.TopicIndex["tech.gadgets"][0])

	// URLBlocklistIndex.
	badHash := HashURL("article:bad")
	require.Len(t, resolved.URLBlocklistIndex[badHash], 1)
	assert.Equal(t, "pkg-food", resolved.URLBlocklistIndex[badHash][0])

	// SegmentIndex.
	assert.Len(t, resolved.SegmentIndex["cooking_fans"], 2)
	assert.Len(t, resolved.SegmentIndex["tech_enthusiasts"], 1)

	// Configs loaded.
	assert.NotNil(t, resolved.ContextConfigs["pkg-food"], "expected context config for pkg-food")
	assert.NotNil(t, resolved.IdentityConfigs["pkg-food"], "expected identity config for pkg-food")
	assert.NotNil(t, resolved.CampaignConfigs["campaign-1"], "expected campaign config for campaign-1")

	// Test helper methods.
	assert.True(t, resolved.IsURLBlocked("pkg-food", badHash))
	assert.False(t, resolved.IsURLBlocked("pkg-tech", badHash))

	candidates := resolved.TopicCandidates([]string{"food.cooking", "tech.gadgets"})
	assert.Len(t, candidates, 2)

	segCandidates := resolved.SegmentCandidates([]string{"cooking_fans"})
	assert.Len(t, segCandidates, 2)
}

func TestResolver_MultipleMediaBuys_MixedResults(t *testing.T) {
	store := NewMockStore()
	now := time.Date(2026, 6, 15, 0, 0, 0, 0, time.UTC)

	// Active, US, all properties — should match.
	store.SetMediaBuy(MediaBuy{
		MediaBuyID: "mb-active",
		SellerID:   "seller-1",
		StartDate:  "2026-01-01",
		EndDate:    "2026-12-31",
		Countries:  []string{"US"},
		Packages:   []MediaBuyPackage{{PackageID: "pkg-active", MediaBuyID: "mb-active"}},
	})

	// Expired — should not match.
	store.SetMediaBuy(MediaBuy{
		MediaBuyID: "mb-old",
		SellerID:   "seller-1",
		StartDate:  "2025-01-01",
		EndDate:    "2025-06-30",
		Packages:   []MediaBuyPackage{{PackageID: "pkg-old", MediaBuyID: "mb-old"}},
	})

	// Active but wrong geo — should not match.
	store.SetMediaBuy(MediaBuy{
		MediaBuyID: "mb-uk-only",
		SellerID:   "seller-1",
		StartDate:  "2026-01-01",
		EndDate:    "2026-12-31",
		Countries:  []string{"GB"},
		Packages:   []MediaBuyPackage{{PackageID: "pkg-uk", MediaBuyID: "mb-uk-only"}},
	})

	pkgs, err := ResolvePackages(context.Background(), store, "seller-1", "pub-1", "US", now)
	require.NoError(t, err)
	require.Len(t, pkgs, 1)
	assert.Equal(t, "pkg-active", pkgs[0].PackageID)
}
