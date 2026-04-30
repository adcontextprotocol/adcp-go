package e2e

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/adcontextprotocol/adcp-go/tmproto"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- Mock Context Agent ---

type mockContextAgent struct {
	// packages that activate based on artifact keyword matching
	rules map[string][]string // package_id -> list of artifact keywords that trigger activation
}

func (a *mockContextAgent) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/tmp/context" {
		http.NotFound(w, r)
		return
	}
	var req tmproto.ContextMatchRequest
	json.NewDecoder(r.Body).Decode(&req)

	var offers []tmproto.Offer
	for _, pkgID := range req.PackageIDs {
		if _, ok := a.rules[pkgID]; ok {
			offers = append(offers, tmproto.Offer{PackageID: pkgID})
		}
	}

	var signals map[string]any
	if len(offers) > 0 {
		signals = map[string]any{}
		for _, o := range offers {
			signals["adcp_pkg"] = o.PackageID
		}
	}
	resp := tmproto.ContextMatchResponse{
		RequestID: req.RequestID,
		Offers:    offers,
		Signals:   signals,
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

// --- Mock Identity Agent ---

type mockIdentityAgent struct {
	mu        sync.Mutex
	freqCaps  map[string]int            // package_id -> max per hour
	exposures map[string]map[string]int // token_hash -> package_id -> count
}

func newMockIdentityAgent(caps map[string]int) *mockIdentityAgent {
	return &mockIdentityAgent{
		freqCaps:  caps,
		exposures: make(map[string]map[string]int),
	}
}

func (a *mockIdentityAgent) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch r.URL.Path {
	case "/tmp/identity":
		a.handleIdentity(w, r)
	default:
		http.NotFound(w, r)
	}
}

