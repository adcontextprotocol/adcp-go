package router

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
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
		Type:           tmproto.TypeContextMatchRequest,
		RequestID:      "ctx-001",
		PropertyRID:    "rid-1001",
		PropertyID:     "pub-oakwood",
		PropertyType:   tmproto.PropertyTypeWebsite,
		PlacementID:    "sidebar-300x250",
		SellerAgentURL: "https://seller.example.com/agent",
		PackageIDs:     []string{"pkg-1"},
	}
	assert.NoError(t, ValidateContextRequest(req))
}

func TestValidateContextRequest_MissingFields(t *testing.T) {
	tests := []struct {
		name string
		req  tmproto.ContextMatchRequest
	}{
		{"missing request_id", tmproto.ContextMatchRequest{PropertyRID: "rid", PropertyID: "p", PlacementID: "pl", PackageIDs: []string{"a"}}},
		{"missing property_rid", tmproto.ContextMatchRequest{RequestID: "r", PropertyID: "p", PlacementID: "pl", PackageIDs: []string{"a"}}},
		{"missing placement_id", tmproto.ContextMatchRequest{RequestID: "r", PropertyRID: "rid", PropertyID: "p", PackageIDs: []string{"a"}}},
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

	merged := mergeContextResponses("ctx-test", []contextResult{
		{providerID: "p1", response: r1},
		{providerID: "p2", response: r2},
	}, nil)

	assert.Len(t, merged.Offers, 3)
	assert.NotNil(t, merged.Signals)
}

func TestMergeIdentityResponses(t *testing.T) {
	// Provider 1 says pkg-1 and pkg-3 are eligible.
	r1 := &tmproto.IdentityMatchResponse{
		EligiblePackageIDs: []string{"pkg-1", "pkg-3"},
		ServeWindowSec:     300,
	}
	// Provider 2 says pkg-1 and pkg-2 are eligible.
	r2 := &tmproto.IdentityMatchResponse{
		EligiblePackageIDs: []string{"pkg-1", "pkg-2"},
		ServeWindowSec:     600,
	}

	merged := mergeIdentityResponses("id-test", []string{"p1", "p2"}, []*tmproto.IdentityMatchResponse{r1, r2}, nil)

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

	// Serve window is the minimum across providers so the merged response keeps
	// the most restrictive buyer throttle.
	assert.Equal(t, 300, merged.ServeWindowSec)
}

// TestMergeIdentityResponses_TmpxProvidersFromNewShape verifies that each
// agent's TmpxMacros[] is folded into the merged TmpxProviders map keyed by
// provider_id, preserving per-provider attribution across fan-out. Legacy
// `tmpx` stays populated for back-compat with consumers that haven't moved
// to the new shape, sourced from the first provider's first macro value.
func TestMergeIdentityResponses_TmpxProvidersFromNewShape(t *testing.T) {
	r1 := &tmproto.IdentityMatchResponse{
		EligiblePackageIDs: []string{"pkg-1"},
		ServeWindowSec:     300,
		TmpxMacros: []tmproto.TmpxMacro{
			{Name: "PIN_TMPX_1", Value: "k1.alpha-value"},
		},
	}
	r2 := &tmproto.IdentityMatchResponse{
		EligiblePackageIDs: []string{"pkg-2"},
		ServeWindowSec:     300,
		TmpxMacros: []tmproto.TmpxMacro{
			{Name: "NOVA_TMPX_1", Value: "k2.beta-value"},
		},
	}

	merged := mergeIdentityResponses("id-tmpx", []string{"pinnacle_id", "nova_id"},
		[]*tmproto.IdentityMatchResponse{r1, r2}, nil)

	require.NotNil(t, merged.TmpxProviders, "tmpx_providers MUST be populated when any agent emitted tmpx_macros")
	require.Len(t, merged.TmpxProviders, 2)

	pin, ok := merged.TmpxProviders["pinnacle_id"]
	require.True(t, ok, "pinnacle_id entry must exist")
	require.Len(t, pin.Macros, 1)
	assert.Equal(t, "PIN_TMPX_1", pin.Macros[0].Name)
	assert.Equal(t, "k1.alpha-value", pin.Macros[0].Value)

	nova, ok := merged.TmpxProviders["nova_id"]
	require.True(t, ok, "nova_id entry must exist")
	assert.Equal(t, "NOVA_TMPX_1", nova.Macros[0].Name)
	assert.Equal(t, "k2.beta-value", nova.Macros[0].Value)

	// Legacy carrier is the first provider's first slot, so back-compat
	// consumers receive a non-empty value without per-provider context.
	assert.Equal(t, "k1.alpha-value", merged.Tmpx,
		"legacy tmpx must mirror the first provider's first slot for back-compat")
}

// TestMergeIdentityResponses_LegacyOnlyAgent covers a transition case: an
// agent that only emits the deprecated `tmpx` string with no TmpxMacros[].
// The router preserves it in the legacy field but does NOT synthesize a
// tmpx_providers entry — the router doesn't have the registered macro names
// in this code path and shouldn't invent them.
func TestMergeIdentityResponses_LegacyOnlyAgent(t *testing.T) {
	legacy := &tmproto.IdentityMatchResponse{
		EligiblePackageIDs: []string{"pkg-1"},
		ServeWindowSec:     300,
		Tmpx:               "k1.legacy-value",
	}

	merged := mergeIdentityResponses("id-legacy", []string{"legacy_provider"},
		[]*tmproto.IdentityMatchResponse{legacy}, nil)

	assert.Empty(t, merged.TmpxProviders, "no TmpxMacros[] emitted → no tmpx_providers entry")
	assert.Equal(t, "k1.legacy-value", merged.Tmpx, "legacy carrier preserved")
}

// TestMergeIdentityResponses_MixedShapeAgents covers a transition fan-out
// where one agent emits the new TmpxMacros[] carrier and another only emits
// the legacy `tmpx` string. The merged response MUST:
//   - populate tmpx_providers with the new-shape agent's entry only
//     (legacy-only agents don't get synthesized — router has no registered
//     names in that path)
//   - source the legacy `Tmpx` carrier from the first response that has a
//     non-empty value (first-source-wins, in input order), which pins the
//     back-compat behavior when responses arrive in mixed order: a
//     legacy-only agent that sorts ahead does NOT lose to a new-shape agent
//     that sorts behind it. The deprecated carrier is best-effort
//     compatibility; tmpx_providers is the authoritative new shape.
func TestMergeIdentityResponses_MixedShapeAgents(t *testing.T) {
	legacyAgent := &tmproto.IdentityMatchResponse{
		EligiblePackageIDs: []string{"pkg-1"},
		ServeWindowSec:     300,
		Tmpx:               "k0.legacy-string",
	}
	newShapeAgent := &tmproto.IdentityMatchResponse{
		EligiblePackageIDs: []string{"pkg-2"},
		ServeWindowSec:     300,
		TmpxMacros: []tmproto.TmpxMacro{
			{Name: "PIN_TMPX_1", Value: "k1.new-shape-value"},
		},
	}

	merged := mergeIdentityResponses("id-mixed",
		[]string{"legacy_provider", "new_provider"},
		[]*tmproto.IdentityMatchResponse{legacyAgent, newShapeAgent}, nil)

	require.Len(t, merged.TmpxProviders, 1,
		"only the new-shape agent contributes to tmpx_providers")
	entry, ok := merged.TmpxProviders["new_provider"]
	require.True(t, ok)
	assert.Equal(t, "k1.new-shape-value", entry.Macros[0].Value)

	// Legacy-only agent sorts ahead → its legacy string wins the legacy
	// mirror, even though a later agent has a new-shape value. Pins the
	// first-source-wins contract for the deprecated carrier.
	assert.Equal(t, "k0.legacy-string", merged.Tmpx,
		"legacy carrier is first-source-wins across mixed-shape responses")
}

// TestMergeContextResponses_DuplicatePackageID covers the dedup-warn path the
// router-architecture spec calls out: same package_id from two providers MUST
// keep the first response and SHOULD log a warning naming both providers.
func TestMergeContextResponses_DuplicatePackageID(t *testing.T) {
	r1 := &tmproto.ContextMatchResponse{
		Offers: []tmproto.Offer{{PackageID: "pkg-dup", Summary: "first"}},
	}
	r2 := &tmproto.ContextMatchResponse{
		Offers: []tmproto.Offer{{PackageID: "pkg-dup", Summary: "second"}, {PackageID: "pkg-2", Summary: "unique"}},
	}

	var logs bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logs, nil))

	merged := mergeContextResponses("ctx-dup", []contextResult{
		{providerID: "alpha", response: r1},
		{providerID: "beta", response: r2},
	}, logger)

	require.Len(t, merged.Offers, 2, "duplicate package_id should be deduped, unique one kept")
	assert.Equal(t, "first", merged.Offers[0].Summary, "first provider's offer wins on dup")

	logText := logs.String()
	assert.Contains(t, logText, "duplicate package_id across providers")
	assert.Contains(t, logText, `"first_provider":"alpha"`)
	assert.Contains(t, logText, `"duplicate_provider":"beta"`)
	assert.Contains(t, logText, `"package_id":"pkg-dup"`)
}

