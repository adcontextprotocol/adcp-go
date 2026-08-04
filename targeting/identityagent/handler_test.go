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

// TestAssignTmpxToResponse_SingleSlotEmitsOneChunk pins the chunk wiring
// for a single-slot deployment. The sealed token fits in one slot, so
// TmpxChunks has one entry pairing the registered slot_id with the token.
func TestAssignTmpxToResponse_SingleSlotEmitsOneChunk(t *testing.T) {
	sealer := &TMPXSealer{slotIDs: []string{"primary"}}
	resp := &tmproto.ProviderIdentityMatchResponse{}
	assignTmpxToResponse(resp, sealer, "k1.short-token")
	require.Len(t, resp.TmpxChunks, 1)
	assert.Equal(t, "primary", resp.TmpxChunks[0].SlotID)
	assert.Equal(t, "k1.short-token", resp.TmpxChunks[0].Value)
}

// TestAssignTmpxToResponse_MultiSlotSplitsToken locks the split-across-slots
// behavior: a token exceeding one slot's byte budget yields one chunk per
// registered slot, in slot order.
func TestAssignTmpxToResponse_MultiSlotSplitsToken(t *testing.T) {
	sealer := &TMPXSealer{slotIDs: []string{"primary", "secondary"}}
	token := strings.Repeat("a", tmproto.TmpxMaxWireBytes) + strings.Repeat("b", 45)
	resp := &tmproto.ProviderIdentityMatchResponse{}
	assignTmpxToResponse(resp, sealer, token)
	require.Len(t, resp.TmpxChunks, 2, "TmpxChunks slice length matches the number of chunks emitted")
	assert.Equal(t, "primary", resp.TmpxChunks[0].SlotID)
	assert.Equal(t, "secondary", resp.TmpxChunks[1].SlotID)
}

// TestAssignTmpxToResponse_NoSlotsEmitsNoChunks covers the no-slots
// deployment: TmpxChunks stays empty because the sealer has no registered
// slot list to pair a chunk with.
func TestAssignTmpxToResponse_NoSlotsEmitsNoChunks(t *testing.T) {
	sealer := &TMPXSealer{} // no slotIDs configured
	resp := &tmproto.ProviderIdentityMatchResponse{}
	assignTmpxToResponse(resp, sealer, "k1.tok")
	assert.Empty(t, resp.TmpxChunks)
}

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

	var logEntry map[string]any
	require.NoError(t, json.Unmarshal(logs.Bytes(), &logEntry))
	logErr, ok := logEntry["error"].(string)
	require.True(t, ok)
	assert.Equal(t, "adcp_major_version is not supported", logErr)
	assert.NotContains(t, logErr, "999")
}

func TestServeWindowSecondsClampsToSchemaMax(t *testing.T) {
	assert.Equal(t, 299, serveWindowSeconds(299*time.Second))
	assert.Equal(t, 300, serveWindowSeconds(300*time.Second))
	assert.Equal(t, 300, serveWindowSeconds(10*time.Minute))
}

// TestBuildServiceRequest_NoCanonicalizer_PassesThroughUnchanged covers the
// legacy code path: when the handler has neither a canonicalizer nor a
// sealer, the request flows to service.Evaluate exactly as received — no
// decode, no shadow. This is the opt-out behavior for deployments that
// explicitly want the publisher-supplied wire string as the audience/fcap
// lookup key.
func TestBuildServiceRequest_NoCanonicalizer_PassesThroughUnchanged(t *testing.T) {
	h := &identityHandler{tmpx: nil, canonicalizer: nil}
	req := &tmproto.IdentityMatchRequest{
		RequestID: "req-1",
		Identities: []tmproto.IdentityToken{
			{UIDType: tmproto.UIDTypeMAID, UserToken: validUserTokenFor(tmproto.UIDTypeMAID)},
		},
	}
	got, decoded := h.buildServiceRequest(t.Context(), req)
	assert.Same(t, req, got, "missing canonicalizer must pass through the original request pointer")
	assert.Nil(t, decoded, "missing canonicalizer must not produce a decoded slice")
}