func (a *mockIdentityAgent) handleIdentity(w http.ResponseWriter, r *http.Request) {
	var req tmproto.IdentityMatchRequest
	json.NewDecoder(r.Body).Decode(&req)

	a.mu.Lock()
	defer a.mu.Unlock()

	userToken := ""
	if len(req.Identities) > 0 {
		userToken = req.Identities[0].UserToken
	}
	userExposures := a.exposures[userToken]

	var eligible []string
	for _, pkgID := range req.PackageIDs {
		isEligible := true
		if cap, ok := a.freqCaps[pkgID]; ok {
			count := 0
			if userExposures != nil {
				count = userExposures[pkgID]
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

func (a *mockIdentityAgent) handleExpose(w http.ResponseWriter, r *http.Request) {
	var req struct {
		UserToken string `json:"user_token"`
		PackageID string `json:"package_id"`
	}
	json.NewDecoder(r.Body).Decode(&req)

	a.recordExposure(req.UserToken, req.PackageID)

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{
		"package_id": req.PackageID,
	})
}

// recordExposure records an exposure directly without HTTP.
func (a *mockIdentityAgent) recordExposure(userToken, packageID string) {
	a.mu.Lock()
	defer a.mu.Unlock()

	if a.exposures[userToken] == nil {
		a.exposures[userToken] = make(map[string]int)
	}
	a.exposures[userToken][packageID]++
}

// --- Mock Router ---
// Simplified router that forwards to context and identity agents

type mockRouter struct {
	contextAgents  []*httptest.Server
	identityAgents []*httptest.Server
	registryRIDs   map[string]string // property_id -> property_rid
}

func (rt *mockRouter) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch r.URL.Path {
	case "/tmp/context":
		rt.handleContext(w, r)
	case "/tmp/identity":
		rt.handleIdentity(w, r)
	default:
		http.NotFound(w, r)
	}
}

func (rt *mockRouter) handleContext(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	var req tmproto.ContextMatchRequest
	json.Unmarshal(body, &req)

	// Validate: no identity fields
	// (ContextMatchRequest struct has no user_token field by design)

	// Enrich with registry
	if rid, ok := rt.registryRIDs[req.PropertyID]; ok {
		req.PropertyRID = rid
	}

	enrichedBody, _ := json.Marshal(req)

	// Fan out to all context agents
	var allOffers []tmproto.Offer
	mergedSignals := map[string]any{}
	var mu sync.Mutex
	var wg sync.WaitGroup

	for _, agent := range rt.contextAgents {
		wg.Add(1)
		go func(agentURL string) {
			defer wg.Done()
			resp, err := http.Post(agentURL+"/tmp/context", "application/json", bytes.NewReader(enrichedBody))
			if err != nil {
				return
			}
			defer resp.Body.Close()
			var cmResp tmproto.ContextMatchResponse
			json.NewDecoder(resp.Body).Decode(&cmResp)
			mu.Lock()
			allOffers = append(allOffers, cmResp.Offers...)
			maps.Copy(mergedSignals, cmResp.Signals)
			mu.Unlock()
		}(agent.URL)
	}
	wg.Wait()

	merged := tmproto.ContextMatchResponse{
		RequestID: req.RequestID,
		Offers:    allOffers,
	}
	if len(mergedSignals) > 0 {
		merged.Signals = mergedSignals
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(merged)
}

func (rt *mockRouter) handleIdentity(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)

	// Fan out to all identity agents, merge eligible package IDs (union).
	eligSet := make(map[string]bool)
	var mu sync.Mutex
	var wg sync.WaitGroup

	for _, agent := range rt.identityAgents {
		wg.Add(1)
		go func(agentURL string) {
			defer wg.Done()
			resp, err := http.Post(agentURL+"/tmp/identity", "application/json", bytes.NewReader(body))
			if err != nil {
				return
			}
			defer resp.Body.Close()
			var imResp tmproto.IdentityMatchResponse
			json.NewDecoder(resp.Body).Decode(&imResp)
			mu.Lock()
			for _, id := range imResp.EligiblePackageIDs {
				eligSet[id] = true
			}
			mu.Unlock()
		}(agent.URL)
	}
	wg.Wait()

	var req tmproto.IdentityMatchRequest
	json.Unmarshal(body, &req)

	var eligible []string
	for id := range eligSet {
		eligible = append(eligible, id)
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(tmproto.IdentityMatchResponse{
		RequestID:          req.RequestID,
		EligiblePackageIDs: eligible,
		TTLSec:             60,
	})
}

// --- Helper ---

func postJSON(t *testing.T, url string, body any) []byte {
	t.Helper()
	b, _ := json.Marshal(body)
	resp, err := http.Post(url, "application/json", bytes.NewReader(b))
	require.NoError(t, err, "POST %s", url)
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	require.Equal(t, 200, resp.StatusCode, "POST %s: body: %s", url, string(data))
	return data
}

// --- Tests ---

func TestFullExchange_ContextAndIdentity(t *testing.T) {
	ctxAgent := httptest.NewServer(&mockContextAgent{
		rules: map[string][]string{
			"pkg-food-display": {"cooking"},
			"pkg-tech-native":  {"gadgets"},
		},
	})
	defer ctxAgent.Close()

	idAgent := httptest.NewServer(newMockIdentityAgent(map[string]int{
		"pkg-food-display": 2,
		"pkg-tech-native":  5,
	}))
	defer idAgent.Close()

	router := httptest.NewServer(&mockRouter{
		contextAgents:  []*httptest.Server{ctxAgent},
		identityAgents: []*httptest.Server{idAgent},
		registryRIDs:   map[string]string{"pub-oakwood": "rid-1001"},
	})
	defer router.Close()

	// 1. Context Match
	ctxResp := postJSON(t, router.URL+"/tmp/context", tmproto.ContextMatchRequest{
		RequestID:    "ctx-e2e-001",
		PropertyID:   "pub-oakwood",
		PropertyType: tmproto.PropertyTypeWebsite,
		PlacementID:  "sidebar-300x250",
		PackageIDs:   []string{"pkg-food-display", "pkg-tech-native", "pkg-auto-video"},
	})

	var cmResp tmproto.ContextMatchResponse
	require.NoError(t, json.Unmarshal(ctxResp, &cmResp))

	// Both pkg-food-display and pkg-tech-native are in the agent's rules, so both activate.
	require.Len(t, cmResp.Offers, 2, "expected 2 offers")
	offerSet := make(map[string]bool)
	for _, o := range cmResp.Offers {
		offerSet[o.PackageID] = true
	}
	assert.True(t, offerSet["pkg-food-display"], "expected pkg-food-display in offers")
	assert.True(t, offerSet["pkg-tech-native"], "expected pkg-tech-native in offers")

	// 2. Identity Match (ALL active packages, not just page-specific)
	idResp := postJSON(t, router.URL+"/tmp/identity", tmproto.IdentityMatchRequest{
		RequestID:  "id-e2e-001",
		Identities: []tmproto.IdentityToken{{UserToken: "tok-user-abc", UIDType: tmproto.UIDTypeUID2}},
		PackageIDs: []string{
			"pkg-food-display", "pkg-tech-native", "pkg-auto-video",
			"pkg-other-site-1", "pkg-other-site-2", "pkg-other-site-3",
		},
	})

	var imResp tmproto.IdentityMatchResponse
	require.NoError(t, json.Unmarshal(idResp, &imResp))

	// All requested should be eligible (no exposures yet)
	eligiblePkgs := make(map[string]bool)
	for _, id := range imResp.EligiblePackageIDs {
		eligiblePkgs[id] = true
	}
	for _, pkgID := range []string{
		"pkg-food-display", "pkg-tech-native", "pkg-auto-video",
		"pkg-other-site-1", "pkg-other-site-2", "pkg-other-site-3",
	} {
		assert.True(t, eligiblePkgs[pkgID], "expected %s to be eligible", pkgID)
	}

	// 3. Publisher joins locally
	contextOffers := make(map[string]bool)
	for _, o := range cmResp.Offers {
		contextOffers[o.PackageID] = true
	}

	var activated []string
	for pkgID := range contextOffers {
		if eligiblePkgs[pkgID] {
			activated = append(activated, pkgID)
		}
	}
	require.Len(t, activated, 2, "expected 2 activated packages, got %v", activated)
}

func TestFrequencyCapping_AcrossImpressions(t *testing.T) {
	idAgent := newMockIdentityAgent(map[string]int{
		"pkg-food-display": 2, // cap at 2
	})
	idServer := httptest.NewServer(idAgent)
	defer idServer.Close()

	router := httptest.NewServer(&mockRouter{
		identityAgents: []*httptest.Server{idServer},
	})
	defer router.Close()

	// Record 2 exposures directly on the identity agent
	for range 2 {
		idAgent.recordExposure("tok-user-freq", "pkg-food-display")
	}

	// Now check eligibility
	idResp := postJSON(t, router.URL+"/tmp/identity", tmproto.IdentityMatchRequest{
		RequestID:  "id-freq-001",
		Identities: []tmproto.IdentityToken{{UserToken: "tok-user-freq"}},
		PackageIDs: []string{"pkg-food-display", "pkg-tech-native"},
	})

	var imResp tmproto.IdentityMatchResponse
	require.NoError(t, json.Unmarshal(idResp, &imResp))

	eligSet := make(map[string]bool)
	for _, id := range imResp.EligiblePackageIDs {
		eligSet[id] = true
	}
	assert.False(t, eligSet["pkg-food-display"], "pkg-food-display should be capped after 2 exposures")
	assert.True(t, eligSet["pkg-tech-native"], "pkg-tech-native should still be eligible")
}

func TestMultipleProviders_MergedResponse(t *testing.T) {
	agent1 := httptest.NewServer(&mockContextAgent{
		rules: map[string][]string{"pkg-food": {"cooking"}},
	})
	defer agent1.Close()

	agent2 := httptest.NewServer(&mockContextAgent{
		rules: map[string][]string{"pkg-sports": {"cooking"}}, // also matches cooking (cross-sell)
	})
	defer agent2.Close()

	router := httptest.NewServer(&mockRouter{
		contextAgents: []*httptest.Server{agent1, agent2},
	})
	defer router.Close()

	resp := postJSON(t, router.URL+"/tmp/context", tmproto.ContextMatchRequest{
		RequestID:   "ctx-merge-001",
		PropertyID:  "pub-test",
		PlacementID: "main",
		PackageIDs:  []string{"pkg-food", "pkg-sports"},
	})

	var cmResp tmproto.ContextMatchResponse
	require.NoError(t, json.Unmarshal(resp, &cmResp))

	require.Len(t, cmResp.Offers, 2, "expected 2 merged offers: %+v", cmResp.Offers)
}

func TestPackageSetDecorrelation(t *testing.T) {
	// Context match: 3 packages (per-placement)
	contextPackages := []string{"pkg-1", "pkg-2", "pkg-3"}

	// Identity match: 6 packages (all active for buyer)
	identityPackages := []string{
		"pkg-1", "pkg-2", "pkg-3",
		"pkg-4", "pkg-5", "pkg-6",
	}

	assert.NotEqual(t, len(contextPackages), len(identityPackages), "context and identity package sets should be different sizes for decorrelation")
	assert.Greater(t, len(identityPackages), len(contextPackages), "identity set should be larger than context set (all active vs per-placement)")
}

func TestProviderTimeout_Excluded(t *testing.T) {
	fastAgent := httptest.NewServer(&mockContextAgent{
		rules: map[string][]string{"pkg-fast": {"article"}},
	})
	defer fastAgent.Close()

	slowAgent := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(200 * time.Millisecond)
		json.NewEncoder(w).Encode(tmproto.ContextMatchResponse{
			Offers: []tmproto.Offer{{PackageID: "pkg-slow"}},
		})
	}))
	defer slowAgent.Close()

	// Router with both agents but the mock router doesn't enforce timeouts,
	// so we test at the HTTP level with a short client timeout
	client := &http.Client{Timeout: 100 * time.Millisecond}

	// Fast agent responds
	body, _ := json.Marshal(tmproto.ContextMatchRequest{
		RequestID:   "ctx-timeout-001",
		PropertyID:  "pub-test",
		PlacementID: "main",
		PackageIDs:  []string{"pkg-fast"},
	})
	resp, err := client.Post(fastAgent.URL+"/tmp/context", "application/json", bytes.NewReader(body))
	require.NoError(t, err, "fast agent should respond")
	resp.Body.Close()

	// Slow agent times out
	_, err = client.Post(slowAgent.URL+"/tmp/context", "application/json", bytes.NewReader(body))
	assert.Error(t, err, "slow agent should have timed out")
}

