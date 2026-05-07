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
	"github.com/adcontextprotocol/adcp-go/targeting/audience"
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

	store.SetAdd("topics:package:pkg-food", "food.cooking", "food.recipes")
	store.SetAdd("topics:artifact:article:pasta", "food.cooking", "food.italian")
	store.SetAdd("topics:package:pkg-tech", "technology.gadgets", "technology.reviews")
	store.SetAdd("topics:artifact:article:cpu-review", "technology.reviews", "technology.hardware")

	store.SetAdd("url:blocklist:pkg-family", targeting.HashURL("article:adult-content"))

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

	store.SetPackageIdentityConfig("pkg-food", targeting.PackageIdentityConfig{
		TargetSegments: []string{"cooking_fans"},
	})
	store.SetPackageIdentityConfig("pkg-family", targeting.PackageIdentityConfig{})

	audSvc := audience.New(audience.NewMockStore())
	require.NoError(t, audSvc.UpsertBatch(context.Background(), []audience.AudienceUpsert{
		{AudienceID: "cooking_fans", Add: []audience.Member{{UserToken: "tok-alice", Score: 0.8}}},
		{AudienceID: "sports_fans", Add: []audience.Member{{UserToken: "tok-bob", Score: 0.5}}},
	}))

	identityEngine := targeting.NewEngine(targeting.EngineConfig{
		ProviderID: "integration-identity",
		Store:      store,
		Audience:   audSvc,
		Packages: []targeting.PackageConfig{
			{PackageID: "pkg-food"},
			{PackageID: "pkg-tech"},
			{PackageID: "pkg-family"},
		},
	})

	idResolved := &targeting.ResolvedPackages{
		SegmentIndex: map[string][]string{
			"cooking_fans": {"pkg-food"},
		},
		IdentityConfigs: map[string]*targeting.PackageIdentityConfig{
			"pkg-food":   {TargetSegments: []string{"cooking_fans"}},
			"pkg-tech":   {},
			"pkg-family": {},
		},
	}

	ctxSrv := httptest.NewServer(agentHandler(contextEngine, nil, nil))
	t.Cleanup(ctxSrv.Close)

	idSrv := httptest.NewServer(agentHandler(nil, identityEngine, idResolved))
	t.Cleanup(idSrv.Close)

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

	client := tmpclient.NewClient(routerSrv.URL, tmpclient.WithHTTPClient(&http.Client{Timeout: 10 * time.Second}))

	return &testStack{
		client:         client,
		store:          store,
		contextEngine:  contextEngine,
		identityEngine: identityEngine,
	}
}

// agentHandler creates an HTTP handler that serves both context and identity endpoints.
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

	activated := map[string]bool{}
	for _, a := range result.Activations {
		activated[a.PackageID] = true
		t.Logf("activated: %s (mediaBuyID=%s)", a.PackageID, a.MediaBuyID)
	}

	assert.True(t, activated["pkg-food"], "pkg-food should be activated (topic match + audience match)")
	assert.False(t, activated["pkg-tech"], "pkg-tech should NOT be activated (topic mismatch)")
	assert.True(t, activated["pkg-family"], "pkg-family should be activated (no blocklist hit, no audience gate)")

	require.NotNil(t, result.Signals, "expected signals")
	segs, _ := result.Signals["segments"].([]any)
	segSet := map[string]bool{}
	for _, seg := range segs {
		if s, ok := seg.(string); ok {
			segSet[s] = true
		}
	}
	assert.True(t, segSet["food"] && segSet["cooking"], "expected food+cooking segments, got %v", result.Signals["segments"])

	assert.NotNil(t, result.Context, "expected raw context response")
	assert.NotNil(t, result.Identity, "expected raw identity response")
}

func TestIntegration_AudienceGating(t *testing.T) {
	s := setupStack(t)

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

func TestIntegration_PropertyBitmapFilter(t *testing.T) {
	s := setupStack(t)

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

	assert.Empty(t, result.Activations, "expected 0 activations for unknown property")
}

func TestIntegration_Mediation(t *testing.T) {
	store := targeting.NewMockStore()

	store.SetAdd("topics:package:pkg-olive-oil", "food.cooking", "food.ingredients")
	store.SetAdd("topics:package:pkg-cookware", "food.cooking", "food.kitchen")
	store.SetAdd("topics:package:pkg-wine", "food.cooking", "food.beverage")
	store.SetAdd("topics:artifact:article:pasta", "food.cooking", "food.italian")

	audSvc := audience.New(audience.NewMockStore())
	require.NoError(t, audSvc.Upsert(context.Background(), audience.AudienceUpsert{
		AudienceID: "cooking_fans",
		Add:        []audience.Member{{UserToken: "tok-alice", Score: 1.0}},
	}))

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

	oliveIdCfg := targeting.PackageIdentityConfig{TargetSegments: []string{"cooking_fans"}}
	cookwareIdCfg := targeting.PackageIdentityConfig{TargetSegments: []string{"cooking_fans"}}
	wineIdCfg := targeting.PackageIdentityConfig{TargetSegments: []string{"cooking_fans"}}
	store.SetPackageIdentityConfig("pkg-olive-oil", oliveIdCfg)
	store.SetPackageIdentityConfig("pkg-cookware", cookwareIdCfg)
	store.SetPackageIdentityConfig("pkg-wine", wineIdCfg)

	identityEngine := targeting.NewEngine(targeting.EngineConfig{
		ProviderID: "mediation-identity",
		Store:      store,
		Audience:   audSvc,
		Packages: []targeting.PackageConfig{
			{PackageID: "pkg-olive-oil"},
			{PackageID: "pkg-cookware"},
			{PackageID: "pkg-wine"},
		},
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
		PackageIDs:   []string{"pkg-olive-oil", "pkg-cookware", "pkg-wine"},
		UserToken:    "tok-alice",
		UIDType:      tmproto.UIDTypeUID2,
	})
	require.NoError(t, err, "Activate failed")

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

	require.NotNil(t, result.Signals, "expected signals")
	t.Logf("  Signals: %v", result.Signals)
}

func TestIntegration_MultiDealMediation(t *testing.T) {
	store := targeting.NewMockStore()

	store.SetAdd("topics:package:pkg-premium-food", "food.cooking", "food.recipes")
	store.SetAdd("topics:artifact:article:pasta", "food.cooking", "food.italian")

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
			{PackageID: "pkg-premium-food"},
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
