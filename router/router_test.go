package router

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/adcontextprotocol/adcp-go/tmproto"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// testRouter builds a Router without SSRF validation, suitable for use with
// httptest.Server (which binds to localhost).
func testRouter(providers []ProviderConfig) *Router {
	return &Router{
		providers:   NewProviderSet(providers),
		client:      &http.Client{Timeout: 10 * time.Second},
		logger:      slog.Default(),
		contextSigs: newContextSignatureCache(0),
	}
}

func TestValidateContextRequest_Valid(t *testing.T) {
	req := &tmproto.ContextMatchRequest{
		Type:         tmproto.TypeContextMatchRequest,
		RequestID:    "ctx-001",
		PropertyID:   "pub-oakwood",
		PropertyType: tmproto.PropertyTypeWebsite,
		PlacementID:  "sidebar-300x250",
		PackageIDs:   []string{"pkg-1"},
	}
	assert.NoError(t, ValidateContextRequest(req))
}

func TestValidateContextRequest_MissingFields(t *testing.T) {
	tests := []struct {
		name string
		req  tmproto.ContextMatchRequest
	}{
		{"missing request_id", tmproto.ContextMatchRequest{PropertyID: "p", PlacementID: "pl", PackageIDs: []string{"a"}}},
		{"missing property_id", tmproto.ContextMatchRequest{RequestID: "r", PlacementID: "pl", PackageIDs: []string{"a"}}},
		{"missing placement_id", tmproto.ContextMatchRequest{RequestID: "r", PropertyID: "p", PackageIDs: []string{"a"}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Error(t, ValidateContextRequest(&tt.req))
		})
	}
}

func TestValidateIdentityRequest_Valid(t *testing.T) {
	req := &tmproto.IdentityMatchRequest{
		Type:           tmproto.TypeIdentityMatchRequest,
		RequestID:      "id-001",
		SellerAgentURL: "https://seller.example.com/agent",
		Identities:     []tmproto.IdentityToken{{UserToken: "tok_abc", UIDType: tmproto.UIDTypeUID2}},
		PackageIDs:     []string{"pkg-1", "pkg-2"},
	}
	assert.NoError(t, ValidateIdentityRequest(req))
}

func TestProviderFiltering_PropertyID(t *testing.T) {
	provider := &ProviderConfig{
		ContextMatch: true,
		PropertyIDs:  []string{"pub-oakwood-*"},
	}

	match := &tmproto.ContextMatchRequest{PropertyID: "pub-oakwood-main", PropertyType: "website", PackageIDs: []string{"a"}}
	noMatch := &tmproto.ContextMatchRequest{PropertyID: "pub-other-site", PropertyType: "website", PackageIDs: []string{"a"}}

	assert.True(t, MatchesContextProvider(match, provider), "should match pub-oakwood-main")
	assert.False(t, MatchesContextProvider(noMatch, provider), "should not match pub-other-site")
}

func TestProviderFiltering_ExcludeProperty(t *testing.T) {
	provider := &ProviderConfig{
		ContextMatch:       true,
		ExcludePropertyIDs: []string{"pub-blocked-*"},
	}

	req := &tmproto.ContextMatchRequest{PropertyID: "pub-blocked-123", PropertyType: "website", PackageIDs: []string{"a"}}
	assert.False(t, MatchesContextProvider(req, provider), "should be excluded")
}

func TestProviderFiltering_PropertyType(t *testing.T) {
	provider := &ProviderConfig{
		ContextMatch:  true,
		PropertyTypes: []string{"website", "ai_assistant"},
	}

	web := &tmproto.ContextMatchRequest{PropertyID: "p", PropertyType: "website", PackageIDs: []string{"a"}}
	ctv := &tmproto.ContextMatchRequest{PropertyID: "p", PropertyType: "ctv_app", PackageIDs: []string{"a"}}

	assert.True(t, MatchesContextProvider(web, provider), "should match website")
	assert.False(t, MatchesContextProvider(ctv, provider), "should not match ctv_app")
}

func TestMergeContextResponses(t *testing.T) {
	r1 := &tmproto.ContextMatchResponse{
		Offers: []tmproto.Offer{{PackageID: "pkg-1"}},
		Signals: map[string]any{
			"segments": []string{"cooking"},
			"adcp_pkg": "pkg-1",
		},
	}
	r2 := &tmproto.ContextMatchResponse{
		Offers: []tmproto.Offer{{PackageID: "pkg-2"}, {PackageID: "pkg-3"}},
		Signals: map[string]any{
			"segments": []string{"sustainability"},
		},
	}

	merged := mergeContextResponses("ctx-test", []*tmproto.ContextMatchResponse{r1, r2})

	assert.Len(t, merged.Offers, 3)
	assert.NotNil(t, merged.Signals)
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
	assert.True(t, eligible["pkg-1"], "pkg-1 should be eligible (both providers include it)")
	assert.True(t, eligible["pkg-2"], "pkg-2 should be eligible (listed by its provider)")
	assert.True(t, eligible["pkg-3"], "pkg-3 should be eligible (listed by its provider)")
	assert.Len(t, merged.EligiblePackageIDs, 3)

	// TTL is the minimum across providers.
	assert.Equal(t, 300, merged.TTLSec)
}

