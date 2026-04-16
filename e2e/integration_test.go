package e2e

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/adcontextprotocol/adcp-go/router"
	"github.com/adcontextprotocol/adcp-go/targeting"
	"github.com/adcontextprotocol/adcp-go/tmpclient"
	"github.com/adcontextprotocol/adcp-go/tmproto"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// testStack holds all components for an integration test.
type testStack struct {
	client         *tmpclient.Client
	store          *targeting.MockStore
	contextEngine  *targeting.Engine
	identityEngine *targeting.Engine
}

func setupStack(t *testing.T) *testStack {
	t.Helper()

	store := targeting.NewMockStore()

	// Seed topic data.
	store.SetAdd("topics:package:pkg-food", "food.cooking", "food.recipes")
	store.SetAdd("topics:artifact:article:pasta", "food.cooking", "food.italian")
	store.SetAdd("topics:package:pkg-tech", "technology.gadgets", "technology.reviews")
	store.SetAdd("topics:artifact:article:cpu-review", "technology.reviews", "technology.hardware")

	// Seed URL blocklist.
	store.SetAdd("url:blocklist:pkg-family", targeting.HashURL("article:adult-content"))

	// Seed audience segments.
	aliceHash := targeting.HashToken("tok-alice")
	bobHash := targeting.HashToken("tok-bob")
	store.SetAdd("audience:cooking_fans", aliceHash)
	store.SetAdd("audience:sports_fans", bobHash)

	// Context engine.
	contextEngine := targeting.NewEngine(targeting.EngineConfig{
		ProviderID: "integration-context",
		Store:      store,
		Properties: targeting.PropertyList{
			Global: targeting.NewMapBitmap(1, 2, 3, 4, 5),
		},
		Packages: []targeting.PackageConfig{
			{PackageID: "pkg-food", TopicTargets: true, EmitSegments: []string{"food", "cooking"}},
			{PackageID: "pkg-tech", TopicTargets: true, EmitSegments: []string{"technology"}},
			{PackageID: "pkg-family", URLBlocklist: true},
		},
	})

	// Seed identity config in Store (data-driven, not static config).
	store.SetPackageIdentityConfig("pkg-food", targeting.PackageIdentityConfig{
		CampaignID:     "campaign-acme",
		FrequencyRules: []targeting.FrequencyRuleJSON{{MaxCount: 3, WindowSeconds: 86400}},
		TargetSegments: []string{"cooking_fans"},
	})
	// pkg-tech: no identity config = always eligible
	store.SetPackageIdentityConfig("pkg-family", targeting.PackageIdentityConfig{
		FrequencyRules: []targeting.FrequencyRuleJSON{{MaxCount: 5, WindowSeconds: 604800}},
	})
	store.SetCampaignFreqConfig("campaign-acme", targeting.CampaignFreqConfig{
		FrequencyRules: []targeting.FrequencyRuleJSON{{MaxCount: 5, WindowSeconds: 604800}},
	})

	// User profiles for identity evaluation.
	store.SetUserProfile("tok-alice", map[string]float64{"cooking_fans": 0.8})
	store.SetUserProfile("tok-bob", map[string]float64{"sports_fans": 0.5})

	// Identity engine (shares the same store).
	identityEngine := targeting.NewEngine(targeting.EngineConfig{
		ProviderID: "integration-identity",
		Store:      store,
		Packages: []targeting.PackageConfig{
			{PackageID: "pkg-food"},
			{PackageID: "pkg-tech"},
			{PackageID: "pkg-family"},
		},
	})

	// Build resolved packages for the identity eval path.
	idResolved := &targeting.ResolvedPackages{
		SegmentIndex: map[string][]string{
			"cooking_fans": {"pkg-food"},
		},
		IdentityConfigs: map[string]*targeting.PackageIdentityConfig{
			"pkg-food":   {CampaignID: "campaign-acme", FrequencyRules: []targeting.FrequencyRuleJSON{{MaxCount: 3, WindowSeconds: 86400}}, TargetSegments: []string{"cooking_fans"}},
			"pkg-tech":   {},
			"pkg-family": {FrequencyRules: []targeting.FrequencyRuleJSON{{MaxCount: 5, WindowSeconds: 604800}}},
		},
		CampaignConfigs: map[string]*targeting.CampaignFreqConfig{
			"campaign-acme": {FrequencyRules: []targeting.FrequencyRuleJSON{{MaxCount: 5, WindowSeconds: 604800}}},
		},
	}

	// Start context agent server.
	ctxSrv := httptest.NewServer(agentHandler(contextEngine, nil))
	t.Cleanup(ctxSrv.Close)

	// Start identity agent server (with resolved path for binary exposure logs).
	idSrv := httptest.NewServer(agentHandler(nil, identityEngine, idResolved))
	t.Cleanup(idSrv.Close)

	// Start real router with property registry populated.
	reg := router.NewRegistry("", "")
	reg.LoadFromData([]router.RegistryProperty{
		{PropertyID: "pub-oakwood", PropertyRID: 1, PropertyType: "website", Domain: "oakwood.example.com"},
		{PropertyID: "pub-techblog", PropertyRID: 2, PropertyType: "website", Domain: "techblog.example.com"},
	}, 1)
	health := router.NewProviderHealth(3, 10*time.Second)
	r, err := router.NewRouter([]router.ProviderConfig{
		{ID: "ctx-agent", Endpoint: ctxSrv.URL, ContextMatch: true, Timeout: 5 * time.Second},
		{ID: "id-agent", Endpoint: idSrv.URL, IdentityMatch: true, Timeout: 5 * time.Second},
	}, reg, nil, health, router.WithoutEndpointValidation())
	require.NoError(t, err, "failed to create router")

	mux := http.NewServeMux()
	mux.HandleFunc("POST /tmp/context", r.HandleContextMatch)
	mux.HandleFunc("POST /tmp/identity", r.HandleIdentityMatch)
	routerSrv := httptest.NewServer(mux)
	t.Cleanup(routerSrv.Close)

	// Create tmpclient pointing at router.
	client := tmpclient.NewClient(routerSrv.URL, tmpclient.WithHTTPClient(&http.Client{Timeout: 10 * time.Second}))

	return &testStack{
		client:         client,
		store:          store,
		contextEngine:  contextEngine,
		identityEngine: identityEngine,
	}
}

