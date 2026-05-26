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

func TestIdentityHandlerValidationErrorIsGenericAndLogged(t *testing.T) {
	var logs bytes.Buffer
	h := NewIdentityHandler(IdentityHandlerConfig{
		RequestTimeout:             time.Second,
		RequestBodyLimit:           64 * 1024,
		ResponseTTL:                time.Minute,
		SupportedADCPMajorVersions: []int{3},
		Logger:                     slog.New(slog.NewJSONHandler(&logs, nil)),
	})

	body := `{
		"type": "identity_match_request",
		"request_id": "id-invalid",
		"identities": [{"user_token": "tok_test_abc", "uid_type": "uid2"}]
	}`
	req := httptest.NewRequest("POST", "/identity", strings.NewReader(body))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	require.Equal(t, http.StatusBadRequest, w.Code, w.Body.String())
	var resp tmproto.ErrorResponse
	require.NoError(t, json.NewDecoder(w.Body).Decode(&resp))
	assert.Equal(t, tmproto.ErrorCodeInvalidRequest, resp.Code)
	assert.Equal(t, "id-invalid", resp.RequestID)
	assert.Equal(t, "invalid request", resp.Message)
	assert.NotContains(t, resp.Message, "seller_agent_url")

	logText := logs.String()
	assert.Contains(t, logText, "invalid identity-match request")
	assert.Contains(t, logText, `"method":"POST"`)
	assert.Contains(t, logText, `"path":"/identity"`)
	assert.Contains(t, logText, "id-invalid")
	assert.Contains(t, logText, "seller_agent_url is required")
}

func TestIdentityHandlerInvalidRequestIDIsNotEchoed(t *testing.T) {
	var logs bytes.Buffer
	h := NewIdentityHandler(IdentityHandlerConfig{
		RequestTimeout:             time.Second,
		RequestBodyLimit:           64 * 1024,
		ResponseTTL:                time.Minute,
		SupportedADCPMajorVersions: []int{3},
		Logger:                     slog.New(slog.NewJSONHandler(&logs, nil)),
	})

	body := `{
		"type": "identity_match_request",
		"request_id": "bad/id",
		"seller_agent_url": "https://seller.example.com/agent",
		"identities": [{"user_token": "tok_test_abc", "uid_type": "uid2"}]
	}`
	req := httptest.NewRequest("POST", "/identity", strings.NewReader(body))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	require.Equal(t, http.StatusBadRequest, w.Code, w.Body.String())
	var resp tmproto.ErrorResponse
	require.NoError(t, json.NewDecoder(w.Body).Decode(&resp))
	assert.Empty(t, resp.RequestID)
	assert.Equal(t, "invalid request", resp.Message)
	assert.NotContains(t, w.Body.String(), "bad/id")

	logText := logs.String()
	assert.Contains(t, logText, "invalid identity-match request")
	assert.Contains(t, logText, `"method":"POST"`)
	assert.Contains(t, logText, `"path":"/identity"`)
	assert.Contains(t, logText, `"request_id_valid":false`)
	assert.NotContains(t, logText, "bad/id")
}

