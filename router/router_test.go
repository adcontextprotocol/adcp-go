package router

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/adcontextprotocol/adcp-go/tmproto"
)

func TestValidateContextRequest_Valid(t *testing.T) {
	req := &tmproto.ContextMatchRequest{
		RequestID:    "ctx-001",
		PropertyID:   "pub-oakwood",
		PropertyType: tmproto.PropertyTypeWebsite,
		PlacementID:  "sidebar-300x250",
		AvailablePkgs: []tmproto.AvailablePackage{
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
		req  tmproto.ContextMatchRequest
	}{
		{"missing request_id", tmproto.ContextMatchRequest{PropertyID: "p", PlacementID: "pl", AvailablePkgs: []tmproto.AvailablePackage{{PackageID: "a", MediaBuyID: "b"}}}},
		{"missing property_id", tmproto.ContextMatchRequest{RequestID: "r", PlacementID: "pl", AvailablePkgs: []tmproto.AvailablePackage{{PackageID: "a", MediaBuyID: "b"}}}},
		{"missing placement_id", tmproto.ContextMatchRequest{RequestID: "r", PropertyID: "p", AvailablePkgs: []tmproto.AvailablePackage{{PackageID: "a", MediaBuyID: "b"}}}},
		{"empty packages", tmproto.ContextMatchRequest{RequestID: "r", PropertyID: "p", PlacementID: "pl"}},
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
	req := &tmproto.IdentityMatchRequest{
		RequestID:  "id-001",
		UserToken:  "tok_abc",
		UIDType:    tmproto.UIDTypeUID2,
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

	match := &tmproto.ContextMatchRequest{PropertyID: "pub-oakwood-main", PropertyType: "website", AvailablePkgs: []tmproto.AvailablePackage{{PackageID: "a", MediaBuyID: "b"}}}
	noMatch := &tmproto.ContextMatchRequest{PropertyID: "pub-other-site", PropertyType: "website", AvailablePkgs: []tmproto.AvailablePackage{{PackageID: "a", MediaBuyID: "b"}}}

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

	req := &tmproto.ContextMatchRequest{PropertyID: "pub-blocked-123", PropertyType: "website", AvailablePkgs: []tmproto.AvailablePackage{{PackageID: "a", MediaBuyID: "b"}}}
	if MatchesContextProvider(req, provider) {
		t.Error("should be excluded")
	}
}

func TestProviderFiltering_PropertyType(t *testing.T) {
	provider := &ProviderConfig{
		ContextMatch:  true,
		PropertyTypes: []string{"website", "ai_assistant"},
	}

	web := &tmproto.ContextMatchRequest{PropertyID: "p", PropertyType: "website", AvailablePkgs: []tmproto.AvailablePackage{{PackageID: "a", MediaBuyID: "b"}}}
	ctv := &tmproto.ContextMatchRequest{PropertyID: "p", PropertyType: "ctv_app", AvailablePkgs: []tmproto.AvailablePackage{{PackageID: "a", MediaBuyID: "b"}}}

	if !MatchesContextProvider(web, provider) {
		t.Error("should match website")
	}
	if MatchesContextProvider(ctv, provider) {
		t.Error("should not match ctv_app")
	}
}

func TestMergeContextResponses(t *testing.T) {
	r1 := &tmproto.ContextMatchResponse{
		Offers: []tmproto.Offer{{PackageID: "pkg-1"}},
		Signals: &tmproto.Signals{
			Segments: []string{"cooking"},
			TargetingKVs: []tmproto.KeyValuePair{{Key: "adcp_pkg", Value: "pkg-1"}},
		},
	}
	r2 := &tmproto.ContextMatchResponse{
		Offers: []tmproto.Offer{{PackageID: "pkg-2"}, {PackageID: "pkg-3"}},
		Signals: &tmproto.Signals{
			Segments: []string{"sustainability"},
		},
	}

	merged := mergeContextResponses("ctx-test", []*tmproto.ContextMatchResponse{r1, r2})

	if len(merged.Offers) != 3 {
		t.Errorf("expected 3 offers, got %d", len(merged.Offers))
	}
	if len(merged.Signals.Segments) != 2 {
		t.Errorf("expected 2 segments, got %d", len(merged.Signals.Segments))
	}
}

func TestMergeIdentityResponses(t *testing.T) {
	// Provider 1 says pkg-1 and pkg-3 are eligible.
	r1 := &tmproto.IdentityMatchResponse{
		EligiblePackageIDs: []string{"pkg-1", "pkg-3"},
		TTLSec:             300,
	}
	// Provider 2 says pkg-1 and pkg-2 are eligible.
	r2 := &tmproto.IdentityMatchResponse{
		EligiblePackageIDs: []string{"pkg-1", "pkg-2"},
		TTLSec:             600,
	}

	merged := mergeIdentityResponses("id-test", []string{"p1", "p2"}, []*tmproto.IdentityMatchResponse{r1, r2})

	eligible := map[string]bool{}
	for _, id := range merged.EligiblePackageIDs {
		eligible[id] = true
	}

	// AND semantics for duplicates: a package listed by all providers that mention it
	// is eligible. Packages are provider-specific, so a package listed by one provider
	// passes (100% of providers that listed it said eligible).
	if !eligible["pkg-1"] {
		t.Error("pkg-1 should be eligible (both providers include it)")
	}
	if !eligible["pkg-2"] {
		t.Error("pkg-2 should be eligible (listed by its provider)")
	}
	if !eligible["pkg-3"] {
		t.Error("pkg-3 should be eligible (listed by its provider)")
	}
	if len(merged.EligiblePackageIDs) != 3 {
		t.Errorf("expected 3 eligible packages, got %d", len(merged.EligiblePackageIDs))
	}

	// TTL is the minimum across providers.
	if merged.TTLSec != 300 {
		t.Errorf("expected TTLSec 300 (min), got %d", merged.TTLSec)
	}
}

func TestRouterContextMatch_EndToEnd(t *testing.T) {
	// Mock provider that activates pkg-1
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(tmproto.ContextMatchResponse{
			RequestID: "ctx-e2e",
			Offers:    []tmproto.Offer{{PackageID: "pkg-1"}},
			Signals: &tmproto.Signals{
				TargetingKVs: []tmproto.KeyValuePair{{Key: "adcp_pkg", Value: "pkg-1"}},
			},
		})
	}))
	defer provider.Close()

	router := NewRouter([]ProviderConfig{
		{ID: "test-provider", Endpoint: provider.URL, ContextMatch: true, Timeout: 5 * time.Second},
	}, nil, nil, nil)

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

	var resp tmproto.ContextMatchResponse
	_ = json.NewDecoder(w.Body).Decode(&resp)

	if len(resp.Offers) != 1 || resp.Offers[0].PackageID != "pkg-1" {
		t.Errorf("expected 1 offer for pkg-1, got: %+v", resp.Offers)
	}
}

