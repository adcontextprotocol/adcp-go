package signing

import (
	"bytes"
	"crypto/ed25519"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/adcontextprotocol/adcp-go/adcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Helper: builds a verifying server backed by the supplied JWK resolver and
// returns it along with its base URL.
func newVerifyingServer(t *testing.T, kid string, pub ed25519.PublicKey) *httptest.Server {
	t.Helper()
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
	resolver.Put(kid, jwk, "https://buyer.example.com")

	mw := Middleware(MiddlewareOptions{
		Resolver:          resolver,
		OperationResolver: func(r *http.Request) string { return "create_media_buy" },
		RequiredFor:       []string{"create_media_buy"},
	})
	handler := mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		signer := VerifiedSignerFromContext(r.Context())
		require.NotNil(t, signer, "expected verified signer on the success path")
		w.WriteHeader(http.StatusOK)
	}))
	return httptest.NewServer(handler)
}

func TestNewSignedHTTPClientRoundTripsAgainstVerifier(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(nil)
	require.NoError(t, err)

	srv := newVerifyingServer(t, "test-kid", pub)
	defer srv.Close()

	client, err := NewSignedHTTPClient(SignedHTTPClientOptions{
		KeyID:              "test-kid",
		PrivateKey:         priv,
		CoverContentDigest: true,
		Inner:              srv.Client().Transport,
		Timeout:            5 * time.Second,
	})
	require.NoError(t, err)

	req, err := http.NewRequest(
		"POST",
		srv.URL+"/adcp/create_media_buy",
		strings.NewReader(`{"plan_id":"p1"}`),
	)
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")

	resp, err := client.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusOK, resp.StatusCode)
}

func TestNewSignedHTTPClientRejectsRedirects(t *testing.T) {
	_, priv, err := ed25519.GenerateKey(nil)
	require.NoError(t, err)

	// Server that always 301s to itself. The signing client must NOT follow
	// — @target-uri is part of the signature base, so following the redirect
	// silently re-targets the binding.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/elsewhere", http.StatusMovedPermanently)
	}))
	defer srv.Close()

	client, err := NewSignedHTTPClient(SignedHTTPClientOptions{
		KeyID:      "test-kid",
		PrivateKey: priv,
		Inner:      srv.Client().Transport,
	})
	require.NoError(t, err)

	resp, err := client.Post(srv.URL+"/adcp/create_media_buy", "application/json", bytes.NewReader([]byte(`{}`)))
	require.NoError(t, err)
	defer resp.Body.Close()
	// CheckRedirect returns http.ErrUseLastResponse → caller sees the 3xx
	// directly rather than the redirect's body.
	assert.Equal(t, http.StatusMovedPermanently, resp.StatusCode)
}

func TestNewSignedHTTPClientRequiresKeyMaterial(t *testing.T) {
	_, err := NewSignedHTTPClient(SignedHTTPClientOptions{})
	require.Error(t, err, "empty options must error")
	assert.Contains(t, err.Error(), "KeyID is required")

	_, err = NewSignedHTTPClient(SignedHTTPClientOptions{KeyID: "kid"})
	require.Error(t, err, "missing PrivateKey must error")
	assert.Contains(t, err.Error(), "PrivateKey is required")

	// PrivateKey present but no KeyID → KeyID-required path (early-return).
	_, err = NewSignedHTTPClient(SignedHTTPClientOptions{PrivateKey: ed25519.PrivateKey(make([]byte, ed25519.PrivateKeySize))})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "KeyID is required")

	// Both fields present but key material an unsupported type → NewSigner
	// rejects. Original test asserted on a byte-slice cast to
	// ed25519.PrivateKey; that doesn't fail at construction (NewSigner
	// only switches on the static type, not byte length). Use a type
	// the signer can't recognize at all to exercise the validation path.
	_, err = NewSignedHTTPClient(SignedHTTPClientOptions{
		KeyID:      "kid",
		PrivateKey: []byte("not-an-ed25519-key"),
	})
	require.Error(t, err, "unsupported key type must error from NewSigner")
}

