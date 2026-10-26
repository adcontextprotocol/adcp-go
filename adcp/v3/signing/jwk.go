package signing

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"

	jose "github.com/go-jose/go-jose/v4"
)

// JWK is the subset of RFC 7517 members the AdCP profile relies on, plus the
// AdCP `adcp_use` discriminator. Unknown members from JWKS documents are
// discarded on decode (the profile's required set is fully typed here).
type JWK struct {
	Kid     string   `json:"kid"`
	Kty     string   `json:"kty"`
	Crv     string   `json:"crv,omitempty"`
	Alg     string   `json:"alg,omitempty"`
	Use     string   `json:"use,omitempty"`
	KeyOps  []string `json:"key_ops,omitempty"`
	AdcpUse string   `json:"adcp_use,omitempty"`
	X       string   `json:"x,omitempty"`
	Y       string   `json:"y,omitempty"`
	// N and E are an RSA key's modulus and exponent. No profile verifies
	// with RSA keys; they are decoded so that JWKThumbprint covers RSA. A Web
	// Bot Auth key directory skips an RSA key by its kty alone.
	N string `json:"n,omitempty"`
	E string `json:"e,omitempty"`

	// PrivateD is only populated for private-key JWKs used by the signer CLI
	// and test vectors. Public JWKS never include it. Call JWK.Public before
	// serializing for publication at jwks_uri to guarantee this member is
	// zeroed regardless of how the JWK was loaded.
	PrivateD string `json:"d,omitempty"`

	// TestOnlyPrivateD is the `_private_d_for_test_only` member used by the
	// spec conformance vector keys.json. Publicly-served JWKs never include it.
	// JWK.Public zeroes this too.
	TestOnlyPrivateD string `json:"_private_d_for_test_only,omitempty"`
}

// Public returns a copy of k with all private-key components zeroed. Use
// this before serializing a JWK that originated from a private source (a
// loaded PEM, a test vector with `_private_d_for_test_only`, or any other
// path where PrivateD / TestOnlyPrivateD may be populated) for publication
// at `jwks_uri`.
//
// Round-tripping a freshly-generated JWK from GenerateSigningKey is already
// safe because PrivateD is never set on that path; Public is a
// defense-in-depth affordance for the library-user who stores a JWK with
// its private half attached and later marshals it for a JWKS document.
func (k JWK) Public() JWK {
	k.PrivateD = ""
	k.TestOnlyPrivateD = ""
	return k
}

// JWKS is a JWK set.
type JWKS struct {
	Keys []JWK `json:"keys"`
}

// Find returns the first key matching kid, or nil.
func (s *JWKS) Find(kid string) *JWK {
	for i := range s.Keys {
		if s.Keys[i].Kid == kid {
			return &s.Keys[i]
		}
	}
	return nil
}

// ParseJWKS parses a JWK Set JSON document.
func ParseJWKS(data []byte) (*JWKS, error) {
	var s JWKS
	if err := json.Unmarshal(data, &s); err != nil {
		return nil, fmt.Errorf("parse jwks: %w", err)
	}
	return &s, nil
}

// PublicKey returns the crypto.PublicKey the JWK represents. The returned
// value is one of ed25519.PublicKey or *ecdsa.PublicKey.
//
// go-jose decodes the key (RFC 7517, RFC 7518 §6.2, RFC 8037), which
// rejects an EC point that is not on P-256 and a low-order Ed25519 point.
// AdCP narrows the key types to the two the profile signs with.
func (k *JWK) PublicKey() (any, error) {
	if err := k.checkProfileCurve(); err != nil {
		return nil, err
	}
	key, err := k.joseKey("")
	if err != nil {
		return nil, err
	}
	return key.Key, nil
}

// PrivateKey returns the private key encoded in the JWK. For test vectors, the
// spec uses `_private_d_for_test_only` instead of the standard `d` member.
//
// go-jose decodes the key, so an Ed25519 key must carry the x that its d
// derives (RFC 8037 §2) and an EC d must be the full 32 bytes (RFC 7518
// §6.2.2.1).
func (k *JWK) PrivateKey() (any, error) {
	d := k.PrivateD
	if d == "" {
		d = k.TestOnlyPrivateD
	}
	if d == "" {
		return nil, fmt.Errorf("jwk missing private component")
	}
	if err := k.checkProfileCurve(); err != nil {
		return nil, err
	}
	key, err := k.joseKey(d)
	if err != nil {
		return nil, err
	}
	return key.Key, nil
}