// agentHandler creates an HTTP handler that serves both context and identity endpoints.
// If resolved is non-nil, uses the resolved path for identity evaluation.
func agentHandler(ctxEngine, idEngine *targeting.Engine, resolved ...*targeting.ResolvedPackages) http.Handler {
	mux := http.NewServeMux()

	if ctxEngine != nil {
		mux.HandleFunc("POST /tmp/context", func(w http.ResponseWriter, r *http.Request) {
			body, err := io.ReadAll(io.LimitReader(r.Body, 64*1024))
			if err != nil {
				writeAgentError(w, "", "failed to read body")
				return
			}
			var req tmproto.ContextMatchRequest
			if err := json.Unmarshal(body, &req); err != nil {
				writeAgentError(w, "", "invalid JSON")
				return
			}
			result, err := ctxEngine.EvaluateContext(r.Context(), &req)
			if err != nil {
				writeAgentError(w, req.RequestID, err.Error())
				return
			}
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(tmproto.ContextMatchResponse{
				RequestID: result.RequestID,
				Offers:    result.Offers,
				Signals:   result.Signals,
			})
		})
	}

	if idEngine != nil {
		mux.HandleFunc("POST /tmp/identity", func(w http.ResponseWriter, r *http.Request) {
			body, err := io.ReadAll(io.LimitReader(r.Body, 64*1024))
			if err != nil {
				writeAgentError(w, "", "failed to read body")
				return
			}
			var req tmproto.IdentityMatchRequest
			if err := json.Unmarshal(body, &req); err != nil {
				writeAgentError(w, "", "invalid JSON")
				return
			}
			var result *targeting.IdentityResult
			var evalErr error
			if len(resolved) > 0 && resolved[0] != nil {
				result, evalErr = idEngine.EvaluateIdentityResolved(r.Context(), resolved[0], &req)
			} else {
				result, evalErr = idEngine.EvaluateIdentity(r.Context(), &req)
			}
			if evalErr != nil {
				writeAgentError(w, req.RequestID, evalErr.Error())
				return
			}
			// Convert internal IdentityResult to wire format.
			var eligible []string
			for _, e := range result.Eligibility {
				if e.Eligible {
					eligible = append(eligible, e.PackageID)
				}
			}
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(tmproto.IdentityMatchResponse{
				RequestID:          result.RequestID,
				EligiblePackageIDs: eligible,
				TTLSec:             60,
			})
		})

	}

	return mux
}

