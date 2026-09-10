package awskms

import (
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/asn1"
	"errors"
	"fmt"
	"math/big"
	"sync"

	"github.com/aws/aws-sdk-go-v2/service/kms"
	"github.com/aws/aws-sdk-go-v2/service/kms/types"

	"github.com/adcontextprotocol/adcp-go/adcp/v3/signing"
)

// SignAPI is the subset of *kms.Client this package calls. Depending on
// this narrow interface instead of the concrete client lets tests
// substitute a fake KMS backend — see doc.go's "Testing without live AWS
// infrastructure" section. *kms.Client satisfies this directly.
type SignAPI interface {
	Sign(ctx context.Context, params *kms.SignInput, optFns ...func(*kms.Options)) (*kms.SignOutput, error)
	GetPublicKey(ctx context.Context, params *kms.GetPublicKeyInput, optFns ...func(*kms.Options)) (*kms.GetPublicKeyOutput, error)
}

// ecdsaP256FieldSize is the byte width of a P-256 field element — half the
// IEEE P1363 (r||s) signature width the AdCP profile requires on the wire.
const ecdsaP256FieldSize = 32

// Provider implements signing.SigningProvider against an AWS KMS asymmetric
// ECC_NIST_P256 signing key.
//
// PublicKey's result is cached after the first successful GetPublicKey call
// and reused for the life of the Provider — deliberately not eagerly
// fetched by New, and deliberately not cached via sync.Once. Both choices
// trace back to a real incident from this interface's design history
// (recorded on adcp-go#99): calling KMS before a listener binds can hang a
// process indefinitely on the AWS SDK retryer's backoff with no visible
// error, and sync.Once would permanently cache a transient failure as if it
// were success. mu guards a cached *ecdsa.PublicKey that's populated only
// on success; a failed fetch is retried on the next call.
type Provider struct {
	client   SignAPI
	kmsKeyID string
	kid      string

	mu        sync.Mutex
	cachedPub *ecdsa.PublicKey
}

var _ signing.SigningProvider = (*Provider)(nil)

// Options configures New.
type Options struct {
	// Client calls KMS's Sign API — normally kms.NewFromConfig(cfg), or a
	// fake satisfying SignAPI in tests. Required.
	Client SignAPI

	// KMSKeyID is the KMS key identifier passed as SignInput.KeyId: a key
	// ID, key ARN, alias name, or alias ARN. Required. The referenced key
	// MUST be an asymmetric key with KeyUsage=SIGN_VERIFY and
	// KeySpec=ECC_NIST_P256 — Provider.Sign asks KMS to sign with the
	// ECDSA_SHA_256 algorithm, which is only valid for that key spec.
	KMSKeyID string

	// KeyID is the AdCP `kid` published in the agent's JWKS and emitted in
	// the Signature-Input `keyid` sig-param. Required.
	//
	// This is deliberately a separate value from KMSKeyID: the JWKS `kid`
	// is a stable, spec-facing identifier an operator chooses and publishes,
	// while the KMS key identifier is an AWS-addressing detail (an alias
	// can be repointed to a new underlying key, cross-account key ARNs
	// exist, etc.) that can change without altering what the AdCP wire
	// format publishes.
	KeyID string
}

// New validates opts and returns a Provider. New does not itself call KMS —
// verifying the key's KeyUsage/KeySpec (e.g. via kms:DescribeKey) is the
// caller's responsibility at provisioning time; a mismatched key surfaces
// as a KMS-side error from Sign at first use.
func New(opts Options) (*Provider, error) {
	if opts.Client == nil {
		return nil, errors.New("awskms: Client is required")
	}
	if opts.KMSKeyID == "" {
		return nil, errors.New("awskms: KMSKeyID is required")
	}
	if opts.KeyID == "" {
		return nil, errors.New("awskms: KeyID is required")
	}
	return &Provider{client: opts.Client, kmsKeyID: opts.KMSKeyID, kid: opts.KeyID}, nil
}

// KeyID returns the AdCP `kid` this provider signs under.
func (p *Provider) KeyID() string { return p.kid }

// Algorithm always returns signing.AlgES256. AWS KMS does not offer
// Ed25519/EdDSA asymmetric signing keys as of this writing, only RSA and
// NIST/SECG elliptic curves, so this provider only ever produces ECDSA-P256
// signatures.
func (p *Provider) Algorithm() signing.Algorithm { return signing.AlgES256 }

