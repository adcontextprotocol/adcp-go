package awskms

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/asn1"
	"errors"
	"math/big"
	"net/http"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/service/kms"
	"github.com/aws/aws-sdk-go-v2/service/kms/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/adcontextprotocol/adcp-go/adcp/v3/signing"
)

// fakeKMS is a SignAPI test double. It never touches the network — the
// AWS SDK for Go v2's documented pattern for unit-testing service clients
// is to depend on a narrow interface (SignAPI here) instead of the
// concrete *kms.Client, and substitute a fake in tests. This is that fake:
// it records the request it was called with and returns a canned response
// or error.
type fakeKMS struct {
	gotInput *kms.SignInput
	out      *kms.SignOutput
	err      error

	getPublicKeyCalls int
	getPublicKeyOut   *kms.GetPublicKeyOutput
	getPublicKeyErr   error
}

func (f *fakeKMS) Sign(_ context.Context, params *kms.SignInput, _ ...func(*kms.Options)) (*kms.SignOutput, error) {
	f.gotInput = params
	if f.err != nil {
		return nil, f.err
	}
	return f.out, nil
}

func (f *fakeKMS) GetPublicKey(_ context.Context, _ *kms.GetPublicKeyInput, _ ...func(*kms.Options)) (*kms.GetPublicKeyOutput, error) {
	f.getPublicKeyCalls++
	if f.getPublicKeyErr != nil {
		return nil, f.getPublicKeyErr
	}
	return f.getPublicKeyOut, nil
}

// pkixPublicKeyOutput builds a kms.GetPublicKeyOutput carrying pub's X.509
// SubjectPublicKeyInfo DER encoding — the shape KMS's real GetPublicKey
// response uses.
func pkixPublicKeyOutput(t *testing.T, pub *ecdsa.PublicKey) *kms.GetPublicKeyOutput {
	t.Helper()
	der, err := x509.MarshalPKIXPublicKey(pub)
	require.NoError(t, err)
	return &kms.GetPublicKeyOutput{PublicKey: der}
}

// signWithRealECDSAKey produces a DER-encoded ECDSA_SHA_256 signature the
// way KMS itself would (RFC 3279 §2.2.3, per SignOutput's doc comment) —
// used to build realistic fake KMS responses and to verify Provider.Sign's
// output round-trips.
func signWithRealECDSAKey(t *testing.T, priv *ecdsa.PrivateKey, digest []byte) []byte {
	t.Helper()
	der, err := ecdsa.SignASN1(rand.Reader, priv, digest)
	require.NoError(t, err)
	return der
}

func TestNewValidation(t *testing.T) {
	_, err := New(Options{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "Client is required")

	_, err = New(Options{Client: &fakeKMS{}})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "KMSKeyID is required")

	_, err = New(Options{Client: &fakeKMS{}, KMSKeyID: "arn:aws:kms:us-east-1:111122223333:key/abc"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "KeyID is required")

	p, err := New(Options{
		Client:   &fakeKMS{},
		KMSKeyID: "arn:aws:kms:us-east-1:111122223333:key/abc",
		KeyID:    "buyer-2026",
	})
	require.NoError(t, err)
	assert.Equal(t, "buyer-2026", p.KeyID())
	assert.Equal(t, signing.AlgES256, p.Algorithm())
}

func TestProviderSatisfiesSigningProviderInterface(t *testing.T) {
	var _ signing.SigningProvider = (*Provider)(nil)
}

// TestSignConstructsExpectedRequest verifies exactly which fields Provider
// sends KMS: the configured KMSKeyID, a SHA-256 digest of the payload (not
// the raw payload) as Message, MessageType=DIGEST, and
// SigningAlgorithm=ECDSA_SHA_256.
func TestSignConstructsExpectedRequest(t *testing.T) {
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)

	payload := []byte("the RFC 9421 signature base")
	digest := sha256.Sum256(payload)
	der := signWithRealECDSAKey(t, priv, digest[:])

	fake := &fakeKMS{out: &kms.SignOutput{
		Signature:        der,
		SigningAlgorithm: types.SigningAlgorithmSpecEcdsaSha256,
	}}
	p, err := New(Options{Client: fake, KMSKeyID: "arn:aws:kms:us-east-1:111122223333:key/abc", KeyID: "buyer-2026"})
	require.NoError(t, err)

	sig, err := p.Sign(context.Background(), payload)
	require.NoError(t, err)

	require.NotNil(t, fake.gotInput)
	assert.Equal(t, "arn:aws:kms:us-east-1:111122223333:key/abc", *fake.gotInput.KeyId)
	assert.Equal(t, types.MessageTypeDigest, fake.gotInput.MessageType)
	assert.Equal(t, types.SigningAlgorithmSpecEcdsaSha256, fake.gotInput.SigningAlgorithm)
	assert.Equal(t, digest[:], fake.gotInput.Message, "Provider must send the SHA-256 digest, not the raw payload")

	// Response mapping: the returned signature must verify against the
	// same key and be the fixed-width P1363 (r||s) encoding, not the DER
	// bytes KMS returned.
	require.Len(t, sig, 64)
	assert.NotEqual(t, der, sig, "Sign must convert DER to P1363, not pass it through")
	r := new(big.Int).SetBytes(sig[:32])
	s := new(big.Int).SetBytes(sig[32:])
	assert.True(t, ecdsa.Verify(&priv.PublicKey, digest[:], r, s))
}

