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
		assert.False(t, ok, "%s has no decoder unless the matching operator client is supplied", uid)
	}
}

// TestNewDefaultRegistry_WithUID2Client covers the opt-in that adds a
// UID2 decoder when the caller supplies a client.
func TestNewDefaultRegistry_WithUID2Client(t *testing.T) {
	reg := NewDefaultRegistry(RegistryOptions{UID2Client: stubUID2Client{}})
	_, ok := reg[tmproto.UIDTypeUID2]
	assert.True(t, ok, "UID2 must be present in the registry when a UID2 client is supplied")
	_, hasEUID := reg[tmproto.UIDTypeEUID]
	assert.False(t, hasEUID, "supplying a UID2 client alone must not enable EUID decoding")
}

// TestNewDefaultRegistry_WithEUIDClient covers the EUID opt-in.
func TestNewDefaultRegistry_WithEUIDClient(t *testing.T) {
	reg := NewDefaultRegistry(RegistryOptions{EUIDClient: stubUID2Client{}})
	_, ok := reg[tmproto.UIDTypeEUID]
	assert.True(t, ok, "EUID must be present when an EUID client is supplied")
	_, hasUID2 := reg[tmproto.UIDTypeUID2]
	assert.False(t, hasUID2, "supplying an EUID client alone must not enable UID2 decoding")
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

