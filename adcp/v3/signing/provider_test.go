package signing

import (
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"fmt"
	"math/big"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- InMemorySigningProvider: sign/verify round trip -----------------------

func TestInMemorySigningProviderEd25519RoundTrip(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)

	p, err := NewInMemorySigningProvider("kid-ed25519", priv)
	require.NoError(t, err)
	assert.Equal(t, "kid-ed25519", p.KeyID())
	assert.Equal(t, AlgEd25519, p.Algorithm())

	payload := []byte("the RFC 9421 signature base")
	sig, err := p.Sign(context.Background(), payload)
	require.NoError(t, err)
	assert.True(t, ed25519.Verify(pub, payload, sig), "signature must verify against the public key")
}

func TestInMemorySigningProviderECDSARoundTrip(t *testing.T) {
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)

	p, err := NewInMemorySigningProvider("kid-es256", priv)
	require.NoError(t, err)
	assert.Equal(t, AlgES256, p.Algorithm())

	payload := []byte("the RFC 9421 signature base")
	sig, err := p.Sign(context.Background(), payload)
	require.NoError(t, err)
	require.Len(t, sig, 64, "P-256 IEEE P1363 (r||s) encoding must be exactly 64 bytes")

	// Decode the P1363 encoding back into (r, s) and verify directly against
	// crypto/ecdsa — proves Sign() produces the wire format the AdCP
	// profile requires (fixed-width r||s, NOT DER).
	rInt := new(big.Int).SetBytes(sig[:32])
	sInt := new(big.Int).SetBytes(sig[32:])
	h := sha256.Sum256(payload)
	assert.True(t, ecdsa.Verify(&priv.PublicKey, h[:], rInt, sInt), "signature must verify against the public key")
}

func TestNewInMemorySigningProviderValidation(t *testing.T) {
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)

	_, err = NewInMemorySigningProvider("", priv)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "KeyID is required")

	_, err = NewInMemorySigningProvider("kid", "not a key")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unsupported private key type")

	p384, err := ecdsa.GenerateKey(elliptic.P384(), rand.Reader)
	require.NoError(t, err)
	_, err = NewInMemorySigningProvider("kid", p384)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unsupported ECDSA curve")
}

// --- Context cancellation ---------------------------------------------------

func TestInMemorySigningProviderRejectsAlreadyCancelledContext(t *testing.T) {
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	p, err := NewInMemorySigningProvider("kid", priv)
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err = p.Sign(ctx, []byte("payload"))
	require.Error(t, err)
	assert.ErrorIs(t, err, context.Canceled)
}

// blockingProvider is a SigningProvider whose Sign blocks until ctx is done
// or release is closed, whichever happens first. It exists to prove two
// things a real KMS/HSM-backed provider needs:
//  1. SignRequest actually threads the signed http.Request's context through
//     to the provider's Sign call (not context.Background()).
//  2. A provider that respects ctx.Done() mid-flight is cancellable by the
//     caller — the exact behavior the issue asks for ("KMS sign latency is
//     typically 10-50ms ... retries/backoff need cancellation").
type blockingProvider struct {
	keyID   string
	alg     Algorithm
	release chan struct{}
	inner   SigningProvider
}

func (b *blockingProvider) KeyID() string        { return b.keyID }
func (b *blockingProvider) Algorithm() Algorithm { return b.alg }

func (b *blockingProvider) Sign(ctx context.Context, payload []byte) ([]byte, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-b.release:
		return b.inner.Sign(ctx, payload)
	}
}

func (b *blockingProvider) PublicKey(ctx context.Context) (crypto.PublicKey, error) {
	if b.inner == nil {
		return nil, fmt.Errorf("blockingProvider: no inner provider configured")
	}
	return b.inner.PublicKey(ctx)
}

func newBlockingProvider(t *testing.T) *blockingProvider {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	inner, err := NewInMemorySigningProvider("kid", priv)
	require.NoError(t, err)
	return &blockingProvider{keyID: "kid", alg: AlgEd25519, release: make(chan struct{}), inner: inner}
}

func TestSignRequestCancelsWhenRequestContextIsCancelled(t *testing.T) {
	provider := newBlockingProvider(t)
	signer, err := NewSigner(SignerOptions{Provider: provider})
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	req, err := http.NewRequestWithContext(ctx, "POST", "https://seller.example.com/adcp/create_media_buy", strings.NewReader(`{}`))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")

	errCh := make(chan error, 1)
	go func() { errCh <- signer.SignRequest(req, SignOptions{}) }()

	// Give SignRequest time to reach the blocked provider.Sign call, then
	// cancel the *request's* context (not release the provider) to prove
	// the cancellation reaches the provider through r.Context().
	time.Sleep(20 * time.Millisecond)
	cancel()

	select {
	case err := <-errCh:
		require.Error(t, err)
		assert.ErrorIs(t, err, context.Canceled, "SignRequest must surface the provider's context.Canceled error")
	case <-time.After(2 * time.Second):
		t.Fatal("SignRequest did not return after the request context was cancelled")
	}
}