func TestNewSignedHTTPClientRespectsCustomInnerTransport(t *testing.T) {
	_, priv, err := ed25519.GenerateKey(nil)
	require.NoError(t, err)

	// Inner that captures requests to confirm the wrapping order is
	// caller-inner-then-signing rather than signing-then-default.
	captured := make(chan *http.Request, 1)
	inner := roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		captured <- r
		return &http.Response{StatusCode: http.StatusOK, Body: http.NoBody}, nil
	})

	client, err := NewSignedHTTPClient(SignedHTTPClientOptions{
		KeyID:      "test-kid",
		PrivateKey: priv,
		Inner:      inner,
	})
	require.NoError(t, err)

	req, err := http.NewRequest("POST", "https://example.com/adcp/x", strings.NewReader(`{}`))
	require.NoError(t, err)
	resp, err := client.Do(req)
	require.NoError(t, err)
	resp.Body.Close()

	got := <-captured
	assert.NotEmpty(t, got.Header.Get("Signature"))
	assert.NotEmpty(t, got.Header.Get("Signature-Input"))
}

type roundTripperFunc func(r *http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(r *http.Request) (*http.Response, error) {
	return f(r)
}

// Capability-aware signing tests — exercise the per-request CapabilityProvider
// path that mirrors Python's capability_provider and TS's getCapability.

func TestCapabilityProviderSignsRequiredFor(t *testing.T) {
	_, priv, err := ed25519.GenerateKey(nil)
	require.NoError(t, err)

	captured := make(chan *http.Request, 1)
	inner := roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		captured <- r
		return &http.Response{StatusCode: http.StatusOK, Body: http.NoBody}, nil
	})

	provider := func(*http.Request) *adcp.RequestSigningCapabilities {
		return &adcp.RequestSigningCapabilities{
			Supported:           true,
			RequiredFor:         []string{"create_media_buy"},
			CoversContentDigest: "either",
		}
	}

	client, err := NewSignedHTTPClient(SignedHTTPClientOptions{
		KeyID:              "kid",
		PrivateKey:         priv,
		Inner:              inner,
		CapabilityProvider: provider,
	})
	require.NoError(t, err)

	req, err := http.NewRequest("POST", "https://example.com/adcp/create_media_buy", strings.NewReader(`{}`))
	require.NoError(t, err)
	resp, err := client.Do(req)
	require.NoError(t, err)
	resp.Body.Close()

	got := <-captured
	assert.NotEmpty(t, got.Header.Get("Signature"), "required_for op must be signed")
}

func TestCapabilityProviderSkipsUnlistedOperation(t *testing.T) {
	_, priv, err := ed25519.GenerateKey(nil)
	require.NoError(t, err)

	captured := make(chan *http.Request, 1)
	inner := roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		captured <- r
		return &http.Response{StatusCode: http.StatusOK, Body: http.NoBody}, nil
	})

	provider := func(*http.Request) *adcp.RequestSigningCapabilities {
		return &adcp.RequestSigningCapabilities{
			Supported:   true,
			RequiredFor: []string{"create_media_buy"},
		}
	}

	client, err := NewSignedHTTPClient(SignedHTTPClientOptions{
		KeyID:              "kid",
		PrivateKey:         priv,
		Inner:              inner,
		CapabilityProvider: provider,
	})
	require.NoError(t, err)

	req, err := http.NewRequest("POST", "https://example.com/adcp/get_products", strings.NewReader(`{}`))
	require.NoError(t, err)
	resp, err := client.Do(req)
	require.NoError(t, err)
	resp.Body.Close()

	got := <-captured
	assert.Empty(t, got.Header.Get("Signature"), "op not in any list must NOT be signed")
}

func TestCapabilityProviderReturningNilSkipsSigning(t *testing.T) {
	_, priv, err := ed25519.GenerateKey(nil)
	require.NoError(t, err)

	var providerCalls atomic.Int32
	captured := make(chan *http.Request, 1)
	inner := roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		captured <- r
		return &http.Response{StatusCode: http.StatusOK, Body: http.NoBody}, nil
	})

	provider := func(*http.Request) *adcp.RequestSigningCapabilities {
		providerCalls.Add(1)
		return nil
	}

	client, err := NewSignedHTTPClient(SignedHTTPClientOptions{
		KeyID:              "kid",
		PrivateKey:         priv,
		Inner:              inner,
		CapabilityProvider: provider,
	})
	require.NoError(t, err)

	req, err := http.NewRequest("POST", "https://example.com/adcp/create_media_buy", strings.NewReader(`{}`))
	require.NoError(t, err)
	resp, err := client.Do(req)
	require.NoError(t, err)
	resp.Body.Close()

	assert.Equal(t, int32(1), providerCalls.Load())
	got := <-captured
	assert.Empty(t, got.Header.Get("Signature"), "nil capability ⇒ skip signing")
}

