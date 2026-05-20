package identityagent

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/adcontextprotocol/adcp-go/tmproto"
)

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

// TestBuildServiceRequest_TMPXEnabled_ShadowsAudienceIdentitiesWithDecodedBytes
// is the contract the second iteration of this work was built around:
// when TMPX is enabled, the request that flows into service.Evaluate has
// the audience/fcap-eligible identities only, and their UserToken is the
// canonical decoded byte form (so identityhash.Hash inside audience/fcap
// keys on the same bytes the buyer master will populate downstream).
//
// MAID and HashedEmail have real decoders → survive with decoded bytes
// in UserToken.
// UID2 has a SHA-512 stub → dropped from the shadow (HasReal=false).
// UIDTypeOther has no TMPX mapping → dropped entirely.
func TestBuildServiceRequest_TMPXEnabled_ShadowsAudienceIdentitiesWithDecodedBytes(t *testing.T) {
	cfg := &TMPXSealer{
		decoders:  defaultTestDecoders(t),
		realTypes: defaultTestRealTypes(),
	}
	h := &identityHandler{tmpx: cfg}

	maidUUID := validUserTokenFor(tmproto.UIDTypeMAID)
	hashedEmail := "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	req := &tmproto.IdentityMatchRequest{
		RequestID: "req-2",
		Identities: []tmproto.IdentityToken{
			{UIDType: tmproto.UIDTypeMAID, UserToken: maidUUID},
			{UIDType: tmproto.UIDTypeHashedEmail, UserToken: hashedEmail},
			{UIDType: tmproto.UIDTypeUID2, UserToken: fixtureToken("uid2-stub")},
			{UIDType: tmproto.UIDTypeOther, UserToken: "ignored"},
		},
	}

	shadow, decoded := h.buildServiceRequest(t.Context(), req)

	require.NotSame(t, req, shadow, "TMPX-on must produce a new shadow request")
	require.NotEqual(t, &req.Identities, &shadow.Identities, "shadow must have its own Identities slice")
	assert.Equal(t, req.RequestID, shadow.RequestID, "non-Identities fields must survive the shadow copy")

	// Two real identities survive (MAID, HashedEmail). Stub UID2 and
	// unmapped UIDTypeOther are filtered out.
	require.Len(t, shadow.Identities, 2)

	wantMAIDBytes := []byte{
		0x55, 0x0e, 0x84, 0x00, 0xe2, 0x9b, 0x41, 0xd4,
		0xa7, 0x16, 0x44, 0x66, 0x55, 0x44, 0x00, 0x00,
	}
	wantHashedEmailBytes := []byte{
		0x01, 0x23, 0x45, 0x67, 0x89, 0xab, 0xcd, 0xef,
		0x01, 0x23, 0x45, 0x67, 0x89, 0xab, 0xcd, 0xef,
		0x01, 0x23, 0x45, 0x67, 0x89, 0xab, 0xcd, 0xef,
		0x01, 0x23, 0x45, 0x67, 0x89, 0xab, 0xcd, 0xef,
	}
	assert.Equal(t, tmproto.UIDTypeMAID, shadow.Identities[0].UIDType)
	assert.Equal(t, string(wantMAIDBytes), shadow.Identities[0].UserToken,
		"audience/fcap must see UserToken set to the raw decoded MAID bytes, not the input UUID string — "+
			"otherwise identityhash.Hash keys mismatch what the buyer master populator publishes")
	assert.Equal(t, tmproto.UIDTypeHashedEmail, shadow.Identities[1].UIDType)
	assert.Equal(t, string(wantHashedEmailBytes), shadow.Identities[1].UserToken,
		"HashedEmail UserToken must be the raw 32 bytes of the SHA-256 hex input")

	// The full decoded slice (including the dropped UID2/Other entries)
	// flows separately to the TMPX seal path. Length matches the input
	// so positional correspondence is preserved; dropped entries have
	// nil/empty Bytes.
	require.Len(t, decoded, 4)
	assert.True(t, decoded[0].HasReal && len(decoded[0].Bytes) > 0, "MAID must be decoded for TMPX too")
	assert.True(t, decoded[1].HasReal && len(decoded[1].Bytes) > 0, "HashedEmail must be decoded for TMPX too")
	assert.False(t, decoded[2].HasReal, "UID2 stub must not be flagged real")
	assert.NotEmpty(t, decoded[2].Bytes, "UID2 stub still produces bytes for the TMPX seal path")
	assert.Empty(t, decoded[3].Bytes, "UIDTypeOther has no TMPX mapping and must be dropped at decode")
}
