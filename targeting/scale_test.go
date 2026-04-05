package targeting

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/adcontextprotocol/adcp-go/tmproto"
)

// TestScale_PropertyBitmap measures property bitmap lookup at increasing scale.
func TestScale_PropertyBitmap(t *testing.T) {
	t.Log("")
	t.Log("=== Property Bitmap: O(1) map lookup ===")
	t.Log("")

	for _, n := range []int{100, 1_000, 10_000, 100_000, 1_000_000} {
		bm := make(MapBitmap, n)
		for i := range n {
			bm[uint64(i)] = struct{}{} //nolint:gosec // test
		}

		store := NewMockStore()
		engine := NewEngine(EngineConfig{
			ProviderID: "bench",
			Store:      store,
			Properties: PropertyList{Global: bm},
			Packages:   []PackageConfig{{PackageID: "pkg-1"}},
		})

		req := &tmproto.ContextMatchRequest{
			RequestID:     "bench",
			PropertyRID:   uint64(n / 2), //nolint:gosec // test value
			AvailablePkgs: []tmproto.AvailablePackage{{PackageID: "pkg-1"}},
		}

		const iterations = 50_000
		start := time.Now()
		for range iterations {
			_, _ = engine.EvaluateContext(context.Background(), req)
		}
		elapsed := time.Since(start)
		t.Logf("  %8d properties: %v/eval", n, elapsed/iterations)
	}
	t.Log("")
}

// TestScale_Campaigns measures identity eval with increasing campaign count.
// Only the relevant campaign is loaded per package — not all campaigns.
func TestScale_Campaigns(t *testing.T) {
	t.Log("")
	t.Log("=== Campaigns: per-request cache, only relevant campaigns loaded ===")
	t.Log("")

	for _, numCampaigns := range []int{1, 10, 100, 1_000, 10_000} {
		store := NewMockStore()

		// Create N campaigns in the Store.
		for i := range numCampaigns {
			store.SetCampaignFreqConfig(fmt.Sprintf("campaign-%d", i), CampaignFreqConfig{
				FrequencyRules: []FrequencyRuleJSON{{MaxCount: 10, WindowSeconds: 86400}},
			})
		}

		// Create 5 packages, all in the LAST campaign (worst case: must skip past others).
		var pkgs []PackageConfig
		for i := range 5 {
			pkgID := fmt.Sprintf("pkg-%d", i)
			pkgs = append(pkgs, PackageConfig{PackageID: pkgID})
			store.SetPackageIdentityConfig(pkgID, PackageIdentityConfig{
				CampaignID:     fmt.Sprintf("campaign-%d", numCampaigns-1),
				FrequencyRules: []FrequencyRuleJSON{{MaxCount: 5, WindowSeconds: 86400}},
			})
		}

		engine := NewEngine(EngineConfig{
			ProviderID: "bench",
			Store:      store,
			Packages:   pkgs,
		})

		req := &tmproto.IdentityMatchRequest{
			RequestID:  "bench",
			UserToken:  "tok-bench",
			PackageIDs: []string{"pkg-0", "pkg-1", "pkg-2", "pkg-3", "pkg-4"},
		}

		const iterations = 10_000
		start := time.Now()
		for range iterations {
			_, _ = engine.EvaluateIdentity(context.Background(), req)
		}
		elapsed := time.Since(start)
		t.Logf("  %6d campaigns in Store: %v/eval (5 packages, 1 campaign loaded)", numCampaigns, elapsed/iterations)
	}
	t.Log("")
}

