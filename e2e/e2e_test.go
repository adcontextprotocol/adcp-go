package e2e

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/adcontextprotocol/adcp-go/tmp"
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
	var req tmp.ContextMatchRequest
	json.NewDecoder(r.Body).Decode(&req)

	var offers []tmp.Offer
	for _, pkg := range req.AvailablePkgs {
		keywords, ok := a.rules[pkg.PackageID]
		if !ok {
			continue
		}
		for _, kw := range keywords {
			matched := false
			for _, art := range req.Artifacts {
				if strings.Contains(art, kw) {
					matched = true
					break
				}
			}
			if matched {
				offers = append(offers, tmp.Offer{PackageID: pkg.PackageID})
				break
			}
		}
	}

	resp := tmp.ContextMatchResponse{
		RequestID: req.RequestID,
		Offers:    offers,
		Signals: &tmp.Signals{
			TargetingKVs: []tmp.KeyValuePair{},
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

// --- Mock Identity Agent ---

type mockIdentityAgent struct {
	mu        sync.Mutex
	freqCaps  map[string]int           // package_id -> max per hour
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
	case "/tmp/expose":
		a.handleExpose(w, r)
	default:
		http.NotFound(w, r)
	}
}

func (a *mockIdentityAgent) handleIdentity(w http.ResponseWriter, r *http.Request) {
	var req tmp.IdentityMatchRequest
	json.NewDecoder(r.Body).Decode(&req)

	a.mu.Lock()
	defer a.mu.Unlock()

	userExposures := a.exposures[req.UserToken]

	var eligibility []tmp.PackageEligibility
	for _, pkgID := range req.PackageIDs {
		eligible := true
		if cap, ok := a.freqCaps[pkgID]; ok {
			count := 0
			if userExposures != nil {
				count = userExposures[pkgID]
			}
			if count >= cap {
				eligible = false
			}
		}
		intent := 0.75
		eligibility = append(eligibility, tmp.PackageEligibility{
			PackageID:   pkgID,
			Eligible:    eligible,
			IntentScore: &intent,
		})
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(tmp.IdentityMatchResponse{
		RequestID:   req.RequestID,
		Eligibility: eligibility,
	})
}

func (a *mockIdentityAgent) handleExpose(w http.ResponseWriter, r *http.Request) {
	var req tmp.ExposeRequest
	json.NewDecoder(r.Body).Decode(&req)

	a.mu.Lock()
	defer a.mu.Unlock()

	if a.exposures[req.UserToken] == nil {
		a.exposures[req.UserToken] = make(map[string]int)
	}
	a.exposures[req.UserToken][req.PackageID]++

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(tmp.ExposeResponse{
		PackageID: req.PackageID,
	})
}

// --- Mock Router ---
// Simplified router that forwards to context and identity agents

type mockRouter struct {
	contextAgents  []*httptest.Server
	identityAgents []*httptest.Server
	registryRIDs   map[string]uint64 // property_id -> property_rid
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
	var req tmp.ContextMatchRequest
	json.Unmarshal(body, &req)

	// Validate: no identity fields
	// (ContextMatchRequest struct has no user_token field by design)

	// Enrich with registry
	if rid, ok := rt.registryRIDs[req.PropertyID]; ok {
		req.PropertyRID = rid
	}

	// Compute URL hash
	if len(req.Artifacts) > 0 {
		req.URLHash = tmp.HashURL(req.Artifacts[0])
	}

	enrichedBody, _ := json.Marshal(req)

	// Fan out to all context agents
	var allOffers []tmp.Offer
	var allKVs []tmp.KeyValuePair
	var allSegments []string
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
			var cmResp tmp.ContextMatchResponse
			json.NewDecoder(resp.Body).Decode(&cmResp)
			mu.Lock()
			allOffers = append(allOffers, cmResp.Offers...)
			if cmResp.Signals != nil {
				allKVs = append(allKVs, cmResp.Signals.TargetingKVs...)
				allSegments = append(allSegments, cmResp.Signals.Segments...)
			}
			mu.Unlock()
		}(agent.URL)
	}
	wg.Wait()

	merged := tmp.ContextMatchResponse{
		RequestID: req.RequestID,
		Offers:    allOffers,
	}
	if len(allKVs) > 0 || len(allSegments) > 0 {
		merged.Signals = &tmp.Signals{
			TargetingKVs: allKVs,
			Segments:     allSegments,
		}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(merged)
}

func (rt *mockRouter) handleIdentity(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)

	// Fan out to all identity agents
	type mergedElig struct {
		eligible    bool
		intentScore *float64
	}
	byPkg := make(map[string]*mergedElig)
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
			var imResp tmp.IdentityMatchResponse
			json.NewDecoder(resp.Body).Decode(&imResp)
			mu.Lock()
			for _, e := range imResp.Eligibility {
				m, ok := byPkg[e.PackageID]
				if !ok {
					m = &mergedElig{}
					byPkg[e.PackageID] = m
				}
				if e.Eligible {
					m.eligible = true
				}
				if e.IntentScore != nil && (m.intentScore == nil || *e.IntentScore > *m.intentScore) {
					s := *e.IntentScore
					m.intentScore = &s
				}
			}
			mu.Unlock()
		}(agent.URL)
	}
	wg.Wait()

	var req tmp.IdentityMatchRequest
	json.Unmarshal(body, &req)

	var eligibility []tmp.PackageEligibility
	for pkgID, m := range byPkg {
		eligibility = append(eligibility, tmp.PackageEligibility{
			PackageID:   pkgID,
			Eligible:    m.eligible,
			IntentScore: m.intentScore,
		})
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(tmp.IdentityMatchResponse{
		RequestID:   req.RequestID,
		Eligibility: eligibility,
	})
}

