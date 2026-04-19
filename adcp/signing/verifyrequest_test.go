package signing

import (
	"context"
	"crypto/ed25519"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestVerifyRequestTriState(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(nil)
	require.NoError(t, err)

	kid := "kid-tri-state"
	jwk := &JWK{
		Kid: kid, Kty: "OKP", Crv: "Ed25519", Alg: "EdDSA",
		Use: "sig", KeyOps: []string{"verify"}, AdcpUse: "request-signing",
		X: b64UrlEncodeRaw(pub),
	}
	resolver := NewStaticJWKSResolver()
	resolver.Put(kid, jwk, "https://agent.example.com")

	opts := VerifyOptions{
		OperationName: "create_media_buy",
		RequiredFor:   []string{"create_media_buy"},
		Resolver:      resolver,
		Replay:        NewMemoryReplayStore(0),
	}

	// StatusVerified — signed, valid signature.
	signer, err := NewSigner(SignerOptions{KeyID: kid, PrivateKey: priv})
	require.NoError(t, err)
	body := strings.NewReader(`{"plan_id":"p1"}`)
	signed, _ := http.NewRequest("POST", "https://seller.example.com/adcp/create_media_buy", body)
	signed.Header.Set("Content-Type", "application/json")
	require.NoError(t, signer.SignRequest(signed, SignOptions{CoverContentDigest: true}))

	res := VerifyRequest(signed, opts)
	assert.Equal(t, StatusVerified, res.Status)
	require.NotNil(t, res.Signer)
	assert.Equal(t, kid, res.Signer.KeyID)
	assert.Nil(t, res.Error)

	// StatusRejected — op in RequiredFor but request unsigned.
	req := httptest.NewRequest("POST", "https://seller.example.com/adcp/create_media_buy", nil)
	res = VerifyRequest(req, opts)
	assert.Equal(t, StatusRejected, res.Status)
	require.NotNil(t, res.Error)
	assert.Equal(t, CodeRequired, res.Error.Code)
	assert.Nil(t, res.Signer)

	// StatusUnsigned — op not in RequiredFor, unsigned.
	optsOpen := opts
	optsOpen.OperationName = "get_products"
	req = httptest.NewRequest("POST", "https://seller.example.com/adcp/get_products", nil)
	res = VerifyRequest(req, optsOpen)
	assert.Equal(t, StatusUnsigned, res.Status)
	assert.Nil(t, res.Signer)
	assert.Nil(t, res.Error)

	// StatusRejected — signed but tampered. The underlying *Error's Code
	// (non-CodeRequired) must survive into VerifyResult untouched.
	body = strings.NewReader(`{"plan_id":"p2"}`)
	tampered, _ := http.NewRequest("POST", "https://seller.example.com/adcp/create_media_buy", body)
	tampered.Header.Set("Content-Type", "application/json")
	require.NoError(t, signer.SignRequest(tampered, SignOptions{CoverContentDigest: true}))
	// Change the method after signing so @method canonical diverges — crypto
	// verify fails deterministically without depending on base64 content.
	tampered.Method = "PUT"
	// Fresh replay store per scenario so we're not colliding with the nonce
	// consumed by the verified-request assertion above.
	tamperedOpts := opts
	tamperedOpts.Replay = NewMemoryReplayStore(0)
	res = VerifyRequest(tampered, tamperedOpts)
	assert.Equal(t, StatusRejected, res.Status)
	require.NotNil(t, res.Error)
	assert.NotEqual(t, CodeRequired, res.Error.Code,
		"rejection for crypto / header tampering must not masquerade as CodeRequired")
	assert.Nil(t, res.Signer)
}

func TestVerifyStatusZeroValueIsUnknown(t *testing.T) {
	// A default-constructed VerifyResult must not claim verification.
	var res VerifyResult
	assert.Equal(t, StatusUnknown, res.Status)
	assert.NotEqual(t, StatusVerified, res.Status,
		"zero value must not be StatusVerified — that would be the footgun this helper exists to prevent")
	assert.Equal(t, "unknown", res.Status.String())
}

func TestVerifyStatusString(t *testing.T) {
	assert.Equal(t, "verified", StatusVerified.String())
	assert.Equal(t, "unsigned", StatusUnsigned.String())
	assert.Equal(t, "rejected", StatusRejected.String())
}

func TestRequireSignedFromContext(t *testing.T) {
	// No signer on context → CodeRequired.
	err := RequireSigned(context.Background())
	require.NotNil(t, err)
	assert.Equal(t, CodeRequired, err.Code)

	// Signer on context → nil.
	ctx := context.WithValue(context.Background(), contextKey{}, &VerifiedSigner{KeyID: "x"})
	assert.Nil(t, RequireSigned(ctx))
}