func TestCapabilityProviderHonorsCoversContentDigestRequired(t *testing.T) {
	_, priv, err := ed25519.GenerateKey(nil)
	require.NoError(t, err)

	captured := make(chan *http.Request, 1)
	inner := roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		captured <- r
		return &http.Response{StatusCode: http.StatusOK, Body: http.NoBody}, nil
	})

	provider := func(*http.Request) *adcp.RequestSigningCapabilities {
		return &adcp.RequestSigningCapabilities{
			Supported:           true,
			RequiredFor:         []string{"create_media_buy"},
			CoversContentDigest: "required",
		}
	}

	// Fallback CoverContentDigest=false; capability="required" overrides.
	client, err := NewSignedHTTPClient(SignedHTTPClientOptions{
		KeyID:              "kid",
		PrivateKey:         priv,
		Inner:              inner,
		CoverContentDigest: false,
		CapabilityProvider: provider,
	})
	require.NoError(t, err)

	req, err := http.NewRequest("POST", "https://example.com/adcp/create_media_buy", strings.NewReader(`{}`))
	require.NoError(t, err)
	resp, err := client.Do(req)
	require.NoError(t, err)
	resp.Body.Close()

	got := <-captured
	sigInput := got.Header.Get("Signature-Input")
	require.NotEmpty(t, sigInput)
	// The covered components are listed in parens before the params block.
	assert.Contains(t, sigInput, "content-digest", "covers='required' ⇒ digest must be covered")
}

func TestCapabilityProviderHonorsCoversContentDigestForbidden(t *testing.T) {
	_, priv, err := ed25519.GenerateKey(nil)
	require.NoError(t, err)

	captured := make(chan *http.Request, 1)
	inner := roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		captured <- r
		return &http.Response{StatusCode: http.StatusOK, Body: http.NoBody}, nil
	})

	provider := func(*http.Request) *adcp.RequestSigningCapabilities {
		return &adcp.RequestSigningCapabilities{
			Supported:           true,
			RequiredFor:         []string{"create_media_buy"},
			CoversContentDigest: "forbidden",
		}
	}

	// Fallback CoverContentDigest=true; capability="forbidden" overrides.
	client, err := NewSignedHTTPClient(SignedHTTPClientOptions{
		KeyID:              "kid",
		PrivateKey:         priv,
		Inner:              inner,
		CoverContentDigest: true,
		CapabilityProvider: provider,
	})
	require.NoError(t, err)

	req, err := http.NewRequest("POST", "https://example.com/adcp/create_media_buy", strings.NewReader(`{}`))
	require.NoError(t, err)
	resp, err := client.Do(req)
	require.NoError(t, err)
	resp.Body.Close()

	got := <-captured
	sigInput := got.Header.Get("Signature-Input")
	require.NotEmpty(t, sigInput)
	assert.NotContains(t, sigInput, "content-digest", "covers='forbidden' ⇒ digest must NOT be covered")
}

func TestCapabilityProviderSkipsWhenSupportedFalse(t *testing.T) {
	_, priv, err := ed25519.GenerateKey(nil)
	require.NoError(t, err)

	captured := make(chan *http.Request, 1)
	inner := roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		captured <- r
		return &http.Response{StatusCode: http.StatusOK, Body: http.NoBody}, nil
	})

	// Even with required_for populated, supported=false means treat as
	// unsupported (matches verifier-side and Python operation_needs_signing).
	provider := func(*http.Request) *adcp.RequestSigningCapabilities {
		return &adcp.RequestSigningCapabilities{
			Supported:   false,
			RequiredFor: []string{"create_media_buy"},
		}
	}

	client, err := NewSignedHTTPClient(SignedHTTPClientOptions{
		KeyID:              "kid",
		PrivateKey:         priv,
		Inner:              inner,
		CapabilityProvider: provider,
	})
	require.NoError(t, err)

	req, err := http.NewRequest("POST", "https://example.com/adcp/create_media_buy", strings.NewReader(`{}`))
	require.NoError(t, err)
	resp, err := client.Do(req)
	require.NoError(t, err)
	resp.Body.Close()

	got := <-captured
	assert.Empty(t, got.Header.Get("Signature"), "supported=false ⇒ skip signing")
}

