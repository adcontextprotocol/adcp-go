package contextagent

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/adcontextprotocol/adcp-go/tmproto"
)

// TestContextHandler_AdcpVersion_Unsupported_Rejected pins the primary
// S5 fix: a request pinning a release-precision `adcp_version` not in
// the configured supported set is rejected before it reaches the
// engine. Without this the handler would silently downshift to
// `adcp_major_version` and serve a version the buyer explicitly did
// not pin.
func TestContextHandler_AdcpVersion_Unsupported_Rejected(t *testing.T) {
	h := NewHandler(HandlerConfig{
		RequestTimeout:             time.Second,
		RequestBodyLimit:           64 * 1024,
		ResponseTTL:                time.Minute,
		SupportedADCPMajorVersions: []int{3},
		SupportedAdcpVersions:      []string{"3.0", "3.1"},
	})
	body := `{
		"type": "context_match_request",
		"adcp_version": "4.0",
		"adcp_major_version": 3,
		"request_id": "r1",
		"property_rid": "rid-1",
		"property_id": "pub-1",
		"property_type": "website",
		"placement_id": "sidebar",
		"seller_agent_url": "https://seller.example.com/agent"
	}`
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/context", strings.NewReader(body)))

	require.Equal(t, http.StatusBadRequest, w.Code)
	var resp tmproto.ErrorResponse
	require.NoError(t, json.NewDecoder(w.Body).Decode(&resp))
	assert.Equal(t, tmproto.ErrorCodeInvalidRequest, resp.Code)
	assert.Contains(t, resp.Message, "unsupported adcp_version")
}

// TestContextHandler_AdcpVersion_TakesPrecedence pins the spec's
// negotiation rule: when both `adcp_version` and `adcp_major_version`
// are present, the release-precision field is authoritative. A request
// whose `adcp_major_version` is in-set but whose `adcp_version` is not
// must be rejected — silently accepting via the major fallback would
// let a buyer pin an unsupported release and get served.
func TestContextHandler_AdcpVersion_TakesPrecedence(t *testing.T) {
	h := NewHandler(HandlerConfig{
		RequestTimeout:             time.Second,
		RequestBodyLimit:           64 * 1024,
		ResponseTTL:                time.Minute,
		SupportedADCPMajorVersions: []int{3},
		SupportedAdcpVersions:      []string{"3.0"},
	})
	body := `{
		"type": "context_match_request",
		"adcp_version": "3.1",
		"adcp_major_version": 3,
		"request_id": "r1",
		"property_rid": "rid-1",
		"property_id": "pub-1",
		"property_type": "website",
		"placement_id": "sidebar",
		"seller_agent_url": "https://seller.example.com/agent"
	}`
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/context", strings.NewReader(body)))

	require.Equal(t, http.StatusBadRequest, w.Code)
	var resp tmproto.ErrorResponse
	require.NoError(t, json.NewDecoder(w.Body).Decode(&resp))
	assert.Contains(t, resp.Message, "unsupported adcp_version",
		"adcp_version pinned to 3.1 must reject even when adcp_major_version=3 is in-set")
}

// TestContextHandler_AdcpMajor_UnsupportedStillRejected pins the
// back-compat contract: a request that omits `adcp_version` still gets
// its `adcp_major_version` checked. Regressions on this gate would let
// pre-S5 clients silently past a stricter deployment.
func TestContextHandler_AdcpMajor_UnsupportedStillRejected(t *testing.T) {
	h := NewHandler(HandlerConfig{
		RequestTimeout:             time.Second,
		RequestBodyLimit:           64 * 1024,
		ResponseTTL:                time.Minute,
		SupportedADCPMajorVersions: []int{3},
		SupportedAdcpVersions:      []string{"3.0"},
	})
	body := `{
		"type": "context_match_request",
		"adcp_major_version": 99,
		"request_id": "r1",
		"property_rid": "rid-1",
		"property_id": "pub-1",
		"property_type": "website",
		"placement_id": "sidebar",
		"seller_agent_url": "https://seller.example.com/agent"
	}`
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/context", strings.NewReader(body)))
	require.Equal(t, http.StatusBadRequest, w.Code)
	var resp tmproto.ErrorResponse
	require.NoError(t, json.NewDecoder(w.Body).Decode(&resp))
	assert.Contains(t, resp.Message, "unsupported adcp_major_version")
}
