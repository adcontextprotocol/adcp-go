package signing

import (
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"math/big"
)

// JWK is the subset of RFC 7517 members the AdCP profile relies on, plus the
// AdCP `adcp_use` discriminator. Unknown members are preserved via the Extra
// map so serializers can round-trip.
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

	// PrivateD is only populated for private-key JWKs used by the signer CLI
	// and test vectors. Public JWKS never include it.
	PrivateD string `json:"d,omitempty"`

	// TestOnlyPrivateD is the `_private_d_for_test_only` member used by the
	// spec conformance vector keys.json. Publicly-served JWKs never include it.
	TestOnlyPrivateD string `json:"_private_d_for_test_only,omitempty"`
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
func (k *JWK) PublicKey() (any, error) {
	switch k.Kty {
	case "OKP":
		if k.Crv != "Ed25519" {
			return nil, fmt.Errorf("unsupported OKP curve %q", k.Crv)
		}
		x, err := b64UrlDecode(k.X)
		if err != nil {
			return nil, fmt.Errorf("decode x: %w", err)
		}
		if len(x) != ed25519.PublicKeySize {
			return nil, fmt.Errorf("ed25519 public key length %d != 32", len(x))
		}
		return ed25519.PublicKey(x), nil
	case "EC":
		if k.Crv != "P-256" {
			return nil, fmt.Errorf("unsupported EC curve %q", k.Crv)
		}
		x, err := b64UrlDecode(k.X)
		if err != nil {
			return nil, fmt.Errorf("decode x: %w", err)
		}
		y, err := b64UrlDecode(k.Y)
		if err != nil {
			return nil, fmt.Errorf("decode y: %w", err)
		}
		if len(x) != 32 || len(y) != 32 {
			return nil, fmt.Errorf("P-256 coordinates must be 32 bytes each (got x=%d y=%d)", len(x), len(y))
		}
		return &ecdsa.PublicKey{
			Curve: elliptic.P256(),
			X:     new(big.Int).SetBytes(x),
			Y:     new(big.Int).SetBytes(y),
		}, nil
	default:
		return nil, fmt.Errorf("unsupported kty %q", k.Kty)
	}
}

// PrivateKey returns the private key encoded in the JWK. For test vectors, the
// spec uses `_private_d_for_test_only` instead of the standard `d` member.
func (k *JWK) PrivateKey() (any, error) {
	d := k.PrivateD
	if d == "" {
		d = k.TestOnlyPrivateD
	}
	if d == "" {
		return nil, fmt.Errorf("jwk missing private component")
	}
	dbytes, err := b64UrlDecode(d)
	if err != nil {
		return nil, fmt.Errorf("decode d: %w", err)
	}
	switch k.Kty {
	case "OKP":
		if k.Crv != "Ed25519" {
			return nil, fmt.Errorf("unsupported OKP curve %q", k.Crv)
		}
		if len(dbytes) != ed25519.SeedSize {
			return nil, fmt.Errorf("ed25519 seed length %d != 32", len(dbytes))
		}
		return ed25519.NewKeyFromSeed(dbytes), nil
	case "EC":
		if k.Crv != "P-256" {
			return nil, fmt.Errorf("unsupported EC curve %q", k.Crv)
		}
		pub, err := k.PublicKey()
		if err != nil {
			return nil, err
		}
		ecPub, ok := pub.(*ecdsa.PublicKey)
		if !ok {
			return nil, fmt.Errorf("jwk public key is not ecdsa")
		}
		return &ecdsa.PrivateKey{
			PublicKey: *ecPub,
			D:         new(big.Int).SetBytes(dbytes),
		}, nil
	default:
		return nil, fmt.Errorf("unsupported kty %q", k.Kty)
	}
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
