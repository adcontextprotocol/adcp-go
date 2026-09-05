package signing

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// recordingHandler is a minimal slog.Handler that captures every record it
// receives, so tests can assert on log level and attributes without parsing
// text/JSON output. Safe for concurrent use.
type recordingHandler struct {
	mu      sync.Mutex
	records []slog.Record
}

func (h *recordingHandler) Enabled(context.Context, slog.Level) bool { return true }

func (h *recordingHandler) Handle(_ context.Context, r slog.Record) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.records = append(h.records, r)
	return nil
}

func (h *recordingHandler) WithAttrs([]slog.Attr) slog.Handler { return h }
func (h *recordingHandler) WithGroup(string) slog.Handler      { return h }

// last returns the most recently handled record, or the zero Record if none
// were captured.
func (h *recordingHandler) last() slog.Record {
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.records) == 0 {
		return slog.Record{}
	}
	return h.records[len(h.records)-1]
}

// attr returns the string value of attribute key on record r, or "" if
// absent.
func recordAttr(r slog.Record, key string) string {
	var v string
	r.Attrs(func(a slog.Attr) bool {
		if a.Key == key {
			v = a.Value.String()
			return false
		}
		return true
	})
	return v
}

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

// newObserveOnlyTestPair builds a signer + resolver pair for the ObserveOnly
// tests below, mirroring the boilerplate TestMiddlewareEndToEndSignAndVerify
// hand-rolls — exactly the pattern issue #53's signingtest package exists to
// collapse for consumers outside this file.
func newObserveOnlyTestPair(t *testing.T, kid string) (*Signer, *StaticJWKSResolver) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(nil)
	require.NoError(t, err)

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

	signer, err := NewSigner(SignerOptions{KeyID: kid, PrivateKey: priv})
	require.NoError(t, err)
	return signer, resolver
}

