package signing

import (
	"bytes"
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"fmt"
)

// SigningProvider abstracts the private-key signing operation so a Signer
// never needs the key material to live in process memory. The AdCP spec
// recommends storing signing keys in an HSM or KMS rather than on disk;
// SigningProvider is the seam that makes that possible in this SDK —
// implement it against AWS KMS, GCP KMS, Azure Key Vault, Vault Transit, or
// any other external signer, and pass it as SignerOptions.Provider.
//
// Sign returns a raw signature over payload — the RFC 9421 signature base
// string bytes SignRequest computes — encoded exactly as the AdCP profile's
// wire format requires for the provider's Algorithm:
//
//   - AlgEd25519: sign payload directly. Ed25519 hashes internally; do not
//     pre-hash.
//   - AlgES256 (ECDSA-P256): sign the SHA-256 digest of payload and return
//     the IEEE P1363 fixed-width encoding (32-byte r || 32-byte s, 64 bytes
//     total) — NOT DER. Most KMS "asymmetric sign" APIs (including AWS KMS's
//     ECDSA_SHA_256 signing) return DER by default and need this
//     conversion; see adcp/v3/signing/awskms for a worked example.
//
// ctx carries the caller's deadline/cancellation through to Sign. External
// providers (KMS/HSM/Vault) typically incur 10-50ms of network latency per
// call and can fail transiently; ctx lets the provider bound retries and
// callers propagate a request-scoped deadline. SignRequest passes the
// signed http.Request's own r.Context().
//
// KeyID returns the `kid` this provider signs under — it MUST match a `kid`
// published in the agent's JWKS, and MUST be stable for the lifetime of the
// provider instance. Rotating keys means constructing a new provider (and
// publishing its PublicKey under the new kid) after the JWKS update has
// propagated — verifiers apply up to a 30-second kid-miss refetch cooldown
// (see HTTPJWKSResolver), so swapping a running Signer's provider to a kid
// the verifier fleet hasn't seen yet will transiently reject.
//
// Algorithm returns the RFC 9421 alg sig-param value this provider
// produces (AlgEd25519 or AlgES256); NewSigner rejects any other value.
//
// PublicKey returns the provider's current public key — an ed25519.PublicKey
// or *ecdsa.PublicKey matching Algorithm. It exists on the interface (rather
// than being bolted on later, a breaking change) so two operational
// safeguards are possible for any SigningProvider, not just the in-memory
// one: NewPublicJWKFromProvider (publish the right JWK at jwks_uri without
// hand-assembling it) and AssertProviderPublicKeyMatchesSPKI (fail loudly
// at startup if a managed key store silently rotated the key out from under
// a pinned kid, instead of quietly emitting signatures every verifier will
// reject). External providers should fetch this lazily and cache only a
// successful result — see adcp/v3/signing/awskms.Provider.PublicKey for the
// documented pattern (a production incident write-up in this issue's
// history is why: eager KMS calls before a listener binds can hang a
// process indefinitely on retryer backoff, and a sync.Once wrapping an
// error permanently poisons the cache).
type SigningProvider interface {
	// Sign returns the signature over payload, encoded per Algorithm's wire
	// format (see type doc). ctx bounds the operation's deadline and
	// cancellation. Implementations backed by an external service should
	// return a *SigningError on failure rather than forwarding the backing
	// SDK's raw error — see SigningError's doc comment.
	Sign(ctx context.Context, payload []byte) ([]byte, error)

	// KeyID returns the `kid` this provider signs under.
	KeyID() string

	// Algorithm returns the RFC 9421 alg sig-param value this provider
	// produces.
	Algorithm() Algorithm

	// PublicKey returns the provider's current public key.
	PublicKey(ctx context.Context) (crypto.PublicKey, error)
}

// InMemorySigningProvider signs using a private key held in process memory.
// This is the original (pre-SigningProvider) behavior of this package and
// remains the default: NewSigner builds one internally whenever
// SignerOptions.Provider is nil and KeyID/PrivateKey are set. Use it
// directly in tests, or as a reference for what an external SigningProvider
// needs to reproduce.
type InMemorySigningProvider struct {
	keyID string
	alg   Algorithm
	key   any // ed25519.PrivateKey or *ecdsa.PrivateKey (P-256)
}

var _ SigningProvider = (*InMemorySigningProvider)(nil)