// TestMergeContextResponses_SingleProviderRepeat covers the within-response
// repeat case: a single provider that returns the same package_id twice in
// its own offers list. The warning names the provider once rather than
// emitting the misleading "alpha duplicated alpha" cross-provider message.
func TestMergeContextResponses_SingleProviderRepeat(t *testing.T) {
	r1 := &tmproto.ContextMatchResponse{
		Offers: []tmproto.Offer{
			{PackageID: "pkg-1", Summary: "first"},
			{PackageID: "pkg-1", Summary: "second"},
		},
	}

	var logs bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logs, nil))

	merged := mergeContextResponses("ctx-single-dup", []contextResult{
		{providerID: "alpha", response: r1},
	}, logger)

	require.Len(t, merged.Offers, 1)
	logText := logs.String()
	assert.Contains(t, logText, "repeated package_id within a single provider's response")
	assert.Contains(t, logText, `"provider":"alpha"`)
	assert.NotContains(t, logText, "first_provider", "should not use the cross-provider warning shape")
}

// TestMergeIdentityResponses_LogsDuplicateWarning covers the warn-on-dup
// path the spec mandates for identity match. Two providers list the same
// package_id; eligibility is the union (per the spec's in-repo authority on
// union merging — packages are provider-specific), but the warning names
// both providers so misconfig is observable.
func TestMergeIdentityResponses_LogsDuplicateWarning(t *testing.T) {
	r1 := &tmproto.IdentityMatchResponse{EligiblePackageIDs: []string{"pkg-dup", "pkg-1"}}
	r2 := &tmproto.IdentityMatchResponse{EligiblePackageIDs: []string{"pkg-dup", "pkg-2"}}

	var logs bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logs, nil))

	merged := mergeIdentityResponses("id-dup", []string{"alpha", "beta"},
		[]*tmproto.IdentityMatchResponse{r1, r2}, logger)

	require.Len(t, merged.EligiblePackageIDs, 3, "union eligibility — all listed packages remain eligible")
	logText := logs.String()
	assert.Contains(t, logText, "duplicate package_id across providers' identity-match responses")
	assert.Contains(t, logText, `"package_id":"pkg-dup"`)
	assert.Contains(t, logText, `"providers":["alpha","beta"]`)
	// Non-duplicates do not generate warnings.
	assert.NotContains(t, logText, `"package_id":"pkg-1"`)
	assert.NotContains(t, logText, `"package_id":"pkg-2"`)
}