// TestMiddlewareObserveOnlyAllowsUnsignedRequiredOp confirms the spec's
// warn_for behavior: an unsigned request to an operation that would normally
// be rejected under RequiredFor instead reaches the handler when
// ObserveOnly is set, with no VerifiedSigner attached, and the failure is
// logged at INFO (not the usual WARN) with observe_only=true.
func TestMiddlewareObserveOnlyAllowsUnsignedRequiredOp(t *testing.T) {
	h := &recordingHandler{}
	called := false
	var gotSigner *VerifiedSigner
	mw := Middleware(MiddlewareOptions{
		Resolver:          NewStaticJWKSResolver(),
		Replay:            NewMemoryReplayStore(0),
		OperationResolver: func(r *http.Request) string { return "create_media_buy" },
		RequiredFor:       []string{"create_media_buy"},
		ObserveOnly:       true,
		Logger:            slog.New(h),
	})
	handler := mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		gotSigner = VerifiedSignerFromContext(r.Context())
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest("POST", "https://seller.example.com/adcp/create_media_buy", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	assert.True(t, called, "handler must run under ObserveOnly")
	assert.Nil(t, gotSigner, "an unsigned request must not establish a VerifiedSigner, even under ObserveOnly")

	rec := h.last()
	assert.Equal(t, slog.LevelInfo, rec.Level)
	assert.Equal(t, string(CodeRequired), recordAttr(rec, "code"))
	assert.Equal(t, "true", recordAttr(rec, "observe_only"))
}

// TestMiddlewareObserveOnlyAllowsBadSignature confirms a well-formed but
// cryptographically invalid signature passes through under ObserveOnly
// (logged at INFO, no VerifiedSigner), and confirms the same request is
// rejected with 401 when ObserveOnly is false — the existing, unchanged
// behavior.
func TestMiddlewareObserveOnlyAllowsBadSignature(t *testing.T) {
	newRequest := func(t *testing.T, signer *Signer) *http.Request {
		t.Helper()
		req, err := http.NewRequest("POST", "https://seller.example.com/adcp/create_media_buy", nil)
		require.NoError(t, err)
		require.NoError(t, signer.SignRequest(req, SignOptions{}))
		// Corrupt the signature bytes while keeping the header well-formed
		// (still `sig1=:<base64url>:`) — a well-formed pair that fails
		// crypto verification, not a malformed pair.
		sigHdr := req.Header.Get("Signature")
		start := strings.Index(sigHdr, ":") + 1
		end := strings.LastIndex(sigHdr, ":")
		require.Greater(t, end, start)
		raw, err := base64.RawURLEncoding.DecodeString(sigHdr[start:end])
		require.NoError(t, err)
		raw[0] ^= 0xFF
		req.Header.Set("Signature", "sig1=:"+base64.RawURLEncoding.EncodeToString(raw)+":")
		return req
	}

	t.Run("ObserveOnly=true passes through", func(t *testing.T) {
		signer, resolver := newObserveOnlyTestPair(t, "bad-sig-kid-observe")
		h := &recordingHandler{}
		called := false
		var gotSigner *VerifiedSigner
		mw := Middleware(MiddlewareOptions{
			Resolver:          resolver,
			Replay:            NewMemoryReplayStore(0),
			OperationResolver: func(r *http.Request) string { return "create_media_buy" },
			ObserveOnly:       true,
			Logger:            slog.New(h),
		})
		handler := mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			called = true
			gotSigner = VerifiedSignerFromContext(r.Context())
			w.WriteHeader(http.StatusOK)
		}))

		w := httptest.NewRecorder()
		handler.ServeHTTP(w, newRequest(t, signer))

		assert.Equal(t, http.StatusOK, w.Code)
		assert.True(t, called)
		assert.Nil(t, gotSigner)

		rec := h.last()
		assert.Equal(t, slog.LevelInfo, rec.Level)
		assert.Equal(t, string(CodeInvalid), recordAttr(rec, "code"))
	})

	t.Run("ObserveOnly=false rejects (unchanged behavior)", func(t *testing.T) {
		signer, resolver := newObserveOnlyTestPair(t, "bad-sig-kid-reject")
		called := false
		mw := Middleware(MiddlewareOptions{
			Resolver:          resolver,
			Replay:            NewMemoryReplayStore(0),
			OperationResolver: func(r *http.Request) string { return "create_media_buy" },
		})
		handler := mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			called = true
			w.WriteHeader(http.StatusOK)
		}))

		w := httptest.NewRecorder()
		handler.ServeHTTP(w, newRequest(t, signer))

		assert.Equal(t, http.StatusUnauthorized, w.Code)
		assert.False(t, called)
		assert.Equal(t, `Signature error="request_signature_invalid"`, w.Header().Get("WWW-Authenticate"))
	})
}

// TestMiddlewareObserveOnlyStillHardRejectsMalformedPair confirms the one
// carve-out documented on MiddlewareOptions.ObserveOnly: a partial
// Signature/Signature-Input header pair still hard-rejects with 401 even
// under ObserveOnly, per the spec's rollout-pattern rule that such a pair
// "cannot be safely interpreted as either signed or unsigned traffic."
func TestMiddlewareObserveOnlyStillHardRejectsMalformedPair(t *testing.T) {
	h := &recordingHandler{}
	called := false
	mw := Middleware(MiddlewareOptions{
		Resolver:          NewStaticJWKSResolver(),
		Replay:            NewMemoryReplayStore(0),
		OperationResolver: func(r *http.Request) string { return "create_media_buy" },
		ObserveOnly:       true,
		Logger:            slog.New(h),
	})
	handler := mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	}))

	// Signature present, Signature-Input absent: a broken pair, not "unsigned".
	req := httptest.NewRequest("POST", "https://seller.example.com/adcp/create_media_buy", nil)
	req.Header.Set("Signature", "sig1=:AAAA:")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	assert.Equal(t, http.StatusUnauthorized, w.Code)
	assert.False(t, called, "a malformed header pair must never reach the handler, even under ObserveOnly")
	assert.Equal(t, `Signature error="request_signature_header_malformed"`, w.Header().Get("WWW-Authenticate"))

	rec := h.last()
	assert.Equal(t, slog.LevelWarn, rec.Level, "malformed-pair rejection logs at the normal WARN level, not INFO")
	assert.Equal(t, "false", recordAttr(rec, "observe_only"))
}
