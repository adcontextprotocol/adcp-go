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

// BinaryEncoding selects the structured-field byte-sequence encoding used by
// a Signature or Content-Digest value.
type BinaryEncoding string

const (
	// BinaryEncodingBase64URL is the AdCP 3.1 legacy, unpadded base64url
	// encoding. It remains available for a peer pinned to the 3.1 wire profile.
	BinaryEncodingBase64URL BinaryEncoding = "base64url"
	// BinaryEncodingRFC8941 is RFC 8941 sf-binary: standard padded Base64.
	// AdCP 3.2 uses this encoding for request signing.
	BinaryEncodingRFC8941 BinaryEncoding = "rfc8941"
)

// Profile selects which AdCP 9421 signing profile a Signer emits or a verifier
// accepts. Two profiles share the same substrate (algorithms, components,
// replay protection) and differ only in the signed `tag` param and the
// required JWK `adcp_use` discriminator:
//
//   - Request signing (adcontextprotocol/adcp#2323): agent-to-agent tool calls.
//   - Webhook signing (adcontextprotocol/adcp#2423): publisher-to-subscriber
//     webhooks. Baseline-required in AdCP 3.0; replaces HMAC-SHA256 (legacy
//     fallback through 3.x, removed in 4.0).
//
// The tag and adcp_use strings are signed params — a future v2 of either
// profile will bump them; v1 and v2 verifiers will reject each other's
// signatures. Callers normally pass one of ProfileRequestSigning or
// ProfileWebhookSigning; custom Profile values are for tests and future
// extensions only.
type Profile struct {
	// Tag is the RFC 9421 `tag` sig-param value. Verifiers enforce byte-for-byte
	// equality. Using distinct tags across profiles guarantees a signature for
	// one profile cannot be replayed as the other.
	Tag string

	// AdcpUse is the value required in the JWK's `adcp_use` member. Prevents
	// cross-profile key reuse: a key published for webhook-signing MUST NOT
	// verify a request-signing signature.
	AdcpUse string

	// ErrorPrefix replaces "request_signature_" in wire error codes emitted
	// via WWW-Authenticate and Error.WireCode. For webhook-signing, the spec
	// mandates "webhook_signature_" so receivers can route the two error
	// taxonomies to different observability pipelines.
	ErrorPrefix string

	// BinaryEncoding specifies how Signature byte sequences are encoded. The
	// zero value preserves the 3.1 base64url wire profile for custom profiles.
	BinaryEncoding BinaryEncoding

	// ContentDigestEncoding specifies how Content-Digest byte sequences are
	// encoded. When zero, BinaryEncoding is used. Webhook signing retains the
	// established RFC 8941 Content-Digest representation while its Signature
	// remains base64url.
	ContentDigestEncoding BinaryEncoding
}

func (p Profile) contentDigestEncoding() BinaryEncoding {
	if p.ContentDigestEncoding != "" {
		return p.ContentDigestEncoding
	}
	return p.BinaryEncoding
}

var (
	// ProfileRequestSigning is the AdCP 3.1 request-signing profile and the
	// compatibility default for signing and verification. Use
	// ProfileRequestSigningRC only after negotiating AdCP 3.2-rc.1.
	ProfileRequestSigning = Profile{
		Tag:                   "adcp/request-signing/v1",
		AdcpUse:               "request-signing",
		ErrorPrefix:           "request_signature_",
		BinaryEncoding:        BinaryEncodingBase64URL,
		ContentDigestEncoding: BinaryEncodingBase64URL,
	}

	// ProfileRequestSigningLegacy names the compatibility default for callers
	// that prefer to select the 3.1 profile explicitly.
	ProfileRequestSigningLegacy = Profile{
		Tag:                   "adcp/request-signing/v1",
		AdcpUse:               "request-signing",
		ErrorPrefix:           "request_signature_",
		BinaryEncoding:        BinaryEncodingBase64URL,
		ContentDigestEncoding: BinaryEncodingBase64URL,
	}

	// ProfileRequestSigningRC is the AdCP 3.2-rc.1 request-signing profile.
	// It uses RFC 8941 sf-binary and must be selected only for a peer that has
	// explicitly negotiated that release-precision protocol version.
	ProfileRequestSigningRC = Profile{
		Tag:                   "adcp/request-signing/v1",
		AdcpUse:               "request-signing",
		ErrorPrefix:           "request_signature_",
		BinaryEncoding:        BinaryEncodingRFC8941,
		ContentDigestEncoding: BinaryEncodingRFC8941,
	}

	// ProfileWebhookSigning is the adcp/webhook-signing/v1 profile — baseline
	// for outbound webhooks in AdCP 3.0. Defined in
	// adcontextprotocol/adcp#2423.
	//
	// The spec requires content-digest coverage on every webhook signature;
	// callers using this profile SHOULD set ContentDigestPolicy=DigestRequired
	// on the verifier and CoverContentDigest=true on the signer. The webhook
	// package wires these defaults automatically.
	ProfileWebhookSigning = Profile{
		Tag:                   "adcp/webhook-signing/v1",
		AdcpUse:               "webhook-signing",
		ErrorPrefix:           "webhook_signature_",
		BinaryEncoding:        BinaryEncodingBase64URL,
		ContentDigestEncoding: BinaryEncodingRFC8941,
	}
)

const (
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

	// Parse-time caps on attacker-controlled header content. Go's http.Server
	// MaxHeaderBytes (default 1 MiB) caps aggregate headers; these per-field
	// caps fail fast on pathological inputs before tokenizer work.
	maxSignatureInputLen = 8 << 10 // 8 KiB
	maxSignatureLen      = 8 << 10 // 8 KiB
	maxNonceLen          = 256
	maxKeyIDLen          = 256
	maxCoveredComponents = 32
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