func TestIdentityHandlerLongRequestIDIsNotEchoed(t *testing.T) {
	var logs bytes.Buffer
	h := NewIdentityHandler(IdentityHandlerConfig{
		RequestTimeout:             time.Second,
		RequestBodyLimit:           64 * 1024,
		ResponseTTL:                time.Minute,
		SupportedADCPMajorVersions: []int{3},
		Logger:                     slog.New(slog.NewJSONHandler(&logs, nil)),
	})

	longID := strings.Repeat("a", tmproto.MaxIDLength+1)
	body, err := json.Marshal(tmproto.IdentityMatchRequest{
		Type:           tmproto.TypeIdentityMatchRequest,
		RequestID:      longID,
		SellerAgentURL: "https://seller.example.com/agent",
		Identities: []tmproto.IdentityToken{
			{UserToken: "tok_test_abc", UIDType: tmproto.UIDTypeUID2},
		},
	})
	require.NoError(t, err)

	req := httptest.NewRequest("POST", "/identity", bytes.NewReader(body))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

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

func TestIdentityHandlerUnsupportedMajorVersionIsGenericAndLogged(t *testing.T) {
	var logs bytes.Buffer
	h := NewIdentityHandler(IdentityHandlerConfig{
		RequestTimeout:             time.Second,
		RequestBodyLimit:           64 * 1024,
		ResponseTTL:                time.Minute,
		SupportedADCPMajorVersions: []int{3},
		Logger:                     slog.New(slog.NewJSONHandler(&logs, nil)),
	})

	body, err := json.Marshal(tmproto.IdentityMatchRequest{
		Type:             tmproto.TypeIdentityMatchRequest,
		RequestID:        "id-version",
		SellerAgentURL:   "https://seller.example.com/agent",
		AdcpMajorVersion: 999,
		Identities: []tmproto.IdentityToken{
			{UserToken: "tok_test_abc", UIDType: tmproto.UIDTypeUID2},
		},
	})
	require.NoError(t, err)

	req := httptest.NewRequest("POST", "/identity", bytes.NewReader(body))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	require.Equal(t, http.StatusBadRequest, w.Code, w.Body.String())
	var resp tmproto.ErrorResponse
	require.NoError(t, json.NewDecoder(w.Body).Decode(&resp))
	assert.Equal(t, "id-version", resp.RequestID)
	assert.Equal(t, "invalid request", resp.Message)
	assert.NotContains(t, w.Body.String(), "999")

	logText := logs.String()
	assert.Contains(t, logText, "invalid identity-match request")
	assert.Contains(t, logText, `"request_id":"id-version"`)
	assert.Contains(t, logText, "adcp_major_version is not supported")
	assert.NotContains(t, logText, "999")
}

// TestBuildServiceRequest_TMPXDisabled_PassesThroughUnchanged covers the
// legacy code path: when the handler has no sealer, the request flows to
// service.Evaluate exactly as received — no decode, no shadow.
func TestBuildServiceRequest_TMPXDisabled_PassesThroughUnchanged(t *testing.T) {
	h := &identityHandler{tmpx: nil}
	req := &tmproto.IdentityMatchRequest{
		RequestID: "req-1",
		Identities: []tmproto.IdentityToken{
			{UIDType: tmproto.UIDTypeMAID, UserToken: validUserTokenFor(tmproto.UIDTypeMAID)},
		},
	}
	got, decoded := h.buildServiceRequest(t.Context(), req)
	assert.Same(t, req, got, "TMPX-off must pass through the original request pointer")
	assert.Nil(t, decoded, "TMPX-off must not produce a decoded slice")
}

// TestBuildServiceRequest_TMPXEnabled_ShadowsAudienceIdentitiesWithCanonicalForm
// pins the shadow-request contract: when TMPX is enabled, the request
// that flows into service.Evaluate carries audience/fcap-eligible
// identities only, and their UserToken is the canonical lowercase-hex
// form of the decoded bytes — matching ExposureLog.user_token per its
// proto spec, which is the keying convention downstream marker writers
// and buyer-master readers honor.
//
// MAID and HashedEmail have decoders → survive with canonical hex in
// UserToken.
// UID2 has no registered decoder → dropped at decode time.
// UIDTypeOther has no TMPX mapping → dropped entirely.
func TestBuildServiceRequest_TMPXEnabled_ShadowsAudienceIdentitiesWithCanonicalForm(t *testing.T) {
	cfg := &TMPXSealer{decoders: defaultTestDecoders(t)}
	h := &identityHandler{tmpx: cfg}

	maidUUID := validUserTokenFor(tmproto.UIDTypeMAID)
	hashedEmail := "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	req := &tmproto.IdentityMatchRequest{
		RequestID: "req-2",
		Identities: []tmproto.IdentityToken{
			{UIDType: tmproto.UIDTypeMAID, UserToken: maidUUID},
			{UIDType: tmproto.UIDTypeHashedEmail, UserToken: hashedEmail},
			{UIDType: tmproto.UIDTypeUID2, UserToken: fixtureToken("uid2-no-decoder")},
			{UIDType: tmproto.UIDTypeOther, UserToken: "ignored"},
		},
	}

	shadow, decoded := h.buildServiceRequest(t.Context(), req)

	require.NotSame(t, req, shadow, "TMPX-on must produce a new shadow request")
	require.NotEqual(t, &req.Identities, &shadow.Identities, "shadow must have its own Identities slice")
	assert.Equal(t, req.RequestID, shadow.RequestID, "non-Identities fields must survive the shadow copy")

	// Two identities survive (MAID, HashedEmail). UID2 lacks a decoder
	// and unmapped UIDTypeOther are filtered out.
	require.Len(t, shadow.Identities, 2)

	// The canonical key form is the lowercase-hex of the decoded bytes:
	// MAID's dashed UUID collapses to its 32-char hex, and HashedEmail's
	// hex input round-trips through decode→hex unchanged.
	wantMAIDHex := "550e8400e29b41d4a716446655440000"
	wantHashedEmailHex := "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	assert.Equal(t, tmproto.UIDTypeMAID, shadow.Identities[0].UIDType)
	assert.Equal(t, wantMAIDHex, shadow.Identities[0].UserToken,
		"audience/fcap must see UserToken in the canonical lowercase-hex form so identityhash.Hash "+
			"keys match the form ExposureLog.user_token publishes downstream")
	assert.Equal(t, tmproto.UIDTypeHashedEmail, shadow.Identities[1].UIDType)
	assert.Equal(t, wantHashedEmailHex, shadow.Identities[1].UserToken,
		"HashedEmail UserToken must be the lowercase-hex of the SHA-256 input")

	// The full decoded slice (including the dropped UID2/Other entries)
	// flows separately to the TMPX seal path. Length matches the input
	// so positional correspondence is preserved; dropped entries have
	// nil/empty Bytes.
	require.Len(t, decoded, 4)
	assert.NotEmpty(t, decoded[0].Bytes, "MAID must be decoded for TMPX too")
	assert.NotEmpty(t, decoded[1].Bytes, "HashedEmail must be decoded for TMPX too")
	assert.Empty(t, decoded[2].Bytes, "UID2 has no decoder and must be dropped at decode")
	assert.Empty(t, decoded[3].Bytes, "UIDTypeOther has no TMPX mapping and must be dropped at decode")
}