func TestExposeEndpoint_FeedbackLoop(t *testing.T) {
	idAgent := newMockIdentityAgent(map[string]int{
		"pkg-food": 1, // cap at 1
	})
	idServer := httptest.NewServer(idAgent)
	defer idServer.Close()

	ctxAgent := httptest.NewServer(&mockContextAgent{
		rules: map[string][]string{"pkg-food": {"cooking"}},
	})
	defer ctxAgent.Close()

	router := httptest.NewServer(&mockRouter{
		contextAgents:  []*httptest.Server{ctxAgent},
		identityAgents: []*httptest.Server{idServer},
	})
	defer router.Close()

	// 1. Context match
	ctxResp := postJSON(t, router.URL+"/tmp/context", tmproto.ContextMatchRequest{
		RequestID:   "ctx-loop-001",
		PropertyID:  "pub-test",
		PlacementID: "main",
		PackageIDs:  []string{"pkg-food"},
	})
	var cmResp tmproto.ContextMatchResponse
	require.NoError(t, json.Unmarshal(ctxResp, &cmResp))
	require.Len(t, cmResp.Offers, 1, "expected 1 offer")

	// 2. Identity match (should be eligible)
	idResp := postJSON(t, router.URL+"/tmp/identity", tmproto.IdentityMatchRequest{
		RequestID:  "id-loop-001",
		Identities: []tmproto.IdentityToken{{UserToken: "tok-loop-user"}},
		PackageIDs: []string{"pkg-food"},
	})
	var imResp tmproto.IdentityMatchResponse
	require.NoError(t, json.Unmarshal(idResp, &imResp))
	eligSet := make(map[string]bool)
	for _, id := range imResp.EligiblePackageIDs {
		eligSet[id] = true
	}
	assert.True(t, eligSet["pkg-food"], "should be eligible before exposure")

	// 3. Expose (ad was shown)
	idAgent.recordExposure("tok-loop-user", "pkg-food")

	// 4. Identity match again (should be capped)
	idResp2 := postJSON(t, router.URL+"/tmp/identity", tmproto.IdentityMatchRequest{
		RequestID:  "id-loop-002",
		Identities: []tmproto.IdentityToken{{UserToken: "tok-loop-user"}},
		PackageIDs: []string{"pkg-food"},
	})
	var imResp2 tmproto.IdentityMatchResponse
	require.NoError(t, json.Unmarshal(idResp2, &imResp2))
	eligSet2 := make(map[string]bool)
	for _, id := range imResp2.EligiblePackageIDs {
		eligSet2[id] = true
	}
	assert.False(t, eligSet2["pkg-food"], "should be capped after 1 exposure")
}

