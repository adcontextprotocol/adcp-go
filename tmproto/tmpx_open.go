package tmproto

// tmpx_open.go is the receiver side of TMPX: it reverses SealTmpx. The buyer
// cluster master holds the X25519 recipient private key and opens the token to
// recover the resolved identity tokens for impression-time frequency state.
//
// SealTmpx shipped without an in-package Open (round-trip verification used a
// test-only helper); OpenTmpx promotes that to a usable receiver API for any
// trusted party that decrypts a TMPX token (e.g. buyer-side receiver code and
// conformance tests).

import (
	"crypto/ecdh"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"

	"golang.org/x/crypto/chacha20poly1305"
)

const tmpxMaxPayloadBytes = 32 + TmpxHeaderBytes + 255*(1+48) + chacha20poly1305.Overhead

// TmpxSealedMaxWireBytes bounds a sealed-credential TMPX wire string. The
// top-level sealed_credentials carrier (network-as-RP / Mechanism B) reuses
// the TMPX envelope FORMAT but carries an attestation, which the AdCP
// verified-identity schema caps at 8192 base64 chars — far larger than the
// identity-token wire budget (TmpxMaxWireBytes, sized for ~3 opaque tokens).
// Receivers MUST still bound count and size to prevent DoS amplification.
const TmpxSealedMaxWireBytes = TmpxMaxKidLen + 1 + 8192

// TmpxKid returns the kid prefix from a TMPX wire token so receivers can look up
// the matching X25519 private key before calling OpenTmpx. The wire may be a
// single-slot token OR a receiver-reassembled multi-chunk token (concatenation
// of up to TmpxMaxSlots chunks in slot order), so the bound is
// TmpxMaxReassembledWireBytes rather than the per-slot TmpxMaxWireBytes.
func TmpxKid(wire string) (string, error) {
	kid, _, err := splitTmpxWire(wire, TmpxMaxReassembledWireBytes)
	return kid, err
}

func splitTmpxWire(wire string, maxWire int) (kid, payload string, err error) {
	if len(wire) > maxWire {
		return "", "", errors.New("tmproto: tmpx wire string exceeds maximum")
	}
	dot := strings.IndexByte(wire, '.')
	if dot <= 0 || dot > TmpxMaxKidLen {
		return "", "", errors.New("tmproto: tmpx wire string missing valid kid prefix")
	}
	kid = wire[:dot]
	if err := validateTmpxKid(kid); err != nil {
		return "", "", errors.New("tmproto: tmpx wire string missing valid kid prefix")
	}
	return kid, wire[dot+1:], nil
}

// OpenTmpx decrypts a TMPX wire token produced by SealTmpx. It parses
// `kid.b64url_no_pad(enc || ciphertext)`, HPKE-opens it under the recipient's
// X25519 private key, and returns the recovered plaintext plus the parsed kid.
//
// TMPX uses HPKE mode_base: a successful open proves only that the ciphertext
// was formed for skR, not that the identity agent created it. Callers MUST
// authenticate the enclosing request or channel before using the plaintext to
// mutate exposure, billing, or frequency-cap state. OpenTmpx is safe for
// trusted internal receivers and conformance tooling; it is not a complete
// public tracking-endpoint primitive by itself.
//
// The caller resolves which private key to use from the kid out of band (for
// example via TmpxKid and a keystore lookup); OpenTmpx does not consult a
// keystore. `info` MUST match the value passed to SealTmpx (nil per the spec
// default).
//
// The wire may be a single-slot token OR a receiver-reassembled multi-chunk
// token: the AdCP TMPX macro carrier chunks sealed tokens at TmpxMaxWireBytes
// boundaries so each chunk fits inside one ad-server macro slot (the GAM
// `%%PATTERN_MACRO%%` substitution limit); the receiver concatenates chunks
// in slot order before calling OpenTmpx. The bound accepts up to TmpxMaxSlots
// chunks worth of payload so the sealer's own conformant receiver accepts
// tokens the sealer may emit; larger reassembled inputs are rejected.
func OpenTmpx(skR *ecdh.PrivateKey, info []byte, wire string) (plaintext []byte, kid string, err error) {
	return openTmpxWire(skR, info, wire, TmpxMaxReassembledWireBytes)
}

// OpenSealedCredential opens a sealed_credentials entry — an attestation
// sealed in the TMPX envelope format and addressed to a specific audience_kid.
// It is identical to OpenTmpx except it permits the larger sealed-credential
// size budget (TmpxSealedMaxWireBytes) the AdCP verified-identity schema
// defines, rather than the identity-token wire bound.
//
// Like OpenTmpx, a successful open proves only that the ciphertext was sealed
// to skR — never that the attestation inside is true. Callers MUST verify the
// attestation (issuer proof, signal_binding freshness, relying_party_id
// provenance, expiry) before trusting any claim.
func OpenSealedCredential(skR *ecdh.PrivateKey, info []byte, wire string) (plaintext []byte, kid string, err error) {
	return openTmpxWire(skR, info, wire, TmpxSealedMaxWireBytes)
}

