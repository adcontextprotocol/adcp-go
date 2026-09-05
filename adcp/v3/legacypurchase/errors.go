package legacypurchase

import "fmt"

// Error codes drawn from AdCP's shared enums/error-code.json vocabulary
// where a direct match exists (this package is SDK-local — its errors are
// never sent over the wire, but reusing the shared vocabulary keeps
// application-level error mapping consistent with the rest of the SDK).
const (
	CodeInvalidRequest      = "INVALID_REQUEST"
	CodeValidationError     = "VALIDATION_ERROR"
	CodeConflict            = "CONFLICT"
	CodeIdempotencyConflict = "IDEMPOTENCY_CONFLICT"
	CodeIdempotencyInFlight = "IDEMPOTENCY_IN_FLIGHT"
)

// InvalidInputError is returned when a CompatibilityPurchaseCoordinatorInput
// or a Continuation registration fails structural validation (missing
// required field, malformed idempotency_key, legacy_create_request not
// valid JSON or not explicit-package mode, etc).
type InvalidInputError struct {
	Field  string
	Reason string
}

func (e *InvalidInputError) Error() string {
	if e.Field == "" {
		return "legacypurchase: invalid input: " + e.Reason
	}
	return fmt.Sprintf("legacypurchase: invalid input at %s: %s", e.Field, e.Reason)
}

// Code returns the protocol error code.
func (*InvalidInputError) Code() string { return CodeInvalidRequest }

// DuplicateTokenError is returned by Backend.PutContinuation when the token
// already has a durable record.
type DuplicateTokenError struct{ Token string }

func (e *DuplicateTokenError) Error() string {
	return "legacypurchase: continuation token already registered: " + logToken(e.Token)
}

// Code returns the protocol error code.
func (*DuplicateTokenError) Code() string { return CodeConflict }

// NotFoundError is returned when continuation_token has no durable record —
// unknown, or already swept past its retention window.
type NotFoundError struct{ Token string }

func (e *NotFoundError) Error() string {
	return "legacypurchase: unknown continuation token: " + logToken(e.Token)
}

// Code returns the protocol error code.
func (*NotFoundError) Code() string { return CodeInvalidRequest }

// ExpiredError is returned when a redemption attempt arrives after the
// continuation's continuation_expires_at.
type ExpiredError struct {
	Token     string
	ExpiresAt string
}

func (e *ExpiredError) Error() string {
	return fmt.Sprintf("legacypurchase: continuation %s expired at %s", logToken(e.Token), e.ExpiresAt)
}

// Code returns the protocol error code.
func (*ExpiredError) Code() string { return CodeValidationError }

// PrincipalMismatchError is returned when the authenticated principal
// resolving the token differs from the principal it was bound to at
// registration — the spec's confused-deputy guard.
type PrincipalMismatchError struct{ Token string }

func (e *PrincipalMismatchError) Error() string {
	return "legacypurchase: continuation " + logToken(e.Token) + " is not bound to the authenticated principal"
}

// Code returns the protocol error code.
func (*PrincipalMismatchError) Code() string { return CodeValidationError }

// AccountMismatchError is returned when the input's account (or, when the
// source version's create_media_buy request carries one, the request's own
// account field) does not equal the token-bound account.
type AccountMismatchError struct {
	Token  string
	Reason string
}

func (e *AccountMismatchError) Error() string {
	return "legacypurchase: continuation " + logToken(e.Token) + " account mismatch: " + e.Reason
}

// Code returns the protocol error code.
func (*AccountMismatchError) Code() string { return CodeValidationError }

// ProductSelectionError is returned when selected_product_ids is not a
// non-empty subset of the token-bound product IDs, or does not equal the
// distinct explicit-package product IDs in legacy_create_request.
type ProductSelectionError struct {
	Token  string
	Reason string
}

func (e *ProductSelectionError) Error() string {
	return "legacypurchase: continuation " + logToken(e.Token) + " product selection invalid: " + e.Reason
}

// Code returns the protocol error code.
func (*ProductSelectionError) Code() string { return CodeValidationError }

// PricingSelectionError is returned when a legacy_create_request package
// names a pricing_option_id not present among the continuation's observed
// pricing options for that product — a substituted price the seller never
// actually offered against this continuation.
type PricingSelectionError struct {
	Token  string
	Reason string
}

func (e *PricingSelectionError) Error() string {
	return "legacypurchase: continuation " + logToken(e.Token) + " pricing selection invalid: " + e.Reason
}

// Code returns the protocol error code.
func (*PricingSelectionError) Code() string { return CodeValidationError }

