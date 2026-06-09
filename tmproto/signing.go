// Package tmproto's signing.go implements the TMP request authentication
// envelope from docs/trusted-match/specification.mdx §"Request Authentication":
// Ed25519 signatures carried in X-AdCP-Signature / X-AdCP-Key-Id headers,
// per-provider binding via provider_endpoint_url, daily-epoch replay window.
//
// Context match signs the newline-joined string:
//
//	type | property_rid | placement_id | sorted-comma-joined package_ids | provider_endpoint_url | daily_epoch
//
// Identity match signs hex(SHA-256(JCS(canonical_object))) where the canonical
// object holds {type, request_id, seller_agent_url, identities_hash, consent,
// package_ids, sealed_credentials_hash, provider_endpoint_url, daily_epoch}.
// JCS protects identity inputs against delimiter injection from arbitrary-byte
// fields like consent.gpp. identities_hash covers the complete identity objects
// including any per-identity attestation, and sealed_credentials_hash covers the
// top-level sealed_credentials, so a stripped, swapped, or injected attestation
// or sealed blob breaks signature verification.
package tmproto

import (
	"crypto/ed25519"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"
)

// HTTP headers carrying the TMP signature envelope.
const (
	HeaderTMPSignature = "X-AdCP-Signature"
	HeaderTMPKeyID     = "X-AdCP-Key-Id"
)

const (
	signedTypeContext  = "context_match_request"
	signedTypeIdentity = "identity_match_request"
	signingAlgorithm   = "EdDSA"
	signingCurve       = "Ed25519"
	signingKeyType     = "OKP"
	secondsPerDay      = 86400
)

// CurrentEpoch returns floor(unix_timestamp / 86400).
// Signatures bind to this value; verifiers accept current and previous epoch.
func CurrentEpoch() int64 {
	return time.Now().Unix() / secondsPerDay
}

// EpochAt returns the daily epoch for a given timestamp.
func EpochAt(t time.Time) int64 {
	return t.Unix() / secondsPerDay
}

// NormalizeProviderEndpointURL returns the canonical form used in signing.
// The spec mandates exact string match with the provider's registered endpoint
// and forbids trailing slashes — we strip them so callers don't have to.
func NormalizeProviderEndpointURL(s string) string {
	return strings.TrimRight(s, "/")
}

// SigningKey is a publisher-attested signing key, shaped to match the
// agent-signing-key.json schema. Verifiers maintain a keystore of these keyed
// by Kid.
type SigningKey struct {
	Kid       string     `json:"kid"`
	Kty       string     `json:"kty"`
	Alg       string     `json:"alg,omitempty"`
	Crv       string     `json:"crv,omitempty"`
	X         string     `json:"x,omitempty"`
	Use       string     `json:"use,omitempty"`
	AdcpUse   string     `json:"adcp_use,omitempty"` // "request-signing" or "tmpx-encrypt"
	IssuedAt  int64      `json:"iat,omitempty"`      // Unix seconds; higher = newer when picking the current key
	RevokedAt *time.Time `json:"revoked_at,omitempty"`
}

// PublicKey extracts the Ed25519 public key from the JWK fields.
// Returns an error if the key is not Ed25519/OKP.
func (k *SigningKey) PublicKey() (ed25519.PublicKey, error) {
	if k.Kty != signingKeyType {
		return nil, fmt.Errorf("tmproto: signing key %q has kty=%q, expected OKP", k.Kid, k.Kty)
	}
	if k.Crv != signingCurve {
		return nil, fmt.Errorf("tmproto: signing key %q has crv=%q, expected Ed25519", k.Kid, k.Crv)
	}
	raw, err := base64.RawURLEncoding.DecodeString(k.X)
	if err != nil {
		return nil, fmt.Errorf("tmproto: signing key %q has invalid base64url x: %w", k.Kid, err)
	}
	if len(raw) != ed25519.PublicKeySize {
		return nil, fmt.Errorf("tmproto: signing key %q has %d-byte x, expected %d", k.Kid, len(raw), ed25519.PublicKeySize)
	}
	return ed25519.PublicKey(raw), nil
}

// PublicSigningKey builds a SigningKey JWK for an Ed25519 public key.
// Used by router config wiring to publish keys to the registry.
func PublicSigningKey(kid string, pub ed25519.PublicKey) SigningKey {
	return SigningKey{
		Kid: kid,
		Kty: signingKeyType,
		Alg: signingAlgorithm,
		Crv: signingCurve,
		Use: "sig",
		X:   base64.RawURLEncoding.EncodeToString(pub),
	}
}

// KeyStore resolves a kid to its SigningKey. Verifiers query this on every
// request — implementations MUST be safe for concurrent reads.
type KeyStore interface {
	LookupKey(kid string) (*SigningKey, bool)
}

