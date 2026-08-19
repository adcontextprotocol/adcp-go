package uid2client

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"time"
)

// Token version bytes on the wire. V3 lives at prefix byte 1 = 112 (0x70),
// V4 at prefix byte 1 = 128 (0x80). V2 is distinguished by prefix byte 0
// being literal 2. Values match uid2-client-java's AdvertisingTokenVersion.
const (
	tokenVersionV2 byte = 2
	tokenVersionV3 byte = 112
	tokenVersionV4 byte = 128
)

// V2 CBC block cipher constants. V2 tokens are AES-256-CBC + PKCS#7 padding
// with a 16-byte IV embedded in the ciphertext prefix. Values match Java's
// Uid2Encryption.decryptV2.
const (
	cbcIVLen = 16
)

// decryptToken parses and decrypts a UID2 or EUID advertising token,
// returning the raw identity bytes (typically 32 bytes for V3/V4 tokens
// and the length-prefixed uid string for V2 tokens after base64 decode).
//
// wantScope must match the token's identity scope. A V3/V4 token carries
// its scope in the high nibble of its prefix byte; a V2 token has no scope
// prefix and is treated as either scope's client accepting it.
func decryptToken(store *keyStore, wantScope IdentityScope, token string, now time.Time) ([]byte, error) {
	if store == nil {
		return nil, ErrNotInitialized
	}
	if len(token) < 4 {
		return nil, ErrInvalidToken
	}
	if !store.isValid(now) {
		return nil, ErrKeysStale
	}

	// Inspect the first 4 base64 characters to figure out the encoding
	// (URL-safe alphabet uses '-' and '_') and the version byte. This
	// mirrors Java's Uid2Encryption.decrypt entry point.
	header := token[:4]
	isBase64URL := bytes.ContainsAny([]byte(header), "-_")
	var headerBytes []byte
	var err error
	if isBase64URL {
		headerBytes, err = decodeBase64URL(header)
	} else {
		headerBytes, err = decodeBase64Std(header)
	}
	if err != nil || len(headerBytes) < 2 {
		return nil, ErrInvalidToken
	}

	switch {
	case headerBytes[0] == tokenVersionV2:
		raw, err := decodeBase64Std(token)
		if err != nil {
			return nil, ErrInvalidToken
		}
		return decryptV2(store, raw, now)
	case headerBytes[1] == tokenVersionV3:
		raw, err := decodeBase64Std(token)
		if err != nil {
			return nil, ErrInvalidToken
		}
		return decryptV3OrV4(store, wantScope, raw, now)
	case headerBytes[1] == tokenVersionV4:
		// V4 is base64url on the wire. Java re-decodes as standard
		// base64 after '-'→'+' / '_'→'/'; we accept both alphabets.
		raw, err := decodeBase64URL(token)
		if err != nil {
			// A V4 label with a standard-base64 body would fail the
			// URL decode; retry as standard.
			raw, err = decodeBase64Std(token)
			if err != nil {
				return nil, ErrInvalidToken
			}
		}
		return decryptV3OrV4(store, wantScope, raw, now)
	default:
		return nil, ErrVersionUnsupported
	}
}

