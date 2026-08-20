package uid2client

// This file is a test-only helper that mints UID2/EUID advertising tokens
// against a supplied key store. It mirrors uid2-client-java's
// Uid2TokenGenerator.generateUID2TokenV3OrV4 and generateUid2TokenV2
// byte-for-byte so decrypt tests can round-trip realistic fixtures. Not
// part of the public API.

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/binary"
	"testing"
	"time"
)

// tokenGenParams packs the knobs the reference Java generator exposes.
// Fields we don't need for the current test surface are omitted (privacy
// bits, client-side-generated flag, etc.); add them here when a test
// wants them.
type tokenGenParams struct {
	Scope       IdentityScope
	IdentityRaw string // 32-byte-decoded UID2/EUID (base64 of the raw bytes)
	// V4 uses "phone" identity type when the base64 alphabet's first char
	// is 'F' or 'B' — matches Java. Everything else is treated as email.
	Expiry    time.Time
	Generated time.Time
}

// generateV4Token mints a V4 advertising token (base64url on the wire).
// masterKey and siteKey must be present in the keyStore under their IDs
// so the corresponding decryptV3OrV4 call can find them.
func generateV4Token(t *testing.T, masterKey, siteKey *key, p tokenGenParams) string {
	t.Helper()
	return generateV3OrV4Token(t, masterKey, siteKey, p, tokenVersionV4)
}

func generateV3Token(t *testing.T, masterKey, siteKey *key, p tokenGenParams) string {
	t.Helper()
	return generateV3OrV4Token(t, masterKey, siteKey, p, tokenVersionV3)
}

func generateV3OrV4Token(t *testing.T, masterKey, siteKey *key, p tokenGenParams, version byte) string {
	t.Helper()
	if p.Expiry.IsZero() {
		p.Expiry = time.Now().Add(1 * time.Hour)
	}
	if p.Generated.IsZero() {
		p.Generated = time.Now()
	}

	uidRaw, err := base64.StdEncoding.DecodeString(p.IdentityRaw)
	if err != nil {
		t.Fatalf("token gen: decode identity raw: %v", err)
	}

	// Site payload (matches Uid2TokenGenerator.generateUID2TokenV3OrV4).
	//   int32 site_id, int64 publisher_id, int32 client_key_id,
	//   int32 privacy_bits, int64 established, int64 refreshed, raw uid
	sitePayload := make([]byte, 0, 4+8+4+4+8+8+len(uidRaw))
	sitePayload = binary.BigEndian.AppendUint32(sitePayload, 0)                               // site_id (unused)
	sitePayload = binary.BigEndian.AppendUint64(sitePayload, 0)                               // publisher_id
	sitePayload = binary.BigEndian.AppendUint32(sitePayload, 0)                               // client_key_id
	sitePayload = binary.BigEndian.AppendUint32(sitePayload, 0)                               // privacy_bits
	sitePayload = binary.BigEndian.AppendUint64(sitePayload, uint64(p.Generated.UnixMilli())) //nolint:gosec // ms timestamp fits.
	sitePayload = binary.BigEndian.AppendUint64(sitePayload, uint64(p.Generated.UnixMilli())) //nolint:gosec // refreshed same as generated in the reference.
	sitePayload = append(sitePayload, uidRaw...)

	siteIV := randBytes(t, gcmIVLen)
	siteCT, err := gcmSeal(siteKey.secret, siteIV, sitePayload)
	if err != nil {
		t.Fatalf("token gen: seal site payload: %v", err)
	}

	// Master payload (matches Uid2TokenGenerator.generateUID2TokenV3OrV4).
	masterPayload := make([]byte, 0, 8+8+4+1+4+4+4+gcmIVLen+len(siteCT))
	masterPayload = binary.BigEndian.AppendUint64(masterPayload, uint64(p.Expiry.UnixMilli()))    //nolint:gosec
	masterPayload = binary.BigEndian.AppendUint64(masterPayload, uint64(p.Generated.UnixMilli())) //nolint:gosec
	masterPayload = binary.BigEndian.AppendUint32(masterPayload, 0)                               // operator site id
	masterPayload = append(masterPayload, 1)                                                      // operator type
	masterPayload = binary.BigEndian.AppendUint32(masterPayload, 0)                               // operator version
	masterPayload = binary.BigEndian.AppendUint32(masterPayload, 0)                               // operator key id
	masterPayload = binary.BigEndian.AppendUint32(masterPayload, uint32(siteKey.id))              //nolint:gosec // key ids are 32-bit on the wire.
	masterPayload = append(masterPayload, siteIV...)
	masterPayload = append(masterPayload, siteCT...)

	masterIV := randBytes(t, gcmIVLen)
	masterCT, err := gcmSeal(masterKey.secret, masterIV, masterPayload)
	if err != nil {
		t.Fatalf("token gen: seal master payload: %v", err)
	}

	// V3/V4 identity type inference from Java: first raw-uid byte 0xB0 or
	// 0xF0 nibble → phone, else email. For our tests we don't cover
	// phone specifically so this bit is 0.
	identityType := byte(0)                                            // email
	prefix := byte((int(p.Scope) << 4) | (int(identityType) << 2) | 3) //nolint:gosec // scope and identity type are 1-bit and 2-bit constants; the OR fits in one byte.

	envelope := make([]byte, 0, 6+gcmIVLen+len(masterCT))
	envelope = append(envelope, prefix)
	envelope = append(envelope, version)
	envelope = binary.BigEndian.AppendUint32(envelope, uint32(masterKey.id)) //nolint:gosec
	envelope = append(envelope, masterIV...)
	envelope = append(envelope, masterCT...)

	if version == tokenVersionV4 {
		return base64.RawURLEncoding.EncodeToString(envelope)
	}
	return base64.StdEncoding.EncodeToString(envelope)
}