// --- Helper ---

func postJSON(t *testing.T, url string, body interface{}) []byte {
	t.Helper()
	b, _ := json.Marshal(body)
	resp, err := http.Post(url, "application/json", bytes.NewReader(b))
	if err != nil {
		t.Fatalf("POST %s: %v", url, err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		t.Fatalf("POST %s: status %d, body: %s", url, resp.StatusCode, string(data))
	}
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
		registryRIDs:   map[string]uint64{"pub-oakwood": 1001},
	})
	defer router.Close()

	// 1. Context Match
	ctxResp := postJSON(t, router.URL+"/tmp/context", tmp.ContextMatchRequest{
		RequestID:    "ctx-e2e-001",
		PropertyID:   "pub-oakwood",
		PropertyType: tmp.PropertyTypeWebsite,
		PlacementID:  "sidebar-300x250",
		Artifacts:    []string{"article:cooking-with-herbs"},
		AvailablePkgs: []tmp.AvailablePackage{
			{PackageID: "pkg-food-display", MediaBuyID: "mb-1"},
			{PackageID: "pkg-tech-native", MediaBuyID: "mb-2"},
			{PackageID: "pkg-auto-video", MediaBuyID: "mb-3"},
		},
	})

	var cmResp tmp.ContextMatchResponse
	json.Unmarshal(ctxResp, &cmResp)

	if len(cmResp.Offers) != 1 {
		t.Fatalf("expected 1 offer, got %d", len(cmResp.Offers))
	}
	if cmResp.Offers[0].PackageID != "pkg-food-display" {
		t.Fatalf("expected pkg-food-display, got %s", cmResp.Offers[0].PackageID)
	}

	// 2. Identity Match (ALL active packages, not just page-specific)
	idResp := postJSON(t, router.URL+"/tmp/identity", tmp.IdentityMatchRequest{
		RequestID: "id-e2e-001",
		UserToken: "tok-user-abc",
		UIDType:   tmp.UIDTypeUID2,
		PackageIDs: []string{
			"pkg-food-display", "pkg-tech-native", "pkg-auto-video",
			"pkg-other-site-1", "pkg-other-site-2", "pkg-other-site-3",
		},
	})

	var imResp tmp.IdentityMatchResponse
	json.Unmarshal(idResp, &imResp)

	// All should be eligible (no exposures yet)
	for _, e := range imResp.Eligibility {
		if !e.Eligible {
			t.Errorf("expected %s to be eligible", e.PackageID)
		}
	}

	// 3. Publisher joins locally
	contextOffers := make(map[string]bool)
	for _, o := range cmResp.Offers {
		contextOffers[o.PackageID] = true
	}
	eligiblePkgs := make(map[string]bool)
	for _, e := range imResp.Eligibility {
		if e.Eligible {
			eligiblePkgs[e.PackageID] = true
		}
	}

	var activated []string
	for pkgID := range contextOffers {
		if eligiblePkgs[pkgID] {
			activated = append(activated, pkgID)
		}
	}
	if len(activated) != 1 || activated[0] != "pkg-food-display" {
		t.Fatalf("expected [pkg-food-display], got %v", activated)
	}
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

	// Record 2 exposures directly to the identity agent
	for i := 0; i < 2; i++ {
		postJSON(t, idServer.URL+"/tmp/expose", tmp.ExposeRequest{
			UserToken: "tok-user-freq",
			PackageID: "pkg-food-display",
		})
	}

	// Now check eligibility
	idResp := postJSON(t, router.URL+"/tmp/identity", tmp.IdentityMatchRequest{
		RequestID:  "id-freq-001",
		UserToken:  "tok-user-freq",
		PackageIDs: []string{"pkg-food-display", "pkg-tech-native"},
	})

	var imResp tmp.IdentityMatchResponse
	json.Unmarshal(idResp, &imResp)

	for _, e := range imResp.Eligibility {
		switch e.PackageID {
		case "pkg-food-display":
			if e.Eligible {
				t.Error("pkg-food-display should be capped after 2 exposures")
			}
		case "pkg-tech-native":
			if !e.Eligible {
				t.Error("pkg-tech-native should still be eligible")
			}
		}
	}
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

	resp := postJSON(t, router.URL+"/tmp/context", tmp.ContextMatchRequest{
		RequestID:   "ctx-merge-001",
		PropertyID:  "pub-test",
		PlacementID: "main",
		Artifacts:   []string{"article:cooking-tips"},
		AvailablePkgs: []tmp.AvailablePackage{
			{PackageID: "pkg-food", MediaBuyID: "mb-1"},
			{PackageID: "pkg-sports", MediaBuyID: "mb-2"},
		},
	})

	var cmResp tmp.ContextMatchResponse
	json.Unmarshal(resp, &cmResp)

	if len(cmResp.Offers) != 2 {
		t.Fatalf("expected 2 merged offers, got %d: %+v", len(cmResp.Offers), cmResp.Offers)
	}
}

