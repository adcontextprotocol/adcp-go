package signing

import (
	"context"
	"net/http"
)

// VerifyStatus reports the outcome of VerifyRequest.
//
// The zero value is StatusUnknown so a default-initialized VerifyResult never
// silently presents as verified. Always switch on Status — reading Signer
// without checking Status is the footgun this type exists to prevent.
type VerifyStatus int

const (
	// StatusUnknown is the zero value. A VerifyResult returned by
	// VerifyRequest will never have this status; it exists so that
	// default-constructed VerifyResult values do not masquerade as verified.
	StatusUnknown VerifyStatus = iota

	// StatusVerified means the request carried a signature that passed every
	// check in the verifier checklist. Signer is non-nil.
	StatusVerified

	// StatusUnsigned means the request had no Signature / Signature-Input
	// headers and the operation was not in RequiredFor. Caller MAY proceed
	// with bearer-only auth. Signer and Error are both nil.
	StatusUnsigned

	// StatusRejected means verification failed (including missing-but-required).
	// Error is non-nil and its Code is safe to emit in
	// `WWW-Authenticate: Signature error="<code>"`.
	StatusRejected
)

// String returns the status name for logs.
func (s VerifyStatus) String() string {
	switch s {
	case StatusVerified:
		return "verified"
	case StatusUnsigned:
		return "unsigned"
	case StatusRejected:
		return "rejected"
	default:
		return "unknown"
	}
}

// VerifyResult is the tri-state outcome returned by VerifyRequest. Exactly
// one of Signer or Error is non-nil, determined by Status; the third case
// (Status == StatusUnsigned) has both nil.
type VerifyResult struct {
	Status VerifyStatus
	Signer *VerifiedSigner
	Error  *Error
}

// VerifyRequest is a tri-state wrapper around VerifyRequestSignature that
// makes it impossible for a caller to silently trust an unsigned request.
//
// Prefer this over VerifyRequestSignature in handler code:
//
//	switch res := signing.VerifyRequest(r, opts); res.Status {
//	case signing.StatusVerified:
//	    handleAuthenticated(res.Signer)
//	case signing.StatusUnsigned:
//	    handleBearerOnly(r)
//	case signing.StatusRejected:
//	    w.Header().Set("WWW-Authenticate", `Signature error="`+string(res.Error.Code)+`"`)
//	    http.Error(w, "unauthorized", http.StatusUnauthorized)
//	}
//
// The underlying VerifyRequestSignature remains the lower-level primitive the
// middleware uses directly and is the right choice when callers want to
// handle the (signer, err) tuple themselves.
func VerifyRequest(r *http.Request, opts VerifyOptions) VerifyResult {
	signer, err := VerifyRequestSignature(r, opts)
	switch {
	case err != nil:
		// VerifyRequestSignature always returns *Error on the error path;
		// any other type is a library-side contract break, not a caller
		// condition — panic so the regression surfaces immediately rather
		// than masquerading as a CodeInvalid (which means crypto-verify-failed).
		e := AsError(err)
		if e == nil {
			panic("signing: VerifyRequestSignature returned non-*Error error: " + err.Error())
		}
		return VerifyResult{Status: StatusRejected, Error: e}
	case signer != nil:
		return VerifyResult{Status: StatusVerified, Signer: signer}
	default:
		return VerifyResult{Status: StatusUnsigned}
	}
}

// RequireSigned is the handler-level escape hatch for gating an operation on
// a signature when MiddlewareOptions.RequiredFor can't express the rule —
// for example, when the requirement depends on a field decoded from the
// request body, or on the authenticated tenant. Returns nil when ctx
// carries a VerifiedSigner, or a *Error with CodeRequired otherwise.
//
//	if err := signing.RequireSigned(r.Context()); err != nil {
//	    w.Header().Set("WWW-Authenticate", `Signature error="`+string(err.Code)+`"`)
//	    http.Error(w, "unauthorized", http.StatusUnauthorized)
//	    return
//	}
//
// Prefer MiddlewareOptions.RequiredFor when the gate is statically known per
// operation — it enforces earlier (before the handler runs) and doesn't
// require every handler author to remember this call.
func RequireSigned(ctx context.Context) *Error {
	if VerifiedSignerFromContext(ctx) != nil {
		return nil
	}
	return newError(CodeRequired, "operation requires signature")
}