// generateV2Token mints a V2 advertising token. Matches
// Uid2TokenGenerator.generateUid2TokenV2 byte-for-byte.
func generateV2Token(t *testing.T, masterKey, siteKey *key, p tokenGenParams) string {
	t.Helper()
	if p.Expiry.IsZero() {
		p.Expiry = time.Now().Add(1 * time.Hour)
	}

	// V2 stores the uid as the base64 STRING inside the identity payload
	// (see Java's decryptV2 — it reads a length-prefixed byte array and
	// then base64-decodes it during our decrypt). Java's generator writes
	// the ASCII base64 string here directly.
	uidStr := []byte(p.IdentityRaw)

	// Identity payload: siteId(4) + uidLen(4) + uid + privacy(4) + established(8)
	identityPayload := make([]byte, 0, 4+4+len(uidStr)+4+8)
	identityPayload = binary.BigEndian.AppendUint32(identityPayload, uint32(siteKey.siteID)) //nolint:gosec
	identityPayload = binary.BigEndian.AppendUint32(identityPayload, uint32(len(uidStr)))    //nolint:gosec
	identityPayload = append(identityPayload, uidStr...)
	identityPayload = binary.BigEndian.AppendUint32(identityPayload, 0)                              //nolint:gosec // privacy bits
	identityPayload = binary.BigEndian.AppendUint64(identityPayload, uint64(time.Now().UnixMilli())) //nolint:gosec // established

	identityIV := randBytes(t, cbcIVLen)
	identityCT := cbcEncrypt(t, siteKey.secret, identityIV, identityPayload)

	// Master payload: expiry(8) + siteKeyId(4) + identityIV(16) + identityCT
	masterPayload := make([]byte, 0, 8+4+len(identityIV)+len(identityCT))
	masterPayload = binary.BigEndian.AppendUint64(masterPayload, uint64(p.Expiry.UnixMilli())) //nolint:gosec
	masterPayload = binary.BigEndian.AppendUint32(masterPayload, uint32(siteKey.id))           //nolint:gosec
	masterPayload = append(masterPayload, identityIV...)
	masterPayload = append(masterPayload, identityCT...)

	masterIV := randBytes(t, cbcIVLen)
	masterCT := cbcEncrypt(t, masterKey.secret, masterIV, masterPayload)

	// Envelope: version(1) + masterKeyId(4) + masterIV(16) + masterCT
	envelope := make([]byte, 0, 1+4+len(masterIV)+len(masterCT))
	envelope = append(envelope, tokenVersionV2)
	envelope = binary.BigEndian.AppendUint32(envelope, uint32(masterKey.id)) //nolint:gosec
	envelope = append(envelope, masterIV...)
	envelope = append(envelope, masterCT...)

	return base64.StdEncoding.EncodeToString(envelope)
}

// cbcEncrypt performs AES-256-CBC + PKCS#7 padding — the test-time
// counterpart of the cbcDecrypt in token.go. Kept in the test file so the
// production code never links against an encrypt primitive it doesn't
// need.
func cbcEncrypt(t *testing.T, secret, iv, plaintext []byte) []byte {
	t.Helper()
	block, err := aes.NewCipher(secret)
	if err != nil {
		t.Fatalf("cbc encrypt: aes: %v", err)
	}
	bs := block.BlockSize()
	pad := bs - (len(plaintext) % bs)
	padded := make([]byte, len(plaintext)+pad)
	copy(padded, plaintext)
	for i := len(plaintext); i < len(padded); i++ {
		padded[i] = byte(pad) //nolint:gosec // pad is bounded to [1, block size] which is 16 bytes; always fits.
	}
	out := make([]byte, len(padded))
	cipher.NewCBCEncrypter(block, iv).CryptBlocks(out, padded)
	return out
}

func randBytes(t *testing.T, n int) []byte {
	t.Helper()
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		t.Fatalf("rand: %v", err)
	}
	return b
}

// makeTestKey builds an in-memory key with a deterministic secret filled
// with the supplied byte value. Convenience for test fixtures that don't
// care about the specific key material.
func makeTestKey(t *testing.T, id int64, siteID int, secretFill byte) *key {
	t.Helper()
	secret := make([]byte, 32)
	for i := range secret {
		secret[i] = secretFill
	}
	return &key{
		id:        id,
		siteID:    siteID,
		created:   time.Now().Add(-24 * time.Hour),
		activates: time.Now().Add(-1 * time.Hour),
		expires:   time.Now().Add(24 * time.Hour),
		secret:    secret,
	}
}

// makeTestStore builds a keyStore from a set of test keys, computing the
// latest-expiry aggregate so isValid returns true.
func makeTestStore(scope IdentityScope, keys ...*key) *keyStore {
	s := &keyStore{keys: make(map[int64]*key), scope: scope}
	for _, k := range keys {
		s.keys[k.id] = k
		if k.expires.After(s.latestExpiry) {
			s.latestExpiry = k.expires
		}
	}
	return s
}
