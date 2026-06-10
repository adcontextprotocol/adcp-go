// Package tmproto's tmpx.go implements TMPX exposure-token encoding per the
// TMP spec §"TMPX Exposure Tokens".
//
// TMPX is an HPKE-encrypted opaque token that flows from the identity-match
// read replica → router → publisher → buyer's impression pixel. Only the
// buyer's cluster master holds the recipient private key. The wire format is
// `<kid>.<base64url_no_pad(enc || ciphertext_with_tag)>` and the cipher suite
// is fixed by the spec:
//
//   - KEM: DHKEM(X25519, HKDF-SHA256)  — RFC 9180 0x0020
//   - KDF: HKDF-SHA256                 — RFC 9180 0x0001
//   - AEAD: ChaCha20-Poly1305          — RFC 9180 0x0003
//   - Mode: mode_base (no PSK, no auth)
//
// HPKE is implemented in this package with stdlib + chacha20poly1305 to keep
// adcp-go's dependency footprint minimal — protocol-layer code shouldn't pull
// in an HPKE framework for one cipher suite.
package tmproto

import (
	"crypto/ecdh"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"time"

	"golang.org/x/crypto/chacha20poly1305"
)

// TmpxFormatVersion is the TMPX binary plaintext format version per spec.
const TmpxFormatVersion uint8 = 0x01

// TmpxMaxKidLen is the spec-defined cap on the kid string prefixed to every
// TMPX wire token. Senders that size payloads against the wire budget should
// reserve this many bytes even when the currently advertised kid is shorter —
// JWKS rotations can change the kid length between seals.
const TmpxMaxKidLen = 8

// TmpxHeaderBytes is the size of the binary plaintext header (version,
// timestamp, country, nonce, count).
const TmpxHeaderBytes = 16

// TmpxHPKEOverheadBytes is the post-seal HPKE overhead added on top of the
// plaintext: 32 bytes of encapsulated KEM key + 16 bytes of AEAD auth tag.
const TmpxHPKEOverheadBytes = 48

// TmpxMaxWireBytes is the maximum size of a TMPX wire string after base64url
// encoding. 255 bytes is the GAM macro substitution limit — tokens above it
// cannot be inlined into creative tracking URLs without truncation.
const TmpxMaxWireBytes = 255

// HPKE algorithm IDs per RFC 9180.
const (
	hpkeKEMX25519HKDFSHA256 uint16 = 0x0020
	hpkeKDFHKDFSHA256       uint16 = 0x0001
	hpkeAEADChaCha20Poly    uint16 = 0x0003
	hpkeModeBase            byte   = 0x00
	hpkeNh                         = 32 // HKDF-SHA256 output size
	hpkeNk                         = chacha20poly1305.KeySize
	hpkeNn                         = chacha20poly1305.NonceSize
)

// TmpxTypeID is one entry in the TMPX type registry. Type IDs are stable —
// new types append, existing IDs never change. Tokens are stored in binary;
// callers convert source string identifiers to binary before encoding.
type TmpxTypeID uint8

const (
	TmpxTypeUID2                TmpxTypeID = 1
	TmpxTypeEUID                TmpxTypeID = 2
	TmpxTypeID5                 TmpxTypeID = 3
	TmpxTypeRampID              TmpxTypeID = 4
	TmpxTypeRampIDDerived       TmpxTypeID = 5
	TmpxTypeMAID                TmpxTypeID = 6
	TmpxTypePairID              TmpxTypeID = 7
	TmpxTypeHashedEmail         TmpxTypeID = 8
	TmpxTypePublisherFirstParty TmpxTypeID = 9
	TmpxTypeWorldIDNullifier    TmpxTypeID = 10
)

// TmpxTokenSize returns the spec-defined binary size for a Type ID.
// Returns (0, false) when typeID is unknown — parsers MUST stop on unknown IDs
// and treat the remaining entries as absent.
func TmpxTokenSize(typeID TmpxTypeID) (int, bool) {
	switch typeID {
	case TmpxTypeUID2, TmpxTypeEUID, TmpxTypeID5, TmpxTypeRampID,
		TmpxTypePairID, TmpxTypeHashedEmail, TmpxTypePublisherFirstParty,
		TmpxTypeWorldIDNullifier:
		return 32, true
	case TmpxTypeRampIDDerived:
		return 48, true
	case TmpxTypeMAID:
		return 16, true
	}
	return 0, false
}

