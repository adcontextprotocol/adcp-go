package e2e

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/adcontextprotocol/adcp-go/tmp"
)

// --- Realistic Module Implementations ---
// These simulate what real TMP providers would do with modules.

// BrandSafetyModule rejects packages when content is in a blocked category.
// A real implementation would call a classification API or use a local model.
type BrandSafetyModule struct {
	// package_id -> list of blocked keywords
	blockedKeywords map[string][]string
}

func (m *BrandSafetyModule) Evaluate(req *tmp.ContextMatchRequest, pkg tmp.AvailablePackage) (bool, float32) {
	blocked := m.blockedKeywords[pkg.PackageID]
	for _, kw := range blocked {
		for _, art := range req.Artifacts {
			if strings.Contains(strings.ToLower(art), kw) {
				return false, 0
			}
		}
	}
	return true, 1.0
}

// TopicRelevanceModule scores packages based on topic overlap.
// A real implementation would use embeddings or a taxonomy lookup.
type TopicRelevanceModule struct {
	// package_id -> list of relevant topic keywords
	topicKeywords map[string][]string
}

func (m *TopicRelevanceModule) Evaluate(req *tmp.ContextMatchRequest, pkg tmp.AvailablePackage) (bool, float32) {
	topics := m.topicKeywords[pkg.PackageID]
	if len(topics) == 0 {
		return true, 0.5 // No topic targeting = pass with neutral score
	}
	matchCount := 0
	for _, topic := range topics {
		for _, art := range req.Artifacts {
			if strings.Contains(strings.ToLower(art), topic) {
				matchCount++
				break
			}
		}
	}
	if matchCount == 0 {
		return false, 0
	}
	return true, float32(matchCount) / float32(len(topics))
}

// CatalogMatchModule checks if a package's catalog items are relevant
// to the content. A real implementation would look up product-content
// affinity scores from a database.
type CatalogMatchModule struct {
	// package_id -> list of relevant content keywords for this catalog
	catalogRelevance map[string][]string
}

func (m *CatalogMatchModule) Evaluate(req *tmp.ContextMatchRequest, pkg tmp.AvailablePackage) (bool, float32) {
	keywords := m.catalogRelevance[pkg.PackageID]
	if len(keywords) == 0 {
		return false, 0 // No catalog data for this package — don't activate
	}
	for _, kw := range keywords {
		for _, art := range req.Artifacts {
			if strings.Contains(strings.ToLower(art), kw) {
				return true, 0.9
			}
		}
	}
	return false, 0
}

// PropertyTargetingModule checks if this property is in the agent's targeting set.
// Uses a simple map instead of Roaring bitmaps for the simulation.
type PropertyTargetingModule struct {
	// package_id -> set of allowed property IDs (simulates Roaring bitmap)
	allowedProperties map[string]map[string]bool
}

func (m *PropertyTargetingModule) Evaluate(req *tmp.ContextMatchRequest, pkg tmp.AvailablePackage) (bool, float32) {
	allowed := m.allowedProperties[pkg.PackageID]
	if allowed == nil {
		return true, 1.0 // No property targeting = run everywhere
	}
	if allowed[req.PropertyID] {
		return true, 1.0
	}
	return false, 0
}

// --- Realistic Context Agent with Module Pipeline ---

type simulatedContextAgent struct {
	name    string
	modules []interface{ Evaluate(*tmp.ContextMatchRequest, tmp.AvailablePackage) (bool, float32) }
}