func writeAgentError(w http.ResponseWriter, reqID, msg string) {
	w.WriteHeader(http.StatusInternalServerError)
	json.NewEncoder(w).Encode(tmproto.ErrorResponse{
		RequestID: reqID,
		Code:      tmproto.ErrorCodeInternalError,
		Message:   msg,
	})
}

// --- Test Cases ---

func TestIntegration_ActivateHappyPath(t *testing.T) {
	s := setupStack(t)

	result, err := s.client.Activate(context.Background(), &tmpclient.ActivateParams{
		PropertyID:   "pub-oakwood",
		PropertyType: tmproto.PropertyTypeWebsite,
		PlacementID:  "sidebar",
		Artifacts:    []string{"article:pasta"},
		Packages: []tmproto.AvailablePackage{
			{PackageID: "pkg-food", MediaBuyID: "mb-food"},
			{PackageID: "pkg-tech", MediaBuyID: "mb-tech"},
			{PackageID: "pkg-family", MediaBuyID: "mb-family"},
		},
		UserToken:  "tok-alice",
		UIDType:    tmproto.UIDTypeUID2,
		PackageIDs: []string{"pkg-food", "pkg-tech", "pkg-family"},
	})
	require.NoError(t, err, "Activate failed")

	// pkg-food should activate: topic match (pasta → food.cooking) + alice is in cooking_fans.
	// pkg-tech should NOT: topic mismatch (pasta article, tech topics).
	// pkg-family should activate: no blocklist hit on pasta article, no audience gate.
	activated := map[string]bool{}
	for _, a := range result.Activations {
		activated[a.PackageID] = true
		t.Logf("activated: %s (mediaBuyID=%s)", a.PackageID, a.MediaBuyID)
	}

	assert.True(t, activated["pkg-food"], "pkg-food should be activated (topic match + audience match)")
	assert.False(t, activated["pkg-tech"], "pkg-tech should NOT be activated (topic mismatch)")
	assert.True(t, activated["pkg-family"], "pkg-family should be activated (no blocklist hit, no audience gate)")

	// Verify signals contain food segments.
	require.NotNil(t, result.Signals, "expected signals")
	segSet := map[string]bool{}
	for _, seg := range result.Signals.Segments {
		segSet[seg] = true
	}
	assert.True(t, segSet["food"] && segSet["cooking"], "expected food+cooking segments, got %v", result.Signals.Segments)

	// Verify raw responses are present.
	assert.NotNil(t, result.Context, "expected raw context response")
	assert.NotNil(t, result.Identity, "expected raw identity response")
}

func TestIntegration_FrequencyCapping(t *testing.T) {
	s := setupStack(t)
	ctx := context.Background()

	params := &tmpclient.ActivateParams{
		PropertyID:   "pub-oakwood",
		PropertyType: tmproto.PropertyTypeWebsite,
		PlacementID:  "sidebar",
		Artifacts:    []string{"article:pasta"},
		Packages:     []tmproto.AvailablePackage{{PackageID: "pkg-food", MediaBuyID: "mb-food"}},
		UserToken:    "tok-alice",
		UIDType:    tmproto.UIDTypeUID2,
		PackageIDs:   []string{"pkg-food"},
	}

	// 3 exposures (hits 3/24h cap on pkg-food).
	// Record exposures directly via the engine (TMPX replaces router-based expose).
	for i := range 3 {
		result, err := s.client.Activate(ctx, params)
		require.NoError(t, err, "activate %d", i)
		require.NotEmpty(t, result.Activations, "activate %d: expected activation before cap", i)
		_, err = s.identityEngine.RecordExposure(ctx, &tmproto.ExposeRequest{
			UserToken:    "tok-alice",
			PackageID:    "pkg-food",
			ImpressionID: fmt.Sprintf("imp-fcap-%d", i),
		})
		require.NoError(t, err, "expose %d", i)
	}

	// 4th activation — should be capped.
	result, err := s.client.Activate(ctx, params)
	require.NoError(t, err, "activate after cap")

	for _, a := range result.Activations {
		assert.NotEqual(t, "pkg-food", a.PackageID, "pkg-food should be capped after 3 exposures")
	}
	t.Logf("freq cap working: %d activations after 3 exposures", len(result.Activations))
}

