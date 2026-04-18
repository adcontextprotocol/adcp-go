package signing

import "time"

// Algorithm is an RFC 9421 `alg` sig-param value allowed by the AdCP profile.
type Algorithm string

const (
	// AlgEd25519 is the RFC 9421 alg value for Ed25519 signatures. JWKs
	// publish it as "EdDSA" (JWS name).
	AlgEd25519 Algorithm = "ed25519"
	// AlgES256 is the RFC 9421 alg value for ECDSA on P-256 with SHA-256 and
	// IEEE P1363 (r||s) fixed-width signature encoding. JWKs publish it as
	// "ES256" (JWS name).
	AlgES256 Algorithm = "ecdsa-p256-sha256"
)

// jwkAlg is the JWS-style alg name published on JWKS entries.
type jwkAlg string

const (
	jwkAlgEdDSA jwkAlg = "EdDSA"
	jwkAlgES256 jwkAlg = "ES256"
)

// Allowed returns true if alg is in the AdCP allowlist.
func (a Algorithm) Allowed() bool {
	return a == AlgEd25519 || a == AlgES256
}

// DigestPolicy governs whether a verifier accepts content-digest-covered signatures.
type DigestPolicy string

const (
	// DigestRequired rejects signatures that don't cover content-digest on
	// requests with a body. Recommended for spend-committing operations.
	DigestRequired DigestPolicy = "required"
	// DigestForbidden rejects signatures that DO cover content-digest.
	// Narrow opt-out for verifiers whose transport cannot preserve body bytes.
	DigestForbidden DigestPolicy = "forbidden"
	// DigestEither accepts signatures whether or not content-digest is covered.
	// The default; signers choose per request.
	DigestEither DigestPolicy = "either"
)

// Profile constants — immutable in adcp/request-signing/v1.
//
// profileTag is part of the signed params. A future v2 profile will bump the
// tag; v1 and v2 verifiers will reject each other's signatures.
const (
	profileTag = "adcp/request-signing/v1"

	componentMethod       = "@method"
	componentTargetURI    = "@target-uri"
	componentAuthority    = "@authority"
	componentContentType  = "content-type"
	componentContentDigst = "content-digest"
	sigParamsComponent    = "@signature-params"

	signatureInputHeader = "Signature-Input"
	signatureHeader      = "Signature"
	contentDigestHeader  = "Content-Digest"
	contentTypeHeader    = "Content-Type"

	maxWindowSeconds int64 = 300 // profile: expires - created ≤ 300
	skewSeconds      int64 = 60  // ±60s tolerance

	// Nonce MUST decode to ≥ 16 bytes (128 bits).
	minNonceBytes = 16

	// Recommended per-keyid replay cap (overridable via NewMemoryReplayStore).
	defaultKeyIDCap = 1_000_000

	// JWKS kid-miss refetch cooldown (between refetches).
	defaultJWKSRefetchCooldown = 30 * time.Second

	// Body buffering ceiling for content-digest recompute. Requests larger than
	// this are rejected with request_signature_digest_mismatch to bound memory
	// consumption. Override via VerifyOptions.MaxBodyBytes.
	defaultMaxBodyBytes int64 = 32 << 20 // 32 MiB
)

// VerifiedSigner is returned by the verifier on success.
//
// KeyID identifies the JWK entry that verified the signature. AgentURL is the
// operator-declared URL of the signing agent. VerifiedAt is the verifier's
// wall-clock time the signature was validated. Label is the Signature-Input
// label the verifier processed (usually "sig1").
//
// Downstream handlers use VerifiedSigner to gate authorization and audit logs.
type VerifiedSigner struct {
	KeyID      string
	AgentURL   string
	VerifiedAt time.Time
	Algorithm  Algorithm
	Label      string
}
