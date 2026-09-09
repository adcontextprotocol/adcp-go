package identityagent

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/adcontextprotocol/adcp-go/targeting"
	"github.com/adcontextprotocol/adcp-go/tmproto"
)

// TestIdentityHandler_AdcpVersion_Unsupported_Rejected pins the primary
// S5 fix for the identity-agent: a request pinning a release-precision
// adcp_version outside the configured supported set is rejected at
// the version-negotiation gate, before the pipeline runs.
func TestIdentityHandler_AdcpVersion_Unsupported_Rejected(t *testing.T) {
	h := NewIdentityHandler(IdentityHandlerConfig{
		RequestTimeout:             time.Second,
		RequestBodyLimit:           64 * 1024,
		ResponseTTL:                time.Minute,
		SupportedADCPMajorVersions: []int{3},
		SupportedAdcpVersions:      []string{"3.0", "3.1"},
		Logger:                     slog.New(slog.NewTextHandler(&nopWriter{}, nil)),
	})
	body := `{
		"type": "identity_match_request",
		"adcp_version": "4.0",
		"adcp_major_version": 3,
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
	assert.Equal(t, tmproto.TypeError, resp.Type)
}

// TestIdentityHandler_AdcpVersion_TakesPrecedence pins that when both
// adcp_version and adcp_major_version are present, the release-
// precision field is authoritative.
func TestIdentityHandler_AdcpVersion_TakesPrecedence(t *testing.T) {
	h := NewIdentityHandler(IdentityHandlerConfig{
		RequestTimeout:             time.Second,
		RequestBodyLimit:           64 * 1024,
		ResponseTTL:                time.Minute,
		SupportedADCPMajorVersions: []int{3},
		SupportedAdcpVersions:      []string{"3.0"}, // only 3.0
		Logger:                     slog.New(slog.NewTextHandler(&nopWriter{}, nil)),
	})
	body := `{
		"type": "identity_match_request",
		"adcp_version": "3.1",
		"adcp_major_version": 3,
		"request_id": "r1",
		"seller_agent_url": "https://seller.example.com/agent",
		"identities": [{"user_token": "tok", "uid_type": "uid2"}]
	}`
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/identity", strings.NewReader(body)))
	require.Equal(t, http.StatusBadRequest, w.Code)
}

// TestTerminalStatus_MapsStoreOutcomes pins the fcap/audience outcome
// → terminal-Status mapping. Only genuine store failures (timeout,
// error) propagate as ErrorResponse; fail-closed decisions rooted in
// semantics stay StatusOK so the router circuit breaker doesn't
// misfire on healthy providers.
func TestTerminalStatus_MapsStoreOutcomes(t *testing.T) {
	cases := []struct {
		name   string
		fcap   string
		aud    string
		want   string
	}{
		{"both pass", OutcomePass, OutcomePass, targeting.StatusOK},
		{"both fail (semantic)", OutcomeFail, OutcomeFail, targeting.StatusOK},
		{"fcap timeout", OutcomeTimeout, OutcomePass, targeting.StatusTimeout},
		{"audience timeout", OutcomePass, OutcomeTimeout, targeting.StatusTimeout},
		{"fcap store error", OutcomeError, OutcomePass, targeting.StatusProviderUnavailable},
		{"audience store error", OutcomePass, OutcomeError, targeting.StatusProviderUnavailable},
		{"canceled sibling", OutcomePass, OutcomeCanceled, targeting.StatusOK},
		{"undecodable fail-closed", OutcomeFailClosedUndecodable, OutcomePass, targeting.StatusOK},
		{"timeout wins over error", OutcomeTimeout, OutcomeError, targeting.StatusTimeout},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, terminalStatus(tc.fcap, tc.aud))
		})
	}
}

// TestErrorCodeForStatus pins the Status → ErrorCode wire mapping.
func TestErrorCodeForStatus(t *testing.T) {
	assert.Equal(t, tmproto.ErrorCodeTimeout, errorCodeForStatus(targeting.StatusTimeout))
	assert.Equal(t, tmproto.ErrorCodeProviderUnavailable, errorCodeForStatus(targeting.StatusProviderUnavailable))
	assert.Equal(t, tmproto.ErrorCodeInternalError, errorCodeForStatus(targeting.StatusInternalError))
	// Unknown values fall back to internal_error so the wire never
	// carries an unenumerated code.
	assert.Equal(t, tmproto.ErrorCodeInternalError, errorCodeForStatus("unknown"))
	assert.Equal(t, tmproto.ErrorCodeInternalError, errorCodeForStatus(targeting.StatusOK))
}

// nopWriter silences the handler's logger during test runs.
type nopWriter struct{}

func (nopWriter) Write(p []byte) (int, error) { return len(p), nil }