// checkProfileCurve admits the key types the AdCP RFC 9421 profile signs
// with: Ed25519 (OKP) and P-256 (EC).
func (k *JWK) checkProfileCurve() error {
	switch k.Kty {
	case "OKP":
		if k.Crv != "Ed25519" {
			return fmt.Errorf("unsupported OKP curve %q", k.Crv)
		}
	case "EC":
		if k.Crv != "P-256" {
			return fmt.Errorf("unsupported EC curve %q", k.Crv)
		}
	default:
		return fmt.Errorf("unsupported kty %q", k.Kty)
	}
	return nil
}

// joseKey decodes the key members of k, plus the private member d when
// non-empty, with go-jose. Only key material is passed: kid, alg, use and
// the AdCP members are the profile's to check, not the decoder's.
//
// AdCP has always accepted "=" padding on these members; go-jose reads
// them as RFC 7515 unpadded base64url only. Each member is decoded with
// b64UrlDecode, the decoder the SDK used before, and re-encoded unpadded,
// so exactly the strings that decoded before decode now.
//
// Errors are fixed strings: go-jose's messages can quote member values.
func (k *JWK) joseKey(d string) (*jose.JSONWebKey, error) {
	members := map[string]string{"kty": k.Kty}
	if k.Crv != "" {
		members["crv"] = k.Crv
	}
	for _, m := range []struct{ name, value string }{
		{"x", k.X}, {"y", k.Y}, {"n", k.N}, {"e", k.E}, {"d", d},
	} {
		if m.value == "" {
			continue
		}
		raw, err := b64UrlDecode(m.value)
		if err != nil {
			return nil, fmt.Errorf("decode jwk member %s: not base64url", m.name)
		}
		members[m.name] = base64.RawURLEncoding.EncodeToString(raw)
	}
	raw, err := json.Marshal(members)
	if err != nil {
		return nil, errors.New("decode jwk: invalid key")
	}
	var key jose.JSONWebKey
	if err := key.UnmarshalJSON(raw); err != nil {
		return nil, errors.New("decode jwk: invalid key")
	}
	return &key, nil
}

// jwkFromPublicKey returns the public JWK go-jose encodes for pub
// (ed25519.PublicKey, *ecdsa.PublicKey, or *rsa.PublicKey): kty, crv and
// the key members, with no kid, alg or use.
func jwkFromPublicKey(pub any) (JWK, error) {
	raw, err := jose.JSONWebKey{Key: pub}.MarshalJSON()
	if err != nil {
		return JWK{}, err
	}
	var out JWK
	if err := json.Unmarshal(raw, &out); err != nil {
		return JWK{}, err
	}
	return out, nil
}

// SigParamAlg returns the RFC 9421 alg value that must appear on
// Signature-Input for this JWK. It does NOT enforce the allowlist.
func (k *JWK) SigParamAlg() (Algorithm, error) {
	switch k.Kty {
	case "OKP":
		if k.Crv == "Ed25519" {
			return AlgEd25519, nil
		}
	case "EC":
		if k.Crv == "P-256" {
			return AlgES256, nil
		}
	}
	return "", fmt.Errorf("jwk kty=%q crv=%q has no AdCP alg mapping", k.Kty, k.Crv)
}

// b64UrlDecode decodes a base64url value that MAY be unpadded.
func b64UrlDecode(s string) ([]byte, error) {
	// RawURLEncoding is unpadded.
	if len(s)%4 != 0 {
		return base64.RawURLEncoding.DecodeString(s)
	}
	return base64.URLEncoding.DecodeString(s)
}

// b64UrlEncodeRaw encodes bytes as unpadded base64url.
func b64UrlEncodeRaw(b []byte) string {
	return base64.RawURLEncoding.EncodeToString(b)
}
