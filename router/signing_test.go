package router

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/adcontextprotocol/adcp-go/tmproto"
)

func newSignedTestRouter(t *testing.T, providers []ProviderConfig) (*Router, *tmproto.Signer, *tmproto.StaticKeyStore) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	signer, err := tmproto.NewSigner("router-test-key", priv)
	require.NoError(t, err)
	r := testRouter(providers)
	r.signer = signer
	ks := tmproto.NewStaticKeyStore([]tmproto.SigningKey{tmproto.PublicSigningKey(signer.KeyID, pub)})
	return r, signer, ks
}

func TestRouter_SignsContextMatchFanOut(t *testing.T) {
	var receivedSig, receivedKid atomic.Value
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedSig.Store(r.Header.Get(tmproto.HeaderTMPSignature))
		receivedKid.Store(r.Header.Get(tmproto.HeaderTMPKeyID))
		_ = json.NewEncoder(w).Encode(tmproto.ContextMatchResponse{RequestID: "ctx-sign"})
	}))
	defer provider.Close()

	router, signer, ks := newSignedTestRouter(t, []ProviderConfig{
		{ID: "p1", Endpoint: provider.URL, ContextMatch: true, Timeout: 5 * time.Second},
	})

	body := `{
		"type":"context_match_request",
		"request_id":"ctx-sign",
		"property_id":"pub",
		"property_rid":"00000000-0000-0000-0000-000000000001",
		"property_type":"website",
		"placement_id":"sb",
		"seller_agent_url":"https://seller.example.com/agent",
		"package_ids":["pkg-a"]
	}`
	w := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/tmp/context", strings.NewReader(body))
	router.HandleContextMatch(w, req)
	require.Equal(t, 200, w.Code, w.Body.String())

	sig, _ := receivedSig.Load().(string)
	kid, _ := receivedKid.Load().(string)
	require.NotEmpty(t, sig, "X-AdCP-Signature must be set on fan-out")
	require.Equal(t, signer.KeyID, kid, "X-AdCP-Key-Id must match signer")

	// Independently verify the received signature against the body the
	// provider would have parsed.
	parsed := &tmproto.ContextMatchRequest{
		RequestID:      "ctx-sign",
		PropertyID:     "pub",
		PropertyRID:    "00000000-0000-0000-0000-000000000001",
		PropertyType:   "website",
		PlacementID:    "sb",
		SellerAgentURL: "https://seller.example.com/agent",
		PackageIDs:     []string{"pkg-a"},
	}
	require.NoError(t, tmproto.VerifyContextMatch(parsed, provider.URL, sig, kid, ks, time.Now()))
}

func TestRouter_SignsIdentityMatchPerProvider(t *testing.T) {
	type capture struct {
		sig string
		kid string
	}
	var capA, capB atomic.Value

	mkProvider := func(slot *atomic.Value) *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			slot.Store(capture{
				sig: r.Header.Get(tmproto.HeaderTMPSignature),
				kid: r.Header.Get(tmproto.HeaderTMPKeyID),
			})
			_ = json.NewEncoder(w).Encode(tmproto.IdentityMatchResponse{
				RequestID:          "id-sign",
				EligiblePackageIDs: []string{"pkg"},
				ServeWindowSec:     60,
			})
		}))
	}
	provA := mkProvider(&capA)
	defer provA.Close()
	provB := mkProvider(&capB)
	defer provB.Close()

	router, _, _ := newSignedTestRouter(t, []ProviderConfig{
		{ID: "a", Endpoint: provA.URL, IdentityMatch: true, Countries: []string{"US"}, UIDTypes: []string{"uid2"}, Timeout: 5 * time.Second},
		{ID: "b", Endpoint: provB.URL, IdentityMatch: true, Countries: []string{"US"}, UIDTypes: []string{"uid2"}, Timeout: 5 * time.Second},
	})

	body := `{
		"type":"identity_match_request",
		"request_id":"id-sign",
		"seller_agent_url":"https://seller.example.com/agent",
		"identities":[{"user_token":"tok","uid_type":"uid2"}],
		"package_ids":["pkg"],
		"country":"US"
	}`
	w := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/tmp/identity", strings.NewReader(body))
	router.HandleIdentityMatch(w, req)
	require.Equal(t, 200, w.Code, w.Body.String())

	a, ok := capA.Load().(capture)
	require.True(t, ok, "provider A did not receive a request")
	b, ok := capB.Load().(capture)
	require.True(t, ok, "provider B did not receive a request")

	require.NotEmpty(t, a.sig)
	require.NotEmpty(t, b.sig)
	// Per-provider binding — different provider_endpoint_url means different
	// signing inputs means different signatures.
	assert.NotEqual(t, a.sig, b.sig, "identity-match signatures must be per-provider")
}