// recordingMetrics is a FanOutMetricsExt impl that captures every callback
// invocation for assertion. Safe for parallel fan-out calls.
type recordingMetrics struct {
	mu              sync.Mutex
	excluded        []string
	durations       []durationSample
	timeoutInc      []string
	errorInc        []string
	offersTotal     int
}

type durationSample struct {
	provider string
	ms       float64
}

func (m *recordingMetrics) IncExcluded(providerID string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.excluded = append(m.excluded, providerID)
}

func (m *recordingMetrics) ObserveProviderDuration(providerID string, ms float64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.durations = append(m.durations, durationSample{provider: providerID, ms: ms})
}

func (m *recordingMetrics) IncProviderTimeout(providerID string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.timeoutInc = append(m.timeoutInc, providerID)
}

func (m *recordingMetrics) IncProviderError(providerID string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.errorInc = append(m.errorInc, providerID)
}

func (m *recordingMetrics) AddOffers(n int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.offersTotal += n
}

// TestFanOut_ObservesDurationOnAllTerminalOutcomes verifies that
// tmp_provider_duration_ms reflects the slow-failure tail, not just
// successful legs. The spec's §Monitoring table calls this "per-provider
// response time" with no success qualifier; observing only successes
// truncates p95/p99 silently when a provider hangs to its deadline.
func TestFanOut_ObservesDurationOnAllTerminalOutcomes(t *testing.T) {
	// A 500-responder counts as an error (non-timeout, non-cancel).
	errSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer errSrv.Close()

	// A slow responder that exceeds the per-provider deadline.
	timeoutSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(50 * time.Millisecond)
		_ = json.NewEncoder(w).Encode(tmproto.ContextMatchResponse{RequestID: "ctx-dur"})
	}))
	defer timeoutSrv.Close()

	rec := &recordingMetrics{}
	router := testRouter([]ProviderConfig{
		{ID: "err-prov", Endpoint: errSrv.URL, ContextMatch: true, Timeout: 5 * time.Second},
		{ID: "timeout-prov", Endpoint: timeoutSrv.URL, ContextMatch: true, Timeout: 5 * time.Millisecond},
	})
	router.metrics = rec

	reqBody := `{
		"type": "context_match_request",
		"request_id": "ctx-dur",
		"property_rid": "rid-1001",
		"property_id": "pub-test",
		"property_type": "website",
		"placement_id": "sidebar",
		"seller_agent_url": "https://seller.example.com/agent",
		"package_ids": ["pkg-1"]
	}`
	w := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/tmp/context", strings.NewReader(reqBody))
	router.HandleContextMatch(w, req)

	rec.mu.Lock()
	defer rec.mu.Unlock()
	require.Len(t, rec.durations, 2, "both error and timeout legs MUST contribute duration samples")

	durByProvider := map[string]float64{}
	for _, d := range rec.durations {
		durByProvider[d.provider] = d.ms
	}
	assert.Contains(t, durByProvider, "err-prov", "non-timeout error leg observes duration")
	assert.Contains(t, durByProvider, "timeout-prov", "timeout leg observes duration")
	assert.Contains(t, rec.errorInc, "err-prov")
	assert.Contains(t, rec.timeoutInc, "timeout-prov")
}