// NewInMemorySigningProvider validates key and wraps it as a SigningProvider.
// key must be an ed25519.PrivateKey or a *ecdsa.PrivateKey on the P-256
// curve — the same set SignerOptions.PrivateKey has always accepted. Use
// LoadPrivateKey to parse a PEM file into one of these types.
func NewInMemorySigningProvider(keyID string, key any) (*InMemorySigningProvider, error) {
	if keyID == "" {
		return nil, fmt.Errorf("signing: KeyID is required")
	}
	alg, err := algorithmForKey(key)
	if err != nil {
		return nil, err
	}
	return &InMemorySigningProvider{keyID: keyID, alg: alg, key: key}, nil
}

// KeyID returns the configured kid.
func (p *InMemorySigningProvider) KeyID() string { return p.keyID }

// Algorithm returns the RFC 9421 alg value this provider's key produces.
func (p *InMemorySigningProvider) Algorithm() Algorithm { return p.alg }

// Sign returns the raw signature over payload: Ed25519 signs payload
// directly; ECDSA-P256 signs the SHA-256 digest and returns the IEEE P1363
// (r||s) fixed-width encoding.
//
// Signing against an in-memory key is pure CPU work with no I/O to cancel
// mid-flight, so the only context behavior that applies is a pre-flight
// check: if ctx is already done, Sign returns ctx.Err() instead of
// performing a signature nobody will use. A context that's cancelled or
// expires *during* the (sub-millisecond) computation isn't observed —
// that's the correct, and only meaningful, contract for a synchronous local
// operation. External providers with real network I/O (see
// adcp/v3/signing/awskms) additionally observe ctx across the call itself.
func (p *InMemorySigningProvider) Sign(ctx context.Context, payload []byte) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	switch key := p.key.(type) {
	case ed25519.PrivateKey:
		return ed25519.Sign(key, payload), nil
	case *ecdsa.PrivateKey:
		h := sha256.Sum256(payload)
		rInt, sInt, err := ecdsa.Sign(rand.Reader, key, h[:])
		if err != nil {
			return nil, err
		}
		// IEEE P1363 (r||s) fixed-width encoding — NOT DER.
		return encodeP1363(rInt, sInt, 32), nil
	default:
		return nil, fmt.Errorf("signing: unsupported private key type %T", p.key)
	}
}

// PublicKey returns the public half of the in-memory private key: an
// ed25519.PublicKey or *ecdsa.PublicKey. ctx is honored the same way Sign
// honors it (pre-flight check only — this is local, synchronous, no I/O).
func (p *InMemorySigningProvider) PublicKey(ctx context.Context) (crypto.PublicKey, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	switch key := p.key.(type) {
	case ed25519.PrivateKey:
		return key.Public(), nil
	case *ecdsa.PrivateKey:
		return &key.PublicKey, nil
	default:
		return nil, fmt.Errorf("signing: unsupported private key type %T", p.key)
	}
}