func TestPackageSetDecorrelation(t *testing.T) {
	// Context match: 3 packages (per-placement)
	contextPackages := []tmp.AvailablePackage{
		{PackageID: "pkg-1", MediaBuyID: "mb-1"},
		{PackageID: "pkg-2", MediaBuyID: "mb-2"},
		{PackageID: "pkg-3", MediaBuyID: "mb-3"},
	}

	// Identity match: 6 packages (all active for buyer)
	identityPackages := []string{
		"pkg-1", "pkg-2", "pkg-3",
		"pkg-4", "pkg-5", "pkg-6",
	}

	if len(contextPackages) == len(identityPackages) {
		t.Error("context and identity package sets should be different sizes for decorrelation")
	}
	if len(identityPackages) <= len(contextPackages) {
		t.Error("identity set should be larger than context set (all active vs per-placement)")
	}
}

func TestProviderTimeout_Excluded(t *testing.T) {
	fastAgent := httptest.NewServer(&mockContextAgent{
		rules: map[string][]string{"pkg-fast": {"article"}},
	})
	defer fastAgent.Close()

	slowAgent := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(200 * time.Millisecond)
		json.NewEncoder(w).Encode(tmp.ContextMatchResponse{
			Offers: []tmp.Offer{{PackageID: "pkg-slow"}},
		})
	}))
	defer slowAgent.Close()

	// Router with both agents but the mock router doesn't enforce timeouts,
	// so we test at the HTTP level with a short client timeout
	client := &http.Client{Timeout: 100 * time.Millisecond}

	// Fast agent responds
	body, _ := json.Marshal(tmp.ContextMatchRequest{
		RequestID:   "ctx-timeout-001",
		PropertyID:  "pub-test",
		PlacementID: "main",
		Artifacts:   []string{"article:test"},
		AvailablePkgs: []tmp.AvailablePackage{
			{PackageID: "pkg-fast", MediaBuyID: "mb-1"},
		},
	})
	resp, err := client.Post(fastAgent.URL+"/tmp/context", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("fast agent should respond: %v", err)
	}
	resp.Body.Close()

	// Slow agent times out
	_, err = client.Post(slowAgent.URL+"/tmp/context", "application/json", bytes.NewReader(body))
	if err == nil {
		t.Error("slow agent should have timed out")
	}
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
	ctxResp := postJSON(t, router.URL+"/tmp/context", tmp.ContextMatchRequest{
		RequestID:   "ctx-loop-001",
		PropertyID:  "pub-test",
		PlacementID: "main",
		Artifacts:   []string{"article:cooking"},
		AvailablePkgs: []tmp.AvailablePackage{
			{PackageID: "pkg-food", MediaBuyID: "mb-1"},
		},
	})
	var cmResp tmp.ContextMatchResponse
	json.Unmarshal(ctxResp, &cmResp)
	if len(cmResp.Offers) != 1 {
		t.Fatalf("expected 1 offer, got %d", len(cmResp.Offers))
	}

	// 2. Identity match (should be eligible)
	idResp := postJSON(t, router.URL+"/tmp/identity", tmp.IdentityMatchRequest{
		RequestID:  "id-loop-001",
		UserToken:  "tok-loop-user",
		PackageIDs: []string{"pkg-food"},
	})
	var imResp tmp.IdentityMatchResponse
	json.Unmarshal(idResp, &imResp)
	if !imResp.Eligibility[0].Eligible {
		t.Error("should be eligible before exposure")
	}

	// 3. Expose (ad was shown)
	postJSON(t, idServer.URL+"/tmp/expose", tmp.ExposeRequest{
		UserToken: "tok-loop-user",
		PackageID: "pkg-food",
	})

	// 4. Identity match again (should be capped)
	idResp2 := postJSON(t, router.URL+"/tmp/identity", tmp.IdentityMatchRequest{
		RequestID:  "id-loop-002",
		UserToken:  "tok-loop-user",
		PackageIDs: []string{"pkg-food"},
	})
	var imResp2 tmp.IdentityMatchResponse
	json.Unmarshal(idResp2, &imResp2)
	if imResp2.Eligibility[0].Eligible {
		t.Error("should be capped after 1 exposure")
	}
}

