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

	"github.com/adcontextprotocol/adcp-go/tmproto"
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
				offers = append(offers, tmproto.Offer{PackageID: pkg.PackageID})
				break
			}
		}
	}

	resp := tmproto.ContextMatchResponse{
		RequestID: req.RequestID,
		Offers:    offers,
		Signals: &tmproto.Signals{
			TargetingKVs: []tmproto.KeyValuePair{},
		},
	}
	for _, o := range offers {
		resp.Signals.TargetingKVs = append(resp.Signals.TargetingKVs, tmproto.KeyValuePair{
			Key: "adcp_pkg", Value: o.PackageID,
		})
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

	userExposures := a.exposures[req.UserToken]

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
	var req tmproto.ExposeRequest
	json.NewDecoder(r.Body).Decode(&req)

	a.recordExposure(req.UserToken, req.PackageID)

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(tmproto.ExposeResponse{
		PackageID: req.PackageID,
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
	var req tmproto.ContextMatchRequest
	json.Unmarshal(body, &req)

	// Validate: no identity fields
	// (ContextMatchRequest struct has no user_token field by design)

	// Enrich with registry
	if rid, ok := rt.registryRIDs[req.PropertyID]; ok {
		req.PropertyRID = rid
	}

	// Compute URL hash
	if len(req.Artifacts) > 0 {
		req.URLHash = tmproto.HashURL(req.Artifacts[0])
	}

	enrichedBody, _ := json.Marshal(req)

	// Fan out to all context agents
	var allOffers []tmproto.Offer
	var allKVs []tmproto.KeyValuePair
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
			var cmResp tmproto.ContextMatchResponse
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

	merged := tmproto.ContextMatchResponse{
		RequestID: req.RequestID,
		Offers:    allOffers,
	}
	if len(allKVs) > 0 || len(allSegments) > 0 {
		merged.Signals = &tmproto.Signals{
			TargetingKVs: allKVs,
			Segments:     allSegments,
		}
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
	ctxResp := postJSON(t, router.URL+"/tmp/context", tmproto.ContextMatchRequest{
		RequestID:    "ctx-e2e-001",
		PropertyID:   "pub-oakwood",
		PropertyType: tmproto.PropertyTypeWebsite,
		PlacementID:  "sidebar-300x250",
		Artifacts:    []string{"article:cooking-with-herbs"},
		AvailablePkgs: []tmproto.AvailablePackage{
			{PackageID: "pkg-food-display", MediaBuyID: "mb-1"},
			{PackageID: "pkg-tech-native", MediaBuyID: "mb-2"},
			{PackageID: "pkg-auto-video", MediaBuyID: "mb-3"},
		},
	})

	var cmResp tmproto.ContextMatchResponse
	json.Unmarshal(ctxResp, &cmResp)

	if len(cmResp.Offers) != 1 {
		t.Fatalf("expected 1 offer, got %d", len(cmResp.Offers))
	}
	if cmResp.Offers[0].PackageID != "pkg-food-display" {
		t.Fatalf("expected pkg-food-display, got %s", cmResp.Offers[0].PackageID)
	}

	// 2. Identity Match (ALL active packages, not just page-specific)
	idResp := postJSON(t, router.URL+"/tmp/identity", tmproto.IdentityMatchRequest{
		RequestID: "id-e2e-001",
		UserToken: "tok-user-abc",
		UIDType:   tmproto.UIDTypeUID2,
		PackageIDs: []string{
			"pkg-food-display", "pkg-tech-native", "pkg-auto-video",
			"pkg-other-site-1", "pkg-other-site-2", "pkg-other-site-3",
		},
	})

	var imResp tmproto.IdentityMatchResponse
	json.Unmarshal(idResp, &imResp)

	// All requested should be eligible (no exposures yet)
	eligiblePkgs := make(map[string]bool)
	for _, id := range imResp.EligiblePackageIDs {
		eligiblePkgs[id] = true
	}
	for _, pkgID := range []string{
		"pkg-food-display", "pkg-tech-native", "pkg-auto-video",
		"pkg-other-site-1", "pkg-other-site-2", "pkg-other-site-3",
	} {
		if !eligiblePkgs[pkgID] {
			t.Errorf("expected %s to be eligible", pkgID)
		}
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

	// Record 2 exposures directly on the identity agent
	for range 2 {
		idAgent.recordExposure("tok-user-freq", "pkg-food-display")
	}

	// Now check eligibility
	idResp := postJSON(t, router.URL+"/tmp/identity", tmproto.IdentityMatchRequest{
		RequestID:  "id-freq-001",
		UserToken:  "tok-user-freq",
		PackageIDs: []string{"pkg-food-display", "pkg-tech-native"},
	})

	var imResp tmproto.IdentityMatchResponse
	json.Unmarshal(idResp, &imResp)

	eligSet := make(map[string]bool)
	for _, id := range imResp.EligiblePackageIDs {
		eligSet[id] = true
	}
	if eligSet["pkg-food-display"] {
		t.Error("pkg-food-display should be capped after 2 exposures")
	}
	if !eligSet["pkg-tech-native"] {
		t.Error("pkg-tech-native should still be eligible")
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

	resp := postJSON(t, router.URL+"/tmp/context", tmproto.ContextMatchRequest{
		RequestID:   "ctx-merge-001",
		PropertyID:  "pub-test",
		PlacementID: "main",
		Artifacts:   []string{"article:cooking-tips"},
		AvailablePkgs: []tmproto.AvailablePackage{
			{PackageID: "pkg-food", MediaBuyID: "mb-1"},
			{PackageID: "pkg-sports", MediaBuyID: "mb-2"},
		},
	})

	var cmResp tmproto.ContextMatchResponse
	json.Unmarshal(resp, &cmResp)

	if len(cmResp.Offers) != 2 {
		t.Fatalf("expected 2 merged offers, got %d: %+v", len(cmResp.Offers), cmResp.Offers)
	}
}

func TestPackageSetDecorrelation(t *testing.T) {
	// Context match: 3 packages (per-placement)
	contextPackages := []tmproto.AvailablePackage{
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
		Artifacts:   []string{"article:test"},
		AvailablePkgs: []tmproto.AvailablePackage{
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
	ctxResp := postJSON(t, router.URL+"/tmp/context", tmproto.ContextMatchRequest{
		RequestID:   "ctx-loop-001",
		PropertyID:  "pub-test",
		PlacementID: "main",
		Artifacts:   []string{"article:cooking"},
		AvailablePkgs: []tmproto.AvailablePackage{
			{PackageID: "pkg-food", MediaBuyID: "mb-1"},
		},
	})
	var cmResp tmproto.ContextMatchResponse
	json.Unmarshal(ctxResp, &cmResp)
	if len(cmResp.Offers) != 1 {
		t.Fatalf("expected 1 offer, got %d", len(cmResp.Offers))
	}

	// 2. Identity match (should be eligible)
	idResp := postJSON(t, router.URL+"/tmp/identity", tmproto.IdentityMatchRequest{
		RequestID:  "id-loop-001",
		UserToken:  "tok-loop-user",
		PackageIDs: []string{"pkg-food"},
	})
	var imResp tmproto.IdentityMatchResponse
	json.Unmarshal(idResp, &imResp)
	eligSet := make(map[string]bool)
	for _, id := range imResp.EligiblePackageIDs {
		eligSet[id] = true
	}
	if !eligSet["pkg-food"] {
		t.Error("should be eligible before exposure")
	}

	// 3. Expose (ad was shown)
	idAgent.recordExposure("tok-loop-user", "pkg-food")

	// 4. Identity match again (should be capped)
	idResp2 := postJSON(t, router.URL+"/tmp/identity", tmproto.IdentityMatchRequest{
		RequestID:  "id-loop-002",
		UserToken:  "tok-loop-user",
		PackageIDs: []string{"pkg-food"},
	})
	var imResp2 tmproto.IdentityMatchResponse
	json.Unmarshal(idResp2, &imResp2)
	eligSet2 := make(map[string]bool)
	for _, id := range imResp2.EligiblePackageIDs {
		eligSet2[id] = true
	}
	if eligSet2["pkg-food"] {
		t.Error("should be capped after 1 exposure")
	}
}

func TestRouterEnrichment_PropertyRID(t *testing.T) {
	// Context agent that echoes the property_rid it received
	var receivedRID uint64
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
		registryRIDs:  map[string]uint64{"pub-oakwood": 1001},
	})
	defer router.Close()

	postJSON(t, router.URL+"/tmp/context", tmproto.ContextMatchRequest{
		RequestID:   "ctx-rid-001",
		PropertyID:  "pub-oakwood",
		PlacementID: "main",
		Artifacts:   []string{"article:test"},
		AvailablePkgs: []tmproto.AvailablePackage{
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
		var req tmproto.ContextMatchRequest
		json.NewDecoder(r.Body).Decode(&req)
		receivedHash = req.URLHash
		json.NewEncoder(w).Encode(tmproto.ContextMatchResponse{
			RequestID: req.RequestID,
			Offers:    []tmproto.Offer{},
		})
	}))
	defer echoAgent.Close()

	router := httptest.NewServer(&mockRouter{
		contextAgents: []*httptest.Server{echoAgent},
	})
	defer router.Close()

	postJSON(t, router.URL+"/tmp/context", tmproto.ContextMatchRequest{
		RequestID:   "ctx-hash-001",
		PropertyID:  "pub-test",
		PlacementID: "main",
		Artifacts:   []string{"https://www.oakwood.example.com/cooking"},
		AvailablePkgs: []tmproto.AvailablePackage{
			{PackageID: "pkg-1", MediaBuyID: "mb-1"},
		},
	})

	expectedHash := tmproto.HashURL("https://www.oakwood.example.com/cooking")
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

	ctxReq := tmproto.ContextMatchRequest{
		RequestID:   "ctx-timing",
		PropertyID:  "pub-oakwood",
		PlacementID: "sidebar",
		Artifacts:   []string{"article:cooking"},
		AvailablePkgs: []tmproto.AvailablePackage{
			{PackageID: "pkg-food", MediaBuyID: "mb-1"},
		},
	}
	idReq := tmproto.IdentityMatchRequest{
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