func TestRouterIdentityMatch_EndToEnd(t *testing.T) {
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(tmproto.IdentityMatchResponse{
			RequestID:          "id-e2e",
			EligiblePackageIDs: []string{"pkg-1"},
			TTLSec:             300,
		})
	}))
	defer provider.Close()

	router := NewRouter([]ProviderConfig{
		{ID: "test-provider", Endpoint: provider.URL, IdentityMatch: true, Timeout: 5 * time.Second},
	}, nil, nil, nil)

	reqBody := `{
		"request_id": "id-e2e",
		"user_token": "tok_test_abc",
		"uid_type": "uid2",
		"package_ids": ["pkg-1", "pkg-2", "pkg-3"]
	}`

	w := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/tmp/identity", strings.NewReader(reqBody))
	router.HandleIdentityMatch(w, req)

	if w.Code != 200 {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp tmproto.IdentityMatchResponse
	_ = json.NewDecoder(w.Body).Decode(&resp)

	if len(resp.EligiblePackageIDs) != 1 || resp.EligiblePackageIDs[0] != "pkg-1" {
		t.Errorf("expected [pkg-1], got %v", resp.EligiblePackageIDs)
	}
	if resp.TTLSec != 300 {
		t.Errorf("expected TTLSec 300, got %d", resp.TTLSec)
	}
}