// TestSignPropagatesKMSError proves the design constraint from the issue's
// triage history (adcp-go#99): a KMS SDK error must not flow into the
// returned error's Error() string (which a careless caller might echo into
// an HTTP response — AGENTS.md's "never echo err.Error()" rule) — it must
// be a typed *signing.SigningError whose safe Code is what Error() renders,
// with the raw KMS error reachable only via errors.Unwrap/errors.As, for
// server-side logging.
func TestSignPropagatesKMSError(t *testing.T) {
	rawErr := errors.New("AccessDeniedException: user: arn:aws:iam::111122223333:user/alice is not authorized")
	fake := &fakeKMS{err: rawErr}
	p, err := New(Options{Client: fake, KMSKeyID: "key", KeyID: "kid"})
	require.NoError(t, err)

	_, err = p.Sign(context.Background(), []byte("payload"))
	require.Error(t, err)

	sigErr := signing.AsSigningError(err)
	require.NotNil(t, sigErr, "must be a *signing.SigningError")
	assert.Equal(t, signing.SignCodeProviderFailed, sigErr.Code)
	assert.ErrorIs(t, err, rawErr, "the raw cause must still be reachable via errors.Is/errors.Unwrap for logging")
	assert.NotContains(t, err.Error(), "AccessDeniedException", "the raw KMS error text must never appear in Error()")
	assert.NotContains(t, err.Error(), "arn:aws:iam", "resource identifiers from the KMS error must never appear in Error()")
}

func TestSignRejectsMismatchedResponseAlgorithm(t *testing.T) {
	fake := &fakeKMS{out: &kms.SignOutput{
		Signature:        []byte{0x30, 0x00}, // well-formed-enough DER shell; never reached
		SigningAlgorithm: types.SigningAlgorithmSpecRsassaPkcs1V15Sha256,
	}}
	p, err := New(Options{Client: fake, KMSKeyID: "key", KeyID: "kid"})
	require.NoError(t, err)

	_, err = p.Sign(context.Background(), []byte("payload"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "SigningAlgorithm")
	sigErr := signing.AsSigningError(err)
	require.NotNil(t, sigErr)
	assert.Equal(t, signing.SignCodeAlgorithmUnexpected, sigErr.Code)
}

func TestSignRejectsMalformedDERSignature(t *testing.T) {
	fake := &fakeKMS{out: &kms.SignOutput{
		Signature:        []byte("not-der-at-all"),
		SigningAlgorithm: types.SigningAlgorithmSpecEcdsaSha256,
	}}
	p, err := New(Options{Client: fake, KMSKeyID: "key", KeyID: "kid"})
	require.NoError(t, err)

	_, err = p.Sign(context.Background(), []byte("payload"))
	require.Error(t, err)
	sigErr := signing.AsSigningError(err)
	require.NotNil(t, sigErr, "must be a *signing.SigningError")
	assert.Equal(t, signing.SignCodeProviderFailed, sigErr.Code)
	require.NotNil(t, sigErr.Wrapped)
	assert.Contains(t, sigErr.Wrapped.Error(), "decode DER", "the DER-decode detail is reachable via Wrapped, not Error()")
}

// --- PublicKey ---------------------------------------------------------------

func TestPublicKeyFetchesAndCaches(t *testing.T) {
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)

	fake := &fakeKMS{getPublicKeyOut: pkixPublicKeyOutput(t, &priv.PublicKey)}
	p, err := New(Options{Client: fake, KMSKeyID: "key", KeyID: "kid"})
	require.NoError(t, err)

	pub, err := p.PublicKey(context.Background())
	require.NoError(t, err)
	ecPub, ok := pub.(*ecdsa.PublicKey)
	require.True(t, ok)
	assert.True(t, priv.PublicKey.Equal(ecPub))
	assert.Equal(t, 1, fake.getPublicKeyCalls)

	// Second call must be served from cache — no second KMS call.
	_, err = p.PublicKey(context.Background())
	require.NoError(t, err)
	assert.Equal(t, 1, fake.getPublicKeyCalls, "PublicKey must cache a successful result instead of calling KMS again")
}

