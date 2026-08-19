package tmpxdecoders

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/adcontextprotocol/adcp-go/tmproto"
)

// decodableUIDTypesWithLiveRamp enumerates the UID types that have a
// registered decoder when LiveRamp is enabled. Anything outside this set is
// silently dropped by the agent at decode time.
var decodableUIDTypesWithLiveRamp = []tmproto.UIDType{
	tmproto.UIDTypeMAID,
	tmproto.UIDTypeHashedEmail,
	tmproto.UIDTypeID5,
	tmproto.UIDTypeRampID,
	tmproto.UIDTypeRampIDDerived,
}

// decodableUIDTypesWithoutLiveRamp enumerates the UID types that have a
// registered decoder when LiveRamp is disabled.
var decodableUIDTypesWithoutLiveRamp = []tmproto.UIDType{
	tmproto.UIDTypeMAID,
	tmproto.UIDTypeHashedEmail,
	tmproto.UIDTypeID5,
}

// fakeLiveRampClient returns a fixed-length string as the mapped value so
// the RampID / RampIDDerived decoders pass selectEntries' size check.
type fakeLiveRampClient struct {
	mappedLen int
}

func (f *fakeLiveRampClient) MappedID(_ context.Context, _ string) (string, error) {
	if f.mappedLen <= 0 {
		return "", ErrLiveRampNoMapping
	}
	return strings.Repeat("x", f.mappedLen), nil
}

func TestNewDefaultRegistry_WithoutLiveRamp_OmitsRampID(t *testing.T) {
	reg := NewDefaultRegistry(RegistryOptions{})
	_, hasRampID := reg[tmproto.UIDTypeRampID]
	_, hasDerived := reg[tmproto.UIDTypeRampIDDerived]
	assert.False(t, hasRampID, "RampID must not be in the registry when LiveRamp is disabled")
	assert.False(t, hasDerived, "RampIDDerived must not be in the registry when LiveRamp is disabled")
	assert.Equal(t, len(decodableUIDTypesWithoutLiveRamp), len(reg),
		"registry has %d entries, expected one per decodable UID type", len(reg))
}

func TestNewDefaultRegistry_WithLiveRamp_CoversEveryDecodableUIDType(t *testing.T) {
	reg := NewDefaultRegistry(RegistryOptions{LiveRampClient: &fakeLiveRampClient{mappedLen: 32}})
	for _, uid := range decodableUIDTypesWithLiveRamp {
		_, ok := reg[uid]
		assert.True(t, ok, "registry missing decoder for %s when LiveRamp enabled", uid)
	}
	assert.Equal(t, len(decodableUIDTypesWithLiveRamp), len(reg),
		"registry has %d entries, expected one per decodable UID type",
		len(reg))
}

func TestNewDefaultRegistry_OmitsUIDTypesWithoutDecoder(t *testing.T) {
	reg := NewDefaultRegistry(RegistryOptions{LiveRampClient: &fakeLiveRampClient{mappedLen: 32}})
	for _, uid := range []tmproto.UIDType{
		tmproto.UIDTypeUID2,
		tmproto.UIDTypeEUID,
		tmproto.UIDTypePairID,
		tmproto.UIDTypePublisherFirstParty,
	} {
		_, ok := reg[uid]
		assert.False(t, ok, "%s has no decoder and must be absent from the registry", uid)
	}
}

// fakeUID2Client returns a fixed-length raw ID so the UID2 / EUID
// decoders' size-through-to-selectEntries path is exercised.
type fakeUID2Client struct {
	rawLen int
}

func (f *fakeUID2Client) Decrypt(_ context.Context, _ string) ([]byte, error) {
	if f.rawLen <= 0 {
		return nil, ErrUID2NoMapping
	}
	out := make([]byte, f.rawLen)
	for i := range out {
		out[i] = 0xA1
	}
	return out, nil
}

func TestNewDefaultRegistry_WithUID2Client_AddsUID2Decoder(t *testing.T) {
	reg := NewDefaultRegistry(RegistryOptions{UID2Client: &fakeUID2Client{rawLen: 32}})
	dec, ok := reg[tmproto.UIDTypeUID2]
	require.True(t, ok, "UID2 decoder must be present when UID2Client is configured")
	_, hasEUID := reg[tmproto.UIDTypeEUID]
	assert.False(t, hasEUID, "EUID must remain absent when only UID2Client is wired")

	got, err := dec.Decode(t.Context(), "any-token")
	require.NoError(t, err)
	want, _ := tmproto.TmpxTokenSize(tmproto.TmpxTypeUID2)
	assert.Len(t, got, want, "decoder must return a 32-byte raw UID2")
}

