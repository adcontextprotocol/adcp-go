package tmpxdecoders

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestHashedEmail_Roundtrip(t *testing.T) {
	want := sha256.Sum256([]byte("user@example.com"))
	in := hex.EncodeToString(want[:])
	got, err := HashedEmail{}.Decode(t.Context(), in)
	require.NoError(t, err)
	assert.Equal(t, want[:], got)
}

func TestHashedEmail_CaseInsensitive(t *testing.T) {
	want := sha256.Sum256([]byte("anyone@example.org"))
	hexLower := hex.EncodeToString(want[:])
	hexUpper := strings.ToUpper(hexLower)
	lower, err := HashedEmail{}.Decode(t.Context(), hexLower)
	require.NoError(t, err)
	upper, err := HashedEmail{}.Decode(t.Context(), hexUpper)
	require.NoError(t, err)
	assert.Equal(t, lower, upper)
}

func TestHashedEmail_RejectsWrongLength(t *testing.T) {
	cases := []string{"", strings.Repeat("a", 63), strings.Repeat("a", 65), strings.Repeat("a", 32)}
	for _, c := range cases {
		_, err := HashedEmail{}.Decode(t.Context(), c)
		assert.Error(t, err, "len=%d should be rejected", len(c))
	}
}

func TestHashedEmail_RejectsNonHex(t *testing.T) {
	bad := strings.Repeat("z", 64)
	_, err := HashedEmail{}.Decode(t.Context(), bad)
	assert.Error(t, err)
}
