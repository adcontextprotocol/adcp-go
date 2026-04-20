package webhook

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/adcontextprotocol/adcp-go/adcp"
	"github.com/adcontextprotocol/adcp-go/adcp/idempotency"
	"github.com/adcontextprotocol/adcp-go/adcp/signing"
)

// webhookKeypair builds a ProfileWebhookSigning key published in a static
// JWKS resolver. Returns the signer, the resolver keyed with the JWK, and
// the keyid the resolver will match.
func webhookKeypair(t *testing.T, keyid string) (*signing.Signer, *signing.StaticJWKSResolver) {
	t.Helper()
	res, err := signing.GenerateKeyForProfile(signing.AlgEd25519, keyid, signing.ProfileWebhookSigning)
	require.NoError(t, err)
	require.Equal(t, "webhook-signing", res.PublicJWK.AdcpUse)
	priv, _, err := signing.LoadPrivateKey(res.PrivateKeyPEM)
	require.NoError(t, err)

	signer, err := NewSigner(signing.SignerOptions{
		KeyID:      keyid,
		PrivateKey: priv,
	})
	require.NoError(t, err)

	resolver := signing.NewStaticJWKSResolver()
	resolver.Put(keyid, &res.PublicJWK, "https://publisher.example.com")
	return signer, resolver
}

func newVerifyingHTTPHandler(t *testing.T, keyid string, resolver signing.JWKSResolver, h Handler) http.Handler {
	t.Helper()
	backend := idempotency.NewMemoryBackend(0)
	t.Cleanup(backend.Close)
	return HTTPHandler(HTTPHandlerOptions{
		Store:   NewStore(Options{Backend: backend, TTL: time.Hour}),
		Handler: h,
		Verification: &VerificationOptions{
			Resolver: resolver,
			Replay:   signing.NewMemoryReplayStore(0),
		},
	})
}

func TestSignVerifyRoundtrip(t *testing.T) {
	keyid := "test-webhook-ed25519"
	signer, resolver := webhookKeypair(t, keyid)

	var received []byte
	handler := newVerifyingHTTPHandler(t, keyid, resolver, func(_ context.Context, body []byte) error {
		received = append([]byte(nil), body...)
		return nil
	})

	p := &adcp.MCPWebhookPayload{
		TaskID:    "task_1",
		TaskType:  "create_media_buy",
		Status:    "completed",
		Timestamp: "2026-04-19T00:00:00Z",
	}
	body, key, err := Marshal(p)
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodPost, "https://subscriber.example.com/webhook", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	require.NoError(t, signer.SignRequest(req, signing.SignOptions{CoverContentDigest: true}))

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, body, received, "handler must see the exact bytes the sender signed")
	require.NoError(t, idempotency.Validate(key))
}

func TestCrossProfileSignatureRejected(t *testing.T) {
	// A signer configured for ProfileRequestSigning must not be accepted by
	// a webhook endpoint, and vice versa. The distinct tag values
	// (request-signing/v1 vs webhook-signing/v1) make each profile's
	// signature unusable against the other's verifier.
	keyid := "test-request-signer"
	res, err := signing.GenerateKeyForProfile(signing.AlgEd25519, keyid, signing.ProfileRequestSigning)
	require.NoError(t, err)
	priv, _, err := signing.LoadPrivateKey(res.PrivateKeyPEM)
	require.NoError(t, err)
	reqSigner, err := signing.NewSigner(signing.SignerOptions{
		KeyID:      keyid,
		PrivateKey: priv,
		// Explicit for clarity; zero value is already ProfileRequestSigning.
		Profile: signing.ProfileRequestSigning,
	})
	require.NoError(t, err)

	resolver := signing.NewStaticJWKSResolver()
	resolver.Put(keyid, &res.PublicJWK, "https://publisher.example.com")

	handler := newVerifyingHTTPHandler(t, keyid, resolver, func(_ context.Context, _ []byte) error { return nil })

	body := []byte(`{"idempotency_key":"` + testKey + `","task_id":"t","task_type":"x","status":"s","timestamp":"2026-04-19T00:00:00Z"}`)
	req := httptest.NewRequest(http.MethodPost, "https://subscriber.example.com/webhook", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	require.NoError(t, reqSigner.SignRequest(req, signing.SignOptions{CoverContentDigest: true}))

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusUnauthorized, rec.Code)

	// WWW-Authenticate emits the webhook_signature_* prefix per PR #2423.
	assert.Contains(t, rec.Header().Get("WWW-Authenticate"), "webhook_signature_")
}