// TmpxEntry is one identity token packed into a TMPX plaintext.
type TmpxEntry struct {
	TypeID TmpxTypeID
	Token  []byte // exactly TmpxTokenSize(TypeID) bytes
}

// TmpxRecipient is a buyer-cluster public key the token is sealed to. Kid is
// max 8 chars, opaque, MUST NOT encode geographic or deployment information.
type TmpxRecipient struct {
	Kid       string
	PublicKey *ecdh.PublicKey // X25519
}

// EncodeTmpxPlaintext builds the binary plaintext per spec §"Binary format":
// 16-byte header (version, ts, country, nonce, count) followed by entries.
// Country is exactly 2 ASCII bytes (ISO 3166-1 alpha-2). The nonce is randomly
// drawn — replay deduplication at the master uses it.
func EncodeTmpxPlaintext(country string, entries []TmpxEntry, ts time.Time) ([]byte, error) {
	return encodeTmpxPlaintextWith(country, entries, ts, rand.Reader)
}

func encodeTmpxPlaintextWith(country string, entries []TmpxEntry, ts time.Time, r io.Reader) ([]byte, error) {
	// TMPX validation errors are caller-visible, so diagnostics below avoid
	// echoing submitted values, indices, type IDs, or counts.
	if len(country) != 2 || !isASCIIUpper(country[0]) || !isASCIIUpper(country[1]) {
		return nil, errors.New("tmproto: tmpx country invalid; expected ISO 3166-1 alpha-2 uppercase ASCII")
	}
	if len(entries) > 255 {
		return nil, errors.New("tmproto: tmpx entries exceed maximum")
	}
	for _, e := range entries {
		size, ok := TmpxTokenSize(e.TypeID)
		if !ok {
			return nil, errors.New("tmproto: tmpx entry has unknown type id")
		}
		if len(e.Token) != size {
			return nil, errors.New("tmproto: tmpx entry token has invalid size")
		}
	}

	var nonce [8]byte
	if _, err := io.ReadFull(r, nonce[:]); err != nil {
		return nil, fmt.Errorf("tmproto: tmpx nonce read: %w", err)
	}

	out := make([]byte, 0, 16+entriesByteLen(entries))
	out = append(out, TmpxFormatVersion)
	out = binary.BigEndian.AppendUint32(out, uint32(ts.Unix())) //nolint:gosec // pre-2106 timestamps fit
	out = append(out, country[0], country[1])
	out = append(out, nonce[:]...)
	out = append(out, byte(len(entries))) //nolint:gosec // bounds-checked to ≤255 above
	for _, e := range entries {
		out = append(out, byte(e.TypeID))
		out = append(out, e.Token...)
	}
	return out, nil
}

func entriesByteLen(entries []TmpxEntry) int {
	n := 0
	for _, e := range entries {
		n += 1 + len(e.Token)
	}
	return n
}

// TmpxWireSize returns the wire-string length a TMPX token will have after
// HPKE sealing and base64url encoding, given a recipient kid of length kidLen
// and entriesBytes worth of plaintext entry payload (sum of 1 + tokenSize over
// the entries). Callers use this to keep emitted tokens under
// TmpxMaxWireBytes before paying for the seal.
func TmpxWireSize(kidLen, entriesBytes int) int {
	rawLen := TmpxHeaderBytes + TmpxHPKEOverheadBytes + entriesBytes
	return kidLen + 1 + base64.RawURLEncoding.EncodedLen(rawLen)
}

func isASCIIUpper(b byte) bool { return b >= 'A' && b <= 'Z' }

func validateTmpxKid(kid string) error {
	if len(kid) == 0 || len(kid) > TmpxMaxKidLen {
		return errors.New("tmproto: tmpx kid must be 1..8 URL-safe chars")
	}
	for i := 0; i < len(kid); i++ {
		if !isTmpxKidChar(kid[i]) {
			return errors.New("tmproto: tmpx kid must be 1..8 URL-safe chars")
		}
	}
	return nil
}

