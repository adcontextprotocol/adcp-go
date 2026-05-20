package tmpxdecoders

import (
	"encoding/hex"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMAID_DashedRoundtrip(t *testing.T) {
	const in = "550e8400-e29b-41d4-a716-446655440000"
	got, err := MAID{}.Decode(t.Context(), in)
	require.NoError(t, err)
	require.Len(t, got, 16)
	assert.Equal(t, "550e8400e29b41d4a716446655440000", hex.EncodeToString(got))
}

func TestMAID_UndashedRoundtrip(t *testing.T) {
	const in = "550e8400e29b41d4a716446655440000"
	got, err := MAID{}.Decode(t.Context(), in)
	require.NoError(t, err)
	assert.Equal(t, "550e8400e29b41d4a716446655440000", hex.EncodeToString(got))
}

func TestMAID_CaseInsensitive(t *testing.T) {
	upper, err := MAID{}.Decode(t.Context(), "550E8400-E29B-41D4-A716-446655440000")
	require.NoError(t, err)
	lower, err := MAID{}.Decode(t.Context(), "550e8400-e29b-41d4-a716-446655440000")
	require.NoError(t, err)
	assert.Equal(t, lower, upper, "case must not affect decoded bytes")
}

func TestMAID_RejectsWrongLength(t *testing.T) {
	cases := []string{
		"",
		"550e8400",
		strings.Repeat("a", 35),
		strings.Repeat("a", 37),
	}
	for _, c := range cases {
		_, err := MAID{}.Decode(t.Context(), c)
		assert.Error(t, err, "len=%d should be rejected", len(c))
	}
}

func TestMAID_RejectsMisplacedDashes(t *testing.T) {
	// Same length (36) but separators in the wrong spots.
	_, err := MAID{}.Decode(t.Context(), "550e84-00e29b41d4-a716-446655-440000-")
	assert.Error(t, err)
}

func TestMAID_RejectsNonHex(t *testing.T) {
	_, err := MAID{}.Decode(t.Context(), "zzze8400-e29b-41d4-a716-446655440000")
	assert.Error(t, err)
}