func TestRouter_NoSigner_DoesNotSetHeaders(t *testing.T) {
	var sawSig atomic.Value
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sawSig.Store(r.Header.Get(tmproto.HeaderTMPSignature))
		_ = json.NewEncoder(w).Encode(tmproto.ContextMatchResponse{RequestID: "x"})
	}))
	defer provider.Close()

	router := testRouter([]ProviderConfig{
		{ID: "p1", Endpoint: provider.URL, ContextMatch: true, Timeout: 5 * time.Second},
	})
	body := `{"type":"context_match_request","request_id":"x","property_id":"p","property_type":"website","placement_id":"s","seller_agent_url":"https://seller.example.com/agent","package_ids":["a"]}`
	w := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/tmp/context", strings.NewReader(body))
	router.HandleContextMatch(w, req)
	got, _ := sawSig.Load().(string)
	require.Empty(t, got, "without signer, no signature header should be attached")
}

func TestContextSignatureCache_ReusesAcrossEpoch(t *testing.T) {
	// Same (placement, endpoint, epoch) → second call returns cached signature
	// without re-invoking the underlying signer. We assert by comparing strings
	// (Ed25519 is deterministic so the cache hit can't be detected by output
	// alone) — test the cache directly via its API.
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	_ = pub
	require.NoError(t, err)
	signer, err := tmproto.NewSigner("kid", priv)
	require.NoError(t, err)
	cache := newContextSignatureCache(8)
	req := &tmproto.ContextMatchRequest{
		RequestID:   "r",
		PropertyRID: "rid",
		PlacementID: "sb",
		PackageIDs:  []string{"pkg"},
	}
	a := cache.signatureFor(signer, req, "https://x", 20000)
	b := cache.signatureFor(signer, req, "https://x", 20000)
	assert.Equal(t, a, b)

	// Different epoch → different signature.
	c := cache.signatureFor(signer, req, "https://x", 20001)
	assert.NotEqual(t, a, c)
}

func TestContextSignatureCache_DistinctPackageIDsGetDistinctSignatures(t *testing.T) {
	// Two requests on the same (placement, endpoint, epoch) but with
	// different package_ids must NOT share a cached signature — Ed25519
	// binds the signature to the exact signing input, and the cached
	// signature would fail provider-side verification when re-applied
	// to a body containing a different package set.
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	signer, err := tmproto.NewSigner("kid", priv)
	require.NoError(t, err)
	ks := tmproto.NewStaticKeyStore([]tmproto.SigningKey{tmproto.PublicSigningKey(signer.KeyID, pub)})
	cache := newContextSignatureCache(8)

	endpoint := "https://provider.example.com"
	epoch := int64(20000)
	now := time.Unix(epoch*86400+1, 0)

	reqA := &tmproto.ContextMatchRequest{
		RequestID:   "r1",
		PropertyRID: "rid",
		PlacementID: "sb",
		PackageIDs:  []string{"pkg-a", "pkg-b"},
	}
	reqB := &tmproto.ContextMatchRequest{
		RequestID:   "r2",
		PropertyRID: "rid",
		PlacementID: "sb",
		PackageIDs:  []string{"pkg-c"},
	}

	sigA := cache.signatureFor(signer, reqA, endpoint, epoch)
	sigB := cache.signatureFor(signer, reqB, endpoint, epoch)
	assert.NotEqual(t, sigA, sigB, "different package_ids must yield different cache entries")

	require.NoError(t, tmproto.VerifyContextMatch(reqA, endpoint, sigA, signer.KeyID, ks, now), "sigA must verify against reqA's package_ids")
	require.NoError(t, tmproto.VerifyContextMatch(reqB, endpoint, sigB, signer.KeyID, ks, now), "sigB must verify against reqB's package_ids")
	assert.Error(t, tmproto.VerifyContextMatch(reqB, endpoint, sigA, signer.KeyID, ks, now), "sigA must not verify against reqB's package_ids (the cache-poisoning case the key change prevents)")
}

func TestContextSignatureCache_PackageIDOrderShareEntry(t *testing.T) {
	// The signing input sorts package_ids before joining, so two requests
	// with the same package set in different orders MUST share a cache
	// entry — otherwise the cache misses on equivalent inputs.
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	_ = pub
	require.NoError(t, err)
	signer, err := tmproto.NewSigner("kid", priv)
	require.NoError(t, err)
	cache := newContextSignatureCache(8)

	reqA := &tmproto.ContextMatchRequest{
		PlacementID: "sb",
		PackageIDs:  []string{"pkg-a", "pkg-b"},
	}
	reqB := &tmproto.ContextMatchRequest{
		PlacementID: "sb",
		PackageIDs:  []string{"pkg-b", "pkg-a"},
	}
	sigA := cache.signatureFor(signer, reqA, "https://x", 20000)
	sigB := cache.signatureFor(signer, reqB, "https://x", 20000)
	assert.Equal(t, sigA, sigB)
}
