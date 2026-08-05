package nitro

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/x509"
	"encoding/base64"
	"fmt"
	"math"
	"time"

	"github.com/veraison/go-cose"

	tee "github.com/adcontextprotocol/adcp-go/tee-attestation"
)

// Verifier is the read side of the Nitro attestation flow. It walks the
// 9-step verification flow from docs/trusted-match/router-attestation.mdx.
type Verifier struct {
	// RootCert is the trust anchor for the NSM certificate chain. In
	// production this is the AWS Nitro Root CA; in tests this is the
	// MockNsm's RootCert.
	RootCert *x509.Certificate

	// Now returns "current time" for expiry checks. Nil defaults to
	// time.Now.
	Now func() time.Time

	// Policy is applied to extracted measurements after platform-side
	// verification succeeds. Nil means "accept any measurement" (useful in
	// tests; production callers should always supply an allowlist).
	Policy Policy
}

// Policy decides whether a given set of measurements is acceptable. See the
// spec's failure-mode table entry `measurement_disallowed`. The allowlist is
// deploy-side policy; this package supplies the plumbing, not the values.
type Policy interface {
	AllowMeasurements(m Measurements) error
}

// PolicyFunc is a convenience adapter for callers who don't want to declare
// a type.
type PolicyFunc func(m Measurements) error

// AllowMeasurements implements Policy.
func (f PolicyFunc) AllowMeasurements(m Measurements) error { return f(m) }

// Measurements is the extracted set of Nitro PCRs.
type Measurements struct {
	Digest string            // e.g. "SHA384"
	PCRs   map[uint32][]byte // by index
}

// VerifiedEnvelope is what a successful Verify returns — the parsed
// envelope, the raw Nitro document fields for downstream inspection, the
// signing key that is now provably held by the attested binary, and the
// measurements the policy accepted.
type VerifiedEnvelope struct {
	Envelope     tee.Envelope
	Document     *Document
	SigningKey   tee.JWK
	Measurements Measurements
}

