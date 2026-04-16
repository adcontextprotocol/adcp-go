package e2e

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/adcontextprotocol/adcp-go/tmproto"
	"github.com/stretchr/testify/assert"
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
	packageCreatives map[string]map[string]any
}

func (a *chatContextAgent) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	var req tmproto.ContextMatchRequest
	json.NewDecoder(r.Body).Decode(&req)

	var offers []tmproto.Offer
	for _, pkgID := range req.PackageIDs {
		topics := a.packageTopics[pkgID]
		if len(topics) == 0 {
			continue
		}

		// Check if the placement ID or request context matches package topics.
		// In the real protocol, context signals come via artifact/artifact_refs/context_signals.
		// For this mock, we check if any topic keyword appears in the placement_id.
		matched := false
		var score float32
		checkStr := strings.ToLower(req.PlacementID)
		for _, topic := range topics {
			if strings.Contains(checkStr, topic) {
				matched = true
				score += 0.3
			}
		}
		if score > 1.0 {
			score = 1.0
		}

		if matched {
			offer := tmproto.Offer{
				PackageID: pkgID,
			}

			// Add brand if available
			if brand, ok := a.packageBrands[pkgID]; ok {
				brandJSON, _ := json.Marshal(map[string]string{
					"name":              brand.name,
					"advertiser_domain": brand.domain,
				})
				offer.Brand = json.RawMessage(brandJSON)
			}

			// Add summary for relevance judgment
			offer.Summary = fmt.Sprintf("Contextual match (score: %.1f) for %s", score, pkgID)

			// Add creative manifest if available
			if creative, ok := a.packageCreatives[pkgID]; ok {
				cm, _ := json.Marshal(creative)
				raw := json.RawMessage(cm)
				offer.CreativeManifest = &raw
			}

			offers = append(offers, offer)
		}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(tmproto.ContextMatchResponse{
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
	default:
		http.NotFound(w, r)
	}
}

func (a *chatIdentityAgent) handleIdentity(w http.ResponseWriter, r *http.Request) {
	var req tmproto.IdentityMatchRequest
	json.NewDecoder(r.Body).Decode(&req)

	a.mu.Lock()
	defer a.mu.Unlock()

	var eligible []string
	for _, pkgID := range req.PackageIDs {
		isEligible := true

		if cap, ok := a.freqCaps[pkgID]; ok {
			count := 0
			if counts, ok := a.freqCounts[req.UserToken]; ok {
				count = counts[pkgID]
			}
			if count >= cap {
				isEligible = false
			}
		}

		if isEligible {
			eligible = append(eligible, pkgID)
		}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(tmproto.IdentityMatchResponse{
		RequestID:          req.RequestID,
		EligiblePackageIDs: eligible,
		TTLSec:             60,
	})
}

func (a *chatIdentityAgent) handleExpose(w http.ResponseWriter, r *http.Request) {
	var req struct {
		UserToken string `json:"user_token"`
		PackageID string `json:"package_id"`
	}
	json.NewDecoder(r.Body).Decode(&req)

	a.recordExposure(req.UserToken, req.PackageID)

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"package_id": req.PackageID})
}

