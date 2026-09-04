// Package signingtest collapses the boilerplate of standing up a matched
// signer + verifier pair for tests of handlers that expect AdCP
// request-signing (RFC 9421) traffic.
//
// Writing that pair by hand means generating a keypair, building a JWK with
// the five fields the profile requires, wiring a signing.StaticJWKSResolver,
// and constructing a signing.NewMemoryReplayStore — roughly 30 lines
// reverse-engineered from adcp/v3/signing's own middleware_test.go every time
// a consumer needs it. NewTestAgent does that wiring once; SignAndSend is the
// one-liner for "send a signed request to this handler."
//
//	func TestCreateMediaBuyRequiresSignature(t *testing.T) {
//	    signer, opts := signingtest.NewTestAgent(t)
//	    opts.OperationResolver = signing.DefaultOperationResolver
//	    opts.RequiredFor = []string{"create_media_buy"}
//	    handler := signing.Middleware(opts)(yourHandler)
//
//	    req := httptest.NewRequest(http.MethodPost,
//	        "https://seller.example.com/adcp/create_media_buy",
//	        strings.NewReader(`{"plan_id":"p1"}`))
//	    req.Header.Set("Content-Type", "application/json")
//
//	    resp := signingtest.SignAndSend(t, signer, handler, req)
//	    if resp.StatusCode != http.StatusOK {
//	        t.Fatalf("got %d", resp.StatusCode)
//	    }
//	}
//
// This package imports "testing" and is intended for use from _test.go
// files only.
package signingtest

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/adcontextprotocol/adcp-go/adcp/v3/signing"
)

// testAgentURL is the agent URL published against the generated key in the
// resolver NewTestAgent builds. Callers that assert on
// signing.VerifiedSigner.AgentURL can compare against this constant.
const testAgentURL = "https://signingtest.invalid/agent"

// NewTestAgent generates a fresh Ed25519 keypair and returns a Signer built
// from it (for constructing outbound signed requests) alongside
// MiddlewareOptions pre-wired to verify signatures from that same key:
//
//   - Resolver: a signing.StaticJWKSResolver containing the signer's public
//     JWK under a kid derived from the test name.
//   - Replay: a fresh signing.NewMemoryReplayStore(0).
//   - Revocation: nil (no revocation checking — the middleware logs a
//     warning about this only when RequiredFor is non-empty, matching
//     signing.Middleware's own dev/test posture).
//
// The returned MiddlewareOptions has no OperationResolver or RequiredFor set;
// callers wire those (e.g. signing.DefaultOperationResolver) when the test
// needs to exercise the RequiredFor / ObserveOnly gating rather than just a
// bare verified round trip.
//
// Each call to NewTestAgent produces an independent keypair and resolver, so
// concurrent subtests (t.Run with t.Parallel) do not share replay or key
// state.
func NewTestAgent(t *testing.T) (*signing.Signer, signing.MiddlewareOptions) {
	t.Helper()

	kid := "signingtest-" + sanitizeKid(t.Name())
	res, err := signing.GenerateSigningKey(signing.AlgEd25519, kid)
	if err != nil {
		t.Fatalf("signingtest: generate signing key: %v", err)
	}
	priv, _, err := signing.LoadPrivateKey(res.PrivateKeyPEM)
	if err != nil {
		t.Fatalf("signingtest: load generated private key: %v", err)
	}
	signer, err := signing.NewSigner(signing.SignerOptions{
		KeyID:      kid,
		PrivateKey: priv,
	})
	if err != nil {
		t.Fatalf("signingtest: construct signer: %v", err)
	}

	resolver := signing.NewStaticJWKSResolver()
	resolver.Put(kid, &res.PublicJWK, testAgentURL)

	opts := signing.MiddlewareOptions{
		Resolver:   resolver,
		Replay:     signing.NewMemoryReplayStore(0),
		Revocation: nil,
	}
	return signer, opts
}

// SignAndSend signs req with signer (covering content-digest, per the AdCP
// recommendation for spend-committing operations — see the signing package
// README's "Caveats" section) and delivers it directly to handler via
// httptest.NewRecorder, returning the resulting *http.Response.
//
// req.URL must be absolute (e.g. constructed with
// httptest.NewRequest(method, "https://seller.example.com/adcp/op", body) or
// http.NewRequest with a full URL) — SignRequest needs an absolute
// @target-uri to sign, and serving the request in-process (rather than over
// a real listener) means the handler sees exactly the URL that was signed,
// with no scheme/host rewriting to account for.
func SignAndSend(t *testing.T, signer *signing.Signer, handler http.Handler, req *http.Request) *http.Response {
	t.Helper()

	if req.URL == nil || !req.URL.IsAbs() {
		t.Fatalf("signingtest: SignAndSend requires req.URL to be absolute (e.g. https://seller.example.com/adcp/create_media_buy), got %q", req.URL)
	}

	if err := signer.SignRequest(req, signing.SignOptions{CoverContentDigest: true}); err != nil {
		t.Fatalf("signingtest: sign request: %v", err)
	}

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	return rec.Result()
}

// sanitizeKid maps a *testing.T name (which may contain '/' from subtests
// and spaces from t.Run names built with fmt.Sprintf) to characters safe for
// a JWK kid and an RFC 9421 quoted-string sig-param.
func sanitizeKid(name string) string {
	var b strings.Builder
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
			b.WriteRune(r)
		default:
			b.WriteByte('-')
		}
	}
	if b.Len() == 0 {
		return "t"
	}
	return b.String()
}