// Verify walks the 9-step verification flow. expectedNonce is the raw bytes
// the verifier sent to GET /.well-known/tmp-router-attestation. Pass nil to
// skip the envelope-fetch-path nonce echo (step 1) — that's what the spec
// prescribes on the per-request X-TMP-Attestation code path, where freshness
// comes from `expires_at` + `min_freshness_sec` instead.
func (v *Verifier) Verify(env tee.Envelope, expectedNonce []byte) (*VerifiedEnvelope, error) {
	if v == nil || v.RootCert == nil {
		return nil, fmt.Errorf("nitro verify: Verifier misconfigured (nil RootCert)")
	}
	now := time.Now
	if v.Now != nil {
		now = v.Now
	}

	// --- Step 1: nonce echo (envelope-fetch path). Skipped when caller
	// passes nil, per the spec's per-request X-TMP-Attestation branch.
	envelopeNonce, err := env.NonceBytes()
	if err != nil {
		return nil, &tee.VerifyError{Mode: tee.FailureNonceMismatch, Err: err}
	}
	if expectedNonce != nil && !bytes.Equal(envelopeNonce, expectedNonce) {
		return nil, &tee.VerifyError{Mode: tee.FailureNonceMismatch,
			Err: fmt.Errorf("envelope.nonce did not byte-equal verifier-sent nonce")}
	}

	// --- Step 2: expiry.
	if !env.ExpiresAt.IsZero() && now().After(env.ExpiresAt) {
		return nil, &tee.VerifyError{Mode: tee.FailureEnvelopeExpired,
			Err: fmt.Errorf("envelope expired at %s (now %s)", env.ExpiresAt, now())}
	}

	// --- Step 3: min-freshness. Verifier-kit-derived per PROJECTION.md
	// (Nitro carries `timestamp` in ms since epoch inside the document);
	// applied after step 5 below when we've parsed the document.

	// --- Step 4: format support. This kit only handles Nitro v1.
	if env.Format != tee.FormatAWSNitroCOSESign1V1 {
		return nil, &tee.VerifyError{Mode: tee.FailureUnsupportedFormat,
			Err: fmt.Errorf("nitro verifier does not handle format %q", env.Format)}
	}

	// --- Step 5: platform document verification.
	docBytes, err := env.DocumentBytes()
	if err != nil {
		return nil, &tee.VerifyError{Mode: tee.FailurePlatformVerification, Err: err}
	}
	msg := cose.NewSign1Message()
	if err := msg.UnmarshalCBOR(docBytes); err != nil {
		return nil, &tee.VerifyError{Mode: tee.FailurePlatformVerification,
			Err: fmt.Errorf("parse COSE_Sign1: %w", err)}
	}
	doc, err := UnmarshalDocumentPayload(msg.Payload)
	if err != nil {
		return nil, &tee.VerifyError{Mode: tee.FailurePlatformVerification, Err: err}
	}
	// Cert chain to RootCert.
	leaf, err := x509.ParseCertificate(doc.Certificate)
	if err != nil {
		return nil, &tee.VerifyError{Mode: tee.FailurePlatformVerification,
			Err: fmt.Errorf("parse NSM certificate: %w", err)}
	}
	roots := x509.NewCertPool()
	roots.AddCert(v.RootCert)
	intermediates := x509.NewCertPool()
	for _, raw := range doc.CABundle {
		if bytes.Equal(raw, v.RootCert.Raw) {
			continue // don't add the root as an intermediate
		}
		c, err := x509.ParseCertificate(raw)
		if err != nil {
			return nil, &tee.VerifyError{Mode: tee.FailurePlatformVerification,
				Err: fmt.Errorf("parse intermediate cert: %w", err)}
		}
		intermediates.AddCert(c)
	}
	if _, err := leaf.Verify(x509.VerifyOptions{
		Roots:         roots,
		Intermediates: intermediates,
		CurrentTime:   now(),
		KeyUsages:     []x509.ExtKeyUsage{x509.ExtKeyUsageCodeSigning, x509.ExtKeyUsageAny},
	}); err != nil {
		return nil, &tee.VerifyError{Mode: tee.FailurePlatformVerification,
			Err: fmt.Errorf("cert chain to root: %w", err)}
	}
	// COSE_Sign1 signature.
	leafPub, ok := leaf.PublicKey.(*ecdsa.PublicKey)
	if !ok {
		return nil, &tee.VerifyError{Mode: tee.FailurePlatformVerification,
			Err: fmt.Errorf("NSM cert public key is %T, expected *ecdsa.PublicKey", leaf.PublicKey)}
	}
	alg, err := msg.Headers.Protected.Algorithm()
	if err != nil {
		return nil, &tee.VerifyError{Mode: tee.FailurePlatformVerification,
			Err: fmt.Errorf("read COSE algorithm: %w", err)}
	}
	verifier, err := cose.NewVerifier(alg, leafPub)
	if err != nil {
		return nil, &tee.VerifyError{Mode: tee.FailurePlatformVerification,
			Err: fmt.Errorf("build COSE verifier: %w", err)}
	}
	if err := msg.Verify(nil, verifier); err != nil {
		return nil, &tee.VerifyError{Mode: tee.FailurePlatformVerification,
			Err: fmt.Errorf("COSE_Sign1 signature: %w", err)}
	}

	// --- Step 3 (deferred): min-freshness.
	if env.ExpiresAt.IsZero() && doc.Timestamp > 0 {
		// If the envelope carries no expires_at, honor a conservative
		// default freshness window derived from doc.Timestamp. Cap at
		// math.MaxInt64 to satisfy gosec (uint64→int64 overflow) — an
		// attestation timestamp near 2^63 ms since epoch is not a real
		// value, and treating an out-of-range one as "very far in the
		// future" keeps the freshness check conservative.
		var docMS int64 = math.MaxInt64
		if doc.Timestamp <= math.MaxInt64 {
			docMS = int64(doc.Timestamp)
		}
		docTime := time.UnixMilli(docMS)
		if now().Sub(docTime) > time.Hour {
			return nil, &tee.VerifyError{Mode: tee.FailureEnvelopeStale,
				Err: fmt.Errorf("document timestamp %s older than 1h default freshness (now %s)", docTime, now())}
		}
	}

	// --- Step 6: slot-bound nonce.
	if !bytes.Equal(doc.Nonce, envelopeNonce) {
		return nil, &tee.VerifyError{Mode: tee.FailureSlotNonceMismatch,
			Err: fmt.Errorf("nonce in document (%d bytes) does not match envelope.nonce (%d bytes)", len(doc.Nonce), len(envelopeNonce))}
	}

	// --- Step 7: binding rule. See PROJECTION.md — Nitro places the raw
	// Ed25519 pubkey bytes in doc.PublicKey. Reconstruct a JWK from those
	// bytes and compare RFC 7638 thumbprints against envelope.signing_key.
	envelopePub, err := env.SigningKey.Ed25519PublicKey()
	if err != nil {
		return nil, &tee.VerifyError{Mode: tee.FailureSigningKeyNotBound,
			Err: fmt.Errorf("envelope.signing_key: %w", err)}
	}
	if !bytes.Equal(doc.PublicKey, envelopePub) {
		return nil, &tee.VerifyError{Mode: tee.FailureSigningKeyNotBound,
			Err: fmt.Errorf("document.public_key does not match envelope.signing_key raw bytes")}
	}
	// Sanity: thumbprints agree. This is redundant given the raw-byte
	// check above, but it's the check the spec's normative page names,
	// and forcing it locally means a future canonicalization change on
	// either side surfaces here rather than in a downstream integration.
	docJWK := tee.Ed25519JWK(envelopePub, env.SigningKey.Kid)
	docTP, err := docJWK.Thumbprint()
	if err != nil {
		return nil, &tee.VerifyError{Mode: tee.FailureSigningKeyNotBound,
			Err: fmt.Errorf("reconstruct doc JWK thumbprint: %w", err)}
	}
	envTP, err := env.SigningKey.Thumbprint()
	if err != nil {
		return nil, &tee.VerifyError{Mode: tee.FailureSigningKeyNotBound,
			Err: fmt.Errorf("envelope JWK thumbprint: %w", err)}
	}
	if !bytes.Equal(docTP, envTP) {
		return nil, &tee.VerifyError{Mode: tee.FailureSigningKeyNotBound,
			Err: fmt.Errorf("RFC 7638 thumbprint mismatch between envelope and reconstructed doc JWK")}
	}

	// --- Step 8: measurement policy.
	measurements := Measurements{Digest: doc.Digest, PCRs: doc.PCRs}
	if v.Policy != nil {
		if err := v.Policy.AllowMeasurements(measurements); err != nil {
			return nil, &tee.VerifyError{Mode: tee.FailureMeasurementDisallowed, Err: err}
		}
	}

	// --- Step 9: caller caches on success.
	return &VerifiedEnvelope{
		Envelope:     env,
		Document:     doc,
		SigningKey:   env.SigningKey,
		Measurements: measurements,
	}, nil
}

// EnvelopeThumbprint returns the RFC 7638 thumbprint of the envelope's
// signing key, encoded as base64url-no-pad. Callers use this as a cache key
// per docs/trusted-match/router-attestation.mdx "Caching".
func EnvelopeThumbprint(env tee.Envelope) (string, error) {
	tp, err := env.SigningKey.Thumbprint()
	if err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(tp), nil
}
