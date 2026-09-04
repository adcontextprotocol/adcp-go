package signingtest_test

import (
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/adcontextprotocol/adcp-go/adcp/v3/signing"
	"github.com/adcontextprotocol/adcp-go/adcp/v3/signing/signingtest"
)

// TestNewTestAgentSignAndSendRoundTrip proves NewTestAgent + SignAndSend
// produce a request a real signed-request-expecting handler accepts, with
// the verified identity available to the handler via
// signing.VerifiedSignerFromContext.
func TestNewTestAgentSignAndSendRoundTrip(t *testing.T) {
	signer, opts := signingtest.NewTestAgent(t)
	opts.OperationResolver = signing.DefaultOperationResolver
	opts.RequiredFor = []string{"create_media_buy"}

	var gotSigner *signing.VerifiedSigner
	handler := signing.Middleware(opts)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotSigner = signing.VerifiedSignerFromContext(r.Context())
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodPost, "https://seller.example.com/adcp/create_media_buy",
		strings.NewReader(`{"plan_id":"p1"}`))
	req.Header.Set("Content-Type", "application/json")

	resp := signingtest.SignAndSend(t, signer, handler, req)
	defer resp.Body.Close() //nolint:errcheck

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if gotSigner == nil {
		t.Fatal("handler saw no VerifiedSigner — signature was not accepted")
	}
	if gotSigner.Algorithm != signing.AlgEd25519 {
		t.Errorf("Algorithm = %q, want %q", gotSigner.Algorithm, signing.AlgEd25519)
	}
}

// TestNewTestAgentRejectsUnsignedWhenRequired confirms the MiddlewareOptions
// NewTestAgent returns wire a real, functioning verifier — not a stub that
// accepts everything — by checking an unsigned request to a RequiredFor
// operation is rejected.
func TestNewTestAgentRejectsUnsignedWhenRequired(t *testing.T) {
	_, opts := signingtest.NewTestAgent(t)
	opts.OperationResolver = signing.DefaultOperationResolver
	opts.RequiredFor = []string{"create_media_buy"}

	called := false
	handler := signing.Middleware(opts)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodPost, "https://seller.example.com/adcp/create_media_buy", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
	if called {
		t.Fatal("handler ran on an unsigned request to a required operation")
	}
}

// TestNewTestAgentReplayIsEnforced confirms the replay store NewTestAgent
// wires is real: replaying the exact same signed request a second time is
// rejected as request_signature_replayed, proving the (keyid, nonce) dedup
// state SignAndSend exercises is not a no-op.
func TestNewTestAgentReplayIsEnforced(t *testing.T) {
	signer, opts := signingtest.NewTestAgent(t)
	opts.OperationResolver = signing.DefaultOperationResolver

	handler := signing.Middleware(opts)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req, err := http.NewRequest(http.MethodPost, "https://seller.example.com/adcp/create_media_buy", nil)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	if err := signer.SignRequest(req, signing.SignOptions{}); err != nil {
		t.Fatalf("sign request: %v", err)
	}

	// First delivery: accepted.
	rec1 := httptest.NewRecorder()
	handler.ServeHTTP(rec1, req.Clone(req.Context()))
	if rec1.Code != http.StatusOK {
		t.Fatalf("first delivery status = %d, want 200", rec1.Code)
	}

	// Replaying the identical Signature/Signature-Input headers must be
	// rejected.
	rec2 := httptest.NewRecorder()
	handler.ServeHTTP(rec2, req.Clone(req.Context()))
	if rec2.Code != http.StatusUnauthorized {
		t.Fatalf("replayed delivery status = %d, want 401", rec2.Code)
	}
	if got := rec2.Header().Get("WWW-Authenticate"); !strings.Contains(got, "request_signature_replayed") {
		t.Errorf("WWW-Authenticate = %q, want it to contain request_signature_replayed", got)
	}
}

// TestSignAndSendRequiresAbsoluteURL confirms SignAndSend fails fast (via
// t.Fatalf) rather than producing a confusing downstream signing error when
// handed a relative request URL.
//
// A t.Fatalf inside a subtest (t.Run) always propagates as a failure of the
// parent test and the whole package run — there is no in-process way to
// observe "the helper correctly called t.Fatalf" without failing this test
// binary's own run. So, like the standard library's own
// os/exec-style TestHelperProcess pattern, the failing call is made in a
// re-exec'd subprocess and its output is asserted on instead.
func TestSignAndSendRequiresAbsoluteURL(t *testing.T) {
	if os.Getenv("SIGNINGTEST_RUN_ABSOLUTE_URL_SUBPROCESS") == "1" {
		signer, _ := signingtest.NewTestAgent(t)
		req := httptest.NewRequest(http.MethodGet, "/adcp/get_products", nil)
		// httptest.NewRequest defaults req.URL to an absolute-path (no
		// scheme/host) URL for a target without one — exactly the case
		// SignAndSend must catch.
		resp := signingtest.SignAndSend(t, signer, http.NotFoundHandler(), req)
		defer resp.Body.Close() //nolint:errcheck
		return
	}

	cmd := exec.Command(os.Args[0], "-test.run=^TestSignAndSendRequiresAbsoluteURL$", "-test.v") //nolint:gosec // re-exec of this same test binary, the standard os/exec TestHelperProcess pattern
	cmd.Env = append(os.Environ(), "SIGNINGTEST_RUN_ABSOLUTE_URL_SUBPROCESS=1")
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("subprocess unexpectedly succeeded (SignAndSend should have failed the test on a relative URL); output:\n%s", out)
	}
	if !strings.Contains(string(out), "requires req.URL to be absolute") {
		t.Fatalf("subprocess failed, but not with the expected message; output:\n%s", out)
	}
}
