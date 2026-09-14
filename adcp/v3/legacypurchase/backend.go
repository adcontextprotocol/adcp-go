package legacypurchase

import (
	"context"
	"time"
)

// Backend is the pluggable durable-store surface for legacy_create
// continuation coordination. Implementations MUST be safe for concurrent
// use, and MUST implement PutContinuation, ClaimPending, CompletePending,
// and FailPending as atomic operations against their storage engine (e.g. a
// unique constraint on Token plus an `UPDATE ... WHERE token = $1 AND
// state = 'offered'` for ClaimPending, mirroring
// adcp/v3/idempotency.Backend's PutIfAbsent contract).
//
// This is a three-state claim/commit/fail FSM rather than idempotency's
// single PutIfAbsent because redeeming a legacy_create continuation is a
// two-phase operation: claim the token, then call an external legacy
// seller whose outcome is not known at claim time. The atomic-claim and
// record-outcome steps cannot be the same write — a crash between them
// must be observable as StatePending, not silently lost (double-purchase
// risk) or silently fabricated as a result.
type Backend interface {
	// PutContinuation durably records a newly offered continuation.
	// Returns *DuplicateTokenError if rec.Token already exists.
	PutContinuation(ctx context.Context, rec *ContinuationRecord) error

	// GetContinuation returns the current record for token, or (nil, nil)
	// on miss.
	GetContinuation(ctx context.Context, token string) (*ContinuationRecord, error)

	// ClaimPending atomically transitions token from StateOffered to
	// StatePending, recording claimantKey (the idempotency_key redeeming
	// it) and requestHash (a canonical hash of the full redemption input,
	// for retry-conflict detection). claimed=false means the token was not
	// in StateOffered at the time of the attempt — it does not exist, or
	// it has already been claimed by this or another idempotency_key. The
	// returned rec is the current record either way, so the caller can
	// proceed without a second round trip.
	ClaimPending(ctx context.Context, token, claimantKey, requestHash string, claimedAt time.Time) (rec *ContinuationRecord, claimed bool, err error)

	// CompletePending atomically transitions a StatePending record claimed
	// by claimantKey to StateCommitted, storing result. ok=false if the
	// record is not currently StatePending and claimed by claimantKey
	// (defensive — should not happen absent a caller bug or a second
	// coordinator instance racing on the same token, which ClaimPending's
	// atomicity already prevents).
	CompletePending(ctx context.Context, token, claimantKey string, result []byte, completedAt time.Time) (rec *ContinuationRecord, ok bool, err error)

	// FailPending is CompletePending's terminal-failure counterpart:
	// StatePending -> StateFailed, recording errCode/errMessage as
	// recovery guidance for the caller.
	FailPending(ctx context.Context, token, claimantKey, errCode, errMessage string, failedAt time.Time) (rec *ContinuationRecord, ok bool, err error)
}
