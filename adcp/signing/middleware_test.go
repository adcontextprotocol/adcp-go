package signing

import (
	"bytes"
	"crypto/ed25519"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMiddlewareEndToEndSignAndVerify(t *testing.T) {
	// Build a fresh Ed25519 keypair for a round-trip.
	pub, priv, err := ed25519.GenerateKey(nil)
	require.NoError(t, err)

	kid := "test-kid"
	jwk := &JWK{
		Kid:     kid,
		Kty:     "OKP",
		Crv:     "Ed25519",
		Alg:     "EdDSA",
		Use:     "sig",
		KeyOps:  []string{"verify"},
		AdcpUse: "request-signing",
		X:       b64UrlEncodeRaw(pub),
	}
	resolver := NewStaticJWKSResolver()
	resolver.Put(kid, jwk, "https://agent.example.com")

	verified := make(chan *VerifiedSigner, 1)
	mw := Middleware(MiddlewareOptions{
		Resolver:            resolver,
		Replay:              NewMemoryReplayStore(0),
		Revocation:          NewStaticRevocationList(nil),
		OperationResolver:   func(r *http.Request) string { return "create_media_buy" },
		RequiredFor:         []string{"create_media_buy"},
		ContentDigestPolicy: DigestEither,
	})
	handler := mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		verified <- VerifiedSignerFromContext(r.Context())
		w.WriteHeader(http.StatusOK)
	}))
	srv := httptest.NewServer(handler)
	defer srv.Close()

	signer, err := NewSigner(SignerOptions{
		KeyID:      kid,
		PrivateKey: priv,
	})
	require.NoError(t, err)

	body := strings.NewReader(`{"plan_id":"p1"}`)
	req, err := http.NewRequest("POST", srv.URL+"/adcp/create_media_buy", body)
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	require.NoError(t, signer.SignRequest(req, SignOptions{CoverContentDigest: true}))

	resp, err := srv.Client().Do(req)
	require.NoError(t, err)
	defer closeResponseBody(t, resp)
	assert.Equal(t, http.StatusOK, resp.StatusCode)

	got := <-verified
	require.NotNil(t, got)
	assert.Equal(t, kid, got.KeyID)
	assert.Equal(t, AlgEd25519, got.Algorithm)
}

func TestMiddlewareRejectsWithWWWAuth(t *testing.T) {
	resolver := NewStaticJWKSResolver()
	mw := Middleware(MiddlewareOptions{
		Resolver:          resolver,
		Replay:            NewMemoryReplayStore(0),
		OperationResolver: func(r *http.Request) string { return "create_media_buy" },
		RequiredFor:       []string{"create_media_buy"},
	})
	handler := mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("handler should not run on reject")
	}))

	// Unsigned request to required operation.
	req := httptest.NewRequest("POST", "https://seller.example.com/adcp/create_media_buy", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	assert.Equal(t, http.StatusUnauthorized, w.Code)
	assert.Equal(t, `Signature error="request_signature_required"`, w.Header().Get("WWW-Authenticate"))
}

func TestMiddlewareUnsignedPassesWhenNotRequired(t *testing.T) {
	called := false
	mw := Middleware(MiddlewareOptions{
		Resolver:          NewStaticJWKSResolver(),
		Replay:            NewMemoryReplayStore(0),
		OperationResolver: func(r *http.Request) string { return "get_products" },
		RequiredFor:       []string{"create_media_buy"},
	})
	handler := mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	}))
	req := httptest.NewRequest("POST", "https://seller.example.com/adcp/get_products", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	assert.Equal(t, http.StatusOK, w.Code)
	assert.True(t, called)
}

// TestRoundTripperSignsAndBodyIsPreserved confirms the signing RoundTripper
// passes the unmodified body to the inner transport and produces a valid
// signature over it.
func TestRoundTripperSignsAndBodyIsPreserved(t *testing.T) {
	_, priv, err := ed25519.GenerateKey(nil)
	require.NoError(t, err)
	signer, err := NewSigner(SignerOptions{KeyID: "kid", PrivateKey: priv})
	require.NoError(t, err)

	var receivedBody []byte
	var receivedSig string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		receivedBody = b
		receivedSig = r.Header.Get("Signature")
	}))
	defer srv.Close()

	client := &http.Client{Transport: signer.RoundTripper(srv.Client().Transport, true)}
	resp, err := client.Post(srv.URL+"/adcp/x", "application/json", bytes.NewReader([]byte(`{"a":1}`)))
	require.NoError(t, err)
	defer closeResponseBody(t, resp)

	assert.Equal(t, `{"a":1}`, string(receivedBody))
	assert.NotEmpty(t, receivedSig)
}