func TestNewDefaultRegistry_WithEUIDClient_AddsEUIDDecoder(t *testing.T) {
	reg := NewDefaultRegistry(RegistryOptions{EUIDClient: &fakeUID2Client{rawLen: 32}})
	dec, ok := reg[tmproto.UIDTypeEUID]
	require.True(t, ok, "EUID decoder must be present when EUIDClient is configured")
	_, hasUID2 := reg[tmproto.UIDTypeUID2]
	assert.False(t, hasUID2, "UID2 must remain absent when only EUIDClient is wired")

	got, err := dec.Decode(t.Context(), "any-token")
	require.NoError(t, err)
	want, _ := tmproto.TmpxTokenSize(tmproto.TmpxTypeEUID)
	assert.Len(t, got, want, "decoder must return a 32-byte raw EUID")
}

func TestNewDefaultRegistry_UID2AndEUIDDistinctClients(t *testing.T) {
	// UID2 and EUID have separate operators (different jurisdictions,
	// different keys) — registering both must not blur them into a
	// single client.
	uid2C := &fakeUID2Client{rawLen: 32}
	euidC := &fakeUID2Client{rawLen: 32}
	reg := NewDefaultRegistry(RegistryOptions{UID2Client: uid2C, EUIDClient: euidC})

	u2, ok := reg[tmproto.UIDTypeUID2].(UID2)
	require.True(t, ok)
	eu, ok := reg[tmproto.UIDTypeEUID].(EUID)
	require.True(t, ok)
	assert.Same(t, uid2C, u2.Client, "UID2 decoder must hold the UID2Client")
	assert.Same(t, euidC, eu.Client, "EUID decoder must hold the EUIDClient")
}

func TestNewDefaultRegistry_AllOptInsCombined(t *testing.T) {
	reg := NewDefaultRegistry(RegistryOptions{
		LiveRampClient: &fakeLiveRampClient{mappedLen: 32},
		UID2Client:     &fakeUID2Client{rawLen: 32},
		EUIDClient:     &fakeUID2Client{rawLen: 32},
	})
	for _, uid := range []tmproto.UIDType{
		tmproto.UIDTypeMAID,
		tmproto.UIDTypeHashedEmail,
		tmproto.UIDTypeID5,
		tmproto.UIDTypeRampID,
		tmproto.UIDTypeRampIDDerived,
		tmproto.UIDTypeUID2,
		tmproto.UIDTypeEUID,
	} {
		_, ok := reg[uid]
		assert.Truef(t, ok, "decoder must be present for %s when its client is configured", uid)
	}
}

func TestNewDefaultRegistry_DecodersReturnCorrectSize(t *testing.T) {
	// Per-type fakes so RampID gets a 32-byte string and RampIDDerived a
	// 48-byte one; selectEntries enforces these sizes in production.
	rampFake := &fakeLiveRampClient{mappedLen: 32}
	derivedFake := &fakeLiveRampClient{mappedLen: 48}

	reg := NewDefaultRegistry(RegistryOptions{LiveRampClient: rampFake})
	reg[tmproto.UIDTypeRampIDDerived] = RampIDDerived{Client: derivedFake}

	inputs := map[tmproto.UIDType]string{
		tmproto.UIDTypeMAID:        "550e8400-e29b-41d4-a716-446655440000",
		tmproto.UIDTypeHashedEmail: "0000000000000000000000000000000000000000000000000000000000000000",
		// ID5 is a pass-through decoder, so input length must match
		// the type's 32-byte slot.
		tmproto.UIDTypeID5:           "id5-canonical-token-padded--32by",
		tmproto.UIDTypeRampID:        "any-env",
		tmproto.UIDTypeRampIDDerived: "any-env",
	}
	typeIDs := map[tmproto.UIDType]tmproto.TmpxTypeID{
		tmproto.UIDTypeID5:           tmproto.TmpxTypeID5,
		tmproto.UIDTypeRampID:        tmproto.TmpxTypeRampID,
		tmproto.UIDTypeRampIDDerived: tmproto.TmpxTypeRampIDDerived,
		tmproto.UIDTypeMAID:          tmproto.TmpxTypeMAID,
		tmproto.UIDTypeHashedEmail:   tmproto.TmpxTypeHashedEmail,
	}
	for uid, dec := range reg {
		got, err := dec.Decode(t.Context(), inputs[uid])
		require.NoError(t, err, "decoder for %s rejected canonical input", uid)
		want, _ := tmproto.TmpxTokenSize(typeIDs[uid])
		assert.Len(t, got, want, "decoder for %s produced wrong byte length", uid)
	}
}

func TestNewDefaultRegistry_ReturnsFreshMap(t *testing.T) {
	a := NewDefaultRegistry(RegistryOptions{})
	b := NewDefaultRegistry(RegistryOptions{})
	delete(a, tmproto.UIDTypeMAID)
	_, ok := b[tmproto.UIDTypeMAID]
	assert.True(t, ok, "mutating one registry must not affect another")
}