// TestFanOut_ParentCancelRecordsNothing verifies the privacy-correct
// behavior introduced when classifyCallFailure split parent-cancel from
// per-provider timeout: a parent-context cancellation (client disconnect,
// server drain) MUST NOT count against the provider — neither timeout,
// error, nor duration. The provider isn't slow; the router gave up.
//
// Use a pre-cancelled request context so the fan-out's HTTP call aborts
// before any response could be produced. This is the same path a real
// client-disconnect or server-drain would take: req.Context() is Done
// before callProvider runs.
func TestFanOut_ParentCancelRecordsNothing(t *testing.T) {
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(tmproto.ContextMatchResponse{RequestID: "ctx-cancel"})
	}))
	defer provider.Close()

	rec := &recordingMetrics{}
	router := testRouter([]ProviderConfig{
		{ID: "cancel-prov", Endpoint: provider.URL, ContextMatch: true, Timeout: 5 * time.Second},
	})
	router.metrics = rec

	parentCtx, cancel := context.WithCancel(context.Background())
	cancel() // already cancelled before the fan-out begins

	reqBody := `{
		"type": "context_match_request",
		"request_id": "ctx-cancel",
		"property_rid": "rid-1001",
		"property_id": "pub-test",
		"property_type": "website",
		"placement_id": "sidebar",
		"seller_agent_url": "https://seller.example.com/agent",
		"package_ids": ["pkg-1"]
	}`
	req := httptest.NewRequestWithContext(parentCtx, "POST", "/tmp/context", strings.NewReader(reqBody))

	w := httptest.NewRecorder()
	router.HandleContextMatch(w, req)

	rec.mu.Lock()
	defer rec.mu.Unlock()
	assert.Empty(t, rec.timeoutInc, "parent-cancel MUST NOT count against tmp_provider_timeout_total")
	assert.Empty(t, rec.errorInc, "parent-cancel MUST NOT count against tmp_provider_error_total")
	assert.Empty(t, rec.durations, "parent-cancel MUST NOT contribute a duration sample — provider attribution is wrong")
}

