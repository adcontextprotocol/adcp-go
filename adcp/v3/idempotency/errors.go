package idempotency

import (
	"errors"
	"fmt"
	"time"
)

// Protocol error codes this package maps onto. Only IDEMPOTENCY_CONFLICT and
// IDEMPOTENCY_EXPIRED are idempotency-specific in the AdCP enum; missing or
// malformed keys are surfaced as the generic INVALID_REQUEST code and carry
// a Field value of "idempotency_key" so callers can handle them specifically
// without inventing codes that won't round-trip across SDKs.
const (
	CodeIdempotencyConflict = "IDEMPOTENCY_CONFLICT"
	CodeIdempotencyExpired  = "IDEMPOTENCY_EXPIRED"
	CodeIdempotencyInFlight = "IDEMPOTENCY_IN_FLIGHT"
	CodeInvalidRequest      = "INVALID_REQUEST"
)

// ErrRecordFailed wraps a backend error from storing a successful response:
// the handler ran but its result is not recorded, so a retry could execute
// it again. Callers should reconcile by natural key.
var ErrRecordFailed = errors.New("idempotency: response could not be recorded")

// ErrReleaseFailed wraps a backend error from releasing a claim after the
// handler failed: the key stays fenced until reconciled.
var ErrReleaseFailed = errors.New("idempotency: claim could not be released")

// ErrOutcomeUnknown marks a handler error after which the handler may or may
// not have taken effect. Wrap it into the error a Handler returns (errors.Is
// must match) to keep the key's claim fenced instead of releasing it: retries
// get IDEMPOTENCY_IN_FLIGHT until an operator reconciles the key. Wrap
// returns the handler's error unchanged.
var ErrOutcomeUnknown = errors.New("idempotency: request outcome is unknown")

// ConflictError is returned when an idempotency key is reused with a different
// canonicalized payload. Recovery is caller-driven: either resend the original
// payload or mint a fresh key.
type ConflictError struct {
	Key string
}

func (e *ConflictError) Error() string {
	return fmt.Sprintf("idempotency: key %s reused with a different payload", LogKey(e.Key))
}

// Code returns the protocol error code.
func (*ConflictError) Code() string { return CodeIdempotencyConflict }

// ExpiredError is returned when an idempotency key was accepted previously but
// is now past the seller's replay window. Callers should natural-key-check
// before minting a fresh key to avoid double-create.
type ExpiredError struct {
	Key string
}

func (e *ExpiredError) Error() string {
	return fmt.Sprintf("idempotency: key %s is past its replay window", LogKey(e.Key))
}

// Code returns the protocol error code.
func (*ExpiredError) Code() string { return CodeIdempotencyExpired }

// MissingKeyError is returned when a required idempotency_key is absent from
// a mutating request. It maps to INVALID_REQUEST with Field="idempotency_key".
type MissingKeyError struct{}

func (*MissingKeyError) Error() string {
	return "idempotency: mutating request requires idempotency_key"
}

// Code returns the protocol error code.
func (*MissingKeyError) Code() string { return CodeInvalidRequest }

// Field names the request field at fault, for envelope error.field.
func (*MissingKeyError) Field() string { return "idempotency_key" }

// InvalidKeyError is returned when a provided idempotency_key fails format
// validation. The malformed key is not embedded to avoid log exposure. Maps
// to INVALID_REQUEST with Field="idempotency_key".
type InvalidKeyError struct {
	Reason string
}

func (e *InvalidKeyError) Error() string {
	if e.Reason == "" {
		return "idempotency: invalid idempotency_key format"
	}
	return "idempotency: invalid idempotency_key format: " + e.Reason
}

// Code returns the protocol error code.
func (*InvalidKeyError) Code() string { return CodeInvalidRequest }

// Field names the request field at fault, for envelope error.field.
func (*InvalidKeyError) Field() string { return "idempotency_key" }

// MissingCapabilityError is a client-side condition: the seller's
// get_adcp_capabilities response did not declare
// adcp.idempotency.replay_ttl_seconds. Per spec, clients MUST NOT assume a
// default. This error is not transmitted over the wire, so it has no Code().
type MissingCapabilityError struct {
	AgentID string
}

func (e *MissingCapabilityError) Error() string {
	if e.AgentID == "" {
		return "idempotency: seller capabilities missing adcp.idempotency.replay_ttl_seconds"
	}
	return "idempotency: seller " + e.AgentID + " capabilities missing adcp.idempotency.replay_ttl_seconds"
}

// InFlightError is returned when an earlier request with the same key and
// payload is still executing. Recovery is transient: retry after RetryAfter
// with the SAME key — minting a new key would turn a safe retry into a
// double execution.
type InFlightError struct {
	Key        string
	RetryAfter time.Duration
}

func (e *InFlightError) Error() string {
	return fmt.Sprintf("idempotency: key %s is still being processed", LogKey(e.Key))
}

// Code returns the protocol error code.
func (*InFlightError) Code() string { return CodeIdempotencyInFlight }
