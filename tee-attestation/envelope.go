package teeattestation

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"time"
)

// Format identifies one of the canonical externally-defined attestation
// formats the spec's `attestation_format` enum names. Kept as a string type
// so extension values can flow through in `ext` without a type change.
type Format string

const (
	FormatAWSNitroCOSESign1V1    Format = "aws_nitro_cose_sign1_v1"
	FormatIntelTDXQuoteV4        Format = "intel_tdx_quote_v4"
	FormatAMDSEVSNPAttestationV1 Format = "amd_sev_snp_attestation_v1"
	FormatGCPConfidentialSpaceV1 Format = "gcp_confidential_space_v1"
)

// KnownFormats returns the v1 enum from the spec. Callers that gate on
// `attestation_requirement.acceptable_formats` should compare against this
// set unless they knowingly accept an experimental value from `ext`.
func KnownFormats() []Format {
	return []Format{
		FormatAWSNitroCOSESign1V1,
		FormatIntelTDXQuoteV4,
		FormatAMDSEVSNPAttestationV1,
		FormatGCPConfidentialSpaceV1,
	}
}

// Envelope is the JSON body returned by GET /.well-known/tmp-router-attestation
// per docs/trusted-match/router-attestation.mdx (spec PR
// adcontextprotocol/adcp#5770).
type Envelope struct {
	Format     Format          `json:"attestation_format"`
	Document   string          `json:"attestation_document"` // base64url, no pad
	Nonce      string          `json:"nonce"`                // base64url echo
	SigningKey JWK             `json:"signing_key"`
	ExpiresAt  time.Time       `json:"expires_at"`
	Ext        json.RawMessage `json:"ext,omitempty"`
}

// DocumentBytes returns the decoded attestation document. Envelope validation
// (schema checks, expiry, nonce echo) is done by the verifier; this helper
// only decodes.
func (e Envelope) DocumentBytes() ([]byte, error) {
	b, err := base64.RawURLEncoding.DecodeString(e.Document)
	if err != nil {
		return nil, fmt.Errorf("attestation_document is not valid base64url-no-pad: %w", err)
	}
	return b, nil
}

// NonceBytes returns the decoded nonce.
func (e Envelope) NonceBytes() ([]byte, error) {
	b, err := base64.RawURLEncoding.DecodeString(e.Nonce)
	if err != nil {
		return nil, fmt.Errorf("nonce is not valid base64url-no-pad: %w", err)
	}
	if len(b) < 16 || len(b) > 32 {
		return nil, fmt.Errorf("nonce must be 16-32 raw bytes, got %d", len(b))
	}
	return b, nil
}

// JWK is the minimal public-key shape the envelope carries. Bokelley's review
// on adcontextprotocol/adcp#5770 flagged that the spec's `$ref` to
// agent-signing-key.json inherits `revoked_at` and other trust-anchor config
// as a second source of truth for revocation. This local JWK keeps the wire
// object minimal — just the fields needed to reconstruct the raw key and
// compute an RFC 7638 thumbprint. `revoked_at` etc. still live on the trust
// anchor the envelope's signing_key resolves against, and the verifier is
// responsible for that resolution — see docs/trusted-match/router-attestation.mdx
// "Interaction with the RFC 9421 signing flow" section.
type JWK struct {
	Kty string `json:"kty"`
	Crv string `json:"crv,omitempty"`
	Alg string `json:"alg,omitempty"`
	Use string `json:"use,omitempty"`
	Kid string `json:"kid,omitempty"`
	X   string `json:"x,omitempty"` // OKP / EC x coordinate, base64url no pad
	Y   string `json:"y,omitempty"` // EC only
	N   string `json:"n,omitempty"` // RSA only
	E   string `json:"e,omitempty"` // RSA only
}

// Ed25519PublicKey returns the raw 32-byte Ed25519 public key when the JWK
// describes one. Returns an error otherwise. The spec allows any JWK; this
// prototype only implements OKP/Ed25519 to keep the surface small.
func (k JWK) Ed25519PublicKey() (ed25519.PublicKey, error) {
	if k.Kty != "OKP" || k.Crv != "Ed25519" {
		return nil, fmt.Errorf("expected OKP/Ed25519 JWK, got kty=%q crv=%q", k.Kty, k.Crv)
	}
	raw, err := base64.RawURLEncoding.DecodeString(k.X)
	if err != nil {
		return nil, fmt.Errorf("JWK.x is not valid base64url-no-pad: %w", err)
	}
	if len(raw) != ed25519.PublicKeySize {
		return nil, fmt.Errorf("JWK.x is %d bytes, expected %d for Ed25519", len(raw), ed25519.PublicKeySize)
	}
	return ed25519.PublicKey(raw), nil
}

// Ed25519JWK builds an OKP/Ed25519 JWK from a raw public key.
func Ed25519JWK(pub ed25519.PublicKey, kid string) JWK {
	return JWK{
		Kty: "OKP",
		Crv: "Ed25519",
		Alg: "EdDSA",
		Use: "sig",
		Kid: kid,
		X:   base64.RawURLEncoding.EncodeToString(pub),
	}
}

// Thumbprint returns the RFC 7638 JWK thumbprint (SHA-256 of the canonical
// JCS-encoded JWK containing only the type-specific required members).
// This is the canonical form the spec's binding rule allows for comparison.
func (k JWK) Thumbprint() ([]byte, error) {
	// RFC 7638 §3 for OKP: {"crv","kty","x"}. Members sorted alphabetically.
	// RFC 7638 §3.1 requires no whitespace, no leading zero padding, ASCII
	// JSON. `json.Marshal` on a map[string]string with a sorted key list
	// produces the same bytes as JCS (RFC 8785) for the flat all-string
	// shape a thumbprint requires — no floats, no nested objects, no
	// escape ambiguity.
	required, err := k.thumbprintMembers()
	if err != nil {
		return nil, err
	}
	keys := make([]string, 0, len(required))
	for k := range required {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	// Hand-emit canonical JSON to guarantee byte-identity across
	// serializer implementations. Same-shape output as RFC 8785 for this
	// flat all-string map.
	buf := []byte{'{'}
	for i, name := range keys {
		if i > 0 {
			buf = append(buf, ',')
		}
		nameJSON, _ := json.Marshal(name)
		buf = append(buf, nameJSON...)
		buf = append(buf, ':')
		valJSON, _ := json.Marshal(required[name])
		buf = append(buf, valJSON...)
	}
	buf = append(buf, '}')
	sum := sha256.Sum256(buf)
	return sum[:], nil
}

func (k JWK) thumbprintMembers() (map[string]string, error) {
	switch k.Kty {
	case "OKP":
		if k.Crv == "" || k.X == "" {
			return nil, errors.New("OKP JWK missing required members crv/x for thumbprint")
		}
		return map[string]string{"crv": k.Crv, "kty": k.Kty, "x": k.X}, nil
	case "EC":
		if k.Crv == "" || k.X == "" || k.Y == "" {
			return nil, errors.New("EC JWK missing required members crv/x/y for thumbprint")
		}
		return map[string]string{"crv": k.Crv, "kty": k.Kty, "x": k.X, "y": k.Y}, nil
	case "RSA":
		if k.N == "" || k.E == "" {
			return nil, errors.New("RSA JWK missing required members n/e for thumbprint")
		}
		return map[string]string{"e": k.E, "kty": k.Kty, "n": k.N}, nil
	default:
		return nil, fmt.Errorf("unsupported JWK kty %q for thumbprint", k.Kty)
	}
}
