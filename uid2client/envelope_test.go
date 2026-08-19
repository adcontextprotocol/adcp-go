package uid2client

import (
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"encoding/binary"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestEnvelope_RoundTrip locks in that a request built by sealRequestEnvelope
// round-trips through the equivalent server-side "decrypt then look up
// timestamp+nonce+payload" to the original payload. If the byte layout
// drifts from the wire spec at
// https://unifiedid.com/docs/getting-started/gs-encryption-decryption
// this fails.
func TestEnvelope_RoundTrip(t *testing.T) {
	secret := randKey(t)
	payload := []byte(`{"foo":"bar"}`)
	now := time.UnixMilli(1_700_000_000_000)

	envelope, nonce, err := sealRequestEnvelope(secret, payload, now)
	require.NoError(t, err)

	// Envelope byte layout: version(1) + iv(12) + ciphertext + tag(16).
	require.GreaterOrEqual(t, len(envelope), 1+gcmIVLen+gcmTagLen)
	assert.Equal(t, byte(envelopeVersion), envelope[0], "envelope version byte")

	// Simulate the server: strip version byte, use IV, decrypt, verify
	// the timestamp+nonce+payload layout.
	iv := envelope[1 : 1+gcmIVLen]
	ct := envelope[1+gcmIVLen:]
	plain, err := gcmOpen(secret, iv, ct)
	require.NoError(t, err)
	require.GreaterOrEqual(t, len(plain), unencryptedHeaderLen+len(payload))

	gotTs := int64(binary.BigEndian.Uint64(plain[0:timestampLen])) //nolint:gosec // millisecond timestamp round-trips through uint64 unambiguously.
	assert.Equal(t, now.UnixMilli(), gotTs, "timestamp preserved")

	assert.Equal(t, nonce[:], plain[timestampLen:unencryptedHeaderLen], "nonce preserved")
	assert.Equal(t, payload, plain[unencryptedHeaderLen:], "payload preserved")
}

// TestEnvelope_OpenResponse_HappyPath simulates the server-produced
// response envelope and confirms openResponseEnvelope returns the JSON
// payload after nonce validation.
func TestEnvelope_OpenResponse_HappyPath(t *testing.T) {
	secret := randKey(t)
	var nonce [nonceLen]byte
	_, _ = rand.Read(nonce[:])

	jsonPayload := []byte(`{"body":{"keys":[]}}`)
	unencrypted := make([]byte, 0, unencryptedHeaderLen+len(jsonPayload))
	unencrypted = binary.BigEndian.AppendUint64(unencrypted, uint64(time.Now().UnixMilli())) //nolint:gosec
	unencrypted = append(unencrypted, nonce[:]...)
	unencrypted = append(unencrypted, jsonPayload...)

	var iv [gcmIVLen]byte
	_, _ = rand.Read(iv[:])
	ct, err := gcmSeal(secret, iv[:], unencrypted)
	require.NoError(t, err)

	wire := append([]byte(nil), iv[:]...)
	wire = append(wire, ct...)
	body := base64.StdEncoding.EncodeToString(wire)

	got, err := openResponseEnvelope(secret, body, nonce)
	require.NoError(t, err)
	assert.Equal(t, jsonPayload, got)
}

// TestEnvelope_OpenResponse_NonceMismatch confirms a response whose nonce
// does not match the request nonce is rejected. This is the replay-attack
// guard.
func TestEnvelope_OpenResponse_NonceMismatch(t *testing.T) {
	secret := randKey(t)
	var requestNonce, responseNonce [nonceLen]byte
	_, _ = rand.Read(requestNonce[:])
	_, _ = rand.Read(responseNonce[:])
	// Guarantee they differ (astronomically unlikely to collide, but be
	// explicit — test failure noise here would look like a wire bug).
	if bytes.Equal(requestNonce[:], responseNonce[:]) {
		responseNonce[0] ^= 0x01
	}

	unencrypted := make([]byte, 0, unencryptedHeaderLen+2)
	unencrypted = binary.BigEndian.AppendUint64(unencrypted, uint64(time.Now().UnixMilli())) //nolint:gosec
	unencrypted = append(unencrypted, responseNonce[:]...)
	unencrypted = append(unencrypted, []byte(`{}`)...)

	var iv [gcmIVLen]byte
	_, _ = rand.Read(iv[:])
	ct, err := gcmSeal(secret, iv[:], unencrypted)
	require.NoError(t, err)
	wire := append([]byte(nil), iv[:]...)
	wire = append(wire, ct...)
	body := base64.StdEncoding.EncodeToString(wire)

	_, err = openResponseEnvelope(secret, body, requestNonce)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "nonce mismatch")
}

// TestEnvelope_OpenResponse_TamperedCiphertext confirms GCM authentication
// rejects any bit-flip in the ciphertext.
func TestEnvelope_OpenResponse_TamperedCiphertext(t *testing.T) {
	secret := randKey(t)
	var nonce [nonceLen]byte
	_, _ = rand.Read(nonce[:])

	unencrypted := make([]byte, 0, unencryptedHeaderLen+2)
	unencrypted = binary.BigEndian.AppendUint64(unencrypted, uint64(time.Now().UnixMilli())) //nolint:gosec
	unencrypted = append(unencrypted, nonce[:]...)
	unencrypted = append(unencrypted, []byte(`{}`)...)

	var iv [gcmIVLen]byte
	_, _ = rand.Read(iv[:])
	ct, err := gcmSeal(secret, iv[:], unencrypted)
	require.NoError(t, err)
	wire := append([]byte(nil), iv[:]...)
	wire = append(wire, ct...)
	// Flip a bit in the ciphertext region (past the IV).
	wire[gcmIVLen+1] ^= 0x40
	body := base64.StdEncoding.EncodeToString(wire)

	_, err = openResponseEnvelope(secret, body, nonce)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "decrypt response envelope")
}

func randKey(t *testing.T) []byte {
	t.Helper()
	k := make([]byte, 32)
	if _, err := rand.Read(k); err != nil {
		t.Fatalf("rand: %v", err)
	}
	return k
}