func TestKeyScopedForWrongProfileRejected(t *testing.T) {
	// A key published with adcp_use="request-signing" cannot verify a webhook
	// signature even when the signer presents the webhook-signing tag.
	keyid := "test-gov-key"
	res, err := signing.GenerateKeyForProfile(signing.AlgEd25519, keyid, signing.ProfileRequestSigning)
	require.NoError(t, err)
	priv, _, err := signing.LoadPrivateKey(res.PrivateKeyPEM)
	require.NoError(t, err)

	// Force the signer to emit webhook-signing tag even though the published
	// key is scoped for request-signing — this simulates a misconfigured
	// publisher reusing a request-signing key for webhooks.
	signer, err := signing.NewSigner(signing.SignerOptions{
		KeyID:      keyid,
		PrivateKey: priv,
		Profile:    signing.ProfileWebhookSigning,
	})
	require.NoError(t, err)

	resolver := signing.NewStaticJWKSResolver()
	resolver.Put(keyid, &res.PublicJWK, "https://publisher.example.com")

	handler := newVerifyingHTTPHandler(t, keyid, resolver, func(_ context.Context, _ []byte) error { return nil })

	body := []byte(`{"idempotency_key":"` + testKey + `","task_id":"t","task_type":"x","status":"s","timestamp":"2026-04-19T00:00:00Z"}`)
	req := httptest.NewRequest(http.MethodPost, "https://subscriber.example.com/webhook", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	require.NoError(t, signer.SignRequest(req, signing.SignOptions{CoverContentDigest: true}))

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusUnauthorized, rec.Code)
	assert.Contains(t, rec.Header().Get("WWW-Authenticate"), "webhook_signature_key_purpose_invalid")
}

func TestUnsignedWebhookRejected(t *testing.T) {
	keyid := "test-webhook-ed25519"
	_, resolver := webhookKeypair(t, keyid)
	handler := newVerifyingHTTPHandler(t, keyid, resolver, func(_ context.Context, _ []byte) error { return nil })

	body := []byte(`{"idempotency_key":"` + testKey + `","task_id":"t","task_type":"x","status":"s","timestamp":"2026-04-19T00:00:00Z"}`)
	req := httptest.NewRequest(http.MethodPost, "https://subscriber.example.com/webhook", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusUnauthorized, rec.Code)
	assert.Contains(t, rec.Header().Get("WWW-Authenticate"), "webhook_signature_required")
}

func TestContentDigestRequired(t *testing.T) {
	// Signing without CoverContentDigest must be rejected — spec #2423 makes
	// content-digest coverage mandatory for webhooks.
	keyid := "test-webhook-ed25519"
	signer, resolver := webhookKeypair(t, keyid)
	handler := newVerifyingHTTPHandler(t, keyid, resolver, func(_ context.Context, _ []byte) error { return nil })

	body := []byte(`{"idempotency_key":"` + testKey + `","task_id":"t","task_type":"x","status":"s","timestamp":"2026-04-19T00:00:00Z"}`)
	req := httptest.NewRequest(http.MethodPost, "https://subscriber.example.com/webhook", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	require.NoError(t, signer.SignRequest(req, signing.SignOptions{CoverContentDigest: false}))

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusUnauthorized, rec.Code)
	assert.Contains(t, rec.Header().Get("WWW-Authenticate"), "webhook_signature_components_incomplete")
}

func TestErrorWireCodeMapping(t *testing.T) {
	e := &signing.Error{Code: signing.CodeTagInvalid, Detail: "x"}
	assert.Equal(t, "request_signature_tag_invalid", e.WireCode(signing.ProfileRequestSigning))
	assert.Equal(t, "webhook_signature_tag_invalid", e.WireCode(signing.ProfileWebhookSigning))
	// Zero Profile falls back to request-signing.
	assert.Equal(t, "request_signature_tag_invalid", e.WireCode(signing.Profile{}))
}

func TestNewSignerForcesWebhookProfile(t *testing.T) {
	res, err := signing.GenerateKeyForProfile(signing.AlgEd25519, "kid", signing.ProfileWebhookSigning)
	require.NoError(t, err)
	priv, _, err := signing.LoadPrivateKey(res.PrivateKeyPEM)
	require.NoError(t, err)

	// Caller asks for ProfileRequestSigning, but webhook.NewSigner overrides.
	signer, err := NewSigner(signing.SignerOptions{
		KeyID:      "kid",
		PrivateKey: priv,
		Profile:    signing.ProfileRequestSigning,
	})
	require.NoError(t, err)

	body := []byte(`{}`)
	req := httptest.NewRequest(http.MethodPost, "https://subscriber.example.com/x", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	require.NoError(t, signer.SignRequest(req, signing.SignOptions{CoverContentDigest: true}))
	assert.Contains(t, req.Header.Get("Signature-Input"), `tag="adcp/webhook-signing/v1"`)
}