func TestRouterContextMatch_EndToEnd(t *testing.T) {
	// Mock provider that activates pkg-1
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(tmproto.ContextMatchResponse{
			RequestID: "ctx-e2e",
			Offers:    []tmproto.Offer{{PackageID: "pkg-1"}},
			Signals: map[string]any{
				"adcp_pkg": "pkg-1",
			},
		})
	}))
	defer provider.Close()

	router := testRouter([]ProviderConfig{
		{ID: "test-provider", Endpoint: provider.URL, ContextMatch: true, Timeout: 5 * time.Second},
	})

	reqBody := `{
		"type": "context_match_request",
		"request_id": "ctx-e2e",
		"property_id": "pub-test",
		"property_type": "website",
		"placement_id": "sidebar",
		"package_ids": ["pkg-1"]
	}`

	w := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/tmp/context", strings.NewReader(reqBody))
	router.HandleContextMatch(w, req)

	require.Equal(t, 200, w.Code, w.Body.String())

	var resp tmproto.ContextMatchResponse
	_ = json.NewDecoder(w.Body).Decode(&resp)

	require.Len(t, resp.Offers, 1)
	assert.Equal(t, "pkg-1", resp.Offers[0].PackageID)
}

func TestRouterContextMatch_StripsArtifactAccess(t *testing.T) {
	var receivedBody []byte
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedBody, _ = io.ReadAll(r.Body)
		_ = json.NewEncoder(w).Encode(tmproto.ContextMatchResponse{RequestID: "ctx-strip"})
	}))
	defer provider.Close()

	router := testRouter([]ProviderConfig{
		{ID: "p", Endpoint: provider.URL, ContextMatch: true, Timeout: 5 * time.Second},
	})

	cm := tmproto.ContextMatchRequest{
		Type:         tmproto.TypeContextMatchRequest,
		RequestID:    "ctx-strip",
		PropertyID:   "pub-test",
		PropertyType: "website",
		PlacementID:  "main",
		PackageIDs:   []string{"pkg-1"},
		Artifact: &tmproto.Artifact{
			Assets: tmproto.Assets{
				func() *tmproto.ImageAsset {
					access := tmproto.NewBearerTokenAccess("secret-bearer-token")
					return &tmproto.ImageAsset{
						URL:    "https://cdn.example.com/img.jpg",
						Access: &access,
					}
				}(),
			},
		},
	}
	body, _ := json.Marshal(&cm)

	w := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/tmp/context", bytes.NewReader(body))
	router.HandleContextMatch(w, req)

	require.Equal(t, 200, w.Code, w.Body.String())
	require.NotEmpty(t, receivedBody)
	assert.NotContains(t, string(receivedBody), "secret-bearer-token", "router must strip Access fields before fan-out")
	assert.NotContains(t, string(receivedBody), "bearer_token", "stripped Access should leave no trace in the forwarded body")
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

	router := testRouter([]ProviderConfig{
		{ID: "test-provider", Endpoint: provider.URL, IdentityMatch: true, Timeout: 5 * time.Second},
	})

	reqBody := `{
		"type": "identity_match_request",
		"request_id": "id-e2e",
		"seller_agent_url": "https://seller.example.com/agent",
		"identities": [{"user_token": "tok_test_abc", "uid_type": "uid2"}],
		"package_ids": ["pkg-1", "pkg-2", "pkg-3"]
	}`

	w := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/tmp/identity", strings.NewReader(reqBody))
	router.HandleIdentityMatch(w, req)

	require.Equal(t, 200, w.Code, w.Body.String())

	var resp tmproto.IdentityMatchResponse
	_ = json.NewDecoder(w.Body).Decode(&resp)

	require.Len(t, resp.EligiblePackageIDs, 1)
	assert.Equal(t, "pkg-1", resp.EligiblePackageIDs[0])
	assert.Equal(t, 300, resp.TTLSec)
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
		Identities: []tmproto.IdentityToken{{UserToken: "tok", UIDType: tmproto.UIDTypeUID2}},
		PackageIDs: []string{"pkg-1"},
		Country:    "US",
	}
	deReq := &tmproto.IdentityMatchRequest{
		RequestID:  "id-de",
		Identities: []tmproto.IdentityToken{{UserToken: "tok", UIDType: tmproto.UIDTypeEUID}},
		PackageIDs: []string{"pkg-1"},
		Country:    "DE",
	}

	assert.True(t, MatchesIdentityProvider(usReq, usProvider), "US request should match US provider")
	assert.False(t, MatchesIdentityProvider(usReq, euProvider), "US request should not match EU provider")
	assert.False(t, MatchesIdentityProvider(deReq, usProvider), "DE request should not match US provider")
	assert.True(t, MatchesIdentityProvider(deReq, euProvider), "DE request should match EU provider")
}

