package idempotency

import "fmt"

// Protocol error codes this package maps onto. Only IDEMPOTENCY_CONFLICT and
// IDEMPOTENCY_EXPIRED are idempotency-specific in the AdCP enum; missing or
// malformed keys are surfaced as the generic INVALID_REQUEST code and carry
// a Field value of "idempotency_key" so callers can handle them specifically
// without inventing codes that won't round-trip across SDKs.
const (
	CodeIdempotencyConflict = "IDEMPOTENCY_CONFLICT"
	CodeIdempotencyExpired  = "IDEMPOTENCY_EXPIRED"
	CodeInvalidRequest      = "INVALID_REQUEST"
)

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
