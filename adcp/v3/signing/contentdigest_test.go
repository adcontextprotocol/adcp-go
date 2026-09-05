package signing

import (
	"crypto/sha256"
	"encoding/base64"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestComputeSHA256DigestHeader(t *testing.T) {
	// Matches positive/002 body and its Content-Digest.
	body := []byte(`{"plan_id":"plan_001"}`)
	got := computeSHA256DigestHeader(body)
	assert.Equal(t, "sha-256=:SNIVma8dgUBx/U1CBaYFQnsJep9S0/tXaNXlQQOdoxQ=:", got)
}

func TestExtractSHA256FromDigestHeader(t *testing.T) {
	raw := []byte("hello")
	sum := sha256.Sum256(raw)
	header := "sha-256=:" + base64.StdEncoding.EncodeToString(sum[:]) + ":"
	got, ok, err := extractSHA256FromDigestHeader(header)
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, sum[:], got)

	// Multiple algorithms — sha-256 last.
	h2 := "sha-512=:abc:, sha-256=:" + base64.StdEncoding.EncodeToString(sum[:]) + ":"
	got2, ok2, err := extractSHA256FromDigestHeader(h2)
	require.NoError(t, err)
	require.True(t, ok2)
	assert.Equal(t, sum[:], got2)
}

func TestBinaryEncodingZeroValueUsesLegacyBase64URL(t *testing.T) {
	value := []byte{0xfb, 0xff}
	assert.Equal(t, "-_8", encodeBinary(value, ""))
	decoded, err := decodeBinary("-_8", "")
	require.NoError(t, err)
	assert.Equal(t, value, decoded)
}
