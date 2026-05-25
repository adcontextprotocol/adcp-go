package signing

import (
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
)

// GenerateKeyResult bundles everything a publisher needs after running
// GenerateSigningKey: the PKCS#8 PEM-encoded private key, the public JWK
// (ready to paste into the agent's JWKS document), and the AdCP-required
// `kid` value.
type GenerateKeyResult struct {
	PrivateKeyPEM []byte
	PublicJWK     JWK
}

// GenerateSigningKey generates a new keypair for the given algorithm and
// returns the PEM-encoded PKCS#8 private key plus a JWK containing the public
// half with AdCP-required members set. The JWK is scoped for
// ProfileRequestSigning (adcp_use: "request-signing"); use
// GenerateKeyForProfile when publishing keys for webhook-signing or any
// future profile.
func GenerateSigningKey(alg Algorithm, kid string) (*GenerateKeyResult, error) {
	return GenerateKeyForProfile(alg, kid, ProfileRequestSigning)
}

// GenerateKeyForProfile generates a keypair and returns a JWK whose adcp_use
// matches the given profile. This is the path to take when publishing a
// webhook-signing key in adagents.json:
//
//	res, _ := signing.GenerateKeyForProfile(signing.AlgEd25519, "pub-webhook-2026", signing.ProfileWebhookSigning)
//
// Publishers that sign both tool calls and webhooks SHOULD publish two
// separate keys (one per profile) rather than reusing one key — a key
// scoped for request-signing cannot verify a webhook signature and vice
// versa, per adcontextprotocol/adcp#2423 step 8.
func GenerateKeyForProfile(alg Algorithm, kid string, profile Profile) (*GenerateKeyResult, error) {
	if profile.AdcpUse == "" {
		profile = ProfileRequestSigning
	}
	if kid == "" {
		nonce, err := generateNonce(rand.Reader)
		if err != nil {
			return nil, err
		}
		kid = "adcp-" + string(alg) + "-" + nonce[:10]
	}
	switch alg {
	case AlgEd25519:
		pub, priv, err := ed25519.GenerateKey(rand.Reader)
		if err != nil {
			return nil, err
		}
		pemBytes, err := encodePKCS8PEM(priv)
		if err != nil {
			return nil, err
		}
		return &GenerateKeyResult{
			PrivateKeyPEM: pemBytes,
			PublicJWK: JWK{
				Kid:     kid,
				Kty:     "OKP",
				Crv:     "Ed25519",
				Alg:     "EdDSA",
				Use:     "sig",
				KeyOps:  []string{"verify"},
				AdcpUse: profile.AdcpUse,
				X:       b64UrlEncodeRaw(pub),
			},
		}, nil
	case AlgES256:
		priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		if err != nil {
			return nil, err
		}
		ecdhPub, err := priv.PublicKey.ECDH()
		if err != nil {
			return nil, err
		}
		pubBytes := ecdhPub.Bytes()
		if len(pubBytes) != 65 || pubBytes[0] != 4 {
			return nil, errors.New("unexpected P-256 public key encoding")
		}
		pemBytes, err := encodePKCS8PEM(priv)
		if err != nil {
			return nil, err
		}
		return &GenerateKeyResult{
			PrivateKeyPEM: pemBytes,
			PublicJWK: JWK{
				Kid:     kid,
				Kty:     "EC",
				Crv:     "P-256",
				Alg:     "ES256",
				Use:     "sig",
				KeyOps:  []string{"verify"},
				AdcpUse: profile.AdcpUse,
				X:       b64UrlEncodeRaw(pubBytes[1:33]),
				Y:       b64UrlEncodeRaw(pubBytes[33:65]),
			},
		}, nil
	}
	return nil, fmt.Errorf("unsupported alg %q", alg)
}

func encodePKCS8PEM(priv any) ([]byte, error) {
	der, err := x509.MarshalPKCS8PrivateKey(priv)
	if err != nil {
		return nil, err
	}
	return pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}), nil
}

// LoadPrivateKey parses a PEM-encoded PKCS#8 private key produced by
// GenerateSigningKey (or equivalent tooling like `openssl genpkey`) and returns
// the private key along with its AdCP Algorithm.
//
// The returned key is ready to pass to SignerOptions.PrivateKey.
func LoadPrivateKey(pemBytes []byte) (any, Algorithm, error) {
	block, _ := pem.Decode(pemBytes)
	if block == nil {
		return nil, "", errors.New("signing: not a PEM block")
	}
	priv, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, "", fmt.Errorf("signing: parse PKCS#8 private key: %w", err)
	}
	switch k := priv.(type) {
	case ed25519.PrivateKey:
		return k, AlgEd25519, nil
	case *ecdsa.PrivateKey:
		if k.Curve.Params().Name != "P-256" {
			return nil, "", fmt.Errorf("signing: unsupported ECDSA curve %q (only P-256)", k.Curve.Params().Name)
		}
		return k, AlgES256, nil
	default:
		return nil, "", fmt.Errorf("signing: unsupported key type %T", priv)
	}
}