// StaticKeyStore is a concurrent-safe map-backed KeyStore for tests and for
// wrapping a pre-built snapshot of the registry.
type StaticKeyStore struct {
	keys map[string]*SigningKey
}

// NewStaticKeyStore builds a keystore from a slice of keys. Keys with empty
// Kid are dropped.
func NewStaticKeyStore(keys []SigningKey) *StaticKeyStore {
	idx := make(map[string]*SigningKey, len(keys))
	for i := range keys {
		k := keys[i]
		if k.Kid == "" {
			continue
		}
		idx[k.Kid] = &k
	}
	return &StaticKeyStore{keys: idx}
}

// LookupKey returns the key with the given kid.
func (s *StaticKeyStore) LookupKey(kid string) (*SigningKey, bool) {
	k, ok := s.keys[kid]
	return k, ok
}

// Sentinel errors returned by Verify*. Use errors.Is to discriminate.
var (
	ErrSignatureMissing    = errors.New("tmproto: signature headers missing")
	ErrSignatureMalformed  = errors.New("tmproto: signature header malformed")
	ErrSignatureKeyUnknown = errors.New("tmproto: signing key not in keystore")
	ErrSignatureKeyRevoked = errors.New("tmproto: signing key revoked")
	ErrSignatureInvalid    = errors.New("tmproto: ed25519 verification failed")
)

// Signer signs context-match and identity-match requests.
type Signer struct {
	KeyID      string
	privateKey ed25519.PrivateKey
}

// NewSigner constructs a Signer. Returns an error if the private key is not
// Ed25519-shaped.
func NewSigner(keyID string, priv ed25519.PrivateKey) (*Signer, error) {
	if keyID == "" {
		return nil, errors.New("tmproto: signer key ID must not be empty")
	}
	if len(priv) != ed25519.PrivateKeySize {
		return nil, fmt.Errorf("tmproto: signer private key has %d bytes, expected %d", len(priv), ed25519.PrivateKeySize)
	}
	return &Signer{KeyID: keyID, privateKey: priv}, nil
}

// PublicJWK returns the SigningKey JWK that verifiers need.
func (s *Signer) PublicJWK() SigningKey {
	pub := s.privateKey.Public().(ed25519.PublicKey)
	return PublicSigningKey(s.KeyID, pub)
}

// SignContextMatch signs a context-match request bound to the given provider
// endpoint URL and epoch. Returns the base64url-no-pad signature for use in
// the X-AdCP-Signature header.
func (s *Signer) SignContextMatch(req *ContextMatchRequest, providerEndpointURL string, epoch int64) string {
	input := BuildContextMatchSigningInput(req, NormalizeProviderEndpointURL(providerEndpointURL), epoch)
	sig := ed25519.Sign(s.privateKey, input)
	return base64.RawURLEncoding.EncodeToString(sig)
}

// SignIdentityMatch signs an identity-match request bound to the given provider
// endpoint URL and epoch. The request's Country field is not part of the
// signing input — callers should strip it before signing per the spec.
func (s *Signer) SignIdentityMatch(req *IdentityMatchRequest, providerEndpointURL string, epoch int64) (string, error) {
	input, err := BuildIdentityMatchSigningInput(req, NormalizeProviderEndpointURL(providerEndpointURL), epoch)
	if err != nil {
		return "", err
	}
	sig := ed25519.Sign(s.privateKey, input)
	return base64.RawURLEncoding.EncodeToString(sig), nil
}

// BuildContextMatchSigningInput returns the bytes the signer feeds to Ed25519
// for context match: newline-joined fields.
//
// seller_agent_url is bound because it selects which seller's active package
// set the provider resolves and returns offers from. Leaving it out of the
// signed bytes would let any holder of a valid registry key spoof another
// seller's URL and read that seller's private offers. It is signed adjacent to
// the type tag, mirroring identity-match (BuildIdentityMatchSigningInput).
// Adding it changes the signed bytes, so signatures produced before this
// binding no longer verify.
func BuildContextMatchSigningInput(req *ContextMatchRequest, providerEndpointURL string, epoch int64) []byte {
	var pkgIDs string
	if len(req.PackageIDs) > 0 {
		ids := append([]string(nil), req.PackageIDs...)
		sort.Strings(ids)
		pkgIDs = strings.Join(ids, ",")
	}
	parts := []string{
		signedTypeContext,
		req.SellerAgentURL,
		req.PropertyRID,
		req.PlacementID,
		pkgIDs,
		providerEndpointURL,
		strconv.FormatInt(epoch, 10),
	}
	return []byte(strings.Join(parts, "\n"))
}