func TestSignRequestSucceedsWhenProviderIsReleasedBeforeCancellation(t *testing.T) {
	provider := newBlockingProvider(t)
	signer, err := NewSigner(SignerOptions{Provider: provider})
	require.NoError(t, err)

	req, err := http.NewRequest("POST", "https://seller.example.com/adcp/create_media_buy", strings.NewReader(`{}`))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")

	errCh := make(chan error, 1)
	go func() { errCh <- signer.SignRequest(req, SignOptions{}) }()

	time.Sleep(20 * time.Millisecond)
	close(provider.release)

	select {
	case err := <-errCh:
		require.NoError(t, err)
		assert.NotEmpty(t, req.Header.Get("Signature"))
		assert.Contains(t, req.Header.Get("Signature-Input"), `keyid="kid"`)
	case <-time.After(2 * time.Second):
		t.Fatal("SignRequest did not return after the provider was released")
	}
}

// --- Signer integration: Provider-based construction, end to end -----------

func TestNewSignerWithProviderSignsAndVerifies(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	provider, err := NewInMemorySigningProvider("provider-kid", priv)
	require.NoError(t, err)

	// SignerOptions.KeyID / PrivateKey deliberately left unset — Provider
	// alone must be sufficient.
	signer, err := NewSigner(SignerOptions{Provider: provider})
	require.NoError(t, err)
	assert.Equal(t, AlgEd25519, signer.Algorithm())

	req, err := http.NewRequest("POST", "https://seller.example.com/adcp/create_media_buy", strings.NewReader(`{"plan_id":"p1"}`))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	require.NoError(t, signer.SignRequest(req, SignOptions{CoverContentDigest: true}))
	assert.Contains(t, req.Header.Get("Signature-Input"), `keyid="provider-kid"`)

	jwk := &JWK{
		Kid: "provider-kid", Kty: "OKP", Crv: "Ed25519", Alg: "EdDSA",
		Use: "sig", KeyOps: []string{"verify"}, AdcpUse: "request-signing",
		X: b64UrlEncodeRaw(pub),
	}
	resolver := NewStaticJWKSResolver()
	resolver.Put("provider-kid", jwk, "https://buyer.example.com")

	res := VerifyRequest(req, VerifyOptions{
		OperationName: "create_media_buy",
		RequiredFor:   []string{"create_media_buy"},
		Resolver:      resolver,
		Replay:        NewMemoryReplayStore(0),
	})
	assert.Equal(t, StatusVerified, res.Status)
	require.NotNil(t, res.Signer)
	assert.Equal(t, "provider-kid", res.Signer.KeyID)
}

func TestNewSignerRejectsProviderWithDisallowedAlgorithm(t *testing.T) {
	_, err := NewSigner(SignerOptions{Provider: &blockingProvider{keyID: "kid", alg: Algorithm("rsa-pss")}})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unsupported algorithm")
}

func TestNewSignerRejectsProviderWithEmptyKeyID(t *testing.T) {
	_, err := NewSigner(SignerOptions{Provider: &blockingProvider{keyID: "", alg: AlgEd25519}})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "KeyID")
}

// --- SigningProvider.PublicKey ----------------------------------------------

func TestInMemorySigningProviderPublicKeyEd25519(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	p, err := NewInMemorySigningProvider("kid", priv)
	require.NoError(t, err)

	got, err := p.PublicKey(context.Background())
	require.NoError(t, err)
	assert.Equal(t, ed25519.PublicKey(pub), got)
}

func TestInMemorySigningProviderPublicKeyECDSA(t *testing.T) {
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	p, err := NewInMemorySigningProvider("kid", priv)
	require.NoError(t, err)

	got, err := p.PublicKey(context.Background())
	require.NoError(t, err)
	ecPub, ok := got.(*ecdsa.PublicKey)
	require.True(t, ok)
	assert.True(t, priv.PublicKey.Equal(ecPub))
}

func TestInMemorySigningProviderPublicKeyRejectsAlreadyCancelledContext(t *testing.T) {
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	p, err := NewInMemorySigningProvider("kid", priv)
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = p.PublicKey(ctx)
	require.Error(t, err)
	assert.ErrorIs(t, err, context.Canceled)
}

// --- NewPublicJWKFromProvider ------------------------------------------------

func TestNewPublicJWKFromProviderEd25519(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	provider, err := NewInMemorySigningProvider("provider-kid", priv)
	require.NoError(t, err)

	jwk, err := NewPublicJWKFromProvider(context.Background(), provider, "published-kid", ProfileRequestSigning.AdcpUse)
	require.NoError(t, err)
	assert.Equal(t, "published-kid", jwk.Kid, "kid is the caller's parameter, not provider.KeyID()")
	assert.Equal(t, "OKP", jwk.Kty)
	assert.Equal(t, "Ed25519", jwk.Crv)
	assert.Equal(t, "EdDSA", jwk.Alg)
	assert.Equal(t, "sig", jwk.Use)
	assert.Equal(t, []string{"verify"}, jwk.KeyOps)
	assert.Equal(t, "request-signing", jwk.AdcpUse)

	// Round trip: the JWK's public key must match the provider's.
	jwkPub, err := jwk.PublicKey()
	require.NoError(t, err)
	assert.Equal(t, ed25519.PublicKey(pub), jwkPub)
}

