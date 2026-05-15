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
			bm[fmt.Sprintf("%d", i)] = struct{}{}
		}

		store := NewMockStore()
		engine := NewContextEngine(ContextEngineConfig{
			ProviderID: "bench",
			Store:      store,
			Properties: PropertyList{Global: bm},
			Packages:   []PackageConfig{{PackageID: "pkg-1"}},
		})

		req := &tmproto.ContextMatchRequest{
			RequestID:   "bench",
			PropertyRID: fmt.Sprintf("%d", n/2),
			PackageIDs:  []string{"pkg-1"},
		}

		const iterations = 1_000
		start := time.Now()
		for range iterations {
			_, _ = engine.EvaluateContext(context.Background(), req)
		}
		elapsed := time.Since(start)
		t.Logf("  %8d properties: %v/eval", n, elapsed/iterations)
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

		for i := range n {
			store.SetAdd("topics:package:pkg-1", fmt.Sprintf("topic-%d", i))
		}
		store.SetAdd("topics:artifact:article:test", fmt.Sprintf("topic-%d", n-1))

		engine := NewContextEngine(ContextEngineConfig{
			ProviderID: "bench",
			Store:      store,
			Properties: PropertyList{Global: NewMapBitmap("1")},
			Packages:   []PackageConfig{{PackageID: "pkg-1", TopicTargets: true}},
		})

		req := &tmproto.ContextMatchRequest{
			RequestID:    "bench",
			PropertyRID:  "1",
			ArtifactRefs: []tmproto.ArtifactRef{{Type: tmproto.ArtifactRefTypeURL, Value: "article:test"}},
			PackageIDs:   []string{"pkg-1"},
		}

		const iterations = 500
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

		engine := NewContextEngine(ContextEngineConfig{
			ProviderID: "bench",
			Store:      store,
			Properties: PropertyList{Global: NewMapBitmap("1")},
			Packages:   []PackageConfig{{PackageID: "pkg-1", URLBlocklist: true}},
		})

		req := &tmproto.ContextMatchRequest{
			RequestID:    "bench",
			PropertyRID:  "1",
			ArtifactRefs: []tmproto.ArtifactRef{{Type: tmproto.ArtifactRefTypeURL, Value: "article:safe-content"}},
			PackageIDs:   []string{"pkg-1"},
		}

		const iterations = 1_000
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

		var staticPkgs []PackageConfig
		var pkgIDs []string
		for i := range numPkgs {
			pkgID := fmt.Sprintf("pkg-%d", i)
			staticPkgs = append(staticPkgs, PackageConfig{PackageID: pkgID, TopicTargets: true})
			pkgIDs = append(pkgIDs, pkgID)
			store.SetAdd(fmt.Sprintf("topics:package:%s", pkgID), "food.cooking")
			store.SetPackageContextConfig(pkgID, PackageContextConfig{
				PackageID:    pkgID,
				TopicTargets: true,
			})
		}
		store.SetAdd("topics:artifact:article:food", "food.cooking")

		staticEngine := NewContextEngine(ContextEngineConfig{
			ProviderID: "bench",
			Store:      store,
			Properties: PropertyList{Global: NewMapBitmap("1")},
			Packages:   staticPkgs,
		})

		dynamicEngine := NewContextEngine(ContextEngineConfig{
			ProviderID:      "bench",
			Store:           store,
			Properties:      PropertyList{Global: NewMapBitmap("1")},
			DynamicPackages: true,
		})

		ctxReq := &tmproto.ContextMatchRequest{
			RequestID:    "bench",
			PropertyRID:  "1",
			ArtifactRefs: []tmproto.ArtifactRef{{Type: tmproto.ArtifactRefTypeURL, Value: "article:food"}},
			PackageIDs:   pkgIDs,
		}

		const iterations = 1_000

		start := time.Now()
		for range iterations {
			_, _ = staticEngine.EvaluateContext(context.Background(), ctxReq)
		}
		staticTime := time.Since(start)

		start = time.Now()
		for range iterations {
			_, _ = dynamicEngine.EvaluateContext(context.Background(), ctxReq)
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

		const iterations = 1_000
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

		var mbPkgs []MediaBuyPackage
		var pkgIDs []string
		for i := range numPkgs {
			pkgID := fmt.Sprintf("pkg-%d", i)
			mbPkgs = append(mbPkgs, MediaBuyPackage{PackageID: pkgID, MediaBuyID: "mb-1"})
			pkgIDs = append(pkgIDs, pkgID)

			store.SetAdd("topics:package:"+pkgID, "food.cooking")
			store.SetPackageContextConfig(pkgID, PackageContextConfig{
				PackageID:    pkgID,
				TopicTargets: true,
				PropertyRIDs: []string{"1"},
			})
		}
		store.SetMediaBuy(MediaBuy{
			MediaBuyID: "mb-1", SellerID: "seller-1",
			StartDate: "2026-01-01", EndDate: "2026-12-31",
			Countries: []string{"US"}, PropertyIDs: []string{"pub-1"},
			Packages: mbPkgs,
		})
		store.SetAdd("topics:artifact:article:food", "food.cooking")

		resolved, err := Resolve(context.Background(), store, "seller-1", "pub-1", "US", now)
		if err != nil {
			t.Fatal(err)
		}

		engine := NewContextEngine(ContextEngineConfig{
			ProviderID:      "bench",
			Store:           store,
			Properties:      PropertyList{Global: NewMapBitmap("1")},
			DynamicPackages: true,
		})

		ctxReq := &tmproto.ContextMatchRequest{
			RequestID:    "bench",
			PropertyRID:  "1",
			ArtifactRefs: []tmproto.ArtifactRef{{Type: tmproto.ArtifactRefTypeURL, Value: "article:food"}},
			PackageIDs:   pkgIDs,
		}

		const iterations = 500

		start := time.Now()
		for range iterations {
			_, _ = engine.EvaluateContext(context.Background(), ctxReq)
		}
		dynamicTime := time.Since(start)

		start = time.Now()
		for range iterations {
			_, _ = engine.EvaluateContextResolved(context.Background(), resolved, ctxReq)
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
		var pkgIDs []string
		idConfigs := make(map[string]*PackageIdentityConfig, numPkgs)
		for i := range numPkgs {
			pkgID := fmt.Sprintf("pkg-%d", i)
			pkgs = append(pkgs, PackageConfig{PackageID: pkgID, TopicTargets: true})
			pkgIDs = append(pkgIDs, pkgID)
			store.SetAdd(fmt.Sprintf("topics:package:%s", pkgID), "food.cooking")
			idCfg := PackageIdentityConfig{}
			idConfigs[pkgID] = &idCfg
		}
		store.SetAdd("topics:artifact:article:food", "food.cooking")

		ctxEngine := NewContextEngine(ContextEngineConfig{
			ProviderID: "bench",
			Store:      store,
			Properties: PropertyList{Global: NewMapBitmap("1")},
			Packages:   pkgs,
		})
		idEngine := NewIdentityEngine(IdentityEngineConfig{})

		resolved := &ResolvedPackages{IdentityConfigs: idConfigs}

		ctxReq := &tmproto.ContextMatchRequest{
			RequestID:    "bench",
			PropertyRID:  "1",
			ArtifactRefs: []tmproto.ArtifactRef{{Type: tmproto.ArtifactRefTypeURL, Value: "article:food"}},
			PackageIDs:   pkgIDs,
		}
		idReq := &tmproto.IdentityMatchRequest{
			RequestID:  "bench",
			Identities: []tmproto.IdentityToken{{UserToken: "tok-bench"}},
			PackageIDs: pkgIDs,
		}

		const iterations = 1_000
		start := time.Now()
		for range iterations {
			_, _ = ctxEngine.EvaluateContext(context.Background(), ctxReq)
			_, _ = idEngine.EvaluateIdentityResolved(context.Background(), resolved, idReq)
		}
		elapsed := time.Since(start)
		perPkg := elapsed / time.Duration(iterations*numPkgs)
		t.Logf("  %3d packages: %v/eval (%v/package)", numPkgs, elapsed/iterations, perPkg)
	}
	t.Log("")
}
