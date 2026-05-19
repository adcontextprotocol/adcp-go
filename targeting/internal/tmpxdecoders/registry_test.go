package tmpxdecoders

import (
	"context"
	"slices"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/adcontextprotocol/adcp-go/tmproto"
)

// tmpxEncodableUIDTypes mirrors the set of UID types that have a TmpxTypeID
// mapping in targeting/identityagent. Keeping the list duplicated here means
// the registry test fails loudly if either side adds a type without the
// other.
var tmpxEncodableUIDTypes = []tmproto.UIDType{
	tmproto.UIDTypeUID2,
	tmproto.UIDTypeEUID,
	tmproto.UIDTypeID5,
	tmproto.UIDTypeRampID,
	tmproto.UIDTypeRampIDDerived,
	tmproto.UIDTypeMAID,
	tmproto.UIDTypePairID,
	tmproto.UIDTypeHashedEmail,
	tmproto.UIDTypePublisherFirstParty,
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
}

func TestNewDefaultRegistry_WithLiveRamp_CoversEveryTmpxEncodableType(t *testing.T) {
	reg := NewDefaultRegistry(RegistryOptions{LiveRampClient: &fakeLiveRampClient{mappedLen: 32}})
	for _, uid := range tmpxEncodableUIDTypes {
		_, ok := reg[uid]
		assert.True(t, ok, "registry missing decoder for %s when LiveRamp enabled", uid)
	}
	assert.Equal(t, len(tmpxEncodableUIDTypes), len(reg),
		"registry has %d entries, expected one per TMPX-encodable UID type",
		len(reg))
}

func TestNewDefaultRegistry_DecodersReturnCorrectSize(t *testing.T) {
	// Per-type fakes so RampID gets a 32-byte string and RampIDDerived a
	// 48-byte one; selectEntries enforces these sizes in production.
	rampFake := &fakeLiveRampClient{mappedLen: 32}
	derivedFake := &fakeLiveRampClient{mappedLen: 48}

	reg := NewDefaultRegistry(RegistryOptions{LiveRampClient: rampFake})
	reg[tmproto.UIDTypeRampIDDerived] = RampIDDerived{Client: derivedFake}

	inputs := map[tmproto.UIDType]string{
		tmproto.UIDTypeMAID:                "550e8400-e29b-41d4-a716-446655440000",
		tmproto.UIDTypeHashedEmail:         "0000000000000000000000000000000000000000000000000000000000000000",
		tmproto.UIDTypeUID2:                "anything",
		tmproto.UIDTypeEUID:                "anything",
		tmproto.UIDTypeID5:                 "anything",
		tmproto.UIDTypeRampID:              "any-env",
		tmproto.UIDTypeRampIDDerived:       "any-env",
		tmproto.UIDTypePairID:              "anything",
		tmproto.UIDTypePublisherFirstParty: "anything",
	}
	typeIDs := map[tmproto.UIDType]tmproto.TmpxTypeID{
		tmproto.UIDTypeUID2:                tmproto.TmpxTypeUID2,
		tmproto.UIDTypeEUID:                tmproto.TmpxTypeEUID,
		tmproto.UIDTypeID5:                 tmproto.TmpxTypeID5,
		tmproto.UIDTypeRampID:              tmproto.TmpxTypeRampID,
		tmproto.UIDTypeRampIDDerived:       tmproto.TmpxTypeRampIDDerived,
		tmproto.UIDTypeMAID:                tmproto.TmpxTypeMAID,
		tmproto.UIDTypePairID:              tmproto.TmpxTypePairID,
		tmproto.UIDTypeHashedEmail:         tmproto.TmpxTypeHashedEmail,
		tmproto.UIDTypePublisherFirstParty: tmproto.TmpxTypePublisherFirstParty,
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

func TestStubbedUIDTypes_ExcludesRealDecoders(t *testing.T) {
	got := StubbedUIDTypes()
	for _, real := range []tmproto.UIDType{
		tmproto.UIDTypeMAID,
		tmproto.UIDTypeHashedEmail,
		tmproto.UIDTypeRampID,
		tmproto.UIDTypeRampIDDerived,
	} {
		assert.False(t, slices.Contains(got, real),
			"%s is not stubbed and must not appear in StubbedUIDTypes()", real)
	}
}

func TestStubbedUIDTypes_StableContent(t *testing.T) {
	got := StubbedUIDTypes()
	want := []tmproto.UIDType{
		tmproto.UIDTypeEUID, tmproto.UIDTypeID5,
		tmproto.UIDTypePairID, tmproto.UIDTypePublisherFirstParty,
		tmproto.UIDTypeUID2,
	}
	sortUID(got)
	sortUID(want)
	assert.Equal(t, want, got)
}

func sortUID(s []tmproto.UIDType) {
	sort.Slice(s, func(i, j int) bool { return s[i] < s[j] })
}