// decryptV2 decrypts a v2 UID2 advertising token. Structure:
//
//	byte 0        : version = 2
//	bytes 1..4    : master key ID (int32 big-endian)
//	bytes 5..20   : 16-byte master AES-CBC IV
//	bytes 21..end : AES-256-CBC + PKCS#7 encrypted master payload
//
// Master payload:
//
//	bytes 0..7   : expiry (millis unix, int64 big-endian)
//	bytes 8..11  : site key ID (int32 big-endian)
//	bytes 12..27 : 16-byte identity AES-CBC IV
//	bytes 28..end: AES-256-CBC + PKCS#7 encrypted identity payload
//
// Identity payload:
//
//	bytes 0..3          : site ID (unused for the raw-bytes return path)
//	bytes 4..7          : uid length (int32 big-endian)
//	bytes 8..8+uidLen   : uid as an ASCII base64 STRING
//	remainder           : privacy bits + established timestamp (unused)
//
// The returned bytes are the base64-decoded uid — the raw 32 bytes of the
// underlying UID2/EUID.
func decryptV2(store *keyStore, encrypted []byte, now time.Time) ([]byte, error) {
	if len(encrypted) < 1+4+cbcIVLen {
		return nil, ErrInvalidToken
	}
	masterKeyID := int64(binary.BigEndian.Uint32(encrypted[1:5]))
	masterKey, ok := store.keys[masterKeyID]
	if !ok {
		return nil, fmt.Errorf("%w: master key %d", ErrKeyNotFound, masterKeyID)
	}
	masterIV := encrypted[5 : 5+cbcIVLen]
	masterCT := encrypted[5+cbcIVLen:]
	masterPlain, err := cbcDecrypt(masterKey.secret, masterIV, masterCT)
	if err != nil {
		return nil, ErrInvalidToken
	}
	if len(masterPlain) < 8+4+cbcIVLen {
		return nil, ErrInvalidToken
	}

	expiryMs := int64(binary.BigEndian.Uint64(masterPlain[0:8])) //nolint:gosec // millisecond timestamp; sign preserved.
	expiry := time.UnixMilli(expiryMs)
	if !now.Before(expiry) {
		return nil, ErrTokenExpired
	}

	siteKeyID := int64(binary.BigEndian.Uint32(masterPlain[8:12]))
	siteKey, ok := store.keys[siteKeyID]
	if !ok {
		return nil, fmt.Errorf("%w: site key %d", ErrKeyNotFound, siteKeyID)
	}
	identityIV := masterPlain[12 : 12+cbcIVLen]
	identityCT := masterPlain[12+cbcIVLen:]
	idPlain, err := cbcDecrypt(siteKey.secret, identityIV, identityCT)
	if err != nil {
		return nil, ErrInvalidToken
	}
	if len(idPlain) < 4+4 {
		return nil, ErrInvalidToken
	}
	uidLen := int(binary.BigEndian.Uint32(idPlain[4:8]))
	if uidLen < 0 || 8+uidLen > len(idPlain) {
		return nil, ErrInvalidToken
	}
	uidStr := string(idPlain[8 : 8+uidLen])
	// V2 stores the uid as a base64 STRING inside the encrypted identity
	// payload. Decode to the raw 32-byte identity. Callers that want the
	// base64 form re-encode.
	raw, err := base64.StdEncoding.DecodeString(uidStr)
	if err != nil {
		return nil, ErrInvalidToken
	}
	return raw, nil
}

// decryptV3OrV4 decrypts a V3 or V4 UID2/EUID advertising token. Both
// versions share the same envelope; V4 uses base64url on the wire and V3
// uses standard base64. Structure after base64 decode:
//
//	byte 0     : prefix — packs identity scope (bit 4), identity type
//	             (bits 2..3), and low-2-bits marker (0b11)
//	byte 1     : token version (112 for V3, 128 for V4)
//	bytes 2..5 : master key ID (int32 big-endian)
//	bytes 6..N : AES-256-GCM(masterKey, iv=[6..17], ciphertext[18..N-16], tag[N-16..N])
//
// Master payload (after GCM decrypt):
//
//	bytes 0..7   : expiry (millis unix, int64 big-endian)
//	bytes 8..15  : generated (millis unix, int64 big-endian, unused)
//	bytes 16..28 : operator site id + type + version + key id (unused)
//	bytes 29..32 : site key ID (int32 big-endian)
//	bytes 33..N  : AES-256-GCM(siteKey, iv=[33..44], ciphertext[45..N-16], tag[N-16..N])
//
// Site payload (after GCM decrypt):
//
//	bytes 0..3   : site id (unused)
//	bytes 4..11  : publisher id (unused)
//	bytes 12..15 : client key id (unused)
//	bytes 16..19 : privacy bits (unused)
//	bytes 20..27 : established (millis, unused)
//	bytes 28..35 : refreshed (millis, unused)
//	bytes 36..N  : raw identity bytes (typically 32 bytes)
func decryptV3OrV4(store *keyStore, wantScope IdentityScope, encrypted []byte, now time.Time) ([]byte, error) {
	if len(encrypted) < 6+gcmIVLen+gcmTagLen {
		return nil, ErrInvalidToken
	}
	prefix := encrypted[0]
	tokenScope := IdentityScope((prefix >> 4) & 1)
	if tokenScope != wantScope {
		return nil, ErrScopeMismatch
	}
	// encrypted[1] is the token version, already validated by the caller
	// switch statement.
	masterKeyID := int64(binary.BigEndian.Uint32(encrypted[2:6]))
	masterKey, ok := store.keys[masterKeyID]
	if !ok {
		return nil, fmt.Errorf("%w: master key %d", ErrKeyNotFound, masterKeyID)
	}
	masterPlain, err := gcmOpen(masterKey.secret, encrypted[6:6+gcmIVLen], encrypted[6+gcmIVLen:])
	if err != nil {
		return nil, ErrInvalidToken
	}

	// Master payload structure documented above. We only read the fields
	// we need — expiry, and the site key ID pointing at the inner payload.
	if len(masterPlain) < 8+8+4+1+4+4+4+gcmIVLen+gcmTagLen {
		return nil, ErrInvalidToken
	}
	expiryMs := int64(binary.BigEndian.Uint64(masterPlain[0:8])) //nolint:gosec // millisecond timestamp; sign preserved.
	expiry := time.UnixMilli(expiryMs)
	if !now.Before(expiry) {
		return nil, ErrTokenExpired
	}
	// bytes 8..15  : generated
	// bytes 16..19 : operator_site_id
	// byte  20     : operator_type
	// bytes 21..24 : operator_version
	// bytes 25..28 : operator_key_id
	// bytes 29..32 : site_key_id
	siteKeyID := int64(binary.BigEndian.Uint32(masterPlain[29:33]))
	siteKey, ok := store.keys[siteKeyID]
	if !ok {
		return nil, fmt.Errorf("%w: site key %d", ErrKeyNotFound, siteKeyID)
	}
	sitePlain, err := gcmOpen(siteKey.secret, masterPlain[33:33+gcmIVLen], masterPlain[33+gcmIVLen:])
	if err != nil {
		return nil, ErrInvalidToken
	}

	// Site payload constants match Uid2Encryption.decryptV3's layout.
	const rawIDOffset = 4 + 8 + 4 + 4 + 8 + 8 // 36
	if len(sitePlain) < rawIDOffset {
		return nil, ErrInvalidToken
	}
	raw := make([]byte, len(sitePlain)-rawIDOffset)
	copy(raw, sitePlain[rawIDOffset:])
	return raw, nil
}