// TestScale_AudienceSegmentSize measures audience matching as segment membership grows.
func TestScale_AudienceSegmentSize(t *testing.T) {
	t.Log("")
	t.Log("=== Audience Segment Size: O(1) set membership ===")
	t.Log("")

	for _, n := range []int{100, 1_000, 10_000, 100_000, 1_000_000} {
		store := NewMockStore()

		// Build a segment with N members.
		for i := range n {
			store.SetAdd("audience:big-segment", fmt.Sprintf("hash-%d", i))
		}

		store.SetPackageIdentityConfig("pkg-1", PackageIdentityConfig{
			TargetSegments: []string{"big-segment"},
		})

		// The lookup user IS in the segment (worst case for "check then pass").
		targetHash := HashToken("tok-bench")
		store.SetAdd("audience:big-segment", targetHash)

		engine := NewEngine(EngineConfig{
			ProviderID: "bench",
			Store:      store,
			Packages:   []PackageConfig{{PackageID: "pkg-1"}},
		})

		req := &tmproto.IdentityMatchRequest{
			RequestID:  "bench",
			UserToken:  "tok-bench",
			PackageIDs: []string{"pkg-1"},
		}

		const iterations = 50_000
		start := time.Now()
		for range iterations {
			_, _ = engine.EvaluateIdentity(context.Background(), req)
		}
		elapsed := time.Since(start)
		t.Logf("  %8d segment members: %v/eval", n, elapsed/iterations)
	}
	t.Log("")
}

// TestScale_FrequencyCapExposures measures freq cap checking as exposure history grows.
func TestScale_FrequencyCapExposures(t *testing.T) {
	t.Log("")
	t.Log("=== Frequency Cap: ZCount over growing sorted set ===")
	t.Log("")

	for _, numExposures := range []int{0, 10, 100, 1_000, 10_000} {
		store := NewMockStore()
		now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
		store.Now = func() time.Time { return now }

		store.SetPackageIdentityConfig("pkg-1", PackageIdentityConfig{
			FrequencyRules: []FrequencyRuleJSON{{MaxCount: 100_000, WindowSeconds: 86400}},
		})

		// Pre-populate exposure history.
		tokenHash := HashToken("tok-bench")
		key := fmt.Sprintf("freq:pkg:pkg-1:%s", tokenHash)
		for i := range numExposures {
			ts := float64(now.Add(-time.Duration(i) * time.Minute).UnixMilli())
			_ = store.ZAdd(context.Background(), key, ts, fmt.Sprintf("%d:pkg-1", i))
		}

		engine := NewEngine(EngineConfig{
			ProviderID: "bench",
			Store:      store,
			Packages:   []PackageConfig{{PackageID: "pkg-1"}},
		})
		engine.Now = func() time.Time { return now }

		req := &tmproto.IdentityMatchRequest{
			RequestID:  "bench",
			UserToken:  "tok-bench",
			PackageIDs: []string{"pkg-1"},
		}

		const iterations = 20_000
		start := time.Now()
		for range iterations {
			_, _ = engine.EvaluateIdentity(context.Background(), req)
		}
		elapsed := time.Since(start)
		t.Logf("  %6d prior exposures: %v/eval", numExposures, elapsed/iterations)
	}
	t.Log("")
}

// TestScale_TopicSetSize measures topic matching as topic sets grow.
func TestScale_TopicSetSize(t *testing.T) {
	t.Log("")
	t.Log("=== Topic Set Size: O(intersection) via set intersect ===")
	t.Log("")

	for _, n := range []int{10, 100, 1_000, 10_000} {
		store := NewMockStore()

		// Package has N topics.
		for i := range n {
			store.SetAdd("topics:package:pkg-1", fmt.Sprintf("topic-%d", i))
		}
		// Artifact matches 1 topic (the last one).
		store.SetAdd("topics:artifact:article:test", fmt.Sprintf("topic-%d", n-1))

		engine := NewEngine(EngineConfig{
			ProviderID: "bench",
			Store:      store,
			Properties: PropertyList{Global: NewMapBitmap(1)},
			Packages:   []PackageConfig{{PackageID: "pkg-1", TopicTargets: true}},
		})

		req := &tmproto.ContextMatchRequest{
			RequestID:     "bench",
			PropertyRID:   1,
			Artifacts:     []string{"article:test"},
			AvailablePkgs: []tmproto.AvailablePackage{{PackageID: "pkg-1"}},
		}

		const iterations = 20_000
		start := time.Now()
		for range iterations {
			_, _ = engine.EvaluateContext(context.Background(), req)
		}
		elapsed := time.Since(start)
		t.Logf("  %6d topics per package: %v/eval", n, elapsed/iterations)
	}
	t.Log("")
}

