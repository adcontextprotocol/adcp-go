package router

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/adcontextprotocol/adcp-go/tmproto"
)

const identityRequestBody = `{
	"type": "identity_match_request",
	"request_id": "id-contract",
	"seller_agent_url": "https://seller.example.com/agent",
	"identities": [{"user_token": "tok_test_abc", "uid_type": "uid2"}],
	"package_ids": ["pkg-1"]
}`

// postIdentity drives HandleIdentityMatch and decodes the publisher-facing
// response.
func postIdentity(t *testing.T, r *Router) tmproto.IdentityMatchResponse {
	t.Helper()
	w := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/tmp/identity", strings.NewReader(identityRequestBody))
	r.HandleIdentityMatch(w, req)
	require.Equal(t, 200, w.Code, w.Body.String())
	var resp tmproto.IdentityMatchResponse
	require.NoError(t, json.NewDecoder(w.Body).Decode(&resp))
	return resp
}

// TestMergeIdentityResponses_ServeWindowFlooredOnEmptyFanOut pins the schema
// floor from identity-match-response.json (serve_window_sec minimum 1). With
// no provider response there is no minimum to take, and the field is required
// — emitting 0 produced a schema-invalid response on the all-providers-failed
// path the spec explicitly calls out ("Return an empty response").
func TestMergeIdentityResponses_ServeWindowFlooredOnEmptyFanOut(t *testing.T) {
	merged := mergeIdentityResponses("id-empty", nil, nil)

	assert.Equal(t, MinServeWindowSec, merged.ServeWindowSec)
	assert.Empty(t, merged.EligiblePackageIDs)
	assert.Equal(t, tmproto.TypeIdentityMatchResponse, merged.Type)
}

// TestMergeIdentityResponses_ServeWindowClampedToSchemaRange covers a provider
// reporting a value outside [1, 300]. The router must not pass it through.
func TestMergeIdentityResponses_ServeWindowClampedToSchemaRange(t *testing.T) {
	tests := []struct {
		name     string
		provider int
		want     int
	}{
		{"below minimum", 0, MinServeWindowSec},
		{"negative", -5, MinServeWindowSec},
		{"above maximum", 600, MaxServeWindowSec},
		{"in range", 45, 45},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			merged := mergeIdentityResponses("id-clamp", []identityResult{{
				providerID: "p1",
				response: &tmproto.ProviderIdentityMatchResponse{
					EligiblePackageIDs: []string{"pkg-1"},
					ServeWindowSec:     tc.provider,
				},
			}}, nil)
			assert.Equal(t, tc.want, merged.ServeWindowSec)
		})
	}
}

// TestMergeIdentityResponses_WarnsOnOutOfRangeServeWindow pins that clamping is
// observable. The most restrictive value wins, so one provider reporting 0 — or
// omitting the required field — pins the publisher's window to 1s for every
// other provider on the same response. Clamping silently would make that look
// like correct behavior.
func TestMergeIdentityResponses_WarnsOnOutOfRangeServeWindow(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))

	merged := mergeIdentityResponses("id-warn", []identityResult{
		{providerID: "broken", response: &tmproto.ProviderIdentityMatchResponse{
			EligiblePackageIDs: []string{"pkg-1"},
			// serve_window_sec omitted on the wire decodes to 0.
		}},
		{providerID: "healthy", response: &tmproto.ProviderIdentityMatchResponse{
			EligiblePackageIDs: []string{"pkg-2"},
			ServeWindowSec:     300,
		}},
	}, logger)

	assert.Equal(t, MinServeWindowSec, merged.ServeWindowSec)
	assert.Contains(t, buf.String(), "serve_window_sec outside the schema range")
	assert.Contains(t, buf.String(), "provider=broken")
	assert.NotContains(t, buf.String(), "provider=healthy",
		"an in-range provider must not be warned about")
}

