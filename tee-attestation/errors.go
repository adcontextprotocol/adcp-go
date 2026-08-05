package teeattestation

// FailureMode is the closed set of verification-failure names the spec's
// normative page defines. A verifier that returns an HTTP 403 to the router
// on an inbound X-TMP-Attestation header uses these as the `code` in the
// TMP error body.
type FailureMode string

const (
	FailureNonceMismatch         FailureMode = "nonce_mismatch"
	FailureEnvelopeExpired       FailureMode = "envelope_expired"
	FailureEnvelopeStale         FailureMode = "envelope_stale"
	FailureUnsupportedFormat     FailureMode = "unsupported_format"
	FailureSlotNonceMismatch     FailureMode = "slot_nonce_mismatch"
	FailureSigningKeyNotBound    FailureMode = "signing_key_not_bound"
	FailureMeasurementDisallowed FailureMode = "measurement_disallowed"
	FailurePlatformVerification  FailureMode = "platform_verification_failed"
	FailureNetworkError          FailureMode = "network_error"
)

// VerifyError is the typed failure a verifier returns. Mode maps to the
// spec's failure-mode enum; the wrapped Err carries underlying detail for
// operator logs. The spec's failure-mode table is the operator-visible
// information budget — surfacing wrapped detail to the calling router
// should be done with care.
type VerifyError struct {
	Mode FailureMode
	Err  error
}

func (e *VerifyError) Error() string {
	if e.Err == nil {
		return string(e.Mode)
	}
	return string(e.Mode) + ": " + e.Err.Error()
}

func (e *VerifyError) Unwrap() error { return e.Err }