func TestMiddlewareDefaultsReplayStoreToInMemory(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(nil)
	require.NoError(t, err)

	jwk := &JWK{
		Kid:     "kid",
		Kty:     "OKP",
		Crv:     "Ed25519",
		Alg:     "EdDSA",
		Use:     "sig",
		KeyOps:  []string{"verify"},
		AdcpUse: "request-signing",
		X:       b64UrlEncodeRaw(pub),
	}
	resolver := NewStaticJWKSResolver()
	resolver.Put("kid", jwk, "https://buyer.example.com")

	// No Replay supplied → middleware must default to in-memory.
	mw := Middleware(MiddlewareOptions{
		Resolver:          resolver,
		OperationResolver: func(r *http.Request) string { return "create_media_buy" },
		RequiredFor:       []string{"create_media_buy"},
	})
	handler := mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	srv := httptest.NewServer(handler)
	defer srv.Close()

	signer, err := NewSigner(SignerOptions{KeyID: "kid", PrivateKey: priv})
	require.NoError(t, err)

	signAndSend := func() *http.Response {
		body := strings.NewReader(`{"plan_id":"p1"}`)
		req, err := http.NewRequest("POST", srv.URL+"/adcp/create_media_buy", body)
		require.NoError(t, err)
		req.Header.Set("Content-Type", "application/json")
		require.NoError(t, signer.SignRequest(req, SignOptions{CoverContentDigest: true}))
		resp, err := srv.Client().Do(req)
		require.NoError(t, err)
		return resp
	}

	// First signed request: 200.
	resp := signAndSend()
	resp.Body.Close()
	assert.Equal(t, http.StatusOK, resp.StatusCode)

	// Each new request gets a fresh nonce, so two distinct signed requests
	// both succeed — proves the default store accepts new (kid, nonce) pairs.
	resp = signAndSend()
	resp.Body.Close()
	assert.Equal(t, http.StatusOK, resp.StatusCode)
}

func TestMiddlewareDefaultReplayStoreRejectsActualReplay(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(nil)
	require.NoError(t, err)

	jwk := &JWK{
		Kid:     "kid",
		Kty:     "OKP",
		Crv:     "Ed25519",
		Alg:     "EdDSA",
		Use:     "sig",
		KeyOps:  []string{"verify"},
		AdcpUse: "request-signing",
		X:       b64UrlEncodeRaw(pub),
	}
	resolver := NewStaticJWKSResolver()
	resolver.Put("kid", jwk, "https://buyer.example.com")

	mw := Middleware(MiddlewareOptions{
		Resolver:          resolver,
		OperationResolver: func(r *http.Request) string { return "create_media_buy" },
		RequiredFor:       []string{"create_media_buy"},
	})
	handler := mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	srv := httptest.NewServer(handler)
	defer srv.Close()

	signer, err := NewSigner(SignerOptions{KeyID: "kid", PrivateKey: priv})
	require.NoError(t, err)

	// Sign once, then replay the exact wire form: same headers (which carry
	// Signature / Signature-Input / Content-Digest) and same body.
	bodyBytes := []byte(`{"plan_id":"p1"}`)
	tmpl, err := http.NewRequest("POST", srv.URL+"/adcp/create_media_buy", bytes.NewReader(bodyBytes))
	require.NoError(t, err)
	tmpl.Header.Set("Content-Type", "application/json")
	require.NoError(t, signer.SignRequest(tmpl, SignOptions{CoverContentDigest: true}))

	signedHeaders := tmpl.Header.Clone()
	build := func() *http.Request {
		r, err := http.NewRequest("POST", srv.URL+"/adcp/create_media_buy", bytes.NewReader(bodyBytes))
		require.NoError(t, err)
		for k, vs := range signedHeaders {
			for _, v := range vs {
				r.Header.Add(k, v)
			}
		}
		return r
	}

	// First → 200.
	resp1, err := srv.Client().Do(build())
	require.NoError(t, err)
	resp1.Body.Close()
	assert.Equal(t, http.StatusOK, resp1.StatusCode)

	// Same (kid, nonce, body) → the default in-memory store must reject.
	resp2, err := srv.Client().Do(build())
	require.NoError(t, err)
	resp2.Body.Close()
	assert.Equal(t, http.StatusUnauthorized, resp2.StatusCode)
	assert.Contains(t, resp2.Header.Get("WWW-Authenticate"), `error="request_signature_replayed"`)
}
