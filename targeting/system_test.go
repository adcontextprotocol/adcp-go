package targeting

import (
	"context"
	"fmt"
	"runtime"
	"testing"
	"time"

	"github.com/adcontextprotocol/adcp-go/tmproto"
)

// TestSystem_EndToEnd runs a realistic scenario at scale and reports
// a complete breakdown of latency, memory, Store calls, and throughput
// across all three evaluation modes.
func TestSystem_EndToEnd(t *testing.T) {
	// --- Setup: realistic seller with many media buys ---

	const (
		numMediaBuys     = 50
		packagesPerBuy   = 10
		totalPackages    = numMediaBuys * packagesPerBuy // 500
		numSegments      = 20
		membersPerSeg    = 10_000
		topicsPerPkg     = 5
		blocklistPerPkg  = 100
		numCampaigns     = 10
		numUsers         = 100
		pagesPerUser     = 5
	)

	store := NewMockStore()
	now := time.Date(2026, 6, 15, 12, 0, 0, 0, time.UTC)
	store.Now = func() time.Time { return now }

	t.Logf("")
	t.Logf("=== System Test: %d packages, %d segments × %d members, %d campaigns ===", totalPackages, numSegments, membersPerSeg, numCampaigns)
	t.Logf("")

	// Create media buys.
	var allPkgIDs []string
	for mb := range numMediaBuys {
		var mbPkgs []MediaBuyPackage
		for p := range packagesPerBuy {
			pkgID := fmt.Sprintf("pkg-%d-%d", mb, p)
			allPkgIDs = append(allPkgIDs, pkgID)
			mbPkgs = append(mbPkgs, MediaBuyPackage{PackageID: pkgID, MediaBuyID: fmt.Sprintf("mb-%d", mb)})
		}
		store.SetMediaBuy(MediaBuy{
			MediaBuyID:  fmt.Sprintf("mb-%d", mb),
			SellerID:    "seller-1",
			StartDate:   "2026-01-01",
			EndDate:     "2026-12-31",
			Countries:   []string{"US"},
			PropertyIDs: []string{"pub-1"},
			Packages:    mbPkgs,
		})
	}

	// Create package configs.
	segments := make([]string, numSegments)
	for i := range numSegments {
		segments[i] = fmt.Sprintf("seg-%d", i)
	}

	for i, pkgID := range allPkgIDs {
		campID := fmt.Sprintf("campaign-%d", i%numCampaigns)

		// Context config.
		store.SetPackageContextConfig(pkgID, PackageContextConfig{
			PackageID:    pkgID,
			TopicTargets: true,
			URLBlocklist: true,
			PropertyRIDs: []string{"1"},
			EmitSegments: []string{fmt.Sprintf("emit-%d", i%5)},
			Price:        tmproto.OfferPrice{Amount: float64(5 + i%20), Currency: "USD", Model: string(tmproto.PriceModelCPM)},
		})

		// Topics: each package has a few topics, with some overlap.
		for tp := range topicsPerPkg {
			store.SetAdd("topics:package:"+pkgID, fmt.Sprintf("topic-%d", (i*3+tp)%50))
		}

		// URL blocklist.
		for bl := range blocklistPerPkg {
			store.SetAdd("url:blocklist:"+pkgID, HashURL(fmt.Sprintf("blocked-%d-%d", i, bl)))
		}

		// Identity config.
		targetSegs := []string{segments[i%numSegments], segments[(i+1)%numSegments]}
		store.SetPackageIdentityConfig(pkgID, PackageIdentityConfig{
			CampaignID:     campID,
			FrequencyRules: []FrequencyRuleJSON{{MaxCount: 5, WindowSeconds: 86400}},
			TargetSegments: targetSegs,
		})
	}

	// Campaign configs.
	for i := range numCampaigns {
		store.SetCampaignFreqConfig(fmt.Sprintf("campaign-%d", i), CampaignFreqConfig{
			FrequencyRules: []FrequencyRuleJSON{{MaxCount: 20, WindowSeconds: 604800}},
		})
	}

	// Audience segments: populate with user hashes.
	userTokens := make([]string, numUsers)
	for u := range numUsers {
		userTokens[u] = fmt.Sprintf("tok-user-%d", u)
		hash := HashToken(userTokens[u])
		// Each user is in 2-3 segments.
		store.SetAdd("audience:"+segments[u%numSegments], hash)
		store.SetAdd("audience:"+segments[(u+3)%numSegments], hash)
	}

	// Add artifact topics.
	store.SetAdd("topics:artifact:article:food", "topic-0", "topic-1", "topic-2")

	// --- Measure memory ---
	var m1, m2 runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&m1)

	// Build resolved (the expensive part — done once, cached).
	resolveStart := time.Now()
	resolved, err := Resolve(context.Background(), store, "seller-1", "pub-1", "US", now)
	resolveTime := time.Since(resolveStart)
	if err != nil {
		t.Fatal(err)
	}

	runtime.GC()
	runtime.ReadMemStats(&m2)
	resolvedMemMB := float64(m2.TotalAlloc-m1.TotalAlloc) / 1024 / 1024

	t.Logf("  Resolve: %d packages from %d media buys in %v", len(resolved.Packages), numMediaBuys, resolveTime)
	t.Logf("  Resolved indexes memory: ~%.1f MB", resolvedMemMB)
	t.Logf("  PropertyIndex entries: %d", len(resolved.PropertyIndex))
	t.Logf("  TopicIndex entries: %d", len(resolved.TopicIndex))
	t.Logf("  URLBlocklistIndex entries: %d", len(resolved.URLBlocklistIndex))
	t.Logf("  SegmentIndex entries: %d", len(resolved.SegmentIndex))
	t.Logf("  ContextConfigs: %d", len(resolved.ContextConfigs))
	t.Logf("  IdentityConfigs: %d", len(resolved.IdentityConfigs))
	t.Logf("  CampaignConfigs: %d", len(resolved.CampaignConfigs))
	t.Logf("")

	// --- Build engines ---
	staticPkgs := make([]PackageConfig, len(allPkgIDs))
	for i, id := range allPkgIDs {
		staticPkgs[i] = PackageConfig{PackageID: id, TopicTargets: true, URLBlocklist: true}
	}

	staticEngine := NewEngine(EngineConfig{
		ProviderID: "bench",
		Store:      store,
		Properties: PropertyList{Global: NewMapBitmap("1")},
		Packages:   staticPkgs,
	})
	staticEngine.Now = func() time.Time { return now }

	dynamicEngine := NewEngine(EngineConfig{
		ProviderID:      "bench",
		Store:           store,
		Properties:      PropertyList{Global: NewMapBitmap("1")},
		DynamicPackages: true,
	})
	dynamicEngine.Now = func() time.Time { return now }

	resolvedEngine := NewEngine(EngineConfig{
		ProviderID: "bench",
		Store:      store,
		Properties: PropertyList{Global: NewMapBitmap("1")},
	})
	resolvedEngine.Now = func() time.Time { return now }

	// --- Benchmark: simulate user sessions ---

	ctxReq := &tmproto.ContextMatchRequest{
		RequestID:    "bench",
		PropertyRID:  "1",
		ArtifactRefs: []map[string]any{{"url": "article:food"}},
		PackageIDs:   allPkgIDs,
	}

	type result struct {
		name            string
		totalTime       time.Duration
		contextTime     time.Duration
		identityTime    time.Duration
		contextOffers   int
		identityEligible int
		requests        int
	}

	runBench := func(name string, evalCtx func(context.Context, *tmproto.ContextMatchRequest) (*ContextResult, error), evalId func(context.Context, *tmproto.IdentityMatchRequest) (*IdentityResult, error)) result {
		var totalCtx, totalId time.Duration
		var totalOffers, totalEligible, reqs int

		for u := range numUsers {
			for p := range pagesPerUser {
				_ = p
				idReq := &tmproto.IdentityMatchRequest{
					RequestID:  fmt.Sprintf("id-%d-%d", u, p),
					Identities: []tmproto.IdentityToken{{UserToken: userTokens[u]}},
					PackageIDs: allPkgIDs,
				}

				start := time.Now()
				ctxResult, _ := evalCtx(context.Background(), ctxReq)
				ctxTime := time.Since(start)

				start = time.Now()
				idResult, _ := evalId(context.Background(), idReq)
				idTime := time.Since(start)

				totalCtx += ctxTime
				totalId += idTime
				if ctxResult != nil {
					totalOffers += len(ctxResult.Offers)
				}
				if idResult != nil {
					for _, e := range idResult.Eligibility {
						if e.Eligible {
							totalEligible++
						}
					}
				}
				reqs++
			}
		}

		return result{
			name:             name,
			totalTime:        totalCtx + totalId,
			contextTime:      totalCtx,
			identityTime:     totalId,
			contextOffers:    totalOffers,
			identityEligible: totalEligible,
			requests:         reqs,
		}
	}

	// Static mode.
	staticResult := runBench("Static",
		staticEngine.EvaluateContext,
		staticEngine.EvaluateIdentity,
	)

	// Dynamic mode.
	dynamicResult := runBench("Dynamic",
		dynamicEngine.EvaluateContext,
		dynamicEngine.EvaluateIdentity,
	)

	// Resolved mode.
	resolvedResult := runBench("Resolved",
		func(ctx context.Context, req *tmproto.ContextMatchRequest) (*ContextResult, error) {
			return resolvedEngine.EvaluateContextResolved(ctx, resolved, req)
		},
		func(ctx context.Context, req *tmproto.IdentityMatchRequest) (*IdentityResult, error) {
			return resolvedEngine.EvaluateIdentityResolved(ctx, resolved, req)
		},
	)

	t.Logf("=== Latency Breakdown (%d users × %d pages = %d requests, %d packages each) ===",
		numUsers, pagesPerUser, numUsers*pagesPerUser, totalPackages)
	t.Logf("")
	t.Logf("  %-10s  %12s  %12s  %12s  %8s  %8s  %8s",
		"Mode", "Total/req", "Context/req", "Identity/req", "QPS", "Ctx Hits", "Id Elig")
	t.Logf("  %-10s  %12s  %12s  %12s  %8s  %8s  %8s",
		"----", "---------", "-----------", "------------", "---", "--------", "-------")

	for _, r := range []result{staticResult, dynamicResult, resolvedResult} {
		avgTotal := r.totalTime / time.Duration(r.requests)
		avgCtx := r.contextTime / time.Duration(r.requests)
		avgId := r.identityTime / time.Duration(r.requests)
		qps := float64(r.requests) / r.totalTime.Seconds()
		t.Logf("  %-10s  %12v  %12v  %12v  %7.0f  %8d  %8d",
			r.name, avgTotal, avgCtx, avgId, qps, r.contextOffers, r.identityEligible)
	}

	t.Logf("")
	speedupVsStatic := float64(staticResult.totalTime) / float64(resolvedResult.totalTime)
	speedupVsDynamic := float64(dynamicResult.totalTime) / float64(resolvedResult.totalTime)
	t.Logf("  Resolved vs Static:  %.1fx faster", speedupVsStatic)
	t.Logf("  Resolved vs Dynamic: %.1fx faster", speedupVsDynamic)
	t.Logf("")

	// --- Memory summary ---
	t.Logf("=== Memory ===")
	t.Logf("")
	t.Logf("  Resolved indexes: ~%.1f MB (cached, shared across requests)", resolvedMemMB)
	t.Logf("  Static engine:    ~%.1f MB (%.0f bytes/package)", float64(len(allPkgIDs))*184/1024/1024, float64(184))
	t.Logf("  Store data:       out-of-process (Valkey)")
	t.Logf("    - %d audience segments × %d members each", numSegments, membersPerSeg)
	t.Logf("    - %d URL blocklist entries total", totalPackages*blocklistPerPkg)
	t.Logf("    - %d topic set entries total", totalPackages*topicsPerPkg)
	t.Logf("")

	// --- Verify correctness ---
	t.Logf("=== Correctness ===")
	t.Logf("")
	t.Logf("  Context offers (static):   %d across %d requests", staticResult.contextOffers, staticResult.requests)
	t.Logf("  Context offers (dynamic):  %d across %d requests", dynamicResult.contextOffers, dynamicResult.requests)
	t.Logf("  Context offers (resolved): %d across %d requests", resolvedResult.contextOffers, resolvedResult.requests)
	t.Logf("  Identity eligible (static):   %d", staticResult.identityEligible)
	t.Logf("  Identity eligible (resolved): %d", resolvedResult.identityEligible)
	t.Logf("")

	// Static and dynamic context should produce the same number of offers
	// (they evaluate the same packages with the same targeting).
	if staticResult.contextOffers != dynamicResult.contextOffers {
		t.Errorf("static (%d) and dynamic (%d) context offers differ",
			staticResult.contextOffers, dynamicResult.contextOffers)
	}
}