// Sign hashes payload locally with SHA-256 and asks KMS to sign that digest
// under the configured key with the ECDSA_SHA_256 algorithm, then converts
// the ASN.1 DER-encoded (r, s) signature KMS returns into the fixed-width
// 64-byte IEEE P1363 (r||s) encoding the AdCP profile's Signature header
// requires — the identical wire format signing.InMemorySigningProvider's
// ECDSA path produces locally.
//
// Sending MessageType=DIGEST (a pre-hashed 32-byte digest) rather than
// MessageType=RAW (the full payload, hashed by KMS) means only the digest
// crosses the wire to KMS and makes this provider directly comparable,
// byte-for-byte, to the in-memory ECDSA path in tests.
//
// ctx bounds both the KMS network call and any retry/backoff the AWS SDK's
// configured retryer performs around it.
//
// Errors are returned as *signing.SigningError: the raw KMS SDK error
// (which can embed the key ARN and other resource identifiers) is only
// reachable via errors.Unwrap/errors.As on the returned error, never via
// its Error() string — see signing.SigningError's doc comment for why.
func (p *Provider) Sign(ctx context.Context, payload []byte) ([]byte, error) {
	digest := sha256.Sum256(payload)
	digestCopy := digest[:] // SignInput.Message keeps no reference to payload

	out, err := p.client.Sign(ctx, &kms.SignInput{
		KeyId:            &p.kmsKeyID,
		Message:          digestCopy,
		MessageType:      types.MessageTypeDigest,
		SigningAlgorithm: types.SigningAlgorithmSpecEcdsaSha256,
	})
	if err != nil {
		return nil, signingProviderError(fmt.Errorf("awskms: kms Sign: %w", err))
	}
	if out.SigningAlgorithm != types.SigningAlgorithmSpecEcdsaSha256 {
		return nil, &signing.SigningError{
			Code:   signing.SignCodeAlgorithmUnexpected,
			Detail: "kms Sign response SigningAlgorithm did not match the requested ECDSA_SHA_256",
		}
	}

	sig, err := derECDSASignatureToP1363(out.Signature)
	if err != nil {
		return nil, signingProviderError(err)
	}
	return sig, nil
}

// PublicKey returns the KMS key's public half, fetched via GetPublicKey and
// cached after the first successful call (see the Provider doc comment for
// why the caching is success-only and why it isn't eager).
func (p *Provider) PublicKey(ctx context.Context) (crypto.PublicKey, error) {
	p.mu.Lock()
	cached := p.cachedPub
	p.mu.Unlock()
	if cached != nil {
		return cached, nil
	}

	out, err := p.client.GetPublicKey(ctx, &kms.GetPublicKeyInput{KeyId: &p.kmsKeyID})
	if err != nil {
		return nil, signingProviderError(fmt.Errorf("awskms: kms GetPublicKey: %w", err))
	}

	pub, err := x509.ParsePKIXPublicKey(out.PublicKey)
	if err != nil {
		return nil, signingProviderError(fmt.Errorf("awskms: parse GetPublicKey response: %w", err))
	}
	ecPub, ok := pub.(*ecdsa.PublicKey)
	if !ok {
		return nil, &signing.SigningError{
			Code:   signing.SignCodeAlgorithmUnexpected,
			Detail: fmt.Sprintf("kms GetPublicKey returned a %T, want *ecdsa.PublicKey", pub),
		}
	}
	if ecPub.Curve.Params().Name != "P-256" {
		return nil, &signing.SigningError{
			Code:   signing.SignCodeAlgorithmUnexpected,
			Detail: fmt.Sprintf("kms GetPublicKey returned curve %q, want P-256", ecPub.Curve.Params().Name),
		}
	}

	p.mu.Lock()
	p.cachedPub = ecPub // cache success only — see Provider doc comment
	p.mu.Unlock()
	return ecPub, nil
}

// signingProviderError wraps cause as a *signing.SigningError with
// SignCodeProviderFailed and no Detail — cause (which may embed a KMS ARN
// or other AWS resource identifiers) is reachable only via errors.Unwrap,
// never via the returned error's Error() string.
func signingProviderError(cause error) error {
	return &signing.SigningError{Code: signing.SignCodeProviderFailed, Wrapped: cause}
}

// derASN1ECDSASignature mirrors the unexported ecdsa-Sig-Value ASN.1
// structure (SEQUENCE { r INTEGER, s INTEGER }) that both crypto/ecdsa's
// ASN.1 output and AWS KMS's ECDSA Sign response use.
type derASN1ECDSASignature struct {
	R, S *big.Int
}

// derECDSASignatureToP1363 decodes an ASN.1 DER ECDSA-Sig-Value and
// re-encodes it as the fixed-width (32-byte r || 32-byte s, 64 bytes total)
// IEEE P1363 form the AdCP profile's Signature header requires — NOT DER.
func derECDSASignatureToP1363(der []byte) ([]byte, error) {
	var sig derASN1ECDSASignature
	rest, err := asn1.Unmarshal(der, &sig)
	if err != nil {
		return nil, fmt.Errorf("awskms: decode DER ECDSA signature: %w", err)
	}
	if len(rest) != 0 {
		return nil, fmt.Errorf("awskms: %d trailing byte(s) after DER ECDSA signature", len(rest))
	}
	if sig.R == nil || sig.S == nil || sig.R.Sign() <= 0 || sig.S.Sign() <= 0 {
		return nil, errors.New("awskms: DER ECDSA signature has a non-positive r or s component")
	}

	rb := sig.R.Bytes()
	sb := sig.S.Bytes()
	if len(rb) > ecdsaP256FieldSize || len(sb) > ecdsaP256FieldSize {
		return nil, fmt.Errorf("awskms: ECDSA signature component too large for P-256 (r=%d s=%d bytes, want <= %d)", len(rb), len(sb), ecdsaP256FieldSize)
	}

	out := make([]byte, 2*ecdsaP256FieldSize)
	copy(out[ecdsaP256FieldSize-len(rb):ecdsaP256FieldSize], rb)
	copy(out[2*ecdsaP256FieldSize-len(sb):], sb)
	return out, nil
}