func TestNewPublicJWKFromProviderES256(t *testing.T) {
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	provider, err := NewInMemorySigningProvider("provider-kid", priv)
	require.NoError(t, err)

	jwk, err := NewPublicJWKFromProvider(context.Background(), provider, "published-kid", ProfileWebhookSigning.AdcpUse)
	require.NoError(t, err)
	assert.Equal(t, "EC", jwk.Kty)
	assert.Equal(t, "P-256", jwk.Crv)
	assert.Equal(t, "ES256", jwk.Alg)
	assert.Equal(t, "webhook-signing", jwk.AdcpUse)

	jwkPub, err := jwk.PublicKey()
	require.NoError(t, err)
	ecPub, ok := jwkPub.(*ecdsa.PublicKey)
	require.True(t, ok)
	assert.True(t, priv.PublicKey.Equal(ecPub))
}

func TestNewPublicJWKFromProviderRequiresAdcpUse(t *testing.T) {
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	provider, err := NewInMemorySigningProvider("kid", priv)
	require.NoError(t, err)

	_, err = NewPublicJWKFromProvider(context.Background(), provider, "kid", "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "adcpUse is required")
}

// TestNewPublicJWKFromProviderRejectsWrongAdcpUseAtVerification proves the
// JWK this helper produces is actually enforced by the verifier's adcp_use
// check (spec-required key separation, adcontextprotocol/adcp#2423) — the
// same failure mode spec conformance vector 008-wrong-adcp-use covers: a
// key published for one purpose must not verify a signature for another.
func TestNewPublicJWKFromProviderRejectsWrongAdcpUseAtVerification(t *testing.T) {
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	provider, err := NewInMemorySigningProvider("kid", priv)
	require.NoError(t, err)

	// Publish the JWK for webhook-signing, but sign a request-signing
	// profile request with it.
	jwk, err := NewPublicJWKFromProvider(context.Background(), provider, "kid", ProfileWebhookSigning.AdcpUse)
	require.NoError(t, err)

	resolver := NewStaticJWKSResolver()
	resolver.Put("kid", &jwk, "https://buyer.example.com")

	signer, err := NewSigner(SignerOptions{Provider: provider}) // defaults to ProfileRequestSigning
	require.NoError(t, err)
	req, err := http.NewRequest("POST", "https://seller.example.com/adcp/create_media_buy", strings.NewReader(`{}`))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	require.NoError(t, signer.SignRequest(req, SignOptions{}))

	res := VerifyRequest(req, VerifyOptions{
		OperationName: "create_media_buy",
		RequiredFor:   []string{"create_media_buy"},
		Resolver:      resolver,
		Replay:        NewMemoryReplayStore(0),
	})
	assert.Equal(t, StatusRejected, res.Status)
	require.NotNil(t, res.Error)
	assert.Equal(t, CodeKeyPurposeInvalid, res.Error.Code)
}

// --- AssertProviderPublicKeyMatchesSPKI -------------------------------------

func TestAssertProviderPublicKeyMatchesSPKISuccess(t *testing.T) {
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	provider, err := NewInMemorySigningProvider("kid", priv)
	require.NoError(t, err)

	pub, err := provider.PublicKey(context.Background())
	require.NoError(t, err)
	spki, err := x509.MarshalPKIXPublicKey(pub)
	require.NoError(t, err)

	err = AssertProviderPublicKeyMatchesSPKI(context.Background(), provider, spki)
	assert.NoError(t, err)
}

func TestAssertProviderPublicKeyMatchesSPKIDetectsRotation(t *testing.T) {
	_, priv1, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	provider1, err := NewInMemorySigningProvider("kid", priv1)
	require.NoError(t, err)
	pub1, err := provider1.PublicKey(context.Background())
	require.NoError(t, err)
	pinnedSPKI, err := x509.MarshalPKIXPublicKey(pub1)
	require.NoError(t, err)

	// A different provider under the same kid — simulates the key store
	// silently rotating the key backing this kid out from under us.
	_, priv2, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	provider2, err := NewInMemorySigningProvider("kid", priv2)
	require.NoError(t, err)

	err = AssertProviderPublicKeyMatchesSPKI(context.Background(), provider2, pinnedSPKI)
	require.Error(t, err)
	sigErr := AsSigningError(err)
	require.NotNil(t, sigErr, "must be a *SigningError")
	assert.Equal(t, SignCodePublicKeyMismatch, sigErr.Code)
}
