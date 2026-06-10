package tmpxdecoders

import (
	"encoding/hex"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/adcontextprotocol/adcp-go/tmproto"
)

func TestWorldIDNullifier_FullWidthHex(t *testing.T) {
	// A 64-hex-char (32-byte) nullifier round-trips to its raw bytes,
	// with or without the 0x prefix.
	raw := strings.Repeat("ab", 32)
	want, err := hex.DecodeString(raw)
	require.NoError(t, err)

	for _, in := range []string{raw, "0x" + raw, "0X" + raw, "  0x" + raw + "  "} {
		got, err := WorldIDNullifier{}.Decode(t.Context(), in)
		require.NoError(t, err, "input %q", in)
		assert.Equal(t, want, got, "input %q", in)
		size, _ := tmproto.TmpxTokenSize(tmproto.TmpxTypeWorldIDNullifier)
		assert.Len(t, got, size, "must produce the registry token width")
	}
}

func TestWorldIDNullifier_LeftPadsShortFieldElement(t *testing.T) {
	// World drops leading-zero nibbles, so a sub-32-byte field element must
	// be left-padded big-endian to the fixed width — and the same logical
	// value must encode identically with or without those leading zeros.
	got, err := WorldIDNullifier{}.Decode(t.Context(), "0x2a")
	require.NoError(t, err)
	require.Len(t, got, 32)
	assert.Equal(t, byte(0x2a), got[31])
	for _, b := range got[:31] {
		assert.Equal(t, byte(0), b)
	}

	padded, err := WorldIDNullifier{}.Decode(t.Context(), "0x"+strings.Repeat("0", 62)+"2a")
	require.NoError(t, err)
	assert.Equal(t, got, padded, "leading zeros must not change the encoding")
}

func TestWorldIDNullifier_RejectsBadInput(t *testing.T) {
	cases := map[string]string{
		"empty":         "",
		"only prefix":   "0x",
		"non-hex":       "0xnothex",
		"over 32 bytes": "0x" + strings.Repeat("ff", 33),
	}
	for name, in := range cases {
		_, err := WorldIDNullifier{}.Decode(t.Context(), in)
		assert.Error(t, err, name)
	}
}

// TestWorldIDNullifier_NotInDefaultRegistry locks the verify-before-trust
// invariant: the nullifier encoder must never be reachable from the inbound
// decode path, with or without LiveRamp. A sender-asserted
// world_id_nullifier token has no decoder and is dropped; the nullifier
// reaches the wire only via the verified-identity stage.
func TestWorldIDNullifier_NotInDefaultRegistry(t *testing.T) {
	for _, opts := range []RegistryOptions{
		{},
		{LiveRampClient: &fakeLiveRampClient{mappedLen: 32}},
	} {
		reg := NewDefaultRegistry(opts)
		_, ok := reg[tmproto.UIDTypeWorldIDNullifier]
		assert.False(t, ok, "world_id_nullifier must never have an inbound decoder")
	}
}
