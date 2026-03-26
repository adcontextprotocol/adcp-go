package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/adcontextprotocol/adcp-go/tmp"
)

func TestValidateContextRequest_Valid(t *testing.T) {
	req := &tmp.ContextMatchRequest{
		RequestID:    "ctx-001",
		PropertyID:   "pub-oakwood",
		PropertyType: tmp.PropertyTypeWebsite,
		PlacementID:  "sidebar-300x250",
		AvailablePkgs: []tmp.AvailablePackage{
			{PackageID: "pkg-1", MediaBuyID: "mb-1"},
		},
	}
	if err := ValidateContextRequest(req); err != nil {
		t.Errorf("expected valid, got: %v", err)
	}
}

func TestValidateContextRequest_MissingFields(t *testing.T) {
	tests := []struct {
		name string
		req  tmp.ContextMatchRequest
	}{
		{"missing request_id", tmp.ContextMatchRequest{PropertyID: "p", PlacementID: "pl", AvailablePkgs: []tmp.AvailablePackage{{PackageID: "a", MediaBuyID: "b"}}}},
		{"missing property_id", tmp.ContextMatchRequest{RequestID: "r", PlacementID: "pl", AvailablePkgs: []tmp.AvailablePackage{{PackageID: "a", MediaBuyID: "b"}}}},
		{"missing placement_id", tmp.ContextMatchRequest{RequestID: "r", PropertyID: "p", AvailablePkgs: []tmp.AvailablePackage{{PackageID: "a", MediaBuyID: "b"}}}},
		{"empty packages", tmp.ContextMatchRequest{RequestID: "r", PropertyID: "p", PlacementID: "pl"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := ValidateContextRequest(&tt.req); err == nil {
				t.Error("expected error")
			}
		})
	}
}

func TestValidateIdentityRequest_Valid(t *testing.T) {
	req := &tmp.IdentityMatchRequest{
		RequestID:  "id-001",
		UserToken:  "tok_abc",
		PackageIDs: []string{"pkg-1", "pkg-2"},
	}
	if err := ValidateIdentityRequest(req); err != nil {
		t.Errorf("expected valid, got: %v", err)
	}
}

func TestProviderFiltering_PropertyID(t *testing.T) {
	provider := &ProviderConfig{
		ContextMatch: true,
		PropertyIDs:  []string{"pub-oakwood-*"},
	}

	match := &tmp.ContextMatchRequest{PropertyID: "pub-oakwood-main", PropertyType: "website", AvailablePkgs: []tmp.AvailablePackage{{PackageID: "a", MediaBuyID: "b"}}}
	noMatch := &tmp.ContextMatchRequest{PropertyID: "pub-other-site", PropertyType: "website", AvailablePkgs: []tmp.AvailablePackage{{PackageID: "a", MediaBuyID: "b"}}}

	if !MatchesContextProvider(match, provider) {
		t.Error("should match pub-oakwood-main")
	}
	if MatchesContextProvider(noMatch, provider) {
		t.Error("should not match pub-other-site")
	}
}

func TestProviderFiltering_ExcludeProperty(t *testing.T) {
	provider := &ProviderConfig{
		ContextMatch:       true,
		ExcludePropertyIDs: []string{"pub-blocked-*"},
	}

	req := &tmp.ContextMatchRequest{PropertyID: "pub-blocked-123", PropertyType: "website", AvailablePkgs: []tmp.AvailablePackage{{PackageID: "a", MediaBuyID: "b"}}}
	if MatchesContextProvider(req, provider) {
		t.Error("should be excluded")
	}
}

func TestProviderFiltering_PropertyType(t *testing.T) {
	provider := &ProviderConfig{
		ContextMatch:  true,
		PropertyTypes: []string{"website", "ai_assistant"},
	}

	web := &tmp.ContextMatchRequest{PropertyID: "p", PropertyType: "website", AvailablePkgs: []tmp.AvailablePackage{{PackageID: "a", MediaBuyID: "b"}}}
	ctv := &tmp.ContextMatchRequest{PropertyID: "p", PropertyType: "ctv_app", AvailablePkgs: []tmp.AvailablePackage{{PackageID: "a", MediaBuyID: "b"}}}

	if !MatchesContextProvider(web, provider) {
		t.Error("should match website")
	}
	if MatchesContextProvider(ctv, provider) {
		t.Error("should not match ctv_app")
	}
}

func TestMergeContextResponses(t *testing.T) {
	r1 := &tmp.ContextMatchResponse{
		Offers: []tmp.Offer{{PackageID: "pkg-1"}},
		Signals: &tmp.Signals{
			Segments: []string{"cooking"},
			TargetingKVs: []tmp.KeyValuePair{{Key: "adcp_pkg", Value: "pkg-1"}},
		},
	}
	r2 := &tmp.ContextMatchResponse{
		Offers: []tmp.Offer{{PackageID: "pkg-2"}, {PackageID: "pkg-3"}},
		Signals: &tmp.Signals{
			Segments: []string{"sustainability"},
		},
	}

	merged := mergeContextResponses("ctx-test", []*tmp.ContextMatchResponse{r1, r2})

	if len(merged.Offers) != 3 {
		t.Errorf("expected 3 offers, got %d", len(merged.Offers))
	}
	if len(merged.Signals.Segments) != 2 {
		t.Errorf("expected 2 segments, got %d", len(merged.Signals.Segments))
	}
}