// LossAcceptanceError is returned when accepted_losses is not exactly equal
// to the continuation's declared loss set — missing, extra, or stale
// consent.
type LossAcceptanceError struct {
	Token    string
	Required []string
	Accepted []string
}

func (e *LossAcceptanceError) Error() string {
	return fmt.Sprintf("legacypurchase: continuation %s loss acceptance mismatch: required %v, got %v",
		logToken(e.Token), e.Required, e.Accepted)
}

// Code returns the protocol error code.
func (*LossAcceptanceError) Code() string { return CodeValidationError }

// ExplicitPackageModeError is returned when legacy_create_request does not
// use explicit-package mode (a non-empty packages[] array where every
// package names a product_id).
type ExplicitPackageModeError struct{ Reason string }

func (e *ExplicitPackageModeError) Error() string {
	return "legacypurchase: legacy_create_request must use explicit-package mode: " + e.Reason
}

// Code returns the protocol error code.
func (*ExplicitPackageModeError) Code() string { return CodeValidationError }

// AlreadyClaimedError is returned when a continuation is no longer
// StateOffered and the redeeming idempotency_key does not match the
// claimant on record — the single-use guard. It is also returned for a
// terminal (StateCommitted/StateFailed) record reused with a different
// idempotency_key, and carries the claim's terminal state for the caller's
// diagnostics.
type AlreadyClaimedError struct {
	Token string
	State ContinuationState
}

func (e *AlreadyClaimedError) Error() string {
	return fmt.Sprintf("legacypurchase: continuation %s already claimed (state=%s)", logToken(e.Token), e.State)
}

// Code returns the protocol error code.
func (*AlreadyClaimedError) Code() string { return CodeIdempotencyConflict }

// RequestConflictError is returned when idempotency_key is reused with a
// different canonicalized redemption payload — the same class of conflict
// adcp/v3/idempotency.ConflictError reports for mutating tool calls.
type RequestConflictError struct {
	Token          string
	IdempotencyKey string
}

func (e *RequestConflictError) Error() string {
	return "legacypurchase: idempotency_key " + logToken(e.IdempotencyKey) + " reused with a different payload for continuation " + logToken(e.Token)
}

// Code returns the protocol error code.
func (*RequestConflictError) Code() string { return CodeIdempotencyConflict }

// InFlightError is returned when the same idempotency_key's earlier claim
// is still within its pending lease window — the Executor call is very
// likely still running in another goroutine or process. Distinct from
// AmbiguousClaimError: this is an ordinary "retry shortly" condition, not a
// crash-recovery one.
type InFlightError struct {
	Token      string
	ClaimedAt  string
	RetryAfter string
}

func (e *InFlightError) Error() string {
	return fmt.Sprintf("legacypurchase: continuation %s claim is in flight (claimed_at=%s); retry after %s",
		logToken(e.Token), e.ClaimedAt, e.RetryAfter)
}

// Code returns the protocol error code.
func (*InFlightError) Code() string { return CodeIdempotencyInFlight }

// AmbiguousClaimError is returned when a continuation's claim has been
// StatePending for longer than the configured pending-lease timeout: the
// process that claimed it most likely crashed between claiming the token
// and recording a terminal outcome, so whether the legacy seller actually
// received the create_media_buy call is unknown. Per spec, this MUST fail
// closed rather than be silently retried — Guidance names the concrete
// recovery step.
type AmbiguousClaimError struct {
	Token     string
	ClaimedAt string
	Guidance  string
}

func (e *AmbiguousClaimError) Error() string {
	return fmt.Sprintf("legacypurchase: continuation %s has an ambiguous crash-suspected claim from %s: %s",
		logToken(e.Token), e.ClaimedAt, e.Guidance)
}

// Code returns the protocol error code.
func (*AmbiguousClaimError) Code() string { return CodeConflict }

// TerminalFailureError is returned on an exact retry of a redemption whose
// Executor call previously failed terminally (StateFailed). It carries the
// recorded failure so the caller does not need a side channel to learn why.
type TerminalFailureError struct {
	Token   string
	ErrCode string
	Message string
}

func (e *TerminalFailureError) Error() string {
	return fmt.Sprintf("legacypurchase: continuation %s previously failed terminally [%s]: %s",
		logToken(e.Token), e.ErrCode, e.Message)
}

// Code returns the protocol error code.
func (e *TerminalFailureError) Code() string { return e.ErrCode }

// logToken returns a prefix-truncated form of a token/key safe for default
// logging, mirroring adcp/v3/idempotency.LogKey — full tokens are replay
// oracles.
func logToken(s string) string {
	const n = 8
	if len(s) <= n {
		return "****"
	}
	return s[:n] + "…"
}