// BuildIdentityMatchSigningInput returns the bytes the signer feeds to Ed25519
// for identity match: hex(SHA-256(JCS(canonical_object))).
func BuildIdentityMatchSigningInput(req *IdentityMatchRequest, providerEndpointURL string, epoch int64) ([]byte, error) {
	idsHash, err := canonicalIdentitiesHash(req.Identities)
	if err != nil {
		return nil, err
	}

	pkgIDs := append([]string(nil), req.PackageIDs...)
	sort.Strings(pkgIDs)

	var consent any // null when absent, verbatim object when present
	if len(req.Consent) > 0 {
		consent = mapAnyFromMap(req.Consent)
	}

	var sealedHash any // null when absent, hex SHA-256 of the canonical bytes when present
	if len(req.SealedCredentials) > 0 {
		h, err := canonicalSealedCredentialsHash(req.SealedCredentials)
		if err != nil {
			return nil, err
		}
		sealedHash = h
	}

	canonical := map[string]any{
		"type":                    signedTypeIdentity,
		"request_id":              req.RequestID,
		"seller_agent_url":        req.SellerAgentURL,
		"identities_hash":         idsHash,
		"consent":                 consent,
		"package_ids":             stringsToAny(pkgIDs),
		"sealed_credentials_hash": sealedHash,
		"provider_endpoint_url":   providerEndpointURL,
		"daily_epoch":             epoch,
	}

	jcs, err := jcsMarshal(canonical)
	if err != nil {
		return nil, fmt.Errorf("tmproto: identity-match JCS: %w", err)
	}
	sum := sha256.Sum256(jcs)
	return []byte(hex.EncodeToString(sum[:])), nil
}

// canonicalIdentitiesHash returns hex(SHA-256(JCS(canonical_identities))).
// Identities are deduplicated on (uid_type, user_token) using byte-exact match,
// then sorted by uid_type, then by user_token, both in UTF-8 byte order. Each
// entry is serialized as a complete identity object — including any per-identity
// attestation — so the signature covers the attestation: a stripped, swapped, or
// injected attestation breaks verification. The dedup key is only for collapsing
// duplicate tokens; it does not exclude attestation from the hash.
func canonicalIdentitiesHash(ids []IdentityToken) (string, error) {
	type idKey struct {
		uid   string
		token string
	}
	seen := make(map[idKey]struct{}, len(ids))
	deduped := make([]IdentityToken, 0, len(ids))
	for _, id := range ids {
		k := idKey{string(id.UIDType), id.UserToken}
		if _, ok := seen[k]; ok {
			continue
		}
		seen[k] = struct{}{}
		deduped = append(deduped, id)
	}
	sort.Slice(deduped, func(i, j int) bool {
		if deduped[i].UIDType != deduped[j].UIDType {
			return string(deduped[i].UIDType) < string(deduped[j].UIDType)
		}
		return deduped[i].UserToken < deduped[j].UserToken
	})

	canonical, err := jcsValue(deduped)
	if err != nil {
		return "", fmt.Errorf("tmproto: identities canonicalization: %w", err)
	}
	jcs, err := jcsMarshal(canonical)
	if err != nil {
		return "", fmt.Errorf("tmproto: identities JCS: %w", err)
	}
	sum := sha256.Sum256(jcs)
	return hex.EncodeToString(sum[:]), nil
}

// canonicalSealedCredentialsHash returns hex(SHA-256(JCS(canonical_sealed))).
// Entries are sorted by audience_kid (UTF-8 byte order) and serialized as a JCS
// array of complete objects. Folding this into the signed input makes an
// injected or swapped sealed blob break signature verification. Callers pass a
// non-empty slice; an absent sealed_credentials is signed as a null
// sealed_credentials_hash, not the hash of an empty array.
func canonicalSealedCredentialsHash(creds []SealedCredential) (string, error) {
	sorted := append([]SealedCredential(nil), creds...)
	sort.Slice(sorted, func(i, j int) bool {
		return sorted[i].AudienceKID < sorted[j].AudienceKID
	})
	canonical, err := jcsValue(sorted)
	if err != nil {
		return "", fmt.Errorf("tmproto: sealed-credentials canonicalization: %w", err)
	}
	jcs, err := jcsMarshal(canonical)
	if err != nil {
		return "", fmt.Errorf("tmproto: sealed-credentials JCS: %w", err)
	}
	sum := sha256.Sum256(jcs)
	return hex.EncodeToString(sum[:]), nil
}