// TestMergeIdentityResponses_SingleProviderRepeat covers a provider that
// returns the same package_id twice in its own eligible list. Eligible
// arrives raw off the wire with no dedup, so this is reachable. The
// warning MUST name a within-provider repeat — not the cross-provider
// duplicate, which would otherwise log
// `"providers":["alpha","alpha"]` (mirrors the Context-path split).
func TestMergeIdentityResponses_SingleProviderRepeat(t *testing.T) {
	r1 := &tmproto.IdentityMatchResponse{EligiblePackageIDs: []string{"pkg-repeat", "pkg-repeat", "pkg-1"}}

	var logs bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logs, nil))

	merged := mergeIdentityResponses("id-self-dup", []string{"alpha"},
		[]*tmproto.IdentityMatchResponse{r1}, logger)

	// Eligibility is built from a set, so the merged response is still correct.
	require.Len(t, merged.EligiblePackageIDs, 2, "self-repeat dedups in the eligible set")

	logText := logs.String()
	assert.Contains(t, logText, "repeated package_id within a single provider's identity-match response")
	assert.Contains(t, logText, `"package_id":"pkg-repeat"`)
	assert.Contains(t, logText, `"provider":"alpha"`)
	// Crucially, the cross-provider warning MUST NOT fire — it would emit
	// `"providers":["alpha","alpha"]`, which is the bug this test guards.
	assert.NotContains(t, logText, "duplicate package_id across providers' identity-match responses")
	assert.NotContains(t, logText, `["alpha","alpha"]`)
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
		"property_rid": "rid-1001",
		"property_id": "pub-test",
		"property_type": "website",
		"placement_id": "sidebar",
		"seller_agent_url": "https://seller.example.com/agent",
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

func TestRouterContextMatch_ValidationErrorIsGenericAndLogged(t *testing.T) {
	var logs bytes.Buffer
	router := testRouter(nil)
	router.logger = slog.New(slog.NewJSONHandler(&logs, nil))

	reqBody := `{
		"type": "context_match_request",
		"request_id": "ctx-invalid",
		"property_rid": "rid-1001",
		"property_id": "bad:property",
		"property_type": "website",
		"placement_id": "sidebar",
		"package_ids": ["pkg-1"]
	}`

	w := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/tmp/context", strings.NewReader(reqBody))
	router.HandleContextMatch(w, req)

	require.Equal(t, http.StatusBadRequest, w.Code, w.Body.String())
	var resp tmproto.ErrorResponse
	require.NoError(t, json.NewDecoder(w.Body).Decode(&resp))
	assert.Equal(t, tmproto.ErrorCodeInvalidRequest, resp.Code)
	assert.Equal(t, "ctx-invalid", resp.RequestID)
	assert.Equal(t, "invalid request", resp.Message)
	assert.NotContains(t, resp.Message, "property_id")

	logText := logs.String()
	assert.Contains(t, logText, "invalid context-match request")
	assert.Contains(t, logText, `"method":"POST"`)
	assert.Contains(t, logText, `"path":"/tmp/context"`)
	assert.Contains(t, logText, "ctx-invalid")
	assert.Contains(t, logText, "property_id contains invalid characters")
}