// TestScale_URLBlocklistSize measures URL blocklist checking as the list grows.
func TestScale_URLBlocklistSize(t *testing.T) {
	t.Log("")
	t.Log("=== URL Blocklist Size: O(1) set membership ===")
	t.Log("")

	for _, n := range []int{100, 1_000, 10_000, 100_000} {
		store := NewMockStore()

		for i := range n {
			store.SetAdd("url:blocklist:pkg-1", HashURL(fmt.Sprintf("article:blocked-%d", i)))
		}

		engine := NewEngine(EngineConfig{
			ProviderID: "bench",
			Store:      store,
			Properties: PropertyList{Global: NewMapBitmap(1)},
			Packages:   []PackageConfig{{PackageID: "pkg-1", URLBlocklist: true}},
		})

		// Check a URL that is NOT blocked (worst case: full lookup, no short-circuit).
		req := &tmproto.ContextMatchRequest{
			RequestID:     "bench",
			PropertyRID:   1,
			Artifacts:     []string{"article:safe-content"},
			AvailablePkgs: []tmproto.AvailablePackage{{PackageID: "pkg-1"}},
		}

		const iterations = 50_000
		start := time.Now()
		for range iterations {
			_, _ = engine.EvaluateContext(context.Background(), req)
		}
		elapsed := time.Since(start)
		t.Logf("  %6d blocked URLs: %v/eval", n, elapsed/iterations)
	}
	t.Log("")
}

// TestScale_DynamicVsStatic compares dynamic (Store-backed) vs static package config.
func TestScale_DynamicVsStatic(t *testing.T) {
	t.Log("")
	t.Log("=== Dynamic vs Static Package Config ===")
	t.Log("")

	for _, numPkgs := range []int{1, 10, 50, 100, 500} {
		store := NewMockStore()

		// Setup for BOTH modes.
		var staticPkgs []PackageConfig
		var available []tmproto.AvailablePackage
		var pkgIDs []string
		for i := range numPkgs {
			pkgID := fmt.Sprintf("pkg-%d", i)
			staticPkgs = append(staticPkgs, PackageConfig{PackageID: pkgID, TopicTargets: true})
			available = append(available, tmproto.AvailablePackage{PackageID: pkgID})
			pkgIDs = append(pkgIDs, pkgID)
			store.SetAdd(fmt.Sprintf("topics:package:%s", pkgID), "food.cooking")
			store.SetPackageIdentityConfig(pkgID, PackageIdentityConfig{
				FrequencyRules: []FrequencyRuleJSON{{MaxCount: 10, WindowSeconds: 86400}},
			})
			store.SetPackageContextConfig(pkgID, PackageContextConfig{
				PackageID:    pkgID,
				TopicTargets: true,
			})
		}
		store.SetAdd("topics:artifact:article:food", "food.cooking")

		// Static engine.
		staticEngine := NewEngine(EngineConfig{
			ProviderID: "bench",
			Store:      store,
			Properties: PropertyList{Global: NewMapBitmap(1)},
			Packages:   staticPkgs,
		})

		// Dynamic engine.
		dynamicEngine := NewEngine(EngineConfig{
			ProviderID:      "bench",
			Store:           store,
			Properties:      PropertyList{Global: NewMapBitmap(1)},
			DynamicPackages: true,
		})

		ctxReq := &tmproto.ContextMatchRequest{
			RequestID:     "bench",
			PropertyRID:   1,
			Artifacts:     []string{"article:food"},
			AvailablePkgs: available,
		}
		idReq := &tmproto.IdentityMatchRequest{
			RequestID:  "bench",
			UserToken:  "tok-bench",
			PackageIDs: pkgIDs,
		}

		const iterations = 5_000

		// Static mode.
		start := time.Now()
		for range iterations {
			_, _ = staticEngine.EvaluateContext(context.Background(), ctxReq)
			_, _ = staticEngine.EvaluateIdentity(context.Background(), idReq)
		}
		staticTime := time.Since(start)

		// Dynamic mode.
		start = time.Now()
		for range iterations {
			_, _ = dynamicEngine.EvaluateContext(context.Background(), ctxReq)
			_, _ = dynamicEngine.EvaluateIdentity(context.Background(), idReq)
		}
		dynamicTime := time.Since(start)

		overhead := float64(dynamicTime-staticTime) / float64(staticTime) * 100
		t.Logf("  %3d packages: static=%v  dynamic=%v  overhead=%.0f%%",
			numPkgs, staticTime/iterations, dynamicTime/iterations, overhead)
	}
	t.Log("")
}