// VerifyContextMatch verifies the signature on a context-match request using
// the verifier's own registered endpoint URL. now should be the wall clock for
// the request — current+previous epoch are accepted.
func VerifyContextMatch(req *ContextMatchRequest, ownEndpointURL, sig, kid string, ks KeyStore, now time.Time) error {
	pub, key, err := resolveSigningKey(kid, ks)
	if err != nil {
		return err
	}
	rawSig, err := decodeSignature(sig)
	if err != nil {
		return err
	}
	endpoint := NormalizeProviderEndpointURL(ownEndpointURL)
	currentEpoch := EpochAt(now)
	for _, epoch := range []int64{currentEpoch, currentEpoch - 1} {
		if keyRevokedForEpoch(key, epoch) {
			continue
		}
		input := BuildContextMatchSigningInput(req, endpoint, epoch)
		if ed25519.Verify(pub, input, rawSig) {
			return nil
		}
	}
	if keyRevokedForEpoch(key, currentEpoch) && keyRevokedForEpoch(key, currentEpoch-1) {
		return ErrSignatureKeyRevoked
	}
	return ErrSignatureInvalid
}

// VerifyIdentityMatch verifies the signature on an identity-match request.
func VerifyIdentityMatch(req *IdentityMatchRequest, ownEndpointURL, sig, kid string, ks KeyStore, now time.Time) error {
	pub, key, err := resolveSigningKey(kid, ks)
	if err != nil {
		return err
	}
	rawSig, err := decodeSignature(sig)
	if err != nil {
		return err
	}
	endpoint := NormalizeProviderEndpointURL(ownEndpointURL)
	currentEpoch := EpochAt(now)
	for _, epoch := range []int64{currentEpoch, currentEpoch - 1} {
		if keyRevokedForEpoch(key, epoch) {
			continue
		}
		input, err := BuildIdentityMatchSigningInput(req, endpoint, epoch)
		if err != nil {
			return err
		}
		if ed25519.Verify(pub, input, rawSig) {
			return nil
		}
	}
	if keyRevokedForEpoch(key, currentEpoch) && keyRevokedForEpoch(key, currentEpoch-1) {
		return ErrSignatureKeyRevoked
	}
	return ErrSignatureInvalid
}

// ExtractSignatureHeaders pulls the X-AdCP-Signature and X-AdCP-Key-Id values
// from a header map. Empty values map to ErrSignatureMissing.
func ExtractSignatureHeaders(h http.Header) (sig, kid string, err error) {
	sig = h.Get(HeaderTMPSignature)
	kid = h.Get(HeaderTMPKeyID)
	if sig == "" || kid == "" {
		return "", "", ErrSignatureMissing
	}
	return sig, kid, nil
}

// LoadEd25519PrivateKeyPEM parses a PKCS#8-encoded Ed25519 private key from
// PEM bytes. Used by cmd/router to load the signing key configured on disk.
func LoadEd25519PrivateKeyPEM(pemBytes []byte) (ed25519.PrivateKey, error) {
	block, _ := pem.Decode(pemBytes)
	if block == nil {
		return nil, errors.New("tmproto: no PEM block found")
	}
	key, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("tmproto: parse PKCS#8 key: %w", err)
	}
	priv, ok := key.(ed25519.PrivateKey)
	if !ok {
		return nil, fmt.Errorf("tmproto: PEM key is %T, expected ed25519.PrivateKey", key)
	}
	return priv, nil
}

func resolveSigningKey(kid string, ks KeyStore) (ed25519.PublicKey, *SigningKey, error) {
	if ks == nil {
		return nil, nil, ErrSignatureKeyUnknown
	}
	key, ok := ks.LookupKey(kid)
	if !ok {
		return nil, nil, ErrSignatureKeyUnknown
	}
	pub, err := key.PublicKey()
	if err != nil {
		return nil, nil, fmt.Errorf("%w: %v", ErrSignatureKeyUnknown, err)
	}
	return pub, key, nil
}

func decodeSignature(s string) ([]byte, error) {
	if s == "" {
		return nil, ErrSignatureMissing
	}
	raw, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return nil, ErrSignatureMalformed
	}
	if len(raw) != ed25519.SignatureSize {
		return nil, fmt.Errorf("%w: signature length %d", ErrSignatureMalformed, len(raw))
	}
	return raw, nil
}

// keyRevokedForEpoch reports whether the spec's revocation rule rejects a
// signature whose signing epoch equals e: reject when revoked_at is present
// and e >= floor(revoked_at_unix / 86400).
func keyRevokedForEpoch(key *SigningKey, e int64) bool {
	if key == nil || key.RevokedAt == nil {
		return false
	}
	revokedEpoch := EpochAt(*key.RevokedAt)
	return e >= revokedEpoch
}

func stringsToAny(in []string) []any {
	out := make([]any, len(in))
	for i, s := range in {
		out[i] = s
	}
	return out
}

// mapAnyFromMap normalizes a map[string]any so every nested map[string]any
// stays a map[string]any (json.Unmarshal already does this, but if a caller
// constructs a Consent map directly we want the same flow through JCS).
func mapAnyFromMap(m map[string]any) map[string]any {
	out := make(map[string]any, len(m))
	maps.Copy(out, m)
	return out
}