func isTmpxKidChar(b byte) bool {
	return (b >= 'A' && b <= 'Z') ||
		(b >= 'a' && b <= 'z') ||
		(b >= '0' && b <= '9') ||
		b == '-' || b == '_'
}

// SealTmpx HPKE-encrypts plaintext under recipient's X25519 public key and
// returns the wire-format string `kid.b64url(enc||ct)` per spec.
//
// info is bound into the HPKE key schedule and is left empty in the spec —
// callers should pass nil unless the buyer profile defines a value.
func SealTmpx(recipient TmpxRecipient, info, plaintext []byte) (string, error) {
	if err := validateTmpxKid(recipient.Kid); err != nil {
		return "", err
	}
	if recipient.PublicKey == nil {
		return "", errors.New("tmproto: tmpx recipient public key required")
	}
	if recipient.PublicKey.Curve() != ecdh.X25519() {
		return "", errors.New("tmproto: tmpx recipient public key must be X25519")
	}

	skE, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		return "", fmt.Errorf("tmproto: tmpx ephemeral key: %w", err)
	}
	enc, ct, err := hpkeSealBase(recipient.PublicKey, skE, info, nil, plaintext)
	if err != nil {
		return "", err
	}
	payload := make([]byte, 0, len(enc)+len(ct))
	payload = append(payload, enc...)
	payload = append(payload, ct...)
	return recipient.Kid + "." + base64.RawURLEncoding.EncodeToString(payload), nil
}

// hpkeSealBase performs single-shot HPKE Seal in mode_base for suite
// (DHKEM(X25519, HKDF-SHA256), HKDF-SHA256, ChaCha20-Poly1305). Returns the
// 32-byte encapsulated KEM key (the ephemeral X25519 public key) and the
// ciphertext (plaintext_len + 16-byte AEAD tag).
//
// The ephemeral private key is supplied by the caller so test vectors can
// pin it. Production callers generate skE from rand.Reader before calling.
func hpkeSealBase(pkR *ecdh.PublicKey, skE *ecdh.PrivateKey, info, aad, plaintext []byte) (enc, ct []byte, err error) {
	pkE := skE.PublicKey()

	dh, err := skE.ECDH(pkR)
	if err != nil {
		return nil, nil, err
	}

	encBytes := pkE.Bytes()
	pkRBytes := pkR.Bytes()

	suiteID := buildHPKESuiteID(hpkeKEMX25519HKDFSHA256, hpkeKDFHKDFSHA256, hpkeAEADChaCha20Poly)
	kemSuiteID := buildHPKEKEMSuiteID(hpkeKEMX25519HKDFSHA256)

	// DHKEM Encap → shared_secret = ExtractAndExpand(dh, kem_context)
	kemContext := make([]byte, 0, len(encBytes)+len(pkRBytes))
	kemContext = append(kemContext, encBytes...)
	kemContext = append(kemContext, pkRBytes...)
	sharedSecret, err := dhkemExtractAndExpand(dh, kemContext, kemSuiteID, hpkeNh)
	if err != nil {
		return nil, nil, err
	}

	// Key schedule (mode_base: default psk = empty, default psk_id = empty).
	pskIDHash, err := labeledExtract(nil, []byte("psk_id_hash"), nil, suiteID)
	if err != nil {
		return nil, nil, err
	}
	infoHash, err := labeledExtract(nil, []byte("info_hash"), info, suiteID)
	if err != nil {
		return nil, nil, err
	}
	keyScheduleContext := make([]byte, 0, 1+len(pskIDHash)+len(infoHash))
	keyScheduleContext = append(keyScheduleContext, hpkeModeBase)
	keyScheduleContext = append(keyScheduleContext, pskIDHash...)
	keyScheduleContext = append(keyScheduleContext, infoHash...)

	secret, err := labeledExtract(sharedSecret, []byte("secret"), nil, suiteID)
	if err != nil {
		return nil, nil, err
	}
	key, err := labeledExpand(secret, []byte("key"), keyScheduleContext, hpkeNk, suiteID)
	if err != nil {
		return nil, nil, err
	}
	baseNonce, err := labeledExpand(secret, []byte("base_nonce"), keyScheduleContext, hpkeNn, suiteID)
	if err != nil {
		return nil, nil, err
	}

	aead, err := chacha20poly1305.New(key)
	if err != nil {
		return nil, nil, err
	}
	// Single-shot: sequence number 0, so the per-message nonce equals base_nonce.
	ct = aead.Seal(nil, baseNonce, plaintext, aad)
	return encBytes, ct, nil
}

