package teeattestation_test

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"testing"
	"time"

	tee "github.com/adcontextprotocol/adcp-go/tee-attestation"
	"github.com/adcontextprotocol/adcp-go/tee-attestation/nitro"
)

// TestNitroRoundTrip walks the full emit → verify path against the mock NSM.
// This is the primary evidence that the wire shape proposed in
// adcontextprotocol/adcp#5770 is realizable and self-consistent.
func TestNitroRoundTrip(t *testing.T) {
	ctx := context.Background()

	mock, err := nitro.NewMockNsm()
	if err != nil {
		t.Fatalf("build mock NSM: %v", err)
	}

	pub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("gen router signing key: %v", err)
	}
	signingKey := tee.Ed25519JWK(pub, "router-test-2026-07")

	nonce := make([]byte, 32)
	if _, err := rand.Read(nonce); err != nil {
		t.Fatalf("gen nonce: %v", err)
	}

	env, err := nitro.Emit(ctx, mock, nitro.EmitRequest{
		Nonce:      nonce,
		SigningKey: signingKey,
		ExpiresAt:  time.Now().Add(5 * time.Minute),
	})
	if err != nil {
		t.Fatalf("Emit: %v", err)
	}
	if env.Format != tee.FormatAWSNitroCOSESign1V1 {
		t.Errorf("envelope.format = %q, want %q", env.Format, tee.FormatAWSNitroCOSESign1V1)
	}
	if _, err := env.DocumentBytes(); err != nil {
		t.Errorf("envelope.document not base64url-decodable: %v", err)
	}

	v := &nitro.Verifier{RootCert: mock.RootCert}
	ver, err := v.Verify(env, nonce)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if ver.SigningKey.X != signingKey.X {
		t.Errorf("verified signing key does not match emitted key")
	}
	if len(ver.Measurements.PCRs) == 0 {
		t.Errorf("verified measurements have no PCRs")
	}
}

// TestVerifyRejectsNonceMismatch — verifier-sent nonce differs from the one
// echoed in the envelope. Step 1 of the flow.
func TestVerifyRejectsNonceMismatch(t *testing.T) {
	env, mock, _ := mustEmit(t)
	wrongNonce := make([]byte, 32)
	wrongNonce[0] = 0xFF
	assertFailure(t, mock, env, wrongNonce, tee.FailureNonceMismatch)
}

// TestVerifyRejectsExpiredEnvelope — step 2.
func TestVerifyRejectsExpiredEnvelope(t *testing.T) {
	env, mock, nonce := mustEmit(t)
	env.ExpiresAt = time.Now().Add(-time.Minute)
	assertFailure(t, mock, env, nonce, tee.FailureEnvelopeExpired)
}

// TestVerifyRejectsUnsupportedFormat — step 4.
func TestVerifyRejectsUnsupportedFormat(t *testing.T) {
	env, mock, nonce := mustEmit(t)
	env.Format = tee.FormatIntelTDXQuoteV4
	assertFailure(t, mock, env, nonce, tee.FailureUnsupportedFormat)
}

// TestVerifyRejectsTamperedDocument — step 5. Flip a byte in the COSE_Sign1
// payload; signature verification must fail.
func TestVerifyRejectsTamperedDocument(t *testing.T) {
	env, mock, nonce := mustEmit(t)
	raw, err := base64.RawURLEncoding.DecodeString(env.Document)
	if err != nil {
		t.Fatalf("decode document: %v", err)
	}
	// Flip the last byte — inside the COSE signature. Signature check
	// must catch it.
	raw[len(raw)-1] ^= 0xFF
	env.Document = base64.RawURLEncoding.EncodeToString(raw)
	assertFailure(t, mock, env, nonce, tee.FailurePlatformVerification)
}

// TestVerifyRejectsSwappedSigningKey — step 7, the load-bearing binding
// rule. Attest with one key, then swap envelope.signing_key to a different
// key of the same shape. Verifier must reject.
func TestVerifyRejectsSwappedSigningKey(t *testing.T) {
	env, mock, nonce := mustEmit(t)
	otherPub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("gen swap key: %v", err)
	}
	env.SigningKey = tee.Ed25519JWK(otherPub, "attacker-swapped")
	assertFailure(t, mock, env, nonce, tee.FailureSigningKeyNotBound)
}

// TestVerifyRejectsWrongRoot — the envelope is valid but was minted by a
// mock the verifier doesn't trust. Cert-chain verification must fail
// (step 5, `platform_verification_failed`).
func TestVerifyRejectsWrongRoot(t *testing.T) {
	env, _, nonce := mustEmit(t)
	otherMock, err := nitro.NewMockNsm()
	if err != nil {
		t.Fatalf("build other mock: %v", err)
	}
	v := &nitro.Verifier{RootCert: otherMock.RootCert}
	_, err = v.Verify(env, nonce)
	if err == nil {
		t.Fatal("Verify accepted envelope minted by a different root — should have rejected")
	}
	var ve *tee.VerifyError
	if !errors.As(err, &ve) || ve.Mode != tee.FailurePlatformVerification {
		t.Errorf("expected FailurePlatformVerification, got %v", err)
	}
}

