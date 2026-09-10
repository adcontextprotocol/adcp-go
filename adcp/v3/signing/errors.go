package signing

import (
	"errors"
	"strings"
)

// ErrorCode is a stable identifier from the AdCP transport error taxonomy.
// Verifiers emit these in `WWW-Authenticate: Signature error="<code>"` on 401.
//
// Each code maps to a specific check in the AdCP verifier checklist. The
// mapping below is stable for the adcp/request-signing/v1 profile:
//
//	CodeRequired             — pre-check 0 (op in required_for, no signature)
//	CodeHeaderMalformed      — pre-check 0 (signature/signature-input pair)
//	                           or step 1 (Signature-Input parse failure)
//	CodeTargetURIMalformed   — step 10 (@target-uri cannot be canonicalized)
//	CodeParamsIncomplete     — step 2 (missing required sig-param)
//	CodeTagInvalid           — step 3 (tag != adcp/request-signing/v1)
//	CodeAlgNotAllowed        — step 4 (alg not in allowlist)
//	CodeWindowInvalid        — step 5 (created/expires/skew/window rules)
//	CodeComponentsIncomplete — step 6 (required covered component missing)
//	CodeComponentsUnexpected — step 6 (content-digest covered when forbidden)
//	CodeKeyUnknown           — step 7 (keyid unresolved after refetch)
//	CodeKeyPurposeInvalid    — step 8 (use/key_ops/adcp_use/alg mismatch)
//	CodeKeyRevoked           — step 9 (keyid in revoked_kids)
//	CodeRevocationStale      — step 9 (revocation list grace exceeded)
//	CodeRateAbuse            — step 9a (per-keyid replay cap hit)
//	CodeInvalid              — step 10 (cryptographic verify failed)
//	CodeDigestMismatch       — step 11 (recomputed sha-256 != header)
//	CodeReplayed             — step 12 ((keyid, nonce) seen in window)
//	CodeJWKSUnavailable      — step 7 transient (JWKS fetch failure)
//	CodeJWKSUntrusted        — step 7 (JWKS URL fails SSRF validation)
type ErrorCode string

const (
	// CodeRequired — unsigned request to an operation in required_for.
	CodeRequired ErrorCode = "request_signature_required"

	// CodeHeaderMalformed — Signature/Signature-Input header pair broken or
	// malformed; Signature-Input parse failed; request URL can't be canonicalized.
	CodeHeaderMalformed ErrorCode = "request_signature_header_malformed"

	// CodeTargetURIMalformed — the received request URL cannot be represented
	// by the AdCP @target-uri canonicalization rules.
	CodeTargetURIMalformed ErrorCode = "request_target_uri_malformed"

	// CodeParamsIncomplete — one of created/expires/nonce/keyid/alg/tag is
	// absent, or the nonce is shorter than 128 bits.
	CodeParamsIncomplete ErrorCode = "request_signature_params_incomplete"

	// CodeTagInvalid — tag parameter is not exactly "adcp/request-signing/v1".
	CodeTagInvalid ErrorCode = "request_signature_tag_invalid"

	// CodeAlgNotAllowed — alg parameter is not in {ed25519, ecdsa-p256-sha256}.
	CodeAlgNotAllowed ErrorCode = "request_signature_alg_not_allowed"

	// CodeWindowInvalid — expires ≤ created, created > now+60s, expires < now-60s,
	// or expires-created > 300s.
	CodeWindowInvalid ErrorCode = "request_signature_window_invalid"

	// CodeComponentsIncomplete — covered components missing a required field
	// (@method, @target-uri, @authority, content-type when body present, or
	// content-digest when the verifier's policy is "required").
	CodeComponentsIncomplete ErrorCode = "request_signature_components_incomplete"

	// CodeComponentsUnexpected — signer covered content-digest but the verifier's
	// policy is "forbidden".
	CodeComponentsUnexpected ErrorCode = "request_signature_components_unexpected"

	// CodeKeyUnknown — keyid cannot be resolved to a JWK (after one refetch
	// subject to the 30-second cooldown).
	CodeKeyUnknown ErrorCode = "request_signature_key_unknown"

	// CodeKeyPurposeInvalid — JWK use != "sig", key_ops lacks "verify", adcp_use
	// != "request-signing", or JWK alg doesn't match the sig-param alg.
	CodeKeyPurposeInvalid ErrorCode = "request_signature_key_purpose_invalid"

	// CodeKeyRevoked — keyid appears in revoked_kids of the revocation list.
	CodeKeyRevoked ErrorCode = "request_signature_key_revoked"

	// CodeRevocationStale — verifier has not refreshed the revocation list
	// within next_update + grace and is blocking new signed mutations.
	CodeRevocationStale ErrorCode = "request_signature_revocation_stale"

	// CodeInvalid — signature failed cryptographic verification against the
	// resolved JWK.
	CodeInvalid ErrorCode = "request_signature_invalid"

	// CodeDigestMismatch — Content-Digest header's sha-256 value does not
	// match the SHA-256 of the received body.
	CodeDigestMismatch ErrorCode = "request_signature_digest_mismatch"

	// CodeReplayed — (keyid, nonce) has been seen within the signature's
	// validity window.
	CodeReplayed ErrorCode = "request_signature_replayed"

	// CodeRateAbuse — per-keyid replay cache cap exceeded; SHOULD alert
	// operators (possible compromised key or misconfigured signer).
	CodeRateAbuse ErrorCode = "request_signature_rate_abuse"

	// CodeJWKSUnavailable — JWKS endpoint fetch failed transiently (network,
	// HTTP 5xx, parse error). Retry with backoff.
	CodeJWKSUnavailable ErrorCode = "request_signature_jwks_unavailable"

	// CodeJWKSUntrusted — JWKS URL failed SSRF validation (resolves to a
	// private/link-local/loopback/CGNAT/ULA address).
	CodeJWKSUntrusted ErrorCode = "request_signature_jwks_untrusted"
)