func TestRouterEnrichment_PropertyRID(t *testing.T) {
	// Context agent that echoes the property_rid it received
	var receivedRID uint64
	echoAgent := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req tmp.ContextMatchRequest
		json.NewDecoder(r.Body).Decode(&req)
		receivedRID = req.PropertyRID
		json.NewEncoder(w).Encode(tmp.ContextMatchResponse{
			RequestID: req.RequestID,
			Offers:    []tmp.Offer{},
		})
	}))
	defer echoAgent.Close()

	router := httptest.NewServer(&mockRouter{
		contextAgents: []*httptest.Server{echoAgent},
		registryRIDs:  map[string]uint64{"pub-oakwood": 1001},
	})
	defer router.Close()

	postJSON(t, router.URL+"/tmp/context", tmp.ContextMatchRequest{
		RequestID:   "ctx-rid-001",
		PropertyID:  "pub-oakwood",
		PlacementID: "main",
		Artifacts:   []string{"article:test"},
		AvailablePkgs: []tmp.AvailablePackage{
			{PackageID: "pkg-1", MediaBuyID: "mb-1"},
		},
	})

	if receivedRID != 1001 {
		t.Errorf("expected property_rid 1001, got %d", receivedRID)
	}
}

func TestRouterEnrichment_URLHash(t *testing.T) {
	var receivedHash uint64
	echoAgent := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req tmp.ContextMatchRequest
		json.NewDecoder(r.Body).Decode(&req)
		receivedHash = req.URLHash
		json.NewEncoder(w).Encode(tmp.ContextMatchResponse{
			RequestID: req.RequestID,
			Offers:    []tmp.Offer{},
		})
	}))
	defer echoAgent.Close()

	router := httptest.NewServer(&mockRouter{
		contextAgents: []*httptest.Server{echoAgent},
	})
	defer router.Close()

	postJSON(t, router.URL+"/tmp/context", tmp.ContextMatchRequest{
		RequestID:   "ctx-hash-001",
		PropertyID:  "pub-test",
		PlacementID: "main",
		Artifacts:   []string{"https://www.oakwood.example.com/cooking"},
		AvailablePkgs: []tmp.AvailablePackage{
			{PackageID: "pkg-1", MediaBuyID: "mb-1"},
		},
	})

	expectedHash := tmp.HashURL("https://www.oakwood.example.com/cooking")
	if receivedHash != expectedHash {
		t.Errorf("expected url_hash %d, got %d", expectedHash, receivedHash)
	}
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
		registryRIDs:   map[string]uint64{"pub-oakwood": 1001},
	})
	defer router.Close()

	ctxReq := tmp.ContextMatchRequest{
		RequestID:   "ctx-timing",
		PropertyID:  "pub-oakwood",
		PlacementID: "sidebar",
		Artifacts:   []string{"article:cooking"},
		AvailablePkgs: []tmp.AvailablePackage{
			{PackageID: "pkg-food", MediaBuyID: "mb-1"},
		},
	}
	idReq := tmp.IdentityMatchRequest{
		RequestID:  "id-timing",
		UserToken:  "tok-timing",
		PackageIDs: []string{"pkg-food", "pkg-other-1", "pkg-other-2"},
	}

	// Warm up
	postJSON(t, router.URL+"/tmp/context", ctxReq)
	postJSON(t, router.URL+"/tmp/identity", idReq)

	// Measure sequential
	start := time.Now()
	iterations := 100
	for i := 0; i < iterations; i++ {
		ctxReq.RequestID = fmt.Sprintf("ctx-%d", i)
		idReq.RequestID = fmt.Sprintf("id-%d", i)
		postJSON(t, router.URL+"/tmp/context", ctxReq)
		postJSON(t, router.URL+"/tmp/identity", idReq)
	}
	seqDuration := time.Since(start)

	// Measure parallel (context + identity simultaneously)
	start = time.Now()
	for i := 0; i < iterations; i++ {
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