// TestScale_Resolver measures resolver performance with increasing media buys.
func TestScale_Resolver(t *testing.T) {
	t.Log("")
	t.Log("=== Resolver: media buy count scaling (2 Store round-trips) ===")
	t.Log("")

	for _, numMBs := range []int{1, 10, 50, 100, 500} {
		store := NewMockStore()
		now := time.Date(2026, 6, 15, 0, 0, 0, 0, time.UTC)

		for i := range numMBs {
			store.SetMediaBuy(MediaBuy{
				MediaBuyID:  fmt.Sprintf("mb-%d", i),
				SellerID:    "seller-1",
				StartDate:   "2026-01-01",
				EndDate:     "2026-12-31",
				Countries:   []string{"US"},
				PropertyIDs: []string{"pub-1"},
				Packages: []MediaBuyPackage{
					{PackageID: fmt.Sprintf("pkg-%d-a", i), MediaBuyID: fmt.Sprintf("mb-%d", i)},
					{PackageID: fmt.Sprintf("pkg-%d-b", i), MediaBuyID: fmt.Sprintf("mb-%d", i)},
				},
			})
		}

		const iterations = 10_000
		start := time.Now()
		var totalPkgs int
		for range iterations {
			pkgs, _ := ResolvePackages(context.Background(), store, "seller-1", "pub-1", "US", now)
			totalPkgs = len(pkgs)
		}
		elapsed := time.Since(start)
		t.Logf("  %3d media buys → %d packages: %v/resolve", numMBs, totalPkgs, elapsed/iterations)
	}
	t.Log("")
}

// TestScale_ResolvedVsDynamic compares resolved (cached indexes) vs dynamic (MGet per request).
func TestScale_ResolvedVsDynamic(t *testing.T) {
	t.Log("")
	t.Log("=== Resolved (cached indexes) vs Dynamic (MGet per request) ===")
	t.Log("")

	for _, numPkgs := range []int{10, 50, 100, 500} {
		store := NewMockStore()
		now := time.Date(2026, 6, 15, 0, 0, 0, 0, time.UTC)

		// Set up a seller with media buys.
		var mbPkgs []MediaBuyPackage
		var available []tmproto.AvailablePackage
		var pkgIDs []string
		for i := range numPkgs {
			pkgID := fmt.Sprintf("pkg-%d", i)
			mbPkgs = append(mbPkgs, MediaBuyPackage{PackageID: pkgID, MediaBuyID: "mb-1"})
			available = append(available, tmproto.AvailablePackage{PackageID: pkgID})
			pkgIDs = append(pkgIDs, pkgID)

			store.SetAdd("topics:package:"+pkgID, "food.cooking")
			store.SetPackageContextConfig(pkgID, PackageContextConfig{
				PackageID:    pkgID,
				TopicTargets: true,
				PropertyRIDs: []uint64{1},
			})
			store.SetPackageIdentityConfig(pkgID, PackageIdentityConfig{
				TargetSegments: []string{"cooking_fans"},
				FrequencyRules: []FrequencyRuleJSON{{MaxCount: 10, WindowSeconds: 86400}},
			})
		}
		store.SetMediaBuy(MediaBuy{
			MediaBuyID: "mb-1", SellerID: "seller-1",
			StartDate: "2026-01-01", EndDate: "2026-12-31",
			Countries: []string{"US"}, PropertyIDs: []string{"pub-1"},
			Packages: mbPkgs,
		})
		store.SetAdd("topics:artifact:article:food", "food.cooking")
		store.SetAdd("audience:cooking_fans", HashToken("tok-bench"))

		// Build resolved once.
		resolved, err := Resolve(context.Background(), store, "seller-1", "pub-1", "US", now)
		if err != nil {
			t.Fatal(err)
		}

		engine := NewEngine(EngineConfig{
			ProviderID:      "bench",
			Store:           store,
			Properties:      PropertyList{Global: NewMapBitmap(1)},
			DynamicPackages: true,
		})

		ctxReq := &tmproto.ContextMatchRequest{
			RequestID:     "bench",
			PropertyRID:   1,
			Artifacts:     []string{"article:food"},
			AvailablePkgs: available,
		}
		idReq := &tmproto.IdentityMatchRequest{
			RequestID:  "bench",
			UserToken:  "tok-bench",
			PackageIDs: pkgIDs,
		}

		const iterations = 2_000

		// Dynamic mode (MGet per request).
		start := time.Now()
		for range iterations {
			_, _ = engine.EvaluateContext(context.Background(), ctxReq)
			_, _ = engine.EvaluateIdentity(context.Background(), idReq)
		}
		dynamicTime := time.Since(start)

		// Resolved mode (cached indexes).
		start = time.Now()
		for range iterations {
			_, _ = engine.EvaluateContextResolved(context.Background(), resolved, ctxReq)
			_, _ = engine.EvaluateIdentityResolved(context.Background(), resolved, idReq)
		}
		resolvedTime := time.Since(start)

		speedup := float64(dynamicTime) / float64(resolvedTime)
		t.Logf("  %3d packages: dynamic=%v  resolved=%v  speedup=%.1fx",
			numPkgs, dynamicTime/iterations, resolvedTime/iterations, speedup)
	}
	t.Log("")
}