func TestIdentityFiltering_UIDType(t *testing.T) {
	provider := &ProviderConfig{
		ID:            "buyer-uid2-only",
		IdentityMatch: true,
		UIDTypes:      []string{"uid2"},
	}

	uid2Req := &tmproto.IdentityMatchRequest{
		RequestID:  "id-1",
		Identities: []tmproto.IdentityToken{{UserToken: "tok", UIDType: tmproto.UIDTypeUID2}},
		PackageIDs: []string{"pkg-1"},
	}
	euidReq := &tmproto.IdentityMatchRequest{
		RequestID:  "id-2",
		Identities: []tmproto.IdentityToken{{UserToken: "tok", UIDType: tmproto.UIDTypeEUID}},
		PackageIDs: []string{"pkg-1"},
	}

	assert.True(t, MatchesIdentityProvider(uid2Req, provider), "uid2 request should match uid2-only provider")
	assert.False(t, MatchesIdentityProvider(euidReq, provider), "euid request should not match uid2-only provider")
}

func TestIdentityFiltering_NoFilters(t *testing.T) {
	provider := &ProviderConfig{
		ID:            "legacy-provider",
		IdentityMatch: true,
	}
	req := &tmproto.IdentityMatchRequest{
		RequestID:  "id-1",
		Identities: []tmproto.IdentityToken{{UserToken: "tok", UIDType: tmproto.UIDTypeUID2}},
		PackageIDs: []string{"pkg-1"},
		Country:    "US",
	}

	assert.True(t, MatchesIdentityProvider(req, provider), "provider with no country/uid_type filters should match all requests")
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

	router := testRouter([]ProviderConfig{
		{ID: "test-provider", Endpoint: provider.URL, IdentityMatch: true, Countries: []string{"US"}, UIDTypes: []string{"uid2"}, Timeout: 5 * time.Second},
	})

	reqBody := `{
		"type": "identity_match_request",
		"request_id": "id-strip",
		"seller_agent_url": "https://seller.example.com/agent",
		"identities": [{"user_token": "tok_test", "uid_type": "uid2"}],
		"package_ids": ["pkg-1"],
		"country": "US"
	}`

	w := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/tmp/identity", strings.NewReader(reqBody))
	router.HandleIdentityMatch(w, req)

	require.Equal(t, 200, w.Code, w.Body.String())

	// Verify country was stripped from forwarded request.
	var forwarded tmproto.IdentityMatchRequest
	_ = json.Unmarshal(receivedBody, &forwarded)
	assert.Empty(t, forwarded.Country, "country should be stripped before forwarding")

	// Verify TMPX token is passed through.
	var resp tmproto.IdentityMatchResponse
	_ = json.NewDecoder(w.Body).Decode(&resp)
	assert.Equal(t, "k1.dGVzdC10b2tlbg", resp.Tmpx)
}

func TestMergeIdentityResponses_Eligibility(t *testing.T) {
	r1 := &tmproto.IdentityMatchResponse{
		EligiblePackageIDs: []string{"pkg-1", "pkg-3"},
		TTLSec:             300,
	}
	r2 := &tmproto.IdentityMatchResponse{
		EligiblePackageIDs: []string{"pkg-2"},
		TTLSec:             600,
	}

	merged := mergeIdentityResponses("test", []string{"acme", "nova"}, []*tmproto.IdentityMatchResponse{r1, r2})

	require.Len(t, merged.EligiblePackageIDs, 3)
	assert.Equal(t, 300, merged.TTLSec)
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

	router := testRouter([]ProviderConfig{
		{ID: "slow", Endpoint: slowProvider.URL, ContextMatch: true, Timeout: 10 * time.Millisecond},
		{ID: "fast", Endpoint: fastProvider.URL, ContextMatch: true, Timeout: 5 * time.Second},
	})

	reqBody := `{
		"type": "context_match_request",
		"request_id": "ctx-timeout",
		"property_id": "pub-test",
		"property_type": "website",
		"placement_id": "sidebar",
		"package_ids": ["pkg-1"]
	}`

	w := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/tmp/context", strings.NewReader(reqBody))
	router.HandleContextMatch(w, req)

	var resp tmproto.ContextMatchResponse
	_ = json.NewDecoder(w.Body).Decode(&resp)

	// Should only have the fast provider's offer
	require.Len(t, resp.Offers, 1)
	assert.Equal(t, "pkg-fast", resp.Offers[0].PackageID)
}