// cbcDecrypt performs AES-256-CBC decrypt with PKCS#7 padding removal.
// V2 UID2 tokens use CBC (per uid2-client-java's Uid2Encryption.decrypt),
// so we need this alongside the GCM primitives.
//
// Padding removal is defensive against malformed pads (the standard "final
// byte is pad length" trick), but note that CBC is malleable — the caller
// must not rely on this function's error/no-error signal as a MAC. In the
// UID2 V2 wire, the outer master key and the inner site key each do their
// own CBC decrypt, and neither carries an authentication tag; a tampered
// V2 token can produce arbitrary garbage after decryption. Callers must
// therefore validate structural invariants (uid_length, base64-decodability
// of the uid, etc.) before trusting the result — which we do above.
func cbcDecrypt(secret, iv, ciphertext []byte) ([]byte, error) {
	if len(iv) != cbcIVLen {
		return nil, fmt.Errorf("uid2client: cbc iv length %d, want %d", len(iv), cbcIVLen)
	}
	block, err := aes.NewCipher(secret)
	if err != nil {
		return nil, fmt.Errorf("uid2client: aes cipher: %w", err)
	}
	if len(ciphertext) == 0 || len(ciphertext)%block.BlockSize() != 0 {
		return nil, fmt.Errorf("uid2client: cbc ciphertext length %d not a multiple of block size", len(ciphertext))
	}
	out := make([]byte, len(ciphertext))
	cipher.NewCBCDecrypter(block, iv).CryptBlocks(out, ciphertext)
	// Strip PKCS#7 padding.
	if len(out) == 0 {
		return nil, errors.New("uid2client: empty cbc plaintext")
	}
	padLen := int(out[len(out)-1])
	if padLen == 0 || padLen > block.BlockSize() || padLen > len(out) {
		return nil, errors.New("uid2client: invalid pkcs7 padding")
	}
	for i := len(out) - padLen; i < len(out); i++ {
		if int(out[i]) != padLen {
			return nil, errors.New("uid2client: invalid pkcs7 padding bytes")
		}
	}
	return out[:len(out)-padLen], nil
}

// decodeBase64Std tolerates both padded and unpadded standard base64. The
// UID2 wire uses padded standard base64 for V2/V3, but hardening the
// parser against a stripped-pad token from a well-intentioned proxy is
// cheap and matches Java's tolerance.
func decodeBase64Std(s string) ([]byte, error) {
	if b, err := base64.StdEncoding.DecodeString(s); err == nil {
		return b, nil
	}
	return base64.RawStdEncoding.DecodeString(s)
}

// decodeBase64URL tolerates both padded and unpadded URL-safe base64. V4
// tokens use URL-safe base64 without padding on the wire.
func decodeBase64URL(s string) ([]byte, error) {
	if b, err := base64.URLEncoding.DecodeString(s); err == nil {
		return b, nil
	}
	return base64.RawURLEncoding.DecodeString(s)
}