// NewPublicJWKFromProvider fetches provider's current public key and
// returns the JWK to publish at the agent's jwks_uri for it: kty/crv/alg
// set from provider.Algorithm(), use="sig", key_ops=["verify"], and kid /
// adcp_use set from the given parameters.
//
// kid is deliberately a separate parameter from provider.KeyID(), not
// implicitly reused — this supports the two-step key-rotation flow
// (publish the new key's JWK under its new kid first, let verifiers'
// refetch propagate, then cut the Signer over to a provider whose KeyID()
// matches) without requiring the provider to already be "live" under that
// kid. In the common case, pass provider.KeyID() here.
//
// adcpUse is required (not defaulted) because the AdCP spec requires
// distinct key material per signing purpose (adcontextprotocol/adcp#2423)
// — pass ProfileRequestSigning.AdcpUse or ProfileWebhookSigning.AdcpUse
// (or a custom profile's) explicitly rather than risk a key published for
// one purpose being trusted for another.
func NewPublicJWKFromProvider(ctx context.Context, provider SigningProvider, kid, adcpUse string) (JWK, error) {
	if provider == nil {
		return JWK{}, fmt.Errorf("signing: provider is required")
	}
	if kid == "" {
		return JWK{}, fmt.Errorf("signing: kid is required")
	}
	if adcpUse == "" {
		return JWK{}, fmt.Errorf("signing: adcpUse is required (e.g. ProfileRequestSigning.AdcpUse)")
	}

	pub, err := provider.PublicKey(ctx)
	if err != nil {
		return JWK{}, wrapSigningError(SignCodeProviderFailed, fmt.Errorf("fetch public key: %w", err))
	}

	switch provider.Algorithm() {
	case AlgEd25519:
		edPub, ok := pub.(ed25519.PublicKey)
		if !ok {
			return JWK{}, fmt.Errorf("signing: provider.Algorithm() is AlgEd25519 but PublicKey returned %T", pub)
		}
		return JWK{
			Kid:     kid,
			Kty:     "OKP",
			Crv:     "Ed25519",
			Alg:     string(jwkAlgEdDSA),
			Use:     "sig",
			KeyOps:  []string{"verify"},
			AdcpUse: adcpUse,
			X:       b64UrlEncodeRaw(edPub),
		}, nil
	case AlgES256:
		ecPub, ok := pub.(*ecdsa.PublicKey)
		if !ok {
			return JWK{}, fmt.Errorf("signing: provider.Algorithm() is AlgES256 but PublicKey returned %T", pub)
		}
		if ecPub.Curve.Params().Name != "P-256" {
			return JWK{}, fmt.Errorf("signing: provider.Algorithm() is AlgES256 but PublicKey curve is %q", ecPub.Curve.Params().Name)
		}
		ecdhPub, err := ecPub.ECDH()
		if err != nil {
			return JWK{}, fmt.Errorf("signing: encode ES256 public key: %w", err)
		}
		pubBytes := ecdhPub.Bytes() // uncompressed point: 0x04 || X(32) || Y(32)
		if len(pubBytes) != 65 || pubBytes[0] != 4 {
			return JWK{}, fmt.Errorf("signing: unexpected P-256 public key encoding")
		}
		return JWK{
			Kid:     kid,
			Kty:     "EC",
			Crv:     "P-256",
			Alg:     string(jwkAlgES256),
			Use:     "sig",
			KeyOps:  []string{"verify"},
			AdcpUse: adcpUse,
			X:       b64UrlEncodeRaw(pubBytes[1:33]),
			Y:       b64UrlEncodeRaw(pubBytes[33:65]),
		}, nil
	default:
		return JWK{}, fmt.Errorf("signing: provider.Algorithm() %q has no JWK mapping", provider.Algorithm())
	}
}

// AssertProviderPublicKeyMatchesSPKI fetches provider's current public key
// and compares its X.509 SubjectPublicKeyInfo (DER) encoding against
// expectedSPKI, returning a *SigningError with SignCodePublicKeyMismatch on
// mismatch.
//
// Call this once at startup (after binding your listener — see
// SigningProvider's doc comment on lazy init) with expectedSPKI pinned
// alongside deployed code (e.g. checked into config, computed once via this
// same function against a known-good provider and committed). A managed
// key store can rotate the key backing a kid without any signal to this
// SDK — an alias repoint, a Vault key rotated out from under a path. Left
// undetected, the Signer keeps producing signatures every verifier
// rejects, with no local error to point at. This tripwire fails loudly at
// the source instead.
func AssertProviderPublicKeyMatchesSPKI(ctx context.Context, provider SigningProvider, expectedSPKI []byte) error {
	if provider == nil {
		return fmt.Errorf("signing: provider is required")
	}
	pub, err := provider.PublicKey(ctx)
	if err != nil {
		return wrapSigningError(SignCodeProviderFailed, fmt.Errorf("fetch public key: %w", err))
	}
	spki, err := x509.MarshalPKIXPublicKey(pub)
	if err != nil {
		return wrapSigningError(SignCodeProviderFailed, fmt.Errorf("marshal public key: %w", err))
	}
	if !bytes.Equal(spki, expectedSPKI) {
		return newSigningError(SignCodePublicKeyMismatch, "provider's current public key does not match the pinned SPKI — the key store may have rotated the key backing this provider's KeyID")
	}
	return nil
}

// algorithmForKey maps a raw private key to the RFC 9421 alg value the AdCP
// profile requires for it. Shared by NewInMemorySigningProvider so
// SignerOptions.PrivateKey and direct InMemorySigningProvider construction
// validate identically.
func algorithmForKey(key any) (Algorithm, error) {
	switch k := key.(type) {
	case ed25519.PrivateKey:
		return AlgEd25519, nil
	case *ecdsa.PrivateKey:
		if k.Curve.Params().Name != "P-256" {
			return "", fmt.Errorf("signing: unsupported ECDSA curve %q (only P-256)", k.Curve.Params().Name)
		}
		return AlgES256, nil
	default:
		return "", fmt.Errorf("signing: unsupported private key type %T", key)
	}
}