func TestRouterIdentityMatch_InvalidRequestIDIsNotEchoed(t *testing.T) {
	var logs bytes.Buffer
	router := testRouter(nil)
	router.logger = slog.New(slog.NewJSONHandler(&logs, nil))

	reqBody := `{
		"type": "identity_match_request",
		"request_id": "bad/id",
		"seller_agent_url": "https://seller.example.com/agent",
		"identities": [{"user_token": "tok_test_abc", "uid_type": "uid2"}],
		"package_ids": ["pkg-1"]
	}`

	w := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/tmp/identity", strings.NewReader(reqBody))
	router.HandleIdentityMatch(w, req)

	require.Equal(t, http.StatusBadRequest, w.Code, w.Body.String())
	var resp tmproto.ErrorResponse
	require.NoError(t, json.NewDecoder(w.Body).Decode(&resp))
	assert.Empty(t, resp.RequestID)
	assert.Equal(t, "invalid request", resp.Message)
	assert.NotContains(t, w.Body.String(), "bad/id")

	logText := logs.String()
	assert.Contains(t, logText, "invalid identity-match request")
	assert.Contains(t, logText, `"method":"POST"`)
	assert.Contains(t, logText, `"path":"/tmp/identity"`)
	assert.Contains(t, logText, `"request_id_valid":false`)
	assert.NotContains(t, logText, "bad/id")
}

func TestRouterContextMatch_LongRequestIDIsNotEchoed(t *testing.T) {
	var logs bytes.Buffer
	router := testRouter(nil)
	router.logger = slog.New(slog.NewJSONHandler(&logs, nil))

	longID := strings.Repeat("a", tmproto.MaxIDLength+1)
	body, err := json.Marshal(tmproto.ContextMatchRequest{
		Type:         tmproto.TypeContextMatchRequest,
		RequestID:    longID,
		PropertyID:   "pub-test",
		PropertyType: tmproto.PropertyTypeWebsite,
		PlacementID:  "sidebar",
		PackageIDs:   []string{"pkg-1"},
	})
	require.NoError(t, err)

	w := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/tmp/context", bytes.NewReader(body))
	router.HandleContextMatch(w, req)

	require.Equal(t, http.StatusBadRequest, w.Code, w.Body.String())
	var resp tmproto.ErrorResponse
	require.NoError(t, json.NewDecoder(w.Body).Decode(&resp))
	assert.Empty(t, resp.RequestID)
	assert.Equal(t, "invalid request", resp.Message)
	assert.NotContains(t, w.Body.String(), longID)

	logText := logs.String()
	assert.Contains(t, logText, `"request_id_valid":false`)
	assert.NotContains(t, logText, longID)
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
		Type:           tmproto.TypeContextMatchRequest,
		RequestID:      "ctx-strip",
		PropertyRID:    "rid-1001",
		PropertyID:     "pub-test",
		PropertyType:   "website",
		PlacementID:    "main",
		SellerAgentURL: "https://seller.example.com/agent",
		PackageIDs:     []string{"pkg-1"},
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
			ServeWindowSec:     300,
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
	assert.Equal(t, 300, resp.ServeWindowSec)
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
			ServeWindowSec:     60,
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
		ServeWindowSec:     300,
	}
	r2 := &tmproto.IdentityMatchResponse{
		EligiblePackageIDs: []string{"pkg-2"},
		ServeWindowSec:     600,
	}

	merged := mergeIdentityResponses("test", []string{"acme", "nova"}, []*tmproto.IdentityMatchResponse{r1, r2}, nil)

	require.Len(t, merged.EligiblePackageIDs, 3)
	assert.Equal(t, 300, merged.ServeWindowSec)
}

func TestMergeIdentityResponses_UsesMostRestrictiveServeWindow(t *testing.T) {
	merged := mergeIdentityResponses("test", []string{"acme", "nova"}, []*tmproto.IdentityMatchResponse{
		{EligiblePackageIDs: []string{"pkg-1"}, ServeWindowSec: 120},
		{EligiblePackageIDs: []string{"pkg-2"}, ServeWindowSec: 45},
		{EligiblePackageIDs: []string{"pkg-3"}, ServeWindowSec: 300},
	}, nil)

	assert.Equal(t, 45, merged.ServeWindowSec)
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
		"property_rid": "rid-1001",
		"property_id": "pub-test",
		"property_type": "website",
		"placement_id": "sidebar",
		"seller_agent_url": "https://seller.example.com/agent",
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