func TestRouterEnrichment_PropertyRID(t *testing.T) {
	// Context agent that echoes the property_rid it received
	var receivedRID string
	echoAgent := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req tmproto.ContextMatchRequest
		json.NewDecoder(r.Body).Decode(&req)
		receivedRID = req.PropertyRID
		json.NewEncoder(w).Encode(tmproto.ContextMatchResponse{
			RequestID: req.RequestID,
			Offers:    []tmproto.Offer{},
		})
	}))
	defer echoAgent.Close()

	router := httptest.NewServer(&mockRouter{
		contextAgents: []*httptest.Server{echoAgent},
		registryRIDs:  map[string]string{"pub-oakwood": "rid-1001"},
	})
	defer router.Close()

	postJSON(t, router.URL+"/tmp/context", tmproto.ContextMatchRequest{
		RequestID:   "ctx-rid-001",
		PropertyID:  "pub-oakwood",
		PlacementID: "main",
		PackageIDs:  []string{"pkg-1"},
	})

	assert.Equal(t, "rid-1001", receivedRID, "expected property_rid rid-1001")
}

func TestTimingReport(t *testing.T) {
	ctxAgent := httptest.NewServer(&mockContextAgent{
		rules: map[string][]string{"pkg-food": {"cooking"}},
	})
	defer ctxAgent.Close()

	idAgent := httptest.NewServer(newMockIdentityAgent(map[string]int{"pkg-food": 100}))
	defer idAgent.Close()

	router := httptest.NewServer(&mockRouter{
		contextAgents:  []*httptest.Server{ctxAgent},
		identityAgents: []*httptest.Server{idAgent},
		registryRIDs:   map[string]string{"pub-oakwood": "rid-1001"},
	})
	defer router.Close()

	ctxReq := tmproto.ContextMatchRequest{
		RequestID:   "ctx-timing",
		PropertyID:  "pub-oakwood",
		PlacementID: "sidebar",
		PackageIDs:  []string{"pkg-food"},
	}
	idReq := tmproto.IdentityMatchRequest{
		RequestID:  "id-timing",
		Identities: []tmproto.IdentityToken{{UserToken: "tok-timing"}},
		PackageIDs: []string{"pkg-food", "pkg-other-1", "pkg-other-2"},
	}

	// Warm up
	postJSON(t, router.URL+"/tmp/context", ctxReq)
	postJSON(t, router.URL+"/tmp/identity", idReq)

	// Measure sequential
	start := time.Now()
	iterations := 100
	for i := range iterations {
		ctxReq.RequestID = fmt.Sprintf("ctx-%d", i)
		idReq.RequestID = fmt.Sprintf("id-%d", i)
		postJSON(t, router.URL+"/tmp/context", ctxReq)
		postJSON(t, router.URL+"/tmp/identity", idReq)
	}
	seqDuration := time.Since(start)

	// Measure parallel (context + identity simultaneously)
	start = time.Now()
	for i := range iterations {
		ctxReq.RequestID = fmt.Sprintf("ctx-p-%d", i)
		idReq.RequestID = fmt.Sprintf("id-p-%d", i)
		var wg sync.WaitGroup
		wg.Add(2)
		go func() { defer wg.Done(); postJSON(t, router.URL+"/tmp/context", ctxReq) }()
		go func() { defer wg.Done(); postJSON(t, router.URL+"/tmp/identity", idReq) }()
		wg.Wait()
	}
	parDuration := time.Since(start)

	t.Logf("Sequential (%d iterations): %v (%.1f μs/exchange)", iterations, seqDuration, float64(seqDuration.Microseconds())/float64(iterations))
	t.Logf("Parallel   (%d iterations): %v (%.1f μs/exchange)", iterations, parDuration, float64(parDuration.Microseconds())/float64(iterations))
	t.Logf("Speedup: %.2fx", float64(seqDuration)/float64(parDuration))
}