// TestSanitizeForLog covers both halves of making provider-supplied text safe to
// log: control bytes stripped so the value cannot forge a log record, and length
// bounded on a rune boundary so truncation cannot emit invalid UTF-8.
func TestSanitizeForLog(t *testing.T) {
	assert.Equal(t, "short", sanitizeForLog("short"))

	// Log-record forgery: a newline plus a fabricated record. Both the newline
	// and the ESC/CSI sequence must be gone.
	forged := "ok\n2026-08-06 level=ERROR msg=\"router key compromised\""
	got := sanitizeForLog(forged)
	assert.NotContains(t, got, "\n", "newlines must not survive into a log record")
	assert.Contains(t, got, "router key compromised", "the text itself is kept, only framing is stripped")

	for _, ctrl := range []string{"\r", "\t", "\x1b[31m", "\x07", "\x7f", "\x00"} {
		assert.NotContains(t, sanitizeForLog("a"+ctrl+"b"), ctrl)
	}

	exact := strings.Repeat("a", maxProviderMessageLog)
	assert.Equal(t, exact, sanitizeForLog(exact))

	long := strings.Repeat("a", maxProviderMessageLog+50)
	assert.Len(t, sanitizeForLog(long), maxProviderMessageLog)

	// "é" is two bytes, so the cut lands mid-rune without the boundary walk.
	multibyte := strings.Repeat("é", maxProviderMessageLog)
	cut := sanitizeForLog(multibyte)
	assert.True(t, utf8.ValidString(cut), "truncation must not produce invalid UTF-8")
	assert.LessOrEqual(t, len(cut), maxProviderMessageLog)
}

// TestRouterIdentityMatch_ProviderErrorEnvelopeExcluded pins the §Error
// Response rule ("The router SHOULD exclude providers that return errors from
// the merged response") against the wire shape §HTTP Status Codes defines for
// application errors: HTTP 200 carrying `{"type": "error", ...}`.
//
// Before the type discriminator was checked, that body decoded cleanly into an
// empty ProviderIdentityMatchResponse — so the provider looked like a
// successful "nothing is eligible" result AND its zero serve_window_sec won
// the min() across the fan-out, dragging the whole merged response below the
// schema floor.
func TestRouterIdentityMatch_ProviderErrorEnvelopeExcluded(t *testing.T) {
	failing := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(tmproto.ErrorResponse{
			Type:      tmproto.TypeError,
			RequestID: "id-contract",
			Code:      tmproto.ErrorCodeInternalError,
			Message:   "downstream store unavailable",
		})
	}))
	defer failing.Close()

	healthy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(tmproto.ProviderIdentityMatchResponse{
			Type:               tmproto.TypeIdentityMatchResponse,
			RequestID:          "id-contract",
			EligiblePackageIDs: []string{"pkg-1"},
			ServeWindowSec:     120,
		})
	}))
	defer healthy.Close()

	var buf bytes.Buffer
	metrics := &recordingMetrics{}
	r := testRouter([]ProviderConfig{
		{ID: "broken", Endpoint: failing.URL, IdentityMatch: true, Timeout: 5 * time.Second},
		{ID: "healthy", Endpoint: healthy.URL, IdentityMatch: true, Timeout: 5 * time.Second},
	})
	r.metrics = metrics
	r.logger = slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))

	resp := postIdentity(t, r)

	assert.Equal(t, []string{"pkg-1"}, resp.EligiblePackageIDs)
	assert.Equal(t, 120, resp.ServeWindowSec,
		"the erroring provider must not contribute its zero serve_window_sec to the min()")
	assert.Equal(t, []string{"broken"}, metrics.errorInc,
		"a TMP error envelope counts against the provider's error rate")
	assert.Contains(t, buf.String(), "provider returned TMP error")
	assert.Contains(t, buf.String(), "downstream store unavailable")
}