func (a *simulatedContextAgent) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/tmp/context" {
		http.NotFound(w, r)
		return
	}
	var req tmp.ContextMatchRequest
	json.NewDecoder(r.Body).Decode(&req)

	var offers []tmp.Offer
	for _, pkg := range req.AvailablePkgs {
		activate := true
		var totalScore float32
		moduleCount := 0

		for _, mod := range a.modules {
			ok, score := mod.Evaluate(&req, pkg)
			if !ok {
				activate = false
				break
			}
			totalScore += score
			moduleCount++
		}

		if activate && moduleCount > 0 {
			avgScore := totalScore / float32(moduleCount)
			offer := tmp.Offer{
				PackageID: pkg.PackageID,
				Summary:   fmt.Sprintf("Activated by %s (score: %.2f)", a.name, avgScore),
			}
			offers = append(offers, offer)
		}
	}

	resp := tmp.ContextMatchResponse{
		RequestID: req.RequestID,
		Offers:    offers,
		Signals: &tmp.Signals{
			Segments: []string{},
			TargetingKVs: []tmp.KeyValuePair{
				{Key: "provider", Value: a.name},
			},
		},
	}
	for _, o := range offers {
		resp.Signals.TargetingKVs = append(resp.Signals.TargetingKVs, tmp.KeyValuePair{
			Key: "adcp_pkg", Value: o.PackageID,
		})
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

// --- Realistic Identity Agent with multiple signal sources ---

type simulatedIdentityAgent struct {
	name string
	mu   sync.Mutex
	// Frequency: token -> package -> count
	freqCounts map[string]map[string]int
	freqCaps   map[string]int // package -> max
	// Audience segments: segment -> set of tokens
	audiences map[string]map[string]bool
	// Package -> required segments (ANY match)
	packageSegments map[string][]string
}

func newSimulatedIdentityAgent(name string, caps map[string]int, segments map[string][]string) *simulatedIdentityAgent {
	return &simulatedIdentityAgent{
		name:            name,
		freqCounts:      make(map[string]map[string]int),
		freqCaps:        caps,
		audiences:       make(map[string]map[string]bool),
		packageSegments: segments,
	}
}

func (a *simulatedIdentityAgent) addToAudience(segment, token string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.audiences[segment] == nil {
		a.audiences[segment] = make(map[string]bool)
	}
	a.audiences[segment][token] = true
}

func (a *simulatedIdentityAgent) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch r.URL.Path {
	case "/tmp/identity":
		a.handleIdentity(w, r)
	case "/tmp/expose":
		a.handleExpose(w, r)
	default:
		http.NotFound(w, r)
	}
}

