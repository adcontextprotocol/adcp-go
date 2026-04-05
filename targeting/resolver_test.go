package targeting

import (
	"context"
	"testing"
	"time"
)

func TestResolver_NoMediaBuys(t *testing.T) {
	store := NewMockStore()
	pkgs, err := ResolvePackages(context.Background(), store, "seller-1", "pub-1", "US", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if len(pkgs) != 0 {
		t.Errorf("expected 0 packages, got %d", len(pkgs))
	}
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
	if err != nil {
		t.Fatal(err)
	}
	if len(pkgs) != 2 {
		t.Fatalf("expected 2 packages, got %d", len(pkgs))
	}
	if pkgs[0].PackageID != "pkg-food" || pkgs[1].PackageID != "pkg-tech" {
		t.Errorf("unexpected packages: %+v", pkgs)
	}
	if pkgs[0].MediaBuyID != "mb-1" {
		t.Errorf("expected media_buy_id mb-1, got %s", pkgs[0].MediaBuyID)
	}
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
	if err != nil {
		t.Fatal(err)
	}
	if len(pkgs) != 0 {
		t.Errorf("expected 0 packages (expired), got %d", len(pkgs))
	}
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
	if err != nil {
		t.Fatal(err)
	}
	if len(pkgs) != 0 {
		t.Errorf("expected 0 packages (future), got %d", len(pkgs))
	}
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
	if err != nil {
		t.Fatal(err)
	}
	if len(pkgs) != 0 {
		t.Errorf("expected 0 packages (geo mismatch), got %d", len(pkgs))
	}
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
	if err != nil {
		t.Fatal(err)
	}
	if len(pkgs) != 0 {
		t.Errorf("expected 0 packages (property mismatch), got %d", len(pkgs))
	}
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
	if err != nil {
		t.Fatal(err)
	}
	if len(pkgs) != 1 {
		t.Errorf("expected 1 package (all geos), got %d", len(pkgs))
	}
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
	if err != nil {
		t.Fatal(err)
	}
	if len(pkgs) != 1 {
		t.Errorf("expected 1 package (all properties), got %d", len(pkgs))
	}
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
	if err != nil {
		t.Fatal(err)
	}

	// Check packages resolved.
	if len(resolved.Packages) != 2 {
		t.Fatalf("expected 2 packages, got %d", len(resolved.Packages))
	}

	// PropertyIndex: RID 1 should map to both packages.
	if pkgs := resolved.PropertyIndex[1]; len(pkgs) != 2 {
		t.Errorf("PropertyIndex[1]: expected 2 packages, got %d", len(pkgs))
	}
	// RID 4 should map to pkg-tech only.
	if pkgs := resolved.PropertyIndex[4]; len(pkgs) != 1 || pkgs[0] != "pkg-tech" {
		t.Errorf("PropertyIndex[4]: expected [pkg-tech], got %v", pkgs)
	}

	// TopicIndex.
	if pkgs := resolved.TopicIndex["food.cooking"]; len(pkgs) != 1 || pkgs[0] != "pkg-food" {
		t.Errorf("TopicIndex[food.cooking]: expected [pkg-food], got %v", pkgs)
	}
	if pkgs := resolved.TopicIndex["tech.gadgets"]; len(pkgs) != 1 || pkgs[0] != "pkg-tech" {
		t.Errorf("TopicIndex[tech.gadgets]: expected [pkg-tech], got %v", pkgs)
	}

	// URLBlocklistIndex.
	badHash := HashURL("article:bad")
	if pkgs := resolved.URLBlocklistIndex[badHash]; len(pkgs) != 1 || pkgs[0] != "pkg-food" {
		t.Errorf("URLBlocklistIndex: expected [pkg-food], got %v", pkgs)
	}

	// SegmentIndex.
	if pkgs := resolved.SegmentIndex["cooking_fans"]; len(pkgs) != 2 {
		t.Errorf("SegmentIndex[cooking_fans]: expected 2 packages, got %d", len(pkgs))
	}
	if pkgs := resolved.SegmentIndex["tech_enthusiasts"]; len(pkgs) != 1 {
		t.Errorf("SegmentIndex[tech_enthusiasts]: expected 1 package, got %d", len(pkgs))
	}

	// Configs loaded.
	if resolved.ContextConfigs["pkg-food"] == nil {
		t.Error("expected context config for pkg-food")
	}
	if resolved.IdentityConfigs["pkg-food"] == nil {
		t.Error("expected identity config for pkg-food")
	}
	if resolved.CampaignConfigs["campaign-1"] == nil {
		t.Error("expected campaign config for campaign-1")
	}

	// Test helper methods.
	if resolved.IsURLBlocked("pkg-food", badHash) != true {
		t.Error("expected article:bad to be blocked for pkg-food")
	}
	if resolved.IsURLBlocked("pkg-tech", badHash) != false {
		t.Error("expected article:bad to NOT be blocked for pkg-tech")
	}

	candidates := resolved.TopicCandidates([]string{"food.cooking", "tech.gadgets"})
	if len(candidates) != 2 {
		t.Errorf("TopicCandidates: expected 2, got %d", len(candidates))
	}

	segCandidates := resolved.SegmentCandidates([]string{"cooking_fans"})
	if len(segCandidates) != 2 {
		t.Errorf("SegmentCandidates: expected 2, got %d", len(segCandidates))
	}
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
	if err != nil {
		t.Fatal(err)
	}
	if len(pkgs) != 1 {
		t.Errorf("expected 1 package, got %d", len(pkgs))
	}
	if len(pkgs) > 0 && pkgs[0].PackageID != "pkg-active" {
		t.Errorf("expected pkg-active, got %s", pkgs[0].PackageID)
	}
}
