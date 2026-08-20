package uid2client

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"encoding/binary"
	"fmt"
	"time"
)

// Envelope constants match the wire spec at
// https://unifiedid.com/docs/getting-started/gs-encryption-decryption and
// the Java reference client's implementation. Do not change these without
// coordinating with the operator.
const (
	envelopeVersion = 1

	gcmIVLen  = 12
	gcmTagLen = 16

	// nonceLen is the "request nonce" the client mints to prove response
	// freshness. This is NOT the AES-GCM IV — see IV vs nonce terminology
	// at https://unifiedid.com/docs/getting-started/gs-encryption-decryption
	nonceLen     = 8
	timestampLen = 8

	// unencryptedHeaderLen is timestamp + nonce, the fixed prefix of the
	// unencrypted request/response envelope before the JSON payload.
	unencryptedHeaderLen = timestampLen + nonceLen
)

// sealRequestEnvelope encrypts an outbound request envelope for a v2 UID2
// endpoint. The returned envelope bytes are:
//
//	byte 0        : version (0x01)
//	bytes 1..12   : 12-byte AES-GCM IV (random)
//	bytes 13..N-16: AES-256-GCM(secret, iv, plaintext)
//	last 16 bytes : GCM authentication tag (appended by AEAD.Seal)
//
// The plaintext is:
//
//	bytes 0..7  : millisecond unix timestamp (int64 big-endian)
//	bytes 8..15 : the returned request nonce (random 8 bytes)
//	bytes 16..N : the caller-supplied payload (typically an empty body for
//	              /v2/key/bidstream, or a JSON object for other endpoints)
//
// The nonce is returned separately so the caller can validate it against
// the server's response envelope.
func sealRequestEnvelope(secret []byte, payload []byte, now time.Time) (envelope []byte, nonce [nonceLen]byte, err error) {
	if _, err := rand.Read(nonce[:]); err != nil {
		return nil, nonce, fmt.Errorf("uid2client: generate nonce: %w", err)
	}

	plaintext := make([]byte, 0, unencryptedHeaderLen+len(payload))
	plaintext = binary.BigEndian.AppendUint64(plaintext, uint64(now.UnixMilli())) //nolint:gosec // millisecond timestamp fits in uint64 unambiguously.
	plaintext = append(plaintext, nonce[:]...)
	plaintext = append(plaintext, payload...)

	var iv [gcmIVLen]byte
	if _, err := rand.Read(iv[:]); err != nil {
		return nil, nonce, fmt.Errorf("uid2client: generate iv: %w", err)
	}

	sealed, err := gcmSeal(secret, iv[:], plaintext)
	if err != nil {
		return nil, nonce, err
	}

	envelope = make([]byte, 0, 1+gcmIVLen+len(sealed))
	envelope = append(envelope, envelopeVersion)
	envelope = append(envelope, iv[:]...)
	envelope = append(envelope, sealed...)
	return envelope, nonce, nil
}

// openResponseEnvelope decrypts a base64-encoded response body from a v2
// UID2 endpoint and validates that the server echoed the caller's nonce.
// Returns the JSON payload bytes (everything after the 16-byte
// timestamp+nonce header of the unencrypted response envelope).
//
// The response envelope format on the wire is:
//
//	bytes 0..11 : 12-byte AES-GCM IV
//	bytes 12..N-16: AES-256-GCM ciphertext
//	last 16 bytes : GCM authentication tag
//
// After AES-GCM decrypt:
//
//	bytes 0..7  : server timestamp (unused by this client)
//	bytes 8..15 : nonce (must match wantNonce)
//	bytes 16..N : JSON payload
//
// The nonce comparison uses subtle.ConstantTimeCompare because that byte
// range is under attacker control on a MITM path. GCM authentication
// already prevents ciphertext forgery, but the constant-time check
// forecloses timing analysis in the unlikely event a partial-decrypt
// primitive shows up in a future refactor.
func openResponseEnvelope(secret []byte, base64Body string, wantNonce [nonceLen]byte) ([]byte, error) {
	body, err := base64.StdEncoding.DecodeString(base64Body)
	if err != nil {
		// Try URL-safe just in case; the UID2 operator responds with
		// standard base64 but a proxy might rewrite. Falling back is
		// cheap and matches the Java client's leniency.
		body, err = base64.URLEncoding.DecodeString(base64Body)
		if err != nil {
			return nil, fmt.Errorf("uid2client: decode response body: %w", err)
		}
	}

	if len(body) < gcmIVLen+gcmTagLen {
		return nil, fmt.Errorf("uid2client: response envelope too short (%d bytes)", len(body))
	}

	plaintext, err := gcmOpen(secret, body[:gcmIVLen], body[gcmIVLen:])
	if err != nil {
		return nil, fmt.Errorf("uid2client: decrypt response envelope: %w", err)
	}

	if len(plaintext) < unencryptedHeaderLen {
		return nil, fmt.Errorf("uid2client: decrypted response too short (%d bytes)", len(plaintext))
	}

	if subtle.ConstantTimeCompare(plaintext[timestampLen:unencryptedHeaderLen], wantNonce[:]) != 1 {
		return nil, fmt.Errorf("uid2client: response nonce mismatch")
	}

	return plaintext[unencryptedHeaderLen:], nil
}

// gcmSeal encrypts plaintext with AES-256-GCM using the supplied 12-byte
// IV. Returns the ciphertext with the 16-byte GCM tag appended. The caller
// is responsible for supplying a per-message-unique IV; reusing an IV
// under the same key breaks GCM's security guarantees.
func gcmSeal(secret, iv, plaintext []byte) ([]byte, error) {
	aead, err := newAEAD(secret)
	if err != nil {
		return nil, err
	}
	if len(iv) != gcmIVLen {
		return nil, fmt.Errorf("uid2client: iv length %d, want %d", len(iv), gcmIVLen)
	}
	return aead.Seal(nil, iv, plaintext, nil), nil
}

// gcmOpen decrypts an AES-256-GCM ciphertext (with tag appended) using the
// supplied 12-byte IV. crypto/cipher.AEAD.Open verifies the tag in
// constant time; a mismatch produces a non-nil error and does not leak
// partial plaintext.
func gcmOpen(secret, iv, ciphertextWithTag []byte) ([]byte, error) {
	aead, err := newAEAD(secret)
	if err != nil {
		return nil, err
	}
	if len(iv) != gcmIVLen {
		return nil, fmt.Errorf("uid2client: iv length %d, want %d", len(iv), gcmIVLen)
	}
	return aead.Open(nil, iv, ciphertextWithTag, nil)
}

func newAEAD(secret []byte) (cipher.AEAD, error) {
	block, err := aes.NewCipher(secret)
	if err != nil {
		return nil, fmt.Errorf("uid2client: aes cipher: %w", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("uid2client: gcm mode: %w", err)
	}
	return aead, nil
}