func TestMergeIdentityResponses(t *testing.T) {
	score1 := 0.7
	score2 := 0.9
	r1 := &tmp.IdentityMatchResponse{
		Eligibility: []tmp.PackageEligibility{
			{PackageID: "pkg-1", Eligible: true, IntentScore: &score1},
			{PackageID: "pkg-2", Eligible: false},
		},
	}
	r2 := &tmp.IdentityMatchResponse{
		Eligibility: []tmp.PackageEligibility{
			{PackageID: "pkg-1", Eligible: false, IntentScore: &score2},
			{PackageID: "pkg-2", Eligible: true},
		},
	}

	merged := mergeIdentityResponses("id-test", []*tmp.IdentityMatchResponse{r1, r2})

	byPkg := map[string]tmp.PackageEligibility{}
	for _, e := range merged.Eligibility {
		byPkg[e.PackageID] = e
	}

	// pkg-1: ineligible (AND semantics — r2 says false overrides r1's true)
	if byPkg["pkg-1"].Eligible {
		t.Error("pkg-1 should be ineligible (AND: one provider says no)")
	}
	// intent_score still tracks the max even when ineligible (useful for analytics)
	if byPkg["pkg-1"].IntentScore == nil || *byPkg["pkg-1"].IntentScore != 0.9 {
		t.Error("pkg-1 intent should be 0.9 (max across providers)")
	}

	// pkg-2: ineligible (AND: r1 says false)
	if byPkg["pkg-2"].Eligible {
		t.Error("pkg-2 should be ineligible (AND: one provider says no)")
	}
}

func TestRouterContextMatch_EndToEnd(t *testing.T) {
	// Mock provider that activates pkg-1
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(tmp.ContextMatchResponse{
			RequestID: "ctx-e2e",
			Offers:    []tmp.Offer{{PackageID: "pkg-1"}},
			Signals: &tmp.Signals{
				TargetingKVs: []tmp.KeyValuePair{{Key: "adcp_pkg", Value: "pkg-1"}},
			},
		})
	}))
	defer provider.Close()

	router := NewRouter([]ProviderConfig{
		{ID: "test-provider", Endpoint: provider.URL, ContextMatch: true, Timeout: 5 * time.Second},
	}, nil, nil)

	reqBody := `{
		"request_id": "ctx-e2e",
		"property_id": "pub-test",
		"property_type": "website",
		"placement_id": "sidebar",
		"available_packages": [{"package_id": "pkg-1", "media_buy_id": "mb-1"}]
	}`

	w := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/tmp/context", strings.NewReader(reqBody))
	router.HandleContextMatch(w, req)

	if w.Code != 200 {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp tmp.ContextMatchResponse
	json.NewDecoder(w.Body).Decode(&resp)

	if len(resp.Offers) != 1 || resp.Offers[0].PackageID != "pkg-1" {
		t.Errorf("expected 1 offer for pkg-1, got: %+v", resp.Offers)
	}
}

func TestRouterIdentityMatch_EndToEnd(t *testing.T) {
	score := 0.82
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(tmp.IdentityMatchResponse{
			RequestID: "id-e2e",
			Eligibility: []tmp.PackageEligibility{
				{PackageID: "pkg-1", Eligible: true, IntentScore: &score},
				{PackageID: "pkg-2", Eligible: false},
			},
		})
	}))
	defer provider.Close()

	router := NewRouter([]ProviderConfig{
		{ID: "test-provider", Endpoint: provider.URL, IdentityMatch: true, Timeout: 5 * time.Second},
	}, nil, nil)

	reqBody := `{
		"request_id": "id-e2e",
		"user_token": "tok_test_abc",
		"package_ids": ["pkg-1", "pkg-2", "pkg-3"]
	}`

	w := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/tmp/identity", strings.NewReader(reqBody))
	router.HandleIdentityMatch(w, req)

	if w.Code != 200 {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp tmp.IdentityMatchResponse
	json.NewDecoder(w.Body).Decode(&resp)

	if len(resp.Eligibility) != 2 {
		t.Errorf("expected 2 eligibility entries, got %d", len(resp.Eligibility))
	}
}

func TestRouterTimeout_ProviderExcluded(t *testing.T) {
	// Slow provider that takes too long
	slowProvider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(100 * time.Millisecond)
		json.NewEncoder(w).Encode(tmp.ContextMatchResponse{
			RequestID: "ctx-slow",
			Offers:    []tmp.Offer{{PackageID: "pkg-slow"}},
		})
	}))
	defer slowProvider.Close()

	// Fast provider
	fastProvider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(tmp.ContextMatchResponse{
			RequestID: "ctx-fast",
			Offers:    []tmp.Offer{{PackageID: "pkg-fast"}},
		})
	}))
	defer fastProvider.Close()

	router := NewRouter([]ProviderConfig{
		{ID: "slow", Endpoint: slowProvider.URL, ContextMatch: true, Timeout: 10 * time.Millisecond},
		{ID: "fast", Endpoint: fastProvider.URL, ContextMatch: true, Timeout: 5 * time.Second},
	}, nil, nil)

	reqBody := `{
		"request_id": "ctx-timeout",
		"property_id": "pub-test",
		"property_type": "website",
		"placement_id": "sidebar",
		"available_packages": [{"package_id": "pkg-1", "media_buy_id": "mb-1"}]
	}`

	w := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/tmp/context", strings.NewReader(reqBody))
	router.HandleContextMatch(w, req)

	var resp tmp.ContextMatchResponse
	json.NewDecoder(w.Body).Decode(&resp)

	// Should only have the fast provider's offer
	if len(resp.Offers) != 1 || resp.Offers[0].PackageID != "pkg-fast" {
		t.Errorf("expected only pkg-fast, got: %+v", resp.Offers)
	}
}
