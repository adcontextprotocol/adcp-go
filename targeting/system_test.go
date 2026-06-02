package targeting

import (
	"context"
	"fmt"
	"runtime"
	"testing"
	"time"

	"github.com/adcontextprotocol/adcp-go/targeting/audience"
	"github.com/adcontextprotocol/adcp-go/targeting/topicstore"
	"github.com/adcontextprotocol/adcp-go/tmproto"
)

var systemTaxonomy = topicstore.Taxonomy{Source: "system", ID: 1}

// TestSystem_EndToEnd runs a realistic scenario at scale and reports
// a complete breakdown of latency, memory, Store calls, and throughput
// across all three evaluation modes.
func TestSystem_EndToEnd(t *testing.T) {
	const (
		numMediaBuys    = 50
		packagesPerBuy  = 10
		totalPackages   = numMediaBuys * packagesPerBuy // 500
		numSegments     = 20
		membersPerSeg   = 10_000
		topicsPerPkg    = 5
		blocklistPerPkg = 100
		numUsers        = 100
		pagesPerUser    = 5
	)

	store := NewMockStore()
	now := time.Date(2026, 6, 15, 12, 0, 0, 0, time.UTC)
	store.Now = func() time.Time { return now }

	t.Logf("")
	t.Logf("=== System Test: %d packages, %d segments × %d members ===", totalPackages, numSegments, membersPerSeg)
	t.Logf("")

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

	segments := make([]string, numSegments)
	for i := range numSegments {
		segments[i] = fmt.Sprintf("seg-%d", i)
	}

	ctx := context.Background()
	for i, pkgID := range allPkgIDs {
		store.SetPackageContextConfig(pkgID, PackageContextConfig{
			PackageID:    pkgID,
			TopicTargets: true,
			URLBlocklist: true,
			PropertyRIDs: []string{"1"},
			EmitSegments: []string{fmt.Sprintf("emit-%d", i%5)},
			Price:        tmproto.OfferPrice{Amount: float64(5 + i%20), Currency: "USD", Model: string(tmproto.PriceModelCPM)},
		})

		for tp := range topicsPerPkg {
			_ = store.SetAdd(ctx, topicstore.PackageKey(systemTaxonomy, pkgID), fmt.Sprintf("topic-%d", (i*3+tp)%50))
		}

		for bl := range blocklistPerPkg {
			_ = store.SetAdd(ctx, "url:blocklist:"+pkgID, HashURL(fmt.Sprintf("blocked-%d-%d", i, bl)))
		}

	}

	userTokens := make([]string, numUsers)
	audSvc := audience.New(audience.NewMockStore())
	upsertsBySegment := make(map[string][]audience.Member)
	for u := range numUsers {
		userTokens[u] = fmt.Sprintf("tok-user-%d", u)
		for _, seg := range []string{segments[u%numSegments], segments[(u+3)%numSegments]} {
			upsertsBySegment[seg] = append(upsertsBySegment[seg], audience.Member{UserToken: userTokens[u], Score: 1.0})
		}
	}
	upserts := make([]audience.AudienceUpsert, 0, len(upsertsBySegment))
	for seg, members := range upsertsBySegment {
		upserts = append(upserts, audience.AudienceUpsert{AudienceID: seg, Add: members})
	}
	if err := audSvc.UpsertBatch(context.Background(), upserts); err != nil {
		t.Fatal(err)
	}

	_ = store.SetAdd(ctx, topicstore.ArtifactKey(systemTaxonomy, "article:food"), "topic-0", "topic-1", "topic-2")

	var m1, m2 runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&m1)

	resolveStart := time.Now()
	resolved, err := Resolve(context.Background(), store, "seller-1", "pub-1", "US", []topicstore.Taxonomy{systemTaxonomy}, now)
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
	t.Logf("  ContextConfigs: %d", len(resolved.ContextConfigs))
	t.Logf("")

	staticPkgs := make([]PackageConfig, len(allPkgIDs))
	for i, id := range allPkgIDs {
		staticPkgs[i] = PackageConfig{PackageID: id, TopicTargets: true, URLBlocklist: true}
	}

	staticEngine := NewContextEngine(ContextEngineConfig{
		ProviderID:         "bench",
		Store:              store,
		Properties:         PropertyList{Global: NewMapBitmap("1")},
		Packages:           staticPkgs,
		AcceptedTaxonomies: []topicstore.Taxonomy{systemTaxonomy},
	})

	dynamicEngine := NewContextEngine(ContextEngineConfig{
		ProviderID:         "bench",
		Store:              store,
		Properties:         PropertyList{Global: NewMapBitmap("1")},
		DynamicPackages:    true,
		AcceptedTaxonomies: []topicstore.Taxonomy{systemTaxonomy},
	})

	resolvedCtxEngine := NewContextEngine(ContextEngineConfig{
		ProviderID:         "bench",
		Store:              store,
		Properties:         PropertyList{Global: NewMapBitmap("1")},
		AcceptedTaxonomies: []topicstore.Taxonomy{systemTaxonomy},
	})
	identityEngine := NewIdentityEngine(IdentityEngineConfig{
		Audience: audSvc,
	})

	ctxReq := &tmproto.ContextMatchRequest{
		RequestID:    "bench",
		PropertyRID:  "1",
		ArtifactRefs: []tmproto.ArtifactRef{{Type: tmproto.ArtifactRefTypeURL, Value: "article:food"}},
		PackageIDs:   allPkgIDs,
	}

	type result struct {
		name             string
		totalTime        time.Duration
		contextTime      time.Duration
		identityTime     time.Duration
		contextOffers    int
		identityEligible int
		requests         int
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

	idEval := func(ctx context.Context, req *tmproto.IdentityMatchRequest) (*IdentityResult, error) {
		return identityEngine.EvaluateIdentityResolved(ctx, resolved, req)
	}

	staticResult := runBench("Static", staticEngine.EvaluateContext, idEval)
	dynamicResult := runBench("Dynamic", dynamicEngine.EvaluateContext, idEval)
	resolvedResult := runBench("Resolved",
		func(ctx context.Context, req *tmproto.ContextMatchRequest) (*ContextResult, error) {
			return resolvedCtxEngine.EvaluateContextResolved(ctx, resolved, req)
		},
		idEval,
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

	if staticResult.contextOffers != dynamicResult.contextOffers {
		t.Errorf("static (%d) and dynamic (%d) context offers differ",
			staticResult.contextOffers, dynamicResult.contextOffers)
	}
}