// recordExposure records an exposure directly without HTTP.
func (a *chatIdentityAgent) recordExposure(userToken, packageID string) {
	a.mu.Lock()
	defer a.mu.Unlock()

	if a.freqCounts[userToken] == nil {
		a.freqCounts[userToken] = make(map[string]int)
	}
	a.freqCounts[userToken][packageID]++
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
		packageCreatives: map[string]map[string]any{
			"pkg-olive-oil": {
				"format_id": "sponsored_chat_card",
				"assets": map[string]any{
					"headline":   "Meridian Extra Virgin Olive Oil",
					"body":       "Cold-pressed from single-origin olives. The secret to authentic carbonara.",
					"image_url":  "https://cdn.meridianfoods.example.com/evoo-bottle.jpg",
					"cta_text":   "Shop now",
					"cta_url":    "https://meridianfoods.example.com/evoo",
					"disclosure": "Sponsored",
				},
			},
			"pkg-knife-set": {
				"format_id": "sponsored_chat_card",
				"assets": map[string]any{
					"headline":   "EdgeCraft Pro Chef Knife Set",
					"body":       "Japanese steel, lifetime warranty. Makes prep work effortless.",
					"image_url":  "https://cdn.edgecraft.example.com/pro-set.jpg",
					"cta_text":   "See collection",
					"cta_url":    "https://edgecraft.example.com/pro",
					"disclosure": "Sponsored",
				},
			},
			"pkg-meal-kit": {
				"format_id": "sponsored_chat_card",
				"assets": map[string]any{
					"headline":   "FreshBox Pasta Night Kit",
					"body":       "Everything you need for restaurant-quality pasta at home. Delivered fresh.",
					"image_url":  "https://cdn.freshbox.example.com/pasta-kit.jpg",
					"cta_text":   "Try it",
					"cta_url":    "https://freshbox.example.com/pasta",
					"disclosure": "Sponsored",
				},
			},
			"pkg-running-shoe": {
				"format_id": "sponsored_chat_card",
				"assets": map[string]any{
					"headline":   "StrideMax Ultra 5",
					"body":       "Engineered for distance. 30% lighter than last generation.",
					"image_url":  "https://cdn.stridemax.example.com/ultra5.jpg",
					"cta_text":   "Shop now",
					"cta_url":    "https://stridemax.example.com/ultra5",
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
		ctxResp := postJSON(t, router.URL+"/tmp/context", tmproto.ContextMatchRequest{
			RequestID:    fmt.Sprintf("ctx-chat-%d", i),
			PropertyID:   "pub-addie-assistant",
			PropertyType: tmproto.PropertyTypeAIAssistant,
			PlacementID:  turn.ArtifactID,
			PackageIDs:   allPackages,
		})

		var cmResp tmproto.ContextMatchResponse
		json.Unmarshal(ctxResp, &cmResp)

		// 2. Identity Match (in parallel in production, sequential here for clarity)
		idResp := postJSON(t, router.URL+"/tmp/identity", tmproto.IdentityMatchRequest{
			RequestID:  fmt.Sprintf("id-chat-%d", i),
			UserToken:  userToken,
			UIDType:    tmproto.UIDTypePublisherFirstParty,
			PackageIDs: allPackages,
		})

		var imResp tmproto.IdentityMatchResponse
		json.Unmarshal(idResp, &imResp)

		// 3. Publisher join
		eligSet := make(map[string]bool)
		for _, id := range imResp.EligiblePackageIDs {
			eligSet[id] = true
		}

		var bestOffer *tmproto.Offer
		seen := make(map[string]bool)
		for _, offer := range cmResp.Offers {
			if seen[offer.PackageID] {
				continue
			}
			seen[offer.PackageID] = true

			if !eligSet[offer.PackageID] {
				t.Logf("  [skip] %s — not eligible", offer.PackageID)
				continue
			}
			t.Logf("  [candidate] %s — eligible", offer.PackageID)
			if bestOffer == nil {
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
			if bestOffer.CreativeManifest != nil && len(*bestOffer.CreativeManifest) > 0 {
				var assets map[string]any
				if json.Unmarshal(*bestOffer.CreativeManifest, &assets) == nil {
					if assetsMap, ok := assets["assets"].(map[string]any); ok {
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
			idAgent.recordExposure(userToken, bestOffer.PackageID)
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
		packageCreatives: map[string]map[string]any{
			"pkg-coffee": {
				"format_id": "sponsored_chat_card",
				"assets": map[string]any{
					"headline":   "BeanCo Single Origin",
					"body":       "Ethically sourced, perfectly roasted.",
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

	for turn := range 5 {
		ctxResp := postJSON(t, router.URL+"/tmp/context", tmproto.ContextMatchRequest{
			RequestID:   fmt.Sprintf("ctx-freq-%d", turn),
			PropertyID:  "pub-addie",
			PlacementID: "coffee-morning-drink",
			PackageIDs:  []string{"pkg-coffee"},
		})

		var cmResp tmproto.ContextMatchResponse
		json.Unmarshal(ctxResp, &cmResp)

		idResp := postJSON(t, router.URL+"/tmp/identity", tmproto.IdentityMatchRequest{
			RequestID:  fmt.Sprintf("id-freq-%d", turn),
			UserToken:  token,
			PackageIDs: []string{"pkg-coffee"},
		})

		var imResp tmproto.IdentityMatchResponse
		json.Unmarshal(idResp, &imResp)

		// Check if we can show the ad
		eligSet := make(map[string]bool)
		for _, id := range imResp.EligiblePackageIDs {
			eligSet[id] = true
		}
		showed := false
		for _, offer := range cmResp.Offers {
			if eligSet[offer.PackageID] {
				showed = true
				impressionCount++
				idAgent.recordExposure(token, offer.PackageID)
			}
		}

		t.Logf("Turn %d: showed=%v (total impressions: %d)", turn+1, showed, impressionCount)
	}

	assert.Equal(t, 2, impressionCount, "expected exactly 2 impressions (cap=2)")
	t.Logf("Frequency cap correctly limited to %d impressions", impressionCount)
}
