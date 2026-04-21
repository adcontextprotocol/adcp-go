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

	// Context engine.
	contextEngine := targeting.NewEngine(targeting.EngineConfig{
		ProviderID: "integration-context",
		Store:      store,
		Properties: targeting.PropertyList{
			Global: targeting.NewMapBitmap("1", "2", "3", "4", "5"),
		},
		Packages: []targeting.PackageConfig{
			{PackageID: "pkg-food", TopicTargets: true, EmitSegments: []string{"food", "cooking"}},
			{PackageID: "pkg-tech", TopicTargets: true, EmitSegments: []string{"technology"}},
			{PackageID: "pkg-family", URLBlocklist: true},
		},
	})

	// Seed identity config in Store (data-driven, not static config).
	store.SetPackageIdentityConfig("pkg-food", targeting.PackageIdentityConfig{
		AdvertiserID:   "adv-acme",
		CampaignID:     "campaign-acme",
		CreativeID:     "creative-food",
		FrequencyRules: []targeting.FrequencyRuleJSON{{MaxCount: 3, WindowSeconds: 86400}},
		TargetSegments: []string{"cooking_fans"},
	})
	// pkg-tech: no identity config = always eligible
	store.SetPackageIdentityConfig("pkg-family", targeting.PackageIdentityConfig{
		CreativeID:     "creative-family",
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
			"pkg-food":   {AdvertiserID: "adv-acme", CampaignID: "campaign-acme", CreativeID: "creative-food", FrequencyRules: []targeting.FrequencyRuleJSON{{MaxCount: 3, WindowSeconds: 86400}}, TargetSegments: []string{"cooking_fans"}},
			"pkg-tech":   {},
			"pkg-family": {CreativeID: "creative-family", FrequencyRules: []targeting.FrequencyRuleJSON{{MaxCount: 5, WindowSeconds: 604800}}},
		},
		CampaignConfigs: map[string]*targeting.CampaignFreqConfig{
			"campaign-acme": {FrequencyRules: []targeting.FrequencyRuleJSON{{MaxCount: 5, WindowSeconds: 604800}}},
		},
	}

	// Start context agent server.
	ctxSrv := httptest.NewServer(agentHandler(contextEngine, nil, nil))
	t.Cleanup(ctxSrv.Close)

	// Start identity agent server.
	idSrv := httptest.NewServer(agentHandler(nil, identityEngine, idResolved))
	t.Cleanup(idSrv.Close)

	// Start real router with property registry populated.
	reg := router.NewRegistry("", "")
	reg.LoadFromData([]router.RegistryProperty{
		{PropertyID: "pub-oakwood", PropertyRID: "1", PropertyType: "website", Domain: "oakwood.example.com"},
		{PropertyID: "pub-techblog", PropertyRID: "2", PropertyType: "website", Domain: "techblog.example.com"},
	}, 1)
	health := router.NewProviderHealth(3, 10*time.Second)
	r, err := router.NewRouter([]router.ProviderConfig{
		{ID: "ctx-agent", Endpoint: ctxSrv.URL, ContextMatch: true, Timeout: 5 * time.Second},
		{ID: "id-agent", Endpoint: idSrv.URL, IdentityMatch: true, Timeout: 5 * time.Second},
	}, reg, health, router.WithoutEndpointValidation())
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
// Identity evaluation uses the resolved-packages path.
func agentHandler(ctxEngine, idEngine *targeting.Engine, resolved *targeting.ResolvedPackages) http.Handler {
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
			result, evalErr := idEngine.EvaluateIdentityResolved(r.Context(), resolved, &req)
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
		ArtifactRefs: []tmproto.ArtifactRef{{Type: tmproto.ArtifactRefTypeURL, Value: "article:pasta"}},
		PackageIDs:   []string{"pkg-food", "pkg-tech", "pkg-family"},
		UserToken:    "tok-alice",
		UIDType:      tmproto.UIDTypeUID2,
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
	segs, _ := result.Signals["segments"].([]any)
	segSet := map[string]bool{}
	for _, seg := range segs {
		if s, ok := seg.(string); ok {
			segSet[s] = true
		}
	}
	assert.True(t, segSet["food"] && segSet["cooking"], "expected food+cooking segments, got %v", result.Signals["segments"])

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
		ArtifactRefs: []tmproto.ArtifactRef{{Type: tmproto.ArtifactRefTypeURL, Value: "article:pasta"}},
		PackageIDs:   []string{"pkg-food"},
		UserToken:    "tok-alice",
		UIDType:      tmproto.UIDTypeUID2,
	}

	// 3 exposures (hits 3/24h cap on pkg-food).
	for i := range 3 {
		result, err := s.client.Activate(ctx, params)
		require.NoError(t, err, "activate %d", i)
		require.NotEmpty(t, result.Activations, "activate %d: expected activation before cap", i)
		s.store.AddExposure("tok-alice", targeting.ExposureEntry{
			ImpressionID: fmt.Sprintf("imp-fcap-%d", i),
			AdvertiserID: "adv-acme",
			CampaignID:   "campaign-acme",
			CreativeID:   "creative-food",
			Timestamp:    time.Now().Unix(),
		})
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
		ArtifactRefs: []tmproto.ArtifactRef{{Type: tmproto.ArtifactRefTypeURL, Value: "article:pasta"}},
		PackageIDs:   []string{"pkg-food"},
		UserToken:    "tok-bob",
		UIDType:      tmproto.UIDTypeUID2,
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
		ArtifactRefs: []tmproto.ArtifactRef{{Type: tmproto.ArtifactRefTypeURL, Value: "article:adult-content"}},
		PackageIDs:   []string{"pkg-family"},
		UserToken:    "tok-alice",
		UIDType:      tmproto.UIDTypeUID2,
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
		ArtifactRefs: []tmproto.ArtifactRef{{Type: tmproto.ArtifactRefTypeURL, Value: "article:pasta"}},
		PackageIDs:   []string{"pkg-food"},
		UserToken:    "tok-alice",
		UIDType:      tmproto.UIDTypeUID2,
	}

	// First activate — should be eligible.
	result1, err := s.client.Activate(ctx, params)
	require.NoError(t, err)
	require.NotEmpty(t, result1.Activations, "expected activation")

	s.store.AddExposure("tok-alice", targeting.ExposureEntry{
		ImpressionID: "imp-state-1",
		AdvertiserID: "adv-acme",
		CampaignID:   "campaign-acme",
		CreativeID:   "creative-food",
		Timestamp:    time.Now().Unix(),
	})

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
		ArtifactRefs: []tmproto.ArtifactRef{{Type: tmproto.ArtifactRefTypeURL, Value: "article:pasta"}},
		PackageIDs:   []string{"pkg-food"},
		UserToken:    "tok-alice",
		UIDType:      tmproto.UIDTypeUID2,
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

	// Alice is a cooking fan.
	store.SetUserProfile("tok-alice", map[string]float64{"cooking_fans": 1.0})

	creativeManifest, _ := json.Marshal(map[string]any{
		"format_id": "sponsored_card",
		"headline":  "Meridian Extra Virgin Olive Oil",
		"body":      "Cold-pressed from hand-picked Koroneiki olives",
		"image_url": "https://cdn.example.com/creative/olive-oil.jpg",
		"cta_text":  "Shop now",
	})

	marshalBrand := func(name, domain string) json.RawMessage {
		b, _ := json.Marshal(map[string]string{"name": name, "advertiser_domain": domain})
		return b
	}

	contextEngine := targeting.NewEngine(targeting.EngineConfig{
		ProviderID: "mediation-context",
		Store:      store,
		Properties: targeting.PropertyList{Global: targeting.NewMapBitmap("1")},
		Packages: []targeting.PackageConfig{
			{
				PackageID:        "pkg-olive-oil",
				TopicTargets:     true,
				Brand:            marshalBrand("Meridian Foods", "meridian-foods.example.com"),
				Price:            tmproto.OfferPrice{Amount: 12.50, Currency: "USD", Model: string(tmproto.PriceModelCPM)},
				Summary:          "Meridian olive oil sponsored content",
				CreativeManifest: creativeManifest,
				Macros:           map[string]string{"click_url": "https://meridian-foods.example.com/shop?utm_source=tmp"},
				EmitSegments:     []string{"food", "premium_brand"},
			},
			{
				PackageID:    "pkg-cookware",
				TopicTargets: true,
				Brand:        marshalBrand("ChefPro", "chefpro.example.com"),
				Price:        tmproto.OfferPrice{Amount: 8.00, Currency: "USD", Model: string(tmproto.PriceModelCPM)},
				Summary:      "ChefPro premium cookware",
				EmitSegments: []string{"food", "kitchen"},
			},
			{
				PackageID:    "pkg-wine",
				TopicTargets: true,
				Brand:        marshalBrand("Vino Select", "vinoselect.example.com"),
				Price:        tmproto.OfferPrice{Amount: 18.75, Currency: "USD", Model: string(tmproto.PriceModelCPM)},
				Summary:      "Vino Select wine pairings",
				EmitSegments: []string{"food", "beverage"},
			},
		},
	})

	// Seed identity config for mediation packages.
	oliveIdCfg := targeting.PackageIdentityConfig{CreativeID: "creative-olive", TargetSegments: []string{"cooking_fans"}}
	cookwareIdCfg := targeting.PackageIdentityConfig{CreativeID: "creative-cookware", TargetSegments: []string{"cooking_fans"}}
	wineIdCfg := targeting.PackageIdentityConfig{CreativeID: "creative-wine", TargetSegments: []string{"cooking_fans"}}
	store.SetPackageIdentityConfig("pkg-olive-oil", oliveIdCfg)
	store.SetPackageIdentityConfig("pkg-cookware", cookwareIdCfg)
	store.SetPackageIdentityConfig("pkg-wine", wineIdCfg)

	identityEngine := targeting.NewEngine(targeting.EngineConfig{
		ProviderID: "mediation-identity",
		Store:      store,
		Packages: []targeting.PackageConfig{
			{PackageID: "pkg-olive-oil"},
			{PackageID: "pkg-cookware"},
			{PackageID: "pkg-wine"},
		},
	})

	// Seed exposure history to drive intent scoring: olive-oil recent, wine older.
	store.SetUserExposures("tok-alice", []targeting.ExposureEntry{
		{ImpressionID: "imp-olive-1", CreativeID: "creative-olive", Timestamp: time.Now().Add(-6 * time.Hour).Unix()},
		{ImpressionID: "imp-wine-1", CreativeID: "creative-wine", Timestamp: time.Now().Add(-4 * 24 * time.Hour).Unix()},
	})

	idResolved := &targeting.ResolvedPackages{
		SegmentIndex: map[string][]string{
			"cooking_fans": {"pkg-olive-oil", "pkg-cookware", "pkg-wine"},
		},
		IdentityConfigs: map[string]*targeting.PackageIdentityConfig{
			"pkg-olive-oil": &oliveIdCfg,
			"pkg-cookware":  &cookwareIdCfg,
			"pkg-wine":      &wineIdCfg,
		},
	}

	// Wire up the stack.
	ctxSrv := httptest.NewServer(agentHandler(contextEngine, nil, nil))
	t.Cleanup(ctxSrv.Close)
	idSrv := httptest.NewServer(agentHandler(nil, identityEngine, idResolved))
	t.Cleanup(idSrv.Close)

	reg := router.NewRegistry("", "")
	reg.LoadFromData([]router.RegistryProperty{
		{PropertyID: "pub-oakwood", PropertyRID: "1", PropertyType: "website"},
	}, 1)
	health := router.NewProviderHealth(3, 10*time.Second)
	r, err := router.NewRouter([]router.ProviderConfig{
		{ID: "ctx", Endpoint: ctxSrv.URL, ContextMatch: true, Timeout: 5 * time.Second},
		{ID: "id", Endpoint: idSrv.URL, IdentityMatch: true, Timeout: 5 * time.Second},
	}, reg, health, router.WithoutEndpointValidation())
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
		ArtifactRefs: []tmproto.ArtifactRef{{Type: tmproto.ArtifactRefTypeURL, Value: "article:pasta"}},
		PackageIDs:   []string{"pkg-olive-oil", "pkg-cookware", "pkg-wine"},
		UserToken:    "tok-alice",
		UIDType:      tmproto.UIDTypeUID2,
	})
	require.NoError(t, err, "Activate failed")

	// All 3 should activate (all topic match + alice in cooking_fans).
	require.Len(t, result.Activations, 3, "expected 3 activations")

	t.Log("")
	t.Log("=== Mediation: Competing Offers ===")
	t.Log("")
	parseBrand := func(raw json.RawMessage) string {
		if len(raw) == 0 {
			return "unknown"
		}
		var b map[string]string
		if json.Unmarshal(raw, &b) != nil {
			return "unknown"
		}
		return b["name"]
	}

	for i, a := range result.Activations {
		price := "none"
		if a.Offer.Price.Amount > 0 {
			price = fmt.Sprintf("$%.2f %s", a.Offer.Price.Amount, a.Offer.Price.Model)
		}
		brand := parseBrand(a.Offer.Brand)
		t.Logf("  #%d %-16s brand=%-20s price=%-12s summary=%q",
			i+1, a.PackageID, brand, price, a.Offer.Summary)
	}
	t.Log("")

	// Verify offers carry full data.
	olive := result.Activations[0] // sorted by intent, olive has highest
	assert.Equal(t, "Meridian Foods", parseBrand(olive.Offer.Brand), "expected brand on olive oil offer")
	assert.Equal(t, 12.50, olive.Offer.Price.Amount, "expected price on olive oil offer")
	assert.NotEmpty(t, olive.Offer.Summary, "expected summary on olive oil offer")
	require.NotNil(t, olive.Offer.CreativeManifest, "expected creative manifest on olive oil offer")
	assert.NotEmpty(t, *olive.Offer.CreativeManifest, "expected creative manifest on olive oil offer")
	assert.NotEmpty(t, olive.Offer.Macros["click_url"], "expected click_url macro on olive oil offer")

	// Publisher mediation: pick by price.
	t.Log("=== Publisher Mediation Decision ===")
	t.Log("")
	var bestPkg string
	var bestPrice float64
	for _, a := range result.Activations {
		price := a.Offer.Price.Amount
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
	t.Logf("  Signals: %v", result.Signals)
}