func TestIntegration_AudienceGating(t *testing.T) {
	s := setupStack(t)

	// Bob is in sports_fans but NOT cooking_fans.
	result, err := s.client.Activate(context.Background(), &tmpclient.ActivateParams{
		PropertyID:   "pub-oakwood",
		PropertyType: tmproto.PropertyTypeWebsite,
		PlacementID:  "sidebar",
		Artifacts:    []string{"article:pasta"},
		Packages:     []tmproto.AvailablePackage{{PackageID: "pkg-food", MediaBuyID: "mb-food"}},
		UserToken:    "tok-bob",
		UIDType:    tmproto.UIDTypeUID2,
		PackageIDs:   []string{"pkg-food"},
	})
	require.NoError(t, err, "Activate failed")

	for _, a := range result.Activations {
		assert.NotEqual(t, "pkg-food", a.PackageID, "pkg-food should NOT activate for bob (not in cooking_fans)")
	}
}

func TestIntegration_URLBlocklist(t *testing.T) {
	s := setupStack(t)

	result, err := s.client.Activate(context.Background(), &tmpclient.ActivateParams{
		PropertyID:   "pub-oakwood",
		PropertyType: tmproto.PropertyTypeWebsite,
		PlacementID:  "sidebar",
		Artifacts:    []string{"article:adult-content"},
		Packages:     []tmproto.AvailablePackage{{PackageID: "pkg-family", MediaBuyID: "mb-family"}},
		UserToken:    "tok-alice",
		UIDType:    tmproto.UIDTypeUID2,
		PackageIDs:   []string{"pkg-family"},
	})
	require.NoError(t, err, "Activate failed")

	for _, a := range result.Activations {
		assert.NotEqual(t, "pkg-family", a.PackageID, "pkg-family should NOT activate (URL is blocklisted)")
	}
}

func TestIntegration_ExposeUpdatesState(t *testing.T) {
	s := setupStack(t)
	ctx := context.Background()

	params := &tmpclient.ActivateParams{
		PropertyID:   "pub-oakwood",
		PropertyType: tmproto.PropertyTypeWebsite,
		PlacementID:  "sidebar",
		Artifacts:    []string{"article:pasta"},
		Packages:     []tmproto.AvailablePackage{{PackageID: "pkg-food", MediaBuyID: "mb-food"}},
		UserToken:    "tok-alice",
		UIDType:    tmproto.UIDTypeUID2,
		PackageIDs:   []string{"pkg-food"},
	}

	// First activate — should be eligible.
	result1, err := s.client.Activate(ctx, params)
	require.NoError(t, err)
	require.NotEmpty(t, result1.Activations, "expected activation")

	// Record exposure directly via the engine (TMPX replaces router-based expose).
	expResp, err := s.identityEngine.RecordExposure(ctx, &tmproto.ExposeRequest{
		UserToken:    "tok-alice",
		PackageID:    "pkg-food",
		ImpressionID: "imp-state-1",
	})
	require.NoError(t, err)
	assert.Equal(t, 1, expResp.CampaignCount, "expected campaign count 1")

	// Second activate — should still be eligible (1 exposure, cap is 3).
	result2, err := s.client.Activate(ctx, params)
	require.NoError(t, err)
	require.NotEmpty(t, result2.Activations, "expected activation after 1 exposure (cap is 3)")
	t.Logf("still activated after 1 exposure: %s", result2.Activations[0].PackageID)
}

