package identityagent

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/adcontextprotocol/adcp-go/tmproto"
)

// TestNewIdentityCanonicalizer_NilLiveRamp_BuildsFormatOnlyDecoders verifies
// that a deployment without a LiveRamp sidecar still gets canonicalization
// for the format-only UID types (MAID, HashedEmail, ID5). RampID and
// RampIDDerived are dropped at decode time without an external client.
func TestNewIdentityCanonicalizer_NilLiveRamp_BuildsFormatOnlyDecoders(t *testing.T) {
	canon := NewIdentityCanonicalizer(nil, nil, nil, nil, nil)
	require.NotNil(t, canon)
	for _, uid := range []tmproto.UIDType{tmproto.UIDTypeMAID, tmproto.UIDTypeHashedEmail, tmproto.UIDTypeID5} {
		_, ok := canon.decoders[uid]
		assert.Truef(t, ok, "format-only decoder must be present for %s even without LiveRamp", uid)
	}
	_, hasRampID := canon.decoders[tmproto.UIDTypeRampID]
	assert.False(t, hasRampID, "RampID requires a LiveRamp client and must be absent without one")
}

// TestNewIdentityCanonicalizer_WithLiveRamp_AddsRampIDDecoders confirms
// that supplying the LiveRamp sidecar adds RampID and RampIDDerived to
// the decoder map.
func TestNewIdentityCanonicalizer_WithLiveRamp_AddsRampIDDecoders(t *testing.T) {
	canon := NewIdentityCanonicalizer(newFixedLiveRampClient(), nil, nil, nil, nil)
	require.NotNil(t, canon)
	for _, uid := range []tmproto.UIDType{tmproto.UIDTypeRampID, tmproto.UIDTypeRampIDDerived} {
		_, ok := canon.decoders[uid]
		assert.Truef(t, ok, "LiveRamp-backed decoder must be present for %s when sidecar is configured", uid)
	}
}

// TestIdentityCanonicalizer_Decode_ProducesCanonicalBytes confirms the
// canonicalizer's decode pass yields the same DecodedIdentity slice the
// TMPXSealer's Decode would — verifying audienceEligibleIdentities sees
// identical canonical form on both code paths.
func TestIdentityCanonicalizer_Decode_ProducesCanonicalBytes(t *testing.T) {
	canon := &IdentityCanonicalizer{decoders: defaultTestDecoders(t)}

	ids := []tmproto.IdentityToken{
		{UIDType: tmproto.UIDTypeMAID, UserToken: validUserTokenFor(tmproto.UIDTypeMAID)},
		{UIDType: tmproto.UIDTypeHashedEmail, UserToken: validUserTokenFor(tmproto.UIDTypeHashedEmail)},
		{UIDType: tmproto.UIDTypeUID2, UserToken: "any-uid2"}, // no decoder
		{UIDType: tmproto.UIDTypeOther, UserToken: "ignored"}, // no mapping
	}
	decoded := canon.Decode(t.Context(), ids)
	require.Len(t, decoded, 4)
	assert.NotEmpty(t, decoded[0].Bytes, "MAID must be decoded")
	assert.NotEmpty(t, decoded[1].Bytes, "HashedEmail must be decoded")
	assert.Empty(t, decoded[2].Bytes, "UID2 (no decoder) must be dropped")
	assert.Empty(t, decoded[3].Bytes, "Other (no mapping) must be dropped")

	shadow := audienceEligibleIdentities(decoded)
	require.Len(t, shadow, 2)
	assert.Equal(t, "550e8400e29b41d4a716446655440000", shadow[0].UserToken,
		"audience must see canonical lowercase-hex of MAID UUID")
}

// TestIdentityCanonicalizer_Decode_RecordsDropsLikeSealer confirms the
// canonicalizer reuses the TmpxIdentityDrop counter so operators don't
// need to learn a second drop-reason vocabulary.
func TestIdentityCanonicalizer_Decode_RecordsDropsLikeSealer(t *testing.T) {
	rec := newTestRecorder()
	canon := &IdentityCanonicalizer{
		decoders: defaultTestDecoders(t),
		recorder: rec,
	}
	canon.Decode(t.Context(), []tmproto.IdentityToken{
		{UIDType: tmproto.UIDTypeOther, UserToken: "x"},              // unmapped
		{UIDType: tmproto.UIDTypeUID2, UserToken: "any-uid2"},        // no decoder
		{UIDType: tmproto.UIDTypeHashedEmail, UserToken: "deadbeef"}, // decoder_error (too short)
	})
	assert.Equal(t, 1, rec.dropCount(TmpxDropUnmapped, string(tmproto.UIDTypeOther)))
	assert.Equal(t, 1, rec.dropCount(TmpxDropNoDecoder, string(tmproto.UIDTypeUID2)))
	assert.Equal(t, 1, rec.dropCount(TmpxDropDecoderError, string(tmproto.UIDTypeHashedEmail)))
}