// TestPublicKeyRetriesAfterFailure proves the design constraint from the
// issue's triage history: caching must be success-only. A sync.Once-based
// cache would permanently poison Provider after one transient KMS failure;
// this provider must retry on the next call instead.
func TestPublicKeyRetriesAfterFailure(t *testing.T) {
	fake := &fakeKMS{getPublicKeyErr: errors.New("ThrottlingException")}
	p, err := New(Options{Client: fake, KMSKeyID: "key", KeyID: "kid"})
	require.NoError(t, err)

	_, err = p.PublicKey(context.Background())
	require.Error(t, err)
	sigErr := signing.AsSigningError(err)
	require.NotNil(t, sigErr)
	assert.Equal(t, signing.SignCodeProviderFailed, sigErr.Code)
	assert.NotContains(t, err.Error(), "ThrottlingException")
	assert.Equal(t, 1, fake.getPublicKeyCalls)

	// Now KMS recovers.
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	fake.getPublicKeyErr = nil
	fake.getPublicKeyOut = pkixPublicKeyOutput(t, &priv.PublicKey)

	pub, err := p.PublicKey(context.Background())
	require.NoError(t, err)
	assert.Equal(t, 2, fake.getPublicKeyCalls, "must retry KMS after a prior failure, not stay poisoned")
	ecPub, ok := pub.(*ecdsa.PublicKey)
	require.True(t, ok)
	assert.True(t, priv.PublicKey.Equal(ecPub))
}

func TestDerECDSASignatureToP1363PadsShortComponents(t *testing.T) {
	// r and s with a leading zero byte trimmed by big.Int must still land
	// at the right offset in the fixed-width output (left-padded with
	// zeros), not be left-aligned.
	sig := derASN1ECDSASignature{
		R: big.NewInt(1),
		S: big.NewInt(2),
	}
	der, err := asn1.Marshal(sig)
	require.NoError(t, err)

	out, err := derECDSASignatureToP1363(der)
	require.NoError(t, err)
	require.Len(t, out, 64)
	assert.Equal(t, byte(1), out[31], "r must be right-aligned in the first 32 bytes")
	assert.Equal(t, byte(2), out[63], "s must be right-aligned in the last 32 bytes")
	for _, b := range out[:31] {
		assert.Equal(t, byte(0), b)
	}
	for _, b := range out[32:63] {
		assert.Equal(t, byte(0), b)
	}
}

// TestSignerEndToEndWithFakeKMS wires Provider into signing.NewSigner and
// signing.SignRequest exactly as a real caller would, then verifies the
// resulting signature with adcp/v3/signing's own verifier — proving the
// AWS KMS provider integrates with the Signer/SigningProvider contract
// end-to-end, not just in isolation.
func TestSignerEndToEndWithFakeKMS(t *testing.T) {
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)

	fake := &kmsBackedBySameKey{priv: priv}
	provider, err := New(Options{Client: fake, KMSKeyID: "kms-key-1", KeyID: "buyer-p256-2026"})
	require.NoError(t, err)

	signer, err := signing.NewSigner(signing.SignerOptions{Provider: provider})
	require.NoError(t, err)

	req, err := http.NewRequest("POST", "https://seller.example.com/adcp/create_media_buy", strings.NewReader(`{"plan_id":"p1"}`))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	require.NoError(t, signer.SignRequest(req, signing.SignOptions{CoverContentDigest: true}))
	assert.Contains(t, req.Header.Get("Signature-Input"), `keyid="buyer-p256-2026"`)

	// Build the publishable JWK via NewPublicJWKFromProvider instead of
	// hand-assembling it — exercises the JWKS-publication helper against
	// this provider end-to-end, the same call an operator's onboarding
	// script would make.
	jwk, err := signing.NewPublicJWKFromProvider(context.Background(), provider, "buyer-p256-2026", signing.ProfileRequestSigning.AdcpUse)
	require.NoError(t, err)
	resolver := signing.NewStaticJWKSResolver()
	resolver.Put("buyer-p256-2026", &jwk, "https://buyer.example.com")

	res := signing.VerifyRequest(req, signing.VerifyOptions{
		OperationName: "create_media_buy",
		RequiredFor:   []string{"create_media_buy"},
		Resolver:      resolver,
		Replay:        signing.NewMemoryReplayStore(0),
	})
	assert.Equal(t, signing.StatusVerified, res.Status)
	require.NotNil(t, res.Signer)
	assert.Equal(t, "buyer-p256-2026", res.Signer.KeyID)
}

// kmsBackedBySameKey is a fakeKMS that actually signs with a real ECDSA
// private key, so TestSignerEndToEndWithFakeKMS exercises a genuine
// cryptographic round trip (sign via the "KMS" fake, verify via
// adcp/v3/signing) rather than canned bytes.
type kmsBackedBySameKey struct {
	priv *ecdsa.PrivateKey
}

func (k *kmsBackedBySameKey) Sign(_ context.Context, params *kms.SignInput, _ ...func(*kms.Options)) (*kms.SignOutput, error) {
	der, err := ecdsa.SignASN1(rand.Reader, k.priv, params.Message)
	if err != nil {
		return nil, err
	}
	return &kms.SignOutput{
		Signature:        der,
		SigningAlgorithm: types.SigningAlgorithmSpecEcdsaSha256,
	}, nil
}

func (k *kmsBackedBySameKey) GetPublicKey(_ context.Context, _ *kms.GetPublicKeyInput, _ ...func(*kms.Options)) (*kms.GetPublicKeyOutput, error) {
	der, err := x509.MarshalPKIXPublicKey(&k.priv.PublicKey)
	if err != nil {
		return nil, err
	}
	return &kms.GetPublicKeyOutput{PublicKey: der}, nil
}