func (a *simulatedIdentityAgent) handleIdentity(w http.ResponseWriter, r *http.Request) {
	var req tmp.IdentityMatchRequest
	json.NewDecoder(r.Body).Decode(&req)

	a.mu.Lock()
	defer a.mu.Unlock()

	var eligibility []tmp.PackageEligibility
	for _, pkgID := range req.PackageIDs {
		eligible := true
		intentScore := 0.5

		// Check frequency cap
		if cap, ok := a.freqCaps[pkgID]; ok {
			count := 0
			if userCounts, ok := a.freqCounts[req.UserToken]; ok {
				count = userCounts[pkgID]
			}
			if count >= cap {
				eligible = false
			}
			// Intent decays with frequency
			if count > 0 {
				intentScore = 0.8 - (float64(count) * 0.1)
				if intentScore < 0.1 {
					intentScore = 0.1
				}
			}
		}

		// Check audience segments
		if eligible {
			reqSegments := a.packageSegments[pkgID]
			if len(reqSegments) > 0 {
				inAudience := false
				for _, seg := range reqSegments {
					if a.audiences[seg] != nil && a.audiences[seg][req.UserToken] {
						inAudience = true
						intentScore += 0.2 // Audience match boosts intent
						break
					}
				}
				if !inAudience {
					eligible = false
				}
			}
		}

		if intentScore > 1.0 {
			intentScore = 1.0
		}
		score := intentScore
		eligibility = append(eligibility, tmp.PackageEligibility{
			PackageID:   pkgID,
			Eligible:    eligible,
			IntentScore: &score,
		})
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(tmp.IdentityMatchResponse{
		RequestID:   req.RequestID,
		Eligibility: eligibility,
	})
}

func (a *simulatedIdentityAgent) handleExpose(w http.ResponseWriter, r *http.Request) {
	var req tmp.ExposeRequest
	json.NewDecoder(r.Body).Decode(&req)

	a.mu.Lock()
	if a.freqCounts[req.UserToken] == nil {
		a.freqCounts[req.UserToken] = make(map[string]int)
	}
	a.freqCounts[req.UserToken][req.PackageID]++
	a.mu.Unlock()

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(tmp.ExposeResponse{PackageID: req.PackageID})
}

// --- Simulation Tests ---

func TestSimulation_MultiAgentRetailMedia(t *testing.T) {
	// Scenario: A grocery retailer with 3 TMP providers
	// 1. Brand safety provider (blocks unsafe content)
	// 2. Contextual targeting provider (matches products to content)
	// 3. Retail catalog provider (matches catalog items to content)

	// One contextual provider with brand safety + topic + property targeting as modules
	contextualAgent := httptest.NewServer(&simulatedContextAgent{
		name: "contextual-provider",
		modules: []interface {
			Evaluate(*tmp.ContextMatchRequest, tmp.AvailablePackage) (bool, float32)
		}{
			// Brand safety runs first — blocks before any other evaluation
			&BrandSafetyModule{
				blockedKeywords: map[string][]string{
					"pkg-alcohol-display": {"children", "school", "pediatric"},
					"pkg-pharma-native":   {"controversy", "lawsuit"},
				},
			},
			// Property targeting — is this property in the package's target set?
			&PropertyTargetingModule{
				allowedProperties: map[string]map[string]bool{
					"pkg-coffee-sponsored": {"pub-grocery-main": true, "pub-recipe-blog": true},
					"pkg-snacks-display":   {"pub-grocery-main": true, "pub-recipe-blog": true},
					"pkg-alcohol-display":  {"pub-grocery-main": true},
					"pkg-pharma-native":    {"pub-grocery-main": true, "pub-health-mag": true},
				},
			},
			// Topic relevance — does the content match the package's topics?
			&TopicRelevanceModule{
				topicKeywords: map[string][]string{
					"pkg-coffee-sponsored":  {"coffee", "beverage", "breakfast"},
					"pkg-snacks-display":    {"snack", "recipe", "cooking"},
					"pkg-alcohol-display":   {"cocktail", "wine", "dining"},
					"pkg-pharma-native":     {"health", "wellness", "fitness"},
					"pkg-cleaning-carousel": {"home", "cleaning", "kitchen"},
				},
			},
		},
	})
	defer contextualAgent.Close()

	// Separate catalog provider — evaluates catalog item relevance
	catalogAgent := httptest.NewServer(&simulatedContextAgent{
		name: "catalog-provider",
		modules: []interface {
			Evaluate(*tmp.ContextMatchRequest, tmp.AvailablePackage) (bool, float32)
		}{
			&CatalogMatchModule{
				catalogRelevance: map[string][]string{
					"pkg-coffee-sponsored":  {"coffee", "espresso", "latte"},
					"pkg-cleaning-carousel": {"kitchen", "clean", "dish"},
				},
			},
		},
	})
	defer catalogAgent.Close()

	// Identity agent with frequency caps and audience targeting
	identityAgent := newSimulatedIdentityAgent("identity-provider",
		map[string]int{
			"pkg-coffee-sponsored": 3,  // 3 per session
			"pkg-snacks-display":   5,  // 5 per session
			"pkg-alcohol-display":  2,  // 2 per session
			"pkg-pharma-native":    1,  // 1 per session
		},
		map[string][]string{
			"pkg-pharma-native": {"health_conscious"}, // Requires audience segment
		},
	)
	// Load audience data
	identityAgent.addToAudience("health_conscious", "tok-user-alice")
	identityAgent.addToAudience("coffee_lover", "tok-user-alice")
	// Bob is NOT in health_conscious
	identityAgent.addToAudience("coffee_lover", "tok-user-bob")

	idServer := httptest.NewServer(identityAgent)
	defer idServer.Close()

	router := httptest.NewServer(&mockRouter{
		contextAgents:  []*httptest.Server{contextualAgent, catalogAgent},
		identityAgents: []*httptest.Server{idServer},
		registryRIDs:   map[string]uint64{"pub-grocery-main": 2001, "pub-recipe-blog": 2002},
	})
	defer router.Close()

	// --- Scenario 1: Coffee category page ---
	t.Run("coffee_category_page", func(t *testing.T) {
		ctxResp := postJSON(t, router.URL+"/tmp/context", tmp.ContextMatchRequest{
			RequestID:   "ctx-sim-coffee-001",
			PropertyID:  "pub-grocery-main",
			PlacementID: "category-sponsored-products",
			Artifacts:   []string{"category:beverages-coffee", "search:cold-brew-espresso"},
			AvailablePkgs: []tmp.AvailablePackage{
				{PackageID: "pkg-coffee-sponsored", MediaBuyID: "mb-coffee-q1"},
				{PackageID: "pkg-snacks-display", MediaBuyID: "mb-snacks-q1"},
				{PackageID: "pkg-alcohol-display", MediaBuyID: "mb-alcohol-q1"},
				{PackageID: "pkg-pharma-native", MediaBuyID: "mb-pharma-q1"},
				{PackageID: "pkg-cleaning-carousel", MediaBuyID: "mb-cleaning-q1"},
			},
		})

		var cmResp tmp.ContextMatchResponse
		json.Unmarshal(ctxResp, &cmResp)

		t.Logf("Context Match returned %d offers:", len(cmResp.Offers))
		for _, o := range cmResp.Offers {
			t.Logf("  - %s: %s", o.PackageID, o.Summary)
		}

		// Coffee should match from contextual + catalog providers
		// Snacks should NOT match (no "snack" in artifacts)
		// Alcohol may match on "beverage" from contextual
		// Dedupe offers by package_id (multiple providers may activate the same package)
		hasOffer := make(map[string]bool)
		for _, o := range cmResp.Offers {
			hasOffer[o.PackageID] = true
		}
		if !hasOffer["pkg-coffee-sponsored"] {
			t.Error("expected pkg-coffee-sponsored to activate (coffee content)")
		}

		// Identity match for Alice
		idResp := postJSON(t, router.URL+"/tmp/identity", tmp.IdentityMatchRequest{
			RequestID: "id-sim-coffee-001",
			UserToken: "tok-user-alice",
			UIDType:   tmp.UIDTypeUID2,
			PackageIDs: []string{
				"pkg-coffee-sponsored", "pkg-snacks-display", "pkg-alcohol-display",
				"pkg-pharma-native", "pkg-cleaning-carousel",
				"pkg-other-1", "pkg-other-2", // ALL active packages
			},
		})

		var imResp tmp.IdentityMatchResponse
		json.Unmarshal(idResp, &imResp)

		t.Logf("Identity Match for Alice:")
		for _, e := range imResp.Eligibility {
			t.Logf("  - %s: eligible=%v intent=%.2f", e.PackageID, e.Eligible, safeIntent(e.IntentScore))
		}

		// Alice is in health_conscious, so pharma should be eligible
		eligMap := make(map[string]bool)
		for _, e := range imResp.Eligibility {
			eligMap[e.PackageID] = e.Eligible
		}
		if !eligMap["pkg-pharma-native"] {
			t.Error("Alice should be eligible for pharma (she's in health_conscious)")
		}

		// Publisher joins: intersect deduped context offers with identity eligibility
		var activated []string
		seen := make(map[string]bool)
		for _, o := range cmResp.Offers {
			if !seen[o.PackageID] && eligMap[o.PackageID] {
				activated = append(activated, o.PackageID)
				seen[o.PackageID] = true
			}
		}
		t.Logf("Publisher activated: %v", activated)
	})

	// --- Scenario 2: Same page, different user (Bob, not in health segment) ---
	t.Run("same_page_different_user", func(t *testing.T) {
		idResp := postJSON(t, router.URL+"/tmp/identity", tmp.IdentityMatchRequest{
			RequestID: "id-sim-coffee-002",
			UserToken: "tok-user-bob",
			PackageIDs: []string{
				"pkg-coffee-sponsored", "pkg-snacks-display", "pkg-alcohol-display",
				"pkg-pharma-native", "pkg-cleaning-carousel",
			},
		})

		var imResp tmp.IdentityMatchResponse
		json.Unmarshal(idResp, &imResp)

		for _, e := range imResp.Eligibility {
			if e.PackageID == "pkg-pharma-native" && e.Eligible {
				t.Error("Bob should NOT be eligible for pharma (not in health_conscious)")
			}
		}
		t.Log("Bob correctly excluded from pharma package (not in audience)")
	})

	// --- Scenario 3: Children's content — brand safety blocks alcohol ---
	t.Run("children_content_brand_safety", func(t *testing.T) {
		ctxResp := postJSON(t, router.URL+"/tmp/context", tmp.ContextMatchRequest{
			RequestID:   "ctx-sim-children-001",
			PropertyID:  "pub-grocery-main",
			PlacementID: "category-sponsored",
			Artifacts:   []string{"article:healthy-snacks-for-children-school-lunches"},
			AvailablePkgs: []tmp.AvailablePackage{
				{PackageID: "pkg-snacks-display", MediaBuyID: "mb-snacks-q1"},
				{PackageID: "pkg-alcohol-display", MediaBuyID: "mb-alcohol-q1"},
			},
		})

		var cmResp tmp.ContextMatchResponse
		json.Unmarshal(ctxResp, &cmResp)

		t.Logf("Children content - %d offers:", len(cmResp.Offers))
		for _, o := range cmResp.Offers {
			t.Logf("  - %s: %s", o.PackageID, o.Summary)
		}

		for _, o := range cmResp.Offers {
			if o.PackageID == "pkg-alcohol-display" {
				t.Error("brand safety should have blocked alcohol on children's content")
			}
		}
	})

	// --- Scenario 4: Frequency capping after multiple exposures ---
	t.Run("frequency_capping_progression", func(t *testing.T) {
		token := "tok-user-charlie"

		for i := 0; i < 3; i++ {
			postJSON(t, idServer.URL+"/tmp/expose", tmp.ExposeRequest{
				UserToken: token,
				PackageID: "pkg-coffee-sponsored",
			})
		}

		idResp := postJSON(t, router.URL+"/tmp/identity", tmp.IdentityMatchRequest{
			RequestID:  "id-sim-freq-001",
			UserToken:  token,
			PackageIDs: []string{"pkg-coffee-sponsored", "pkg-snacks-display"},
		})

		var imResp tmp.IdentityMatchResponse
		json.Unmarshal(idResp, &imResp)

		for _, e := range imResp.Eligibility {
			switch e.PackageID {
			case "pkg-coffee-sponsored":
				if e.Eligible {
					t.Error("coffee should be capped after 3 exposures (cap=3)")
				}
				t.Logf("Coffee capped: eligible=%v intent=%.2f", e.Eligible, safeIntent(e.IntentScore))
			case "pkg-snacks-display":
				if !e.Eligible {
					t.Error("snacks should still be eligible (no exposures)")
				}
			}
		}
	})

	// --- Scenario 5: Property targeting --- different property, fewer packages match ---
	t.Run("property_targeting", func(t *testing.T) {
		ctxResp := postJSON(t, router.URL+"/tmp/context", tmp.ContextMatchRequest{
			RequestID:   "ctx-sim-blog-001",
			PropertyID:  "pub-recipe-blog",
			PlacementID: "sidebar",
			Artifacts:   []string{"article:best-coffee-beans-2026"},
			AvailablePkgs: []tmp.AvailablePackage{
				{PackageID: "pkg-coffee-sponsored", MediaBuyID: "mb-coffee-q1"},
				{PackageID: "pkg-alcohol-display", MediaBuyID: "mb-alcohol-q1"},
			},
		})

		var cmResp tmp.ContextMatchResponse
		json.Unmarshal(ctxResp, &cmResp)

		t.Logf("Recipe blog - %d offers:", len(cmResp.Offers))
		for _, o := range cmResp.Offers {
			t.Logf("  - %s: %s", o.PackageID, o.Summary)
		}

		// Coffee is targeted to pub-recipe-blog, alcohol is NOT
		hasAlcohol := false
		for _, o := range cmResp.Offers {
			if o.PackageID == "pkg-alcohol-display" {
				hasAlcohol = true
			}
		}
		if hasAlcohol {
			t.Error("alcohol should not activate on pub-recipe-blog (property targeting)")
		}
	})
}

func TestSimulation_FullLifecycle_WithTiming(t *testing.T) {
	// Simulate a realistic lifecycle:
	// 1. User arrives on page
	// 2. Publisher fires context + identity in parallel
	// 3. Publisher joins, activates winning package
	// 4. Publisher reports exposure
	// 5. User navigates to next page
	// 6. Same flow, but frequency cap state has changed

	ctxAgent := httptest.NewServer(&simulatedContextAgent{
		name: "contextual-agent",
		modules: []interface {
			Evaluate(*tmp.ContextMatchRequest, tmp.AvailablePackage) (bool, float32)
		}{
			&TopicRelevanceModule{
				topicKeywords: map[string][]string{
					"pkg-food": {"recipe", "cooking", "food"},
					"pkg-tech": {"gadget", "review", "tech"},
				},
			},
		},
	})
	defer ctxAgent.Close()

	idAgent := newSimulatedIdentityAgent("identity-agent",
		map[string]int{"pkg-food": 2, "pkg-tech": 3},
		nil,
	)
	idServer := httptest.NewServer(idAgent)
	defer idServer.Close()

	router := httptest.NewServer(&mockRouter{
		contextAgents:  []*httptest.Server{ctxAgent},
		identityAgents: []*httptest.Server{idServer},
	})
	defer router.Close()

	token := "tok-lifecycle-user"
	allPackages := []string{"pkg-food", "pkg-tech", "pkg-auto", "pkg-travel"}

	pages := []struct {
		name      string
		artifacts []string
		packages  []tmp.AvailablePackage
	}{
		{
			name:      "recipe page",
			artifacts: []string{"article:pasta-recipe-carbonara"},
			packages: []tmp.AvailablePackage{
				{PackageID: "pkg-food", MediaBuyID: "mb-1"},
				{PackageID: "pkg-tech", MediaBuyID: "mb-2"},
			},
		},
		{
			name:      "gadget review",
			artifacts: []string{"article:best-kitchen-gadgets-review"},
			packages: []tmp.AvailablePackage{
				{PackageID: "pkg-food", MediaBuyID: "mb-1"},
				{PackageID: "pkg-tech", MediaBuyID: "mb-2"},
			},
		},
		{
			name:      "another recipe",
			artifacts: []string{"article:cooking-with-cast-iron"},
			packages: []tmp.AvailablePackage{
				{PackageID: "pkg-food", MediaBuyID: "mb-1"},
				{PackageID: "pkg-tech", MediaBuyID: "mb-2"},
			},
		},
	}

	for i, page := range pages {
		t.Logf("\n=== Page %d: %s ===", i+1, page.name)

		// Parallel context + identity
		start := time.Now()
		var ctxData, idData []byte
		var wg sync.WaitGroup
		wg.Add(2)
		go func() {
			defer wg.Done()
			ctxData = postJSON(t, router.URL+"/tmp/context", tmp.ContextMatchRequest{
				RequestID:     fmt.Sprintf("ctx-life-%d", i),
				PropertyID:    "pub-foodie",
				PlacementID:   "main-content",
				Artifacts:     page.artifacts,
				AvailablePkgs: page.packages,
			})
		}()
		go func() {
			defer wg.Done()
			idData = postJSON(t, router.URL+"/tmp/identity", tmp.IdentityMatchRequest{
				RequestID:  fmt.Sprintf("id-life-%d", i),
				UserToken:  token,
				PackageIDs: allPackages,
			})
		}()
		wg.Wait()
		elapsed := time.Since(start)

		var cmResp tmp.ContextMatchResponse
		var imResp tmp.IdentityMatchResponse
		json.Unmarshal(ctxData, &cmResp)
		json.Unmarshal(idData, &imResp)

		// Join
		contextOffers := make(map[string]tmp.Offer)
		for _, o := range cmResp.Offers {
			contextOffers[o.PackageID] = o
		}
		eligMap := make(map[string]tmp.PackageEligibility)
		for _, e := range imResp.Eligibility {
			eligMap[e.PackageID] = e
		}

		var activated []string
		var bestPkg string
		var bestIntent float64 = -1
		for pkgID, offer := range contextOffers {
			e, ok := eligMap[pkgID]
			if ok && e.Eligible {
				activated = append(activated, pkgID)
				intent := safeIntent(e.IntentScore)
				t.Logf("  Eligible: %s (intent=%.2f) - %s", pkgID, intent, offer.Summary)
				if intent > bestIntent {
					bestIntent = intent
					bestPkg = pkgID
				}
			} else if ok && !e.Eligible {
				t.Logf("  Capped:   %s (intent=%.2f)", pkgID, safeIntent(e.IntentScore))
			}
		}

		t.Logf("  Activated: %v (chose: %s) in %v", activated, bestPkg, elapsed)

		// Report exposure for the best package
		if bestPkg != "" {
			postJSON(t, idServer.URL+"/tmp/expose", tmp.ExposeRequest{
				UserToken: token,
				PackageID: bestPkg,
			})
			t.Logf("  Exposed: %s", bestPkg)
		}
	}
}

func safeIntent(score *float64) float64 {
	if score == nil {
		return 0
	}
	return *score
}