// TestRouterIdentityMatch_RejectsMismatchedResponseType covers a provider
// answering with the wrong message type entirely.
func TestRouterIdentityMatch_RejectsMismatchedResponseType(t *testing.T) {
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"type":                 tmproto.TypeContextMatchResponse,
			"request_id":           "id-contract",
			"eligible_package_ids": []string{"pkg-1"},
			"serve_window_sec":     60,
		})
	}))
	defer provider.Close()

	metrics := &recordingMetrics{}
	r := testRouter([]ProviderConfig{
		{ID: "confused", Endpoint: provider.URL, IdentityMatch: true, Timeout: 5 * time.Second},
	})
	r.metrics = metrics

	resp := postIdentity(t, r)

	assert.Empty(t, resp.EligiblePackageIDs)
	assert.Equal(t, MinServeWindowSec, resp.ServeWindowSec)
	assert.Equal(t, []string{"confused"}, metrics.errorInc)
}

// TestRouterIdentityMatch_ToleratesAbsentResponseType documents the deliberate
// leniency: `type` is schema-required on responses, but the spec puts no MUST
// on the router to police it, and the error envelope the check exists to catch
// always carries `type: "error"`. Rejecting an omitted type would drop
// otherwise well-formed responses from lenient providers.
func TestRouterIdentityMatch_ToleratesAbsentResponseType(t *testing.T) {
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"request_id":           "id-contract",
			"eligible_package_ids": []string{"pkg-1"},
			"serve_window_sec":     60,
		})
	}))
	defer provider.Close()

	r := testRouter([]ProviderConfig{
		{ID: "lenient", Endpoint: provider.URL, IdentityMatch: true, Timeout: 5 * time.Second},
	})

	resp := postIdentity(t, r)

	assert.Equal(t, []string{"pkg-1"}, resp.EligiblePackageIDs)
	assert.Equal(t, 60, resp.ServeWindowSec)
}

// TestRouterContextMatch_ProviderErrorEnvelopeExcluded is the context-path half
// of the type check: an error envelope must not be merged as a valid
// zero-offer response, and it must count against the provider's error rate.
func TestRouterContextMatch_ProviderErrorEnvelopeExcluded(t *testing.T) {
	failing := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(tmproto.ErrorResponse{
			Type:      tmproto.TypeError,
			RequestID: "ctx-contract",
			Code:      tmproto.ErrorCodeProviderUnavailable,
			Message:   "cold cache",
		})
	}))
	defer failing.Close()

	metrics := &recordingMetrics{}
	r := testRouter([]ProviderConfig{
		{ID: "broken", Endpoint: failing.URL, ContextMatch: true, Timeout: 5 * time.Second},
	})
	r.metrics = metrics

	body := `{
		"type": "context_match_request",
		"request_id": "ctx-contract",
		"property_rid": "rid-1001",
		"property_type": "website",
		"placement_id": "slot-1",
		"seller_agent_url": "https://seller.example.com/agent",
		"package_ids": ["pkg-1"]
	}`

	w := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/tmp/context", strings.NewReader(body))
	r.HandleContextMatch(w, req)
	require.Equal(t, 200, w.Code, w.Body.String())

	var resp tmproto.ContextMatchResponse
	require.NoError(t, json.NewDecoder(w.Body).Decode(&resp))

	assert.Empty(t, resp.Offers)
	assert.Equal(t, []string{"broken"}, metrics.errorInc)
}

// TestCheckResponseType_ErrorEnvelopeCarriesProviderCode pins that the
// provider's own error code and message survive onto the returned error, which
// is what makes the fan-out log actionable.
func TestCheckResponseType_ErrorEnvelopeCarriesProviderCode(t *testing.T) {
	body := []byte(`{"type":"error","code":"rate_limited","message":"slow down"}`)
	target := &tmproto.ProviderIdentityMatchResponse{Type: tmproto.TypeError}

	err := checkResponseType(target, body)

	var appErr *providerAppError
	require.ErrorAs(t, err, &appErr)
	assert.Equal(t, tmproto.ErrorCodeRateLimited, appErr.Code)
	assert.Equal(t, "slow down", appErr.Message)
	assert.Contains(t, err.Error(), "rate_limited")
}
