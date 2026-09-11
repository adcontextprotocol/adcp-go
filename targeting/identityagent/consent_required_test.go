package identityagent

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/adcontextprotocol/adcp-go/tmproto"
)

// TestIdentityHandler_ConsentRequired_RejectsAbsent pins the
// deployment-context consent gate: identity-match-request.json §consent
// says "Buyers in regulated jurisdictions MUST NOT process the user
// token without consent information", but the schema cannot express
// the jurisdiction. When RequireConsent is set (operator opt-in for
// CONSENT_REQUIRED=true) the handler MUST reject any request that
// omits the consent object with 400 invalid_request before any store
// lookup runs.
func TestIdentityHandler_ConsentRequired_RejectsAbsent(t *testing.T) {
	var logs bytes.Buffer
	h := NewIdentityHandler(IdentityHandlerConfig{
		RequestTimeout:             time.Second,
		RequestBodyLimit:           64 * 1024,
		ResponseTTL:                time.Minute,
		SupportedADCPMajorVersions: []int{3},
		RequireConsent:             true,
		Logger:                     slog.New(slog.NewJSONHandler(&logs, nil)),
	})
	body := `{
		"type": "identity_match_request",
		"request_id": "r1",
		"seller_agent_url": "https://seller.example.com/agent",
		"identities": [{"user_token": "tok", "uid_type": "uid2"}]
	}`
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/identity", strings.NewReader(body)))

	require.Equal(t, http.StatusBadRequest, w.Code)
	var resp tmproto.ErrorResponse
	require.NoError(t, json.NewDecoder(w.Body).Decode(&resp))
	assert.Equal(t, tmproto.ErrorCodeInvalidRequest, resp.Code)
	assert.Contains(t, logs.String(), "consent object is required in this jurisdiction")
}

// TestIdentityHandler_ConsentRequired_AcceptsPresent pins the positive
// case: when RequireConsent is set and the request DOES include a
// consent object, the handler routes past the gate. Asserted on the
// server-side outcome via a real Service (nil eligibility store is
// fine — we only care that the consent check itself didn't reject).
// The schema-level cross-field rule (gdpr:true ⇒ tcf_consent|gpp)
// still runs in ValidateIdentityRequest.
func TestIdentityHandler_ConsentRequired_AcceptsPresent(t *testing.T) {
	svc := newTestService(t, testServiceOptions{})
	h := NewIdentityHandler(IdentityHandlerConfig{
		Service:                    svc,
		RequestTimeout:             time.Second,
		RequestBodyLimit:           64 * 1024,
		ResponseTTL:                time.Minute,
		SupportedADCPMajorVersions: []int{3},
		RequireConsent:             true,
		Logger:                     slog.New(slog.NewJSONHandler(&nopWriter{}, nil)),
	})
	body := `{
		"type": "identity_match_request",
		"request_id": "r1",
		"seller_agent_url": "https://seller.example.com/agent",
		"identities": [{"user_token": "tok", "uid_type": "uid2"}],
		"consent": {"gdpr": false}
	}`
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/identity", strings.NewReader(body)))

	// Gate passed → not a 400 from the consent check. Downstream may
	// still 200 with an empty eligibility list (no packages seeded),
	// which is what the tmproto response type discriminates on.
	assert.NotEqual(t, http.StatusBadRequest, w.Code)
}

// TestIdentityHandler_ConsentDefaultOff pins the back-compat contract:
// deployments that haven't opted in keep pre-CONSENT_REQUIRED behavior
// — an omitted consent object is accepted at this layer, and the
// schema's cross-field rule remains the only consent-related check.
func TestIdentityHandler_ConsentDefaultOff(t *testing.T) {
	svc := newTestService(t, testServiceOptions{})
	h := NewIdentityHandler(IdentityHandlerConfig{
		Service:                    svc,
		RequestTimeout:             time.Second,
		RequestBodyLimit:           64 * 1024,
		ResponseTTL:                time.Minute,
		SupportedADCPMajorVersions: []int{3},
		Logger:                     slog.New(slog.NewJSONHandler(&nopWriter{}, nil)),
		// RequireConsent unset.
	})
	body := `{
		"type": "identity_match_request",
		"request_id": "r1",
		"seller_agent_url": "https://seller.example.com/agent",
		"identities": [{"user_token": "tok", "uid_type": "uid2"}]
	}`
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/identity", strings.NewReader(body)))
	assert.NotEqual(t, http.StatusBadRequest, w.Code,
		"consent gate must be inert when RequireConsent is not set")
}
