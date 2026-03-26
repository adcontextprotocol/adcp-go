package e2e

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/adcontextprotocol/adcp-go/tmp"
)

// ChatTurn simulates a user message and the AI assistant's response.
type ChatTurn struct {
	UserMessage string
	Topics      []string // Classified topics for this turn
	ArtifactID  string   // Registered artifact ID
}

// SponsoredContent is what gets rendered in the chat.
type SponsoredContent struct {
	Brand       string
	Headline    string
	Body        string
	ImageURL    string
	Disclosure  string // "Sponsored" / "Ad" / "Promoted"
	PackageID   string
	IntentScore float64
}

// chatContextAgent is a context match provider for AI assistant content.
// It evaluates conversation topics against package targeting.
type chatContextAgent struct {
	// package -> targeting topics
	packageTopics map[string][]string
	// package -> brand info for offers
	packageBrands map[string]struct {
		name   string
		domain string
	}
	// package -> creative manifest templates
	packageCreatives map[string]map[string]interface{}
}

func (a *chatContextAgent) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	var req tmp.ContextMatchRequest
	json.NewDecoder(r.Body).Decode(&req)

	var offers []tmp.Offer
	for _, pkg := range req.AvailablePkgs {
		topics := a.packageTopics[pkg.PackageID]
		if len(topics) == 0 {
			continue
		}

		// Check if any artifact topics match package topics
		matched := false
		var score float32
		for _, art := range req.Artifacts {
			artLower := strings.ToLower(art)
			for _, topic := range topics {
				if strings.Contains(artLower, topic) {
					matched = true
					score += 0.3
				}
			}
		}
		if score > 1.0 {
			score = 1.0
		}

		if matched {
			offer := tmp.Offer{
				PackageID: pkg.PackageID,
			}

			// Add brand if available
			if brand, ok := a.packageBrands[pkg.PackageID]; ok {
				offer.Brand = &tmp.BrandRef{
					Name:             brand.name,
					AdvertiserDomain: brand.domain,
				}
			}

			// Add summary for relevance judgment
			offer.Summary = fmt.Sprintf("Contextual match (score: %.1f) for %s", score, pkg.PackageID)

			// Add creative manifest if available
			if creative, ok := a.packageCreatives[pkg.PackageID]; ok {
				offer.CreativeManifest = creative
			}

			offers = append(offers, offer)
		}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(tmp.ContextMatchResponse{
		RequestID: req.RequestID,
		Offers:    offers,
	})
}

// chatIdentityAgent handles frequency and audience for chat users.
type chatIdentityAgent struct {
	mu         sync.Mutex
	freqCounts map[string]map[string]int // token -> package -> count
	freqCaps   map[string]int            // package -> max per session
}

func (a *chatIdentityAgent) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch r.URL.Path {
	case "/tmp/identity":
		a.handleIdentity(w, r)
	case "/tmp/expose":
		a.handleExpose(w, r)
	default:
		http.NotFound(w, r)
	}
}