func TestIdentityFiltering_Country(t *testing.T) {
	usProvider := &ProviderConfig{
		ID:            "buyer-us",
		IdentityMatch: true,
		Countries:     []string{"US"},
		UIDTypes:      []string{"uid2", "rampid"},
	}
	euProvider := &ProviderConfig{
		ID:            "buyer-eu",
		IdentityMatch: true,
		Countries:     []string{"DE", "FR"},
		UIDTypes:      []string{"euid", "id5"},
	}

	usReq := &tmproto.IdentityMatchRequest{
		RequestID:  "id-us",
		UserToken:  "tok",
		UIDType:    tmproto.UIDTypeUID2,
		PackageIDs: []string{"pkg-1"},
		Country:    "US",
	}
	deReq := &tmproto.IdentityMatchRequest{
		RequestID:  "id-de",
		UserToken:  "tok",
		UIDType:    tmproto.UIDTypeEUID,
		PackageIDs: []string{"pkg-1"},
		Country:    "DE",
	}

	if !MatchesIdentityProvider(usReq, usProvider) {
		t.Error("US request should match US provider")
	}
	if MatchesIdentityProvider(usReq, euProvider) {
		t.Error("US request should not match EU provider")
	}
	if MatchesIdentityProvider(deReq, usProvider) {
		t.Error("DE request should not match US provider")
	}
	if !MatchesIdentityProvider(deReq, euProvider) {
		t.Error("DE request should match EU provider")
	}
}

func TestIdentityFiltering_UIDType(t *testing.T) {
	provider := &ProviderConfig{
		ID:            "buyer-uid2-only",
		IdentityMatch: true,
		UIDTypes:      []string{"uid2"},
	}

	uid2Req := &tmproto.IdentityMatchRequest{
		RequestID:  "id-1",
		UserToken:  "tok",
		UIDType:    tmproto.UIDTypeUID2,
		PackageIDs: []string{"pkg-1"},
	}
	euidReq := &tmproto.IdentityMatchRequest{
		RequestID:  "id-2",
		UserToken:  "tok",
		UIDType:    tmproto.UIDTypeEUID,
		PackageIDs: []string{"pkg-1"},
	}

	if !MatchesIdentityProvider(uid2Req, provider) {
		t.Error("uid2 request should match uid2-only provider")
	}
	if MatchesIdentityProvider(euidReq, provider) {
		t.Error("euid request should not match uid2-only provider")
	}
}

func TestIdentityFiltering_NoFilters(t *testing.T) {
	provider := &ProviderConfig{
		ID:            "legacy-provider",
		IdentityMatch: true,
	}
	req := &tmproto.IdentityMatchRequest{
		RequestID:  "id-1",
		UserToken:  "tok",
		UIDType:    tmproto.UIDTypeUID2,
		PackageIDs: []string{"pkg-1"},
		Country:    "US",
	}

	if !MatchesIdentityProvider(req, provider) {
		t.Error("provider with no country/uid_type filters should match all requests")
	}
}

