package tmpxdecoders

import (
	"crypto/sha256"
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
		assert.Len(t, got, worldIDNullifierBytes, "Decode yields the 32-byte nullifier component")
	}
}

func TestWorldIDNullifier_TokenIsRelyingPartyScoped(t *testing.T) {
	const rp = "rp.example"
	token, err := WorldIDNullifier{}.Token(t.Context(), rp, "0x2a")
	require.NoError(t, err)

	size, _ := tmproto.TmpxTokenSize(tmproto.TmpxTypeWorldIDNullifier)
	require.Len(t, token, size, "token must be the registry width")

	digest := sha256.Sum256([]byte(rp))
	assert.Equal(t, digest[:worldIDRelyingPartyDigestBytes], token[:worldIDRelyingPartyDigestBytes],
		"leading bytes are the relying-party digest")
	nullifier, err := WorldIDNullifier{}.Decode(t.Context(), "0x2a")
	require.NoError(t, err)
	assert.Equal(t, nullifier, token[worldIDRelyingPartyDigestBytes:], "trailing bytes are the nullifier")

	other, err := WorldIDNullifier{}.Token(t.Context(), "rp.other", "0x2a")
	require.NoError(t, err)
	assert.NotEqual(t, token, other, "same nullifier under different relying parties yields distinct tokens")
}

func TestWorldIDNullifier_TokenRejectsMissingRelyingPartyOrBadNullifier(t *testing.T) {
	_, err := WorldIDNullifier{}.Token(t.Context(), "", "0x2a")
	assert.Error(t, err, "a token without a relying party must not be formed")

	_, err = WorldIDNullifier{}.Token(t.Context(), "rp.example", "0xnothex")
	assert.Error(t, err, "a malformed nullifier must not be sealed")
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