// Error is the typed error surface for the verifier.
//
// Code is stable and safe to emit in WWW-Authenticate. Detail is for
// server-side logging and MUST NOT be echoed to callers.
type Error struct {
	Code    ErrorCode
	Detail  string
	Wrapped error
}

func (e *Error) Error() string {
	if e.Detail != "" {
		return string(e.Code) + ": " + e.Detail
	}
	return string(e.Code)
}

func (e *Error) Unwrap() error { return e.Wrapped }

// WireCode returns the error code formatted for the given signing profile.
// For ProfileRequestSigning the result equals string(e.Code). For
// ProfileWebhookSigning, the "request_signature_" prefix is swapped for
// "webhook_signature_" so the wire taxonomy matches adcontextprotocol/adcp#2423.
// A zero-value Profile is treated as ProfileRequestSigning.
//
// Middleware callers emit WireCode in WWW-Authenticate; the Go-level Code
// constants stay stable so callers can still switch on them with errors.As.
func (e *Error) WireCode(profile Profile) string {
	code := string(e.Code)
	if profile.ErrorPrefix == "" || profile.ErrorPrefix == ProfileRequestSigning.ErrorPrefix {
		return code
	}
	const legacy = "request_signature_"
	if strings.HasPrefix(code, legacy) {
		return profile.ErrorPrefix + code[len(legacy):]
	}
	return code
}

// newError constructs a typed error without wrapping.
func newError(code ErrorCode, detail string) *Error {
	return &Error{Code: code, Detail: detail}
}

// wrapError attaches an underlying cause for logging.
func wrapError(code ErrorCode, detail string, cause error) *Error {
	return &Error{Code: code, Detail: detail, Wrapped: cause}
}

// AsError returns e as *Error if it is one, else nil.
func AsError(err error) *Error {
	var e *Error
	if errors.As(err, &e) {
		return e
	}
	return nil
}

// SigningErrorCode is a stable identifier for a failure on the *signing*
// (outbound, provider) path — the write-side counterpart to ErrorCode,
// which covers the verifier.
type SigningErrorCode string

const (
	// SignCodeProviderFailed means a SigningProvider's call to its backing
	// service (KMS, HSM, Vault, ...) failed or returned a response Sign
	// could not use. The underlying cause is available via errors.Unwrap /
	// errors.As, never via Detail — see SigningError's doc comment.
	SignCodeProviderFailed SigningErrorCode = "signing_provider_failed"

	// SignCodeAlgorithmUnexpected means a provider produced or reported an
	// algorithm other than the one NewSigner validated at construction
	// time (SigningProvider.Algorithm()).
	SignCodeAlgorithmUnexpected SigningErrorCode = "signing_algorithm_unexpected"

	// SignCodePublicKeyMismatch means AssertProviderPublicKeyMatchesSPKI
	// found the provider's current public key does not match the SPKI
	// bytes pinned at deploy time — the managed key store silently
	// rotated the key backing this provider's KeyID.
	SignCodePublicKeyMismatch SigningErrorCode = "signing_public_key_mismatch"
)

// SigningError is the typed error surface for the signing (outbound) path.
// A SigningProvider backed by an external service should return one of
// these from Sign or PublicKey instead of forwarding the backend SDK's raw
// error.
//
// Code is stable and safe to log or compare with errors.Is/errors.As.
//
// Detail MUST be a caller-controlled, static string — NEVER the raw error
// message from a KMS/HSM/Vault SDK call. Those messages routinely embed
// resource identifiers (KMS key ARNs; GCP KMS resource names of the form
// projects/<id>/locations/<region>/keyRings/<ring>/cryptoKeyVersions/<n>;
// Vault paths) that AGENTS.md's error-message rule — "never echo
// err.Error() in HTTP responses," "never interpolate user-supplied values
// into error messages" — is designed to keep off of any surface a caller
// might build on top of Sign's return value.
//
// The underlying cause belongs in Wrapped instead: reachable via
// errors.Unwrap / errors.As for server-side structured logging
// (slog.Error("signing failed", "error", err)), never for direct display.
// SigningError.Error() deliberately does not include Wrapped's message for
// this reason — only Code, and Detail if the caller explicitly set one.
type SigningError struct {
	Code    SigningErrorCode
	Detail  string
	Wrapped error
}

func (e *SigningError) Error() string {
	if e.Detail != "" {
		return string(e.Code) + ": " + e.Detail
	}
	return string(e.Code)
}

func (e *SigningError) Unwrap() error { return e.Wrapped }

// newSigningError constructs a typed signing error without wrapping.
func newSigningError(code SigningErrorCode, detail string) *SigningError {
	return &SigningError{Code: code, Detail: detail}
}

// wrapSigningError attaches an underlying cause for logging. Detail is
// deliberately not accepted here — see SigningError's doc comment on why
// the underlying cause must never become Detail.
func wrapSigningError(code SigningErrorCode, cause error) *SigningError {
	return &SigningError{Code: code, Wrapped: cause}
}

// AsSigningError returns err as *SigningError if it is (or wraps) one, else nil.
func AsSigningError(err error) *SigningError {
	var e *SigningError
	if errors.As(err, &e) {
		return e
	}
	return nil
}