func TestIntegration_PropertyBitmapFilter(t *testing.T) {
	s := setupStack(t)

	// PropertyRID 999 is not in the global bitmap {1,2,3,4,5}.
	// The router won't set PropertyRID (it uses registry lookup by PropertyID),
	// so the context engine will see PropertyRID=0 which is not in the bitmap.
	result, err := s.client.Activate(context.Background(), &tmpclient.ActivateParams{
		PropertyID:   "pub-unknown",
		PropertyType: tmproto.PropertyTypeWebsite,
		PlacementID:  "sidebar",
		Artifacts:    []string{"article:pasta"},
		Packages:     []tmproto.AvailablePackage{{PackageID: "pkg-food"}},
		UserToken:    "tok-alice",
		UIDType:    tmproto.UIDTypeUID2,
		PackageIDs:   []string{"pkg-food"},
	})
	require.NoError(t, err, "Activate failed")

	// Context engine should return zero offers (PropertyRID=0 not in bitmap).
	assert.Empty(t, result.Activations, "expected 0 activations for unknown property")
}

func TestIntegration_Mediation(t *testing.T) {
	store := targeting.NewMockStore()

	// Topics for matching.
	store.SetAdd("topics:package:pkg-olive-oil", "food.cooking", "food.ingredients")
	store.SetAdd("topics:package:pkg-cookware", "food.cooking", "food.kitchen")
	store.SetAdd("topics:package:pkg-wine", "food.cooking", "food.beverage")
	store.SetAdd("topics:artifact:article:pasta", "food.cooking", "food.italian")

	// Audience: alice is a cooking fan.
	store.SetAdd("audience:cooking_fans", targeting.HashToken("tok-alice"))

	creativeManifest, _ := json.Marshal(map[string]any{
		"format_id": "sponsored_card",
		"headline":  "Meridian Extra Virgin Olive Oil",
		"body":      "Cold-pressed from hand-picked Koroneiki olives",
		"image_url": "https://cdn.example.com/creative/olive-oil.jpg",
		"cta_text":  "Shop now",
	})

	contextEngine := targeting.NewEngine(targeting.EngineConfig{
		ProviderID: "mediation-context",
		Store:      store,
		Properties: targeting.PropertyList{Global: targeting.NewMapBitmap(1)},
		Packages: []targeting.PackageConfig{
			{
				PackageID:    "pkg-olive-oil",
				TopicTargets: true,
				Brand:        &tmproto.BrandRef{Name: "Meridian Foods", AdvertiserDomain: "meridian-foods.example.com"},
				Price:        &tmproto.OfferPrice{Amount: 12.50, Currency: "USD", Model: tmproto.PriceModelCPM},
				Summary:      "Meridian olive oil sponsored content",
				CreativeManifest: creativeManifest,
				Macros:       map[string]string{"click_url": "https://meridian-foods.example.com/shop?utm_source=tmp"},
				EmitSegments: []string{"food", "premium_brand"},
			},
			{
				PackageID:    "pkg-cookware",
				TopicTargets: true,
				Brand:        &tmproto.BrandRef{Name: "ChefPro", AdvertiserDomain: "chefpro.example.com"},
				Price:        &tmproto.OfferPrice{Amount: 8.00, Currency: "USD", Model: tmproto.PriceModelCPM},
				Summary:      "ChefPro premium cookware",
				EmitSegments: []string{"food", "kitchen"},
			},
			{
				PackageID:    "pkg-wine",
				TopicTargets: true,
				Brand:        &tmproto.BrandRef{Name: "Vino Select", AdvertiserDomain: "vinoselect.example.com"},
				Price:        &tmproto.OfferPrice{Amount: 18.75, Currency: "USD", Model: tmproto.PriceModelCPM},
				Summary:      "Vino Select wine pairings",
				EmitSegments: []string{"food", "beverage"},
			},
		},
	})

	// Seed identity config for mediation packages.
	store.SetPackageIdentityConfig("pkg-olive-oil", targeting.PackageIdentityConfig{TargetSegments: []string{"cooking_fans"}})
	store.SetPackageIdentityConfig("pkg-cookware", targeting.PackageIdentityConfig{TargetSegments: []string{"cooking_fans"}})
	store.SetPackageIdentityConfig("pkg-wine", targeting.PackageIdentityConfig{TargetSegments: []string{"cooking_fans"}})

	identityEngine := targeting.NewEngine(targeting.EngineConfig{
		ProviderID: "mediation-identity",
		Store:      store,
		Packages: []targeting.PackageConfig{
			{PackageID: "pkg-olive-oil"},
			{PackageID: "pkg-cookware"},
			{PackageID: "pkg-wine"},
		},
	})

	// Seed intent scores directly in store to simulate prior exposure history.
	oliveHash := targeting.HashToken("tok-alice")
	_ = store.Set(context.Background(), "intent:pkg-olive-oil:"+oliveHash, fmt.Sprintf("%d", time.Now().Add(-6*time.Hour).Unix()), 7*24*time.Hour)
	_ = store.Set(context.Background(), "intent:pkg-wine:"+oliveHash, fmt.Sprintf("%d", time.Now().Add(-4*24*time.Hour).Unix()), 7*24*time.Hour)

	// Wire up the stack.
	ctxSrv := httptest.NewServer(agentHandler(contextEngine, nil))
	t.Cleanup(ctxSrv.Close)
	idSrv := httptest.NewServer(agentHandler(nil, identityEngine))
	t.Cleanup(idSrv.Close)

	reg := router.NewRegistry("", "")
	reg.LoadFromData([]router.RegistryProperty{
		{PropertyID: "pub-oakwood", PropertyRID: 1, PropertyType: "website"},
	}, 1)
	health := router.NewProviderHealth(3, 10*time.Second)
	r, err := router.NewRouter([]router.ProviderConfig{
		{ID: "ctx", Endpoint: ctxSrv.URL, ContextMatch: true, Timeout: 5 * time.Second},
		{ID: "id", Endpoint: idSrv.URL, IdentityMatch: true, Timeout: 5 * time.Second},
	}, reg, nil, health, router.WithoutEndpointValidation())
	require.NoError(t, err, "failed to create router")

	mux := http.NewServeMux()
	mux.HandleFunc("POST /tmp/context", r.HandleContextMatch)
	mux.HandleFunc("POST /tmp/identity", r.HandleIdentityMatch)
	routerSrv := httptest.NewServer(mux)
	t.Cleanup(routerSrv.Close)

	client := tmpclient.NewClient(routerSrv.URL, tmpclient.WithHTTPClient(&http.Client{Timeout: 10 * time.Second}))

	// Activate: 3 competing packages for the same placement.
	result, err := client.Activate(context.Background(), &tmpclient.ActivateParams{
		PropertyID:   "pub-oakwood",
		PropertyType: tmproto.PropertyTypeWebsite,
		PlacementID:  "main-content",
		Artifacts:    []string{"article:pasta"},
		Packages: []tmproto.AvailablePackage{
			{PackageID: "pkg-olive-oil", MediaBuyID: "mb-meridian"},
			{PackageID: "pkg-cookware", MediaBuyID: "mb-chefpro"},
			{PackageID: "pkg-wine", MediaBuyID: "mb-vino"},
		},
		UserToken:  "tok-alice",
		UIDType:    tmproto.UIDTypeUID2,
		PackageIDs: []string{"pkg-olive-oil", "pkg-cookware", "pkg-wine"},
	})
	require.NoError(t, err, "Activate failed")

	// All 3 should activate (all topic match + alice in cooking_fans).
	require.Len(t, result.Activations, 3, "expected 3 activations")

	t.Log("")
	t.Log("=== Mediation: Competing Offers ===")
	t.Log("")
	for i, a := range result.Activations {
		price := "none"
		if a.Offer.Price != nil {
			price = fmt.Sprintf("$%.2f %s", a.Offer.Price.Amount, a.Offer.Price.Model)
		}
		brand := "unknown"
		if a.Offer.Brand != nil {
			brand = a.Offer.Brand.Name
		}
		t.Logf("  #%d %-16s brand=%-20s price=%-12s summary=%q",
			i+1, a.PackageID, brand, price, a.Offer.Summary)
	}
	t.Log("")

	// Verify offers carry full data.
	olive := result.Activations[0] // sorted by intent, olive has highest
	require.NotNil(t, olive.Offer.Brand, "expected brand on olive oil offer")
	assert.Equal(t, "Meridian Foods", olive.Offer.Brand.Name)
	require.NotNil(t, olive.Offer.Price, "expected price on olive oil offer")
	assert.Equal(t, 12.50, olive.Offer.Price.Amount)
	assert.NotEmpty(t, olive.Offer.Summary, "expected summary on olive oil offer")
	assert.NotEmpty(t, olive.Offer.CreativeManifest, "expected creative manifest on olive oil offer")
	assert.NotEmpty(t, olive.Offer.Macros["click_url"], "expected click_url macro on olive oil offer")

	// Publisher mediation: pick by price.
	t.Log("=== Publisher Mediation Decision ===")
	t.Log("")
	var bestPkg string
	var bestPrice float64
	for _, a := range result.Activations {
		price := 0.0
		if a.Offer.Price != nil {
			price = a.Offer.Price.Amount
		}
		t.Logf("  %-16s price=$%.2f", a.PackageID, price)
		if price > bestPrice {
			bestPrice = price
			bestPkg = a.PackageID
		}
	}
	t.Logf("")
	t.Logf("  Winner: %s (price=$%.2f)", bestPkg, bestPrice)
	t.Log("")

	assert.NotEmpty(t, bestPkg, "expected a mediation winner")

	// Verify signals.
	require.NotNil(t, result.Signals, "expected signals")
	t.Logf("  Segments: %v", result.Signals.Segments)
}