// labeledExtract per RFC 9180 §4:
//
//	labeled_ikm = "HPKE-v1" || suite_id || label || ikm
//	return Extract(salt, labeled_ikm)
func labeledExtract(salt, label, ikm, suiteID []byte) ([]byte, error) {
	labeledIKM := make([]byte, 0, 7+len(suiteID)+len(label)+len(ikm))
	labeledIKM = append(labeledIKM, []byte("HPKE-v1")...)
	labeledIKM = append(labeledIKM, suiteID...)
	labeledIKM = append(labeledIKM, label...)
	labeledIKM = append(labeledIKM, ikm...)
	return hkdf.Extract(sha256.New, labeledIKM, salt)
}

// labeledExpand per RFC 9180 §4:
//
//	labeled_info = I2OSP(L, 2) || "HPKE-v1" || suite_id || label || info
//	return Expand(prk, labeled_info, L)
//
// L is encoded as a uint16; rfc9180 caps the per-call output at HKDF-SHA256's
// 8160-byte limit anyway. This rejects lengths above the uint16 ceiling so a
// future caller can't silently truncate.
func labeledExpand(prk, label, info []byte, length int, suiteID []byte) ([]byte, error) {
	if length < 0 || length > 0xffff {
		return nil, errors.New("tmproto: hpke labeled_expand length outside uint16 range")
	}
	labeledInfo := make([]byte, 0, 2+7+len(suiteID)+len(label)+len(info))
	labeledInfo = binary.BigEndian.AppendUint16(labeledInfo, uint16(length))
	labeledInfo = append(labeledInfo, []byte("HPKE-v1")...)
	labeledInfo = append(labeledInfo, suiteID...)
	labeledInfo = append(labeledInfo, label...)
	labeledInfo = append(labeledInfo, info...)
	return hkdf.Expand(sha256.New, prk, string(labeledInfo), length)
}

// dhkemExtractAndExpand per RFC 9180 §4.1:
//
//	eae_prk = LabeledExtract("", "eae_prk", dh)
//	shared_secret = LabeledExpand(eae_prk, "shared_secret", kem_context, Nsecret)
func dhkemExtractAndExpand(dh, kemContext, kemSuiteID []byte, length int) ([]byte, error) {
	eaePrk, err := labeledExtract(nil, []byte("eae_prk"), dh, kemSuiteID)
	if err != nil {
		return nil, err
	}
	return labeledExpand(eaePrk, []byte("shared_secret"), kemContext, length, kemSuiteID)
}

func buildHPKESuiteID(kem, kdf, aead uint16) []byte {
	out := make([]byte, 0, 4+6)
	out = append(out, []byte("HPKE")...)
	out = binary.BigEndian.AppendUint16(out, kem)
	out = binary.BigEndian.AppendUint16(out, kdf)
	out = binary.BigEndian.AppendUint16(out, aead)
	return out
}

func buildHPKEKEMSuiteID(kem uint16) []byte {
	out := make([]byte, 0, 3+2)
	out = append(out, []byte("KEM")...)
	out = binary.BigEndian.AppendUint16(out, kem)
	return out
}

// LoadX25519PublicKey parses 32 raw bytes into an ecdh.PublicKey. Used by
// reference agents that load buyer-published TMPX recipient keys from disk.
func LoadX25519PublicKey(b []byte) (*ecdh.PublicKey, error) {
	pk, err := ecdh.X25519().NewPublicKey(b)
	if err != nil {
		return nil, fmt.Errorf("tmproto: parse X25519 public key: %w", err)
	}
	return pk, nil
}