// openTmpxWire is the shared TMPX-envelope open, parameterized by the maximum
// permitted wire length so the identity-token path (OpenTmpx) and the
// larger sealed-credential path (OpenSealedCredential) share one
// implementation with distinct size budgets.
func openTmpxWire(skR *ecdh.PrivateKey, info []byte, wire string, maxWire int) (plaintext []byte, kid string, err error) {
	if skR == nil {
		return nil, "", errors.New("tmproto: tmpx recipient private key required")
	}
	if skR.Curve() != ecdh.X25519() {
		return nil, "", errors.New("tmproto: tmpx recipient private key must be X25519")
	}
	kid, payloadText, err := splitTmpxWire(wire, maxWire)
	if err != nil {
		return nil, "", err
	}
	if base64.RawURLEncoding.DecodedLen(len(payloadText)) > tmpxMaxPayloadBytes {
		return nil, kid, errors.New("tmproto: tmpx payload too large")
	}
	payload, err := base64.RawURLEncoding.Strict().DecodeString(payloadText)
	if err != nil {
		return nil, kid, fmt.Errorf("tmproto: tmpx base64url decode: %w", err)
	}
	// payload = enc (32-byte X25519 ephemeral public key) || ciphertext+tag.
	if len(payload) < 32+chacha20poly1305.Overhead {
		return nil, kid, errors.New("tmproto: tmpx payload too short")
	}
	enc, ct := payload[:32], payload[32:]
	pt, err := hpkeOpenBase(skR, enc, info, nil, ct)
	if err != nil {
		return nil, kid, err
	}
	return pt, kid, nil
}

// hpkeOpenBase is the inverse of hpkeSealBase: single-shot HPKE Open in
// mode_base for suite (DHKEM(X25519, HKDF-SHA256), HKDF-SHA256,
// ChaCha20-Poly1305). enc is the 32-byte encapsulated KEM key (sender's
// ephemeral X25519 public key); ct is the AEAD ciphertext+tag.
func hpkeOpenBase(skR *ecdh.PrivateKey, enc, info, aad, ct []byte) ([]byte, error) {
	pkE, err := ecdh.X25519().NewPublicKey(enc)
	if err != nil {
		return nil, fmt.Errorf("tmproto: hpke open: parse enc: %w", err)
	}

	// DHKEM Decap: dh = DH(skR, pkE); kem_context = enc || pkRm (same byte
	// order the sender used in Encap).
	dh, err := skR.ECDH(pkE)
	if err != nil {
		return nil, fmt.Errorf("tmproto: hpke open: ecdh: %w", err)
	}
	pkRBytes := skR.PublicKey().Bytes()

	suiteID := buildHPKESuiteID(hpkeKEMX25519HKDFSHA256, hpkeKDFHKDFSHA256, hpkeAEADChaCha20Poly)
	kemSuiteID := buildHPKEKEMSuiteID(hpkeKEMX25519HKDFSHA256)

	kemContext := make([]byte, 0, len(enc)+len(pkRBytes))
	kemContext = append(kemContext, enc...)
	kemContext = append(kemContext, pkRBytes...)
	sharedSecret, err := dhkemExtractAndExpand(dh, kemContext, kemSuiteID, hpkeNh)
	if err != nil {
		return nil, err
	}

	// Key schedule (mode_base: empty psk / psk_id) — identical to Seal.
	pskIDHash, err := labeledExtract(nil, []byte("psk_id_hash"), nil, suiteID)
	if err != nil {
		return nil, err
	}
	infoHash, err := labeledExtract(nil, []byte("info_hash"), info, suiteID)
	if err != nil {
		return nil, err
	}
	keyScheduleContext := make([]byte, 0, 1+len(pskIDHash)+len(infoHash))
	keyScheduleContext = append(keyScheduleContext, hpkeModeBase)
	keyScheduleContext = append(keyScheduleContext, pskIDHash...)
	keyScheduleContext = append(keyScheduleContext, infoHash...)

	secret, err := labeledExtract(sharedSecret, []byte("secret"), nil, suiteID)
	if err != nil {
		return nil, err
	}
	key, err := labeledExpand(secret, []byte("key"), keyScheduleContext, hpkeNk, suiteID)
	if err != nil {
		return nil, err
	}
	baseNonce, err := labeledExpand(secret, []byte("base_nonce"), keyScheduleContext, hpkeNn, suiteID)
	if err != nil {
		return nil, err
	}

	aead, err := chacha20poly1305.New(key)
	if err != nil {
		return nil, err
	}
	// Single-shot: sequence number 0, so the per-message nonce equals base_nonce.
	pt, err := aead.Open(nil, baseNonce, ct, aad)
	if err != nil {
		return nil, fmt.Errorf("tmproto: hpke open (auth/decrypt failed): %w", err)
	}
	return pt, nil
}

// LoadX25519PrivateKey parses 32 raw bytes into an ecdh.PrivateKey — the
// receiver-side mirror of LoadX25519PublicKey, used to load the recipient key a
// kid maps to.
func LoadX25519PrivateKey(b []byte) (*ecdh.PrivateKey, error) {
	sk, err := ecdh.X25519().NewPrivateKey(b)
	if err != nil {
		return nil, fmt.Errorf("tmproto: parse X25519 private key: %w", err)
	}
	return sk, nil
}