func TestIntegration_MultiDealMediation(t *testing.T) {
	store := targeting.NewMockStore()

	// Topic data.
	store.SetAdd("topics:package:pkg-premium-food", "food.cooking", "food.recipes")
	store.SetAdd("topics:artifact:article:pasta", "food.cooking", "food.italian")

	// All users eligible (no audience gate).

	contextEngine := targeting.NewEngine(targeting.EngineConfig{
		ProviderID: "multideal-context",
		Store:      store,
		Properties: targeting.PropertyList{Global: targeting.NewMapBitmap(1)},
		Packages: []targeting.PackageConfig{
			{
				PackageID:    "pkg-premium-food",
				TopicTargets: true,
				EmitSegments: []string{"food"},
				Offers: []targeting.OfferConfig{
					{
						Brand:   &tmproto.BrandRef{Name: "Meridian Foods", AdvertiserDomain: "meridian-foods.example.com"},
						Price:   &tmproto.OfferPrice{Amount: 12.50, Currency: "USD", Model: tmproto.PriceModelCPM},
						Summary: "Meridian Extra Virgin Olive Oil",
					},
					{
						Brand:   &tmproto.BrandRef{Name: "ChefPro", AdvertiserDomain: "chefpro.example.com"},
						Price:   &tmproto.OfferPrice{Amount: 8.00, Currency: "USD", Model: tmproto.PriceModelCPM},
						Summary: "ChefPro Premium Cookware Set",
					},
					{
						Brand:   &tmproto.BrandRef{Name: "Vino Select", AdvertiserDomain: "vinoselect.example.com"},
						Price:   &tmproto.OfferPrice{Amount: 18.75, Currency: "USD", Model: tmproto.PriceModelCPM},
						Summary: "Vino Select — Perfect wine for pasta night",
					},
				},
			},
		},
	})

	identityEngine := targeting.NewEngine(targeting.EngineConfig{
		ProviderID: "multideal-identity",
		Store:      store,
		Packages: []targeting.PackageConfig{
			{PackageID: "pkg-premium-food"}, // no caps, no audience gate
		},
	})

	ctxSrv := httptest.NewServer(agentHandler(contextEngine, nil))
	t.Cleanup(ctxSrv.Close)
	idSrv := httptest.NewServer(agentHandler(nil, identityEngine))
	t.Cleanup(idSrv.Close)

	reg := router.NewRegistry("", "")
	reg.LoadFromData([]router.RegistryProperty{
		{PropertyID: "pub-oakwood", PropertyRID: 1, PropertyType: "website"},
	}, 1)
	health := router.NewProviderHealth(3, 10*time.Second)
	r, err := router.NewRouter([]router.ProviderConfig{
		{ID: "ctx", Endpoint: ctxSrv.URL, ContextMatch: true, Timeout: 5 * time.Second},
		{ID: "id", Endpoint: idSrv.URL, IdentityMatch: true, Timeout: 5 * time.Second},
	}, reg, nil, health, router.WithoutEndpointValidation())
	require.NoError(t, err, "failed to create router")

	mux := http.NewServeMux()
	mux.HandleFunc("POST /tmp/context", r.HandleContextMatch)
	mux.HandleFunc("POST /tmp/identity", r.HandleIdentityMatch)
	routerSrv := httptest.NewServer(mux)
	t.Cleanup(routerSrv.Close)

	client := tmpclient.NewClient(routerSrv.URL, tmpclient.WithHTTPClient(&http.Client{Timeout: 10 * time.Second}))

	result, err := client.Activate(context.Background(), &tmpclient.ActivateParams{
		PropertyID:   "pub-oakwood",
		PropertyType: tmproto.PropertyTypeWebsite,
		PlacementID:  "main-content",
		Artifacts:    []string{"article:pasta"},
		Packages:     []tmproto.AvailablePackage{{PackageID: "pkg-premium-food", MediaBuyID: "mb-premium"}},
		UserToken:    "tok-alice",
		UIDType:    tmproto.UIDTypeUID2,
		PackageIDs:   []string{"pkg-premium-food"},
	})
	require.NoError(t, err, "Activate failed")

	// 3 deals should produce 3 activations, all for the same package.
	require.Len(t, result.Activations, 3, "expected 3 activations (3 deals for 1 package)")

	t.Log("")
	t.Log("=== Multi-Deal Auction: 3 brands competing for pkg-premium-food ===")
	t.Log("")

	var bestBrand string
	var bestPrice float64
	for i, a := range result.Activations {
		assert.Equal(t, "pkg-premium-food", a.PackageID, "activation %d: unexpected package", i)
		brand := "?"
		if a.Offer.Brand != nil {
			brand = a.Offer.Brand.Name
		}
		price := 0.0
		if a.Offer.Price != nil {
			price = a.Offer.Price.Amount
		}
		t.Logf("  %-20s $%.2f CPM — %s", brand, price, a.Offer.Summary)

		if price > bestPrice {
			bestPrice = price
			bestBrand = brand
		}
	}

	t.Logf("")
	t.Logf("  Highest bidder: %s at $%.2f CPM", bestBrand, bestPrice)
	t.Log("")

	assert.Equal(t, "Vino Select", bestBrand, "expected Vino Select as highest bidder")
	assert.Equal(t, 18.75, bestPrice, "expected $18.75")

	// Verify all 3 offers have distinct brands.
	brands := map[string]bool{}
	for _, a := range result.Activations {
		if a.Offer.Brand != nil {
			brands[a.Offer.Brand.Name] = true
		}
	}
	assert.Len(t, brands, 3, "expected 3 distinct brands: %v", brands)
}

func TestIntegration_Throughput(t *testing.T) {
	s := setupStack(t)
	ctx := context.Background()

	params := &tmpclient.ActivateParams{
		PropertyID:   "pub-oakwood",
		PropertyType: tmproto.PropertyTypeWebsite,
		PlacementID:  "sidebar",
		Artifacts:    []string{"article:pasta"},
		Packages: []tmproto.AvailablePackage{
			{PackageID: "pkg-food", MediaBuyID: "mb-food"},
			{PackageID: "pkg-family", MediaBuyID: "mb-family"},
		},
		UserToken:  "tok-alice",
		UIDType:    tmproto.UIDTypeUID2,
		PackageIDs: []string{"pkg-food", "pkg-family"},
	}

	const iterations = 100
	start := time.Now()
	var errors int
	for range iterations {
		_, err := s.client.Activate(ctx, params)
		if err != nil {
			errors++
		}
	}
	elapsed := time.Since(start)

	avg := elapsed / iterations
	qps := float64(iterations) / elapsed.Seconds()
	t.Logf("throughput: %d iterations in %v (avg=%v, qps=%.0f, errors=%d)", iterations, elapsed, avg, qps, errors)

	assert.Zero(t, errors, "%d errors in %d iterations", errors, iterations)
}