// TestBuildServiceRequest_CanonicalizerOnly_TMPXOff is the bug-fix
// regression test: a deployment with the canonicalizer wired in but TMPX
// sealing disabled (no JWKS, no recipient) must still see audience/fcap
// keyed on the canonical lowercase-hex form of the decoded bytes — not
// on the publisher-supplied wire string. Otherwise TMPX-on and TMPX-off
// deployments key the same logical user differently and downstream
// marker-writer lookups diverge.
func TestBuildServiceRequest_CanonicalizerOnly_TMPXOff(t *testing.T) {
	h := &identityHandler{
		tmpx:          nil,
		canonicalizer: testCanonicalizer(t),
	}

	maidUUID := validUserTokenFor(tmproto.UIDTypeMAID)
	hashedEmail := "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	req := &tmproto.IdentityMatchRequest{
		RequestID: "req-off",
		Identities: []tmproto.IdentityToken{
			{UIDType: tmproto.UIDTypeMAID, UserToken: maidUUID},
			{UIDType: tmproto.UIDTypeHashedEmail, UserToken: hashedEmail},
			{UIDType: tmproto.UIDTypeUID2, UserToken: fixtureToken("uid2-no-decoder")},
			{UIDType: tmproto.UIDTypeOther, UserToken: "ignored"},
		},
	}

	shadow, decoded := h.buildServiceRequest(t.Context(), req)

	require.NotSame(t, req, shadow, "canonicalizer-on must produce a new shadow request even with TMPX off")
	require.Len(t, shadow.Identities, 2,
		"only MAID and HashedEmail have decoders; UID2 (no decoder) and UIDTypeOther (no mapping) are dropped")

	// The audience/fcap keys must be the canonical lowercase-hex form of
	// the decoded bytes — identical to the form TMPX-on deployments
	// would produce. Before the fix this test would have observed the
	// raw publisher-supplied strings instead.
	wantMAIDHex := "550e8400e29b41d4a716446655440000"
	wantHashedEmailHex := "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	assert.Equal(t, tmproto.UIDTypeMAID, shadow.Identities[0].UIDType)
	assert.Equal(t, wantMAIDHex, shadow.Identities[0].UserToken,
		"TMPX-off deployments must still key audience/fcap on the canonical lowercase-hex form")
	assert.Equal(t, tmproto.UIDTypeHashedEmail, shadow.Identities[1].UIDType)
	assert.Equal(t, wantHashedEmailHex, shadow.Identities[1].UserToken)

	require.Len(t, decoded, 4, "positional correspondence is preserved; dropped entries have nil Bytes")
}

// TestBuildServiceRequest_CanonicalizerAndTMPXOn_SharedDecodePass pins the
// shadow-request contract for the TMPX-on path: the request that flows
// into service.Evaluate carries audience/fcap-eligible identities with
// UserToken set to the canonical lowercase-hex form, and the same
// decoded slice is returned for the TMPX seal step so LiveRamp-backed
// RampIDs make at most one sidecar call per request.
func TestBuildServiceRequest_CanonicalizerAndTMPXOn_SharedDecodePass(t *testing.T) {
	h := &identityHandler{
		tmpx:          &TMPXSealer{decoders: defaultTestDecoders(t)},
		canonicalizer: testCanonicalizer(t),
	}

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

	require.NotSame(t, req, shadow, "canonicalizer-on must produce a new shadow request")
	require.NotEqual(t, &req.Identities, &shadow.Identities, "shadow must have its own Identities slice")
	assert.Equal(t, req.RequestID, shadow.RequestID, "non-Identities fields must survive the shadow copy")

	require.Len(t, shadow.Identities, 2)
	wantMAIDHex := "550e8400e29b41d4a716446655440000"
	wantHashedEmailHex := "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	assert.Equal(t, tmproto.UIDTypeMAID, shadow.Identities[0].UIDType)
	assert.Equal(t, wantMAIDHex, shadow.Identities[0].UserToken,
		"audience/fcap must see UserToken in the canonical lowercase-hex form so identityhash.Hash "+
			"keys match the form ExposureLog.user_token publishes downstream")
	assert.Equal(t, tmproto.UIDTypeHashedEmail, shadow.Identities[1].UIDType)
	assert.Equal(t, wantHashedEmailHex, shadow.Identities[1].UserToken)

	// The full decoded slice (including the dropped UID2/Other entries)
	// flows separately to the TMPX seal path. Length matches the input
	// so positional correspondence is preserved; dropped entries have
	// nil/empty Bytes.
	require.Len(t, decoded, 4)
	assert.NotEmpty(t, decoded[0].Bytes, "MAID must be decoded once and shared with TMPX")
	assert.NotEmpty(t, decoded[1].Bytes, "HashedEmail must be decoded once and shared with TMPX")
	assert.Empty(t, decoded[2].Bytes, "UID2 has no decoder and must be dropped at decode")
	assert.Empty(t, decoded[3].Bytes, "UIDTypeOther has no TMPX mapping and must be dropped at decode")
}

// testCanonicalizer constructs an IdentityCanonicalizer wired to the same
// fake LiveRamp client the TMPXSealer test decoders use, so the
// MAID/HashedEmail/ID5 format decoders and the RampID decoders match
// what production would produce.
func testCanonicalizer(t *testing.T) *IdentityCanonicalizer {
	t.Helper()
	return &IdentityCanonicalizer{decoders: defaultTestDecoders(t)}
}