func TestRouterIdentityMatch_StripsCountry(t *testing.T) {
	var receivedBody []byte
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedBody, _ = io.ReadAll(r.Body)
		_ = json.NewEncoder(w).Encode(tmproto.IdentityMatchResponse{
			RequestID:          "id-strip",
			EligiblePackageIDs: []string{"pkg-1"},
			TTLSec:             60,
			Tmpx:               "k1.dGVzdC10b2tlbg",
		})
	}))
	defer provider.Close()

	router := NewRouter([]ProviderConfig{
		{ID: "test-provider", Endpoint: provider.URL, IdentityMatch: true, Countries: []string{"US"}, UIDTypes: []string{"uid2"}, Timeout: 5 * time.Second},
	}, nil, nil, nil)

	reqBody := `{
		"request_id": "id-strip",
		"user_token": "tok_test",
		"uid_type": "uid2",
		"package_ids": ["pkg-1"],
		"country": "US"
	}`

	w := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/tmp/identity", strings.NewReader(reqBody))
	router.HandleIdentityMatch(w, req)

	if w.Code != 200 {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	// Verify country was stripped from forwarded request.
	var forwarded tmproto.IdentityMatchRequest
	_ = json.Unmarshal(receivedBody, &forwarded)
	if forwarded.Country != "" {
		t.Errorf("country should be stripped before forwarding, got %q", forwarded.Country)
	}

	// Verify TMPX in tmpx_providers map.
	var resp tmproto.IdentityMatchResponse
	_ = json.NewDecoder(w.Body).Decode(&resp)
	if resp.TmpxProviders["test-provider"] != "k1.dGVzdC10b2tlbg" {
		t.Errorf("expected TMPX in tmpx_providers, got %v", resp.TmpxProviders)
	}
}

func TestMergeIdentityResponses_TMPX(t *testing.T) {
	r1 := &tmproto.IdentityMatchResponse{
		EligiblePackageIDs: []string{"pkg-1", "pkg-3"},
		TTLSec:             300,
		Tmpx:               "k1.acme-token",
	}
	r2 := &tmproto.IdentityMatchResponse{
		EligiblePackageIDs: []string{"pkg-2"},
		TTLSec:             600,
		Tmpx:               "k2.nova-token",
	}

	merged := mergeIdentityResponses("test", []string{"acme", "nova"}, []*tmproto.IdentityMatchResponse{r1, r2})

	// tmpx_providers should map provider ID → token.
	if len(merged.TmpxProviders) != 2 {
		t.Fatalf("expected 2 tmpx_providers entries, got %d", len(merged.TmpxProviders))
	}
	if merged.TmpxProviders["acme"] != "k1.acme-token" {
		t.Errorf("acme TMPX: got %q, want k1.acme-token", merged.TmpxProviders["acme"])
	}
	if merged.TmpxProviders["nova"] != "k2.nova-token" {
		t.Errorf("nova TMPX: got %q, want k2.nova-token", merged.TmpxProviders["nova"])
	}
}

func TestMergeIdentityResponses_TMPXOmittedWhenEmpty(t *testing.T) {
	r1 := &tmproto.IdentityMatchResponse{
		EligiblePackageIDs: []string{"pkg-1"},
		TTLSec:             300,
	}
	merged := mergeIdentityResponses("test", []string{"p1"}, []*tmproto.IdentityMatchResponse{r1})
	if merged.TmpxProviders != nil {
		t.Errorf("tmpx_providers should be nil when no tokens present, got %v", merged.TmpxProviders)
	}
}

func TestRouterTimeout_ProviderExcluded(t *testing.T) {
	// Slow provider that takes too long
	slowProvider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(100 * time.Millisecond)
		_ = json.NewEncoder(w).Encode(tmproto.ContextMatchResponse{
			RequestID: "ctx-slow",
			Offers:    []tmproto.Offer{{PackageID: "pkg-slow"}},
		})
	}))
	defer slowProvider.Close()

	// Fast provider
	fastProvider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(tmproto.ContextMatchResponse{
			RequestID: "ctx-fast",
			Offers:    []tmproto.Offer{{PackageID: "pkg-fast"}},
		})
	}))
	defer fastProvider.Close()

	router := NewRouter([]ProviderConfig{
		{ID: "slow", Endpoint: slowProvider.URL, ContextMatch: true, Timeout: 10 * time.Millisecond},
		{ID: "fast", Endpoint: fastProvider.URL, ContextMatch: true, Timeout: 5 * time.Second},
	}, nil, nil, nil)

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

	var resp tmproto.ContextMatchResponse
	_ = json.NewDecoder(w.Body).Decode(&resp)

	// Should only have the fast provider's offer
	if len(resp.Offers) != 1 || resp.Offers[0].PackageID != "pkg-fast" {
		t.Errorf("expected only pkg-fast, got: %+v", resp.Offers)
	}
}