// TestScale_PackagesPerRequest measures how eval time scales with packages in a single request.
func TestScale_PackagesPerRequest(t *testing.T) {
	t.Log("")
	t.Log("=== Packages Per Request: linear in request size (expected) ===")
	t.Log("")

	for _, numPkgs := range []int{1, 5, 10, 25, 50} {
		store := NewMockStore()

		var pkgs []PackageConfig
		var available []tmproto.AvailablePackage
		var pkgIDs []string
		for i := range numPkgs {
			pkgID := fmt.Sprintf("pkg-%d", i)
			pkgs = append(pkgs, PackageConfig{PackageID: pkgID, TopicTargets: true})
			available = append(available, tmproto.AvailablePackage{PackageID: pkgID})
			pkgIDs = append(pkgIDs, pkgID)
			store.SetAdd(fmt.Sprintf("topics:package:%s", pkgID), "food.cooking")
			store.SetPackageIdentityConfig(pkgID, PackageIdentityConfig{
				FrequencyRules: []FrequencyRuleJSON{{MaxCount: 10, WindowSeconds: 86400}},
			})
		}
		store.SetAdd("topics:artifact:article:food", "food.cooking")

		engine := NewEngine(EngineConfig{
			ProviderID: "bench",
			Store:      store,
			Properties: PropertyList{Global: NewMapBitmap(1)},
			Packages:   pkgs,
		})

		ctxReq := &tmproto.ContextMatchRequest{
			RequestID:     "bench",
			PropertyRID:   1,
			Artifacts:     []string{"article:food"},
			AvailablePkgs: available,
		}
		idReq := &tmproto.IdentityMatchRequest{
			RequestID:  "bench",
			UserToken:  "tok-bench",
			PackageIDs: pkgIDs,
		}

		const iterations = 10_000
		start := time.Now()
		for range iterations {
			_, _ = engine.EvaluateContext(context.Background(), ctxReq)
			_, _ = engine.EvaluateIdentity(context.Background(), idReq)
		}
		elapsed := time.Since(start)
		perPkg := elapsed / time.Duration(iterations*numPkgs)
		t.Logf("  %3d packages: %v/eval (%v/package)", numPkgs, elapsed/iterations, perPkg)
	}
	t.Log("")
}