// TestVerifyRejectsPolicyDisallow — step 8. Policy returns an error.
func TestVerifyRejectsPolicyDisallow(t *testing.T) {
	env, mock, nonce := mustEmit(t)
	v := &nitro.Verifier{
		RootCert: mock.RootCert,
		Policy: nitro.PolicyFunc(func(m nitro.Measurements) error {
			return errors.New("test policy rejects everything")
		}),
	}
	_, err := v.Verify(env, nonce)
	if err == nil {
		t.Fatal("Verify accepted envelope despite policy rejection")
	}
	var ve *tee.VerifyError
	if !errors.As(err, &ve) || ve.Mode != tee.FailureMeasurementDisallowed {
		t.Errorf("expected FailureMeasurementDisallowed, got %v", err)
	}
}

// TestVerifyAcceptsPerRequestPathWithoutNonceEcho — the per-request
// X-TMP-Attestation code path calls Verify with expectedNonce=nil. Step 1
// is skipped; freshness comes from expiry only. Same envelope should still
// verify.
func TestVerifyAcceptsPerRequestPathWithoutNonceEcho(t *testing.T) {
	env, mock, _ := mustEmit(t)
	v := &nitro.Verifier{RootCert: mock.RootCert}
	if _, err := v.Verify(env, nil); err != nil {
		t.Fatalf("Verify (per-request path) rejected a valid envelope: %v", err)
	}
}

// TestEnvelopeJSONRoundTrip — the on-wire JSON survives a round-trip
// through encoding/json without losing fields the verifier relies on.
func TestEnvelopeJSONRoundTrip(t *testing.T) {
	env, _, _ := mustEmit(t)
	body, err := json.Marshal(env)
	if err != nil {
		t.Fatalf("marshal envelope: %v", err)
	}
	var back tee.Envelope
	if err := json.Unmarshal(body, &back); err != nil {
		t.Fatalf("unmarshal envelope: %v", err)
	}
	if back.Format != env.Format {
		t.Errorf("format changed on round-trip: %q vs %q", back.Format, env.Format)
	}
	if back.Document != env.Document {
		t.Errorf("document changed on round-trip")
	}
	if back.Nonce != env.Nonce {
		t.Errorf("nonce changed on round-trip")
	}
	if back.SigningKey.X != env.SigningKey.X {
		t.Errorf("signing key changed on round-trip")
	}
}

// TestJWKThumbprintStable — RFC 7638 thumbprint of the same Ed25519 pubkey
// is stable across constructions with different Kid / Alg / Use metadata.
// The thumbprint excludes those fields by spec.
func TestJWKThumbprintStable(t *testing.T) {
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("gen key: %v", err)
	}
	a := tee.Ed25519JWK(pub, "kid-a")
	b := tee.Ed25519JWK(pub, "kid-b")
	b.Alg = "different-alg-marker"
	b.Use = "enc" // any non-required metadata
	aTP, err := a.Thumbprint()
	if err != nil {
		t.Fatalf("thumbprint a: %v", err)
	}
	bTP, err := b.Thumbprint()
	if err != nil {
		t.Fatalf("thumbprint b: %v", err)
	}
	if !bytes.Equal(aTP, bTP) {
		t.Errorf("thumbprint differs across metadata changes: %x vs %x", aTP, bTP)
	}
}

// --- helpers ---

// mustEmit builds a mock NSM, emits an envelope with a fresh nonce and
// signing key, and returns everything the failure-mode tests need to
// mutate. Any failure here fails the calling test.
func mustEmit(t *testing.T) (tee.Envelope, *nitro.MockNsm, []byte) {
	t.Helper()
	ctx := context.Background()
	mock, err := nitro.NewMockNsm()
	if err != nil {
		t.Fatalf("build mock NSM: %v", err)
	}
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("gen signing key: %v", err)
	}
	signingKey := tee.Ed25519JWK(pub, "router-test")
	nonce := make([]byte, 32)
	if _, err := rand.Read(nonce); err != nil {
		t.Fatalf("gen nonce: %v", err)
	}
	env, err := nitro.Emit(ctx, mock, nitro.EmitRequest{
		Nonce:      nonce,
		SigningKey: signingKey,
		ExpiresAt:  time.Now().Add(5 * time.Minute),
	})
	if err != nil {
		t.Fatalf("Emit: %v", err)
	}
	return env, mock, nonce
}

// assertFailure runs Verify and asserts it returned a VerifyError with the
// given Mode. Called by the failure-mode tests.
func assertFailure(t *testing.T, mock *nitro.MockNsm, env tee.Envelope, nonce []byte, want tee.FailureMode) {
	t.Helper()
	v := &nitro.Verifier{RootCert: mock.RootCert}
	_, err := v.Verify(env, nonce)
	if err == nil {
		t.Fatalf("Verify accepted envelope — expected failure %q", want)
	}
	var ve *tee.VerifyError
	if !errors.As(err, &ve) {
		t.Fatalf("expected *tee.VerifyError, got %T: %v", err, err)
	}
	if ve.Mode != want {
		t.Errorf("expected failure %q, got %q (%v)", want, ve.Mode, ve.Err)
	}
}