func TestIntegration_MultiDealMediation(t *testing.T) {
	store := targeting.NewMockStore()

	// Topic data.
	store.SetAdd("topics:package:pkg-premium-food", "food.cooking", "food.recipes")
	store.SetAdd("topics:artifact:article:pasta", "food.cooking", "food.italian")

	// All users eligible (no audience gate).

	marshalBrand := func(name, domain string) json.RawMessage {
		b, _ := json.Marshal(map[string]string{"name": name, "advertiser_domain": domain})
		return b
	}

	contextEngine := targeting.NewEngine(targeting.EngineConfig{
		ProviderID: "multideal-context",
		Store:      store,
		Properties: targeting.PropertyList{Global: targeting.NewMapBitmap("1")},
		Packages: []targeting.PackageConfig{
			{
				PackageID:    "pkg-premium-food",
				TopicTargets: true,
				EmitSegments: []string{"food"},
				Offers: []targeting.OfferConfig{
					{
						Brand:   marshalBrand("Meridian Foods", "meridian-foods.example.com"),
						Price:   tmproto.OfferPrice{Amount: 12.50, Currency: "USD", Model: string(tmproto.PriceModelCPM)},
						Summary: "Meridian Extra Virgin Olive Oil",
					},
					{
						Brand:   marshalBrand("ChefPro", "chefpro.example.com"),
						Price:   tmproto.OfferPrice{Amount: 8.00, Currency: "USD", Model: string(tmproto.PriceModelCPM)},
						Summary: "ChefPro Premium Cookware Set",
					},
					{
						Brand:   marshalBrand("Vino Select", "vinoselect.example.com"),
						Price:   tmproto.OfferPrice{Amount: 18.75, Currency: "USD", Model: string(tmproto.PriceModelCPM)},
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

	idResolved := &targeting.ResolvedPackages{
		IdentityConfigs: map[string]*targeting.PackageIdentityConfig{
			"pkg-premium-food": {},
		},
	}

	ctxSrv := httptest.NewServer(agentHandler(contextEngine, nil, nil))
	t.Cleanup(ctxSrv.Close)
	idSrv := httptest.NewServer(agentHandler(nil, identityEngine, idResolved))
	t.Cleanup(idSrv.Close)

	reg := router.NewRegistry("", "")
	reg.LoadFromData([]router.RegistryProperty{
		{PropertyID: "pub-oakwood", PropertyRID: "1", PropertyType: "website"},
	}, 1)
	health := router.NewProviderHealth(3, 10*time.Second)
	r, err := router.NewRouter([]router.ProviderConfig{
		{ID: "ctx", Endpoint: ctxSrv.URL, ContextMatch: true, Timeout: 5 * time.Second},
		{ID: "id", Endpoint: idSrv.URL, IdentityMatch: true, Timeout: 5 * time.Second},
	}, reg, health, router.WithoutEndpointValidation())
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
		ArtifactRefs: []tmproto.ArtifactRef{{Type: tmproto.ArtifactRefTypeURL, Value: "article:pasta"}},
		PackageIDs:   []string{"pkg-premium-food"},
		UserToken:    "tok-alice",
		UIDType:      tmproto.UIDTypeUID2,
	})
	require.NoError(t, err, "Activate failed")

	// 3 deals should produce 3 activations, all for the same package.
	require.Len(t, result.Activations, 3, "expected 3 activations (3 deals for 1 package)")

	t.Log("")
	t.Log("=== Multi-Deal Auction: 3 brands competing for pkg-premium-food ===")
	t.Log("")

	parseBrandName := func(raw json.RawMessage) string {
		var b map[string]string
		if json.Unmarshal(raw, &b) != nil {
			return "?"
		}
		return b["name"]
	}

	var bestBrand string
	var bestPrice float64
	for i, a := range result.Activations {
		assert.Equal(t, "pkg-premium-food", a.PackageID, "activation %d: unexpected package", i)
		brand := parseBrandName(a.Offer.Brand)
		price := a.Offer.Price.Amount
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
		brands[parseBrandName(a.Offer.Brand)] = true
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
		ArtifactRefs: []tmproto.ArtifactRef{{Type: tmproto.ArtifactRefTypeURL, Value: "article:pasta"}},
		PackageIDs:   []string{"pkg-food", "pkg-family"},
		UserToken:    "tok-alice",
		UIDType:      tmproto.UIDTypeUID2,
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