func (a *chatIdentityAgent) handleIdentity(w http.ResponseWriter, r *http.Request) {
	var req tmp.IdentityMatchRequest
	json.NewDecoder(r.Body).Decode(&req)

	a.mu.Lock()
	defer a.mu.Unlock()

	var eligibility []tmp.PackageEligibility
	for _, pkgID := range req.PackageIDs {
		eligible := true
		intent := 0.5

		if cap, ok := a.freqCaps[pkgID]; ok {
			count := 0
			if counts, ok := a.freqCounts[req.UserToken]; ok {
				count = counts[pkgID]
			}
			if count >= cap {
				eligible = false
			}
			// Returning users get higher intent
			if count > 0 && count < cap {
				intent = 0.8
			}
		}

		score := intent
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

func (a *chatIdentityAgent) handleExpose(w http.ResponseWriter, r *http.Request) {
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

func TestSimulation_AIAssistantChat(t *testing.T) {
	// Set up a food/cooking brand context agent
	ctxAgent := httptest.NewServer(&chatContextAgent{
		packageTopics: map[string][]string{
			"pkg-olive-oil":    {"cooking", "recipe", "pasta", "italian", "mediterranean"},
			"pkg-knife-set":    {"cooking", "kitchen", "chef", "prep"},
			"pkg-meal-kit":     {"recipe", "dinner", "cooking", "easy"},
			"pkg-running-shoe": {"fitness", "running", "exercise", "marathon"},
			"pkg-protein":      {"fitness", "workout", "nutrition", "protein"},
		},
		packageBrands: map[string]struct {
			name   string
			domain string
		}{
			"pkg-olive-oil":    {name: "Meridian Foods", domain: "meridianfoods.example.com"},
			"pkg-knife-set":    {name: "EdgeCraft", domain: "edgecraft.example.com"},
			"pkg-meal-kit":     {name: "FreshBox", domain: "freshbox.example.com"},
			"pkg-running-shoe": {name: "StrideMax", domain: "stridemax.example.com"},
			"pkg-protein":      {name: "CoreFuel", domain: "corefuel.example.com"},
		},
		packageCreatives: map[string]map[string]interface{}{
			"pkg-olive-oil": {
				"format_id": "sponsored_chat_card",
				"assets": map[string]interface{}{
					"headline":    "Meridian Extra Virgin Olive Oil",
					"body":        "Cold-pressed from single-origin olives. The secret to authentic carbonara.",
					"image_url":   "https://cdn.meridianfoods.example.com/evoo-bottle.jpg",
					"cta_text":    "Shop now",
					"cta_url":     "https://meridianfoods.example.com/evoo",
					"disclosure":  "Sponsored",
				},
			},
			"pkg-knife-set": {
				"format_id": "sponsored_chat_card",
				"assets": map[string]interface{}{
					"headline":  "EdgeCraft Pro Chef Knife Set",
					"body":      "Japanese steel, lifetime warranty. Makes prep work effortless.",
					"image_url": "https://cdn.edgecraft.example.com/pro-set.jpg",
					"cta_text":  "See collection",
					"cta_url":   "https://edgecraft.example.com/pro",
					"disclosure": "Sponsored",
				},
			},
			"pkg-meal-kit": {
				"format_id": "sponsored_chat_card",
				"assets": map[string]interface{}{
					"headline":  "FreshBox Pasta Night Kit",
					"body":      "Everything you need for restaurant-quality pasta at home. Delivered fresh.",
					"image_url": "https://cdn.freshbox.example.com/pasta-kit.jpg",
					"cta_text":  "Try it",
					"cta_url":   "https://freshbox.example.com/pasta",
					"disclosure": "Sponsored",
				},
			},
			"pkg-running-shoe": {
				"format_id": "sponsored_chat_card",
				"assets": map[string]interface{}{
					"headline":  "StrideMax Ultra 5",
					"body":      "Engineered for distance. 30% lighter than last generation.",
					"image_url": "https://cdn.stridemax.example.com/ultra5.jpg",
					"cta_text":  "Shop now",
					"cta_url":   "https://stridemax.example.com/ultra5",
					"disclosure": "Sponsored",
				},
			},
		},
	})
	defer ctxAgent.Close()

	idAgent := &chatIdentityAgent{
		freqCounts: make(map[string]map[string]int),
		freqCaps: map[string]int{
			"pkg-olive-oil":    2, // Max 2 per session
			"pkg-knife-set":    1, // Max 1 per session
			"pkg-meal-kit":     3,
			"pkg-running-shoe": 2,
			"pkg-protein":      2,
		},
	}
	idServer := httptest.NewServer(idAgent)
	defer idServer.Close()

	router := httptest.NewServer(&mockRouter{
		contextAgents:  []*httptest.Server{ctxAgent},
		identityAgents: []*httptest.Server{idServer},
	})
	defer router.Close()

	// Simulate a conversation
	userToken := "tok-chat-user-alice"
	allPackages := []string{
		"pkg-olive-oil", "pkg-knife-set", "pkg-meal-kit",
		"pkg-running-shoe", "pkg-protein",
	}

	conversation := []ChatTurn{
		{
			UserMessage: "What's a good recipe for pasta carbonara?",
			Topics:      []string{"cooking", "pasta", "italian", "recipe"},
			ArtifactID:  "turn:cooking-pasta-carbonara",
		},
		{
			UserMessage: "What kind of olive oil should I use?",
			Topics:      []string{"cooking", "olive-oil", "ingredient"},
			ArtifactID:  "turn:cooking-olive-oil-selection",
		},
		{
			UserMessage: "Any tips for getting the egg mixture right?",
			Topics:      []string{"cooking", "technique", "pasta"},
			ArtifactID:  "turn:cooking-egg-technique",
		},
		{
			UserMessage: "I also want to start running. What shoes do you recommend?",
			Topics:      []string{"fitness", "running", "shoes"},
			ArtifactID:  "turn:fitness-running-shoes",
		},
		{
			UserMessage: "Back to cooking - what knife do I need for prep?",
			Topics:      []string{"cooking", "kitchen", "knife", "prep"},
			ArtifactID:  "turn:cooking-knife-selection",
		},
	}

	t.Log("=== AI Assistant Chat Simulation ===")
	t.Log("")

	for i, turn := range conversation {
		t.Logf("--- Turn %d ---", i+1)
		t.Logf("User: %s", turn.UserMessage)

		// 1. Context Match
		ctxResp := postJSON(t, router.URL+"/tmp/context", tmp.ContextMatchRequest{
			RequestID:   fmt.Sprintf("ctx-chat-%d", i),
			PropertyID:  "pub-addie-assistant",
			PropertyType: tmp.PropertyTypeAIAssistant,
			PlacementID: "conversation-inline",
			Artifacts:   []string{turn.ArtifactID},
			AvailablePkgs: []tmp.AvailablePackage{
				{PackageID: "pkg-olive-oil", MediaBuyID: "mb-meridian-q1"},
				{PackageID: "pkg-knife-set", MediaBuyID: "mb-edgecraft-q1"},
				{PackageID: "pkg-meal-kit", MediaBuyID: "mb-freshbox-q1"},
				{PackageID: "pkg-running-shoe", MediaBuyID: "mb-stridemax-q1"},
				{PackageID: "pkg-protein", MediaBuyID: "mb-corefuel-q1"},
			},
		})

		var cmResp tmp.ContextMatchResponse
		json.Unmarshal(ctxResp, &cmResp)

		// 2. Identity Match (in parallel in production, sequential here for clarity)
		idResp := postJSON(t, router.URL+"/tmp/identity", tmp.IdentityMatchRequest{
			RequestID:  fmt.Sprintf("id-chat-%d", i),
			UserToken:  userToken,
			UIDType:    tmp.UIDTypePublisherFirstParty,
			PackageIDs: allPackages,
		})

		var imResp tmp.IdentityMatchResponse
		json.Unmarshal(idResp, &imResp)

		// 3. Publisher join
		eligMap := make(map[string]tmp.PackageEligibility)
		for _, e := range imResp.Eligibility {
			eligMap[e.PackageID] = e
		}

		var bestOffer *tmp.Offer
		var bestIntent float64 = -1
		seen := make(map[string]bool)
		for _, offer := range cmResp.Offers {
			if seen[offer.PackageID] {
				continue
			}
			seen[offer.PackageID] = true

			e, ok := eligMap[offer.PackageID]
			if !ok || !e.Eligible {
				t.Logf("  [skip] %s — not eligible", offer.PackageID)
				continue
			}
			intent := safeIntent(e.IntentScore)
			t.Logf("  [candidate] %s — eligible, intent=%.2f", offer.PackageID, intent)
			if intent > bestIntent {
				bestIntent = intent
				offerCopy := offer
				bestOffer = &offerCopy
			}
		}

		// 4. Render sponsored content (or not)
		if bestOffer != nil {
			t.Logf("")
			t.Logf("  Addie responds with recipe advice...")
			t.Logf("")

			// Extract creative details
			if bestOffer.CreativeManifest != nil {
				if assets, ok := bestOffer.CreativeManifest.(map[string]interface{}); ok {
					if assetsMap, ok := assets["assets"].(map[string]interface{}); ok {
						disclosure := "Sponsored"
						if d, ok := assetsMap["disclosure"].(string); ok {
							disclosure = d
						}
						t.Logf("  ┌─ [%s] ──────────────────────────────────────┐", disclosure)
						if h, ok := assetsMap["headline"].(string); ok {
							t.Logf("  │ %s", h)
						}
						if b, ok := assetsMap["body"].(string); ok {
							t.Logf("  │ %s", b)
						}
						if cta, ok := assetsMap["cta_text"].(string); ok {
							t.Logf("  │ [%s]", cta)
						}
						t.Logf("  └────────────────────────────────────────────────┘")
					}
				}
			} else if bestOffer.Summary != "" {
				t.Logf("  [Sponsored] %s", bestOffer.Summary)
			}

			// 5. Report exposure
			postJSON(t, idServer.URL+"/tmp/expose", tmp.ExposeRequest{
				UserToken: userToken,
				PackageID: bestOffer.PackageID,
			})
			t.Logf("  → Exposed: %s (frequency count incremented)", bestOffer.PackageID)
		} else {
			t.Logf("  Addie responds (no sponsored content this turn)")
		}
		t.Logf("")
	}

	// Verify frequency capping worked
	t.Log("=== Final Frequency State ===")
	idAgent.mu.Lock()
	for pkg, count := range idAgent.freqCounts[userToken] {
		cap := idAgent.freqCaps[pkg]
		t.Logf("  %s: %d/%d impressions", pkg, count, cap)
	}
	idAgent.mu.Unlock()
}

func TestSimulation_ChatFrequencyCapping(t *testing.T) {
	// Verify that after hitting frequency cap, the same package stops appearing
	ctxAgent := httptest.NewServer(&chatContextAgent{
		packageTopics: map[string][]string{
			"pkg-coffee": {"coffee", "drink", "morning"},
		},
		packageBrands: map[string]struct {
			name   string
			domain string
		}{
			"pkg-coffee": {name: "BeanCo", domain: "beanco.example.com"},
		},
		packageCreatives: map[string]map[string]interface{}{
			"pkg-coffee": {
				"format_id": "sponsored_chat_card",
				"assets": map[string]interface{}{
					"headline":  "BeanCo Single Origin",
					"body":      "Ethically sourced, perfectly roasted.",
					"disclosure": "Sponsored",
				},
			},
		},
	})
	defer ctxAgent.Close()

	idAgent := &chatIdentityAgent{
		freqCounts: make(map[string]map[string]int),
		freqCaps:   map[string]int{"pkg-coffee": 2},
	}
	idServer := httptest.NewServer(idAgent)
	defer idServer.Close()

	router := httptest.NewServer(&mockRouter{
		contextAgents:  []*httptest.Server{ctxAgent},
		identityAgents: []*httptest.Server{idServer},
	})
	defer router.Close()

	token := "tok-freq-test"
	impressionCount := 0

	for turn := 0; turn < 5; turn++ {
		ctxResp := postJSON(t, router.URL+"/tmp/context", tmp.ContextMatchRequest{
			RequestID:   fmt.Sprintf("ctx-freq-%d", turn),
			PropertyID:  "pub-addie",
			PlacementID: "inline",
			Artifacts:   []string{"turn:coffee-discussion"},
			AvailablePkgs: []tmp.AvailablePackage{
				{PackageID: "pkg-coffee", MediaBuyID: "mb-1"},
			},
		})

		var cmResp tmp.ContextMatchResponse
		json.Unmarshal(ctxResp, &cmResp)

		idResp := postJSON(t, router.URL+"/tmp/identity", tmp.IdentityMatchRequest{
			RequestID:  fmt.Sprintf("id-freq-%d", turn),
			UserToken:  token,
			PackageIDs: []string{"pkg-coffee"},
		})

		var imResp tmp.IdentityMatchResponse
		json.Unmarshal(idResp, &imResp)

		// Check if we can show the ad
		showed := false
		for _, offer := range cmResp.Offers {
			for _, e := range imResp.Eligibility {
				if e.PackageID == offer.PackageID && e.Eligible {
					showed = true
					impressionCount++
					postJSON(t, idServer.URL+"/tmp/expose", tmp.ExposeRequest{
						UserToken: token,
						PackageID: offer.PackageID,
					})
				}
			}
		}

		t.Logf("Turn %d: showed=%v (total impressions: %d)", turn+1, showed, impressionCount)
	}

	if impressionCount != 2 {
		t.Errorf("expected exactly 2 impressions (cap=2), got %d", impressionCount)
	}
	t.Logf("Frequency cap correctly limited to %d impressions", impressionCount)
}
