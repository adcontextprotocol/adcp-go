package expbench

import (
	"context"
	"time"
)

// Variant is the common interface implemented by each storage shape so the
// bench harness can iterate over them uniformly.
type Variant interface {
	Name() string

	// Seed bulk-writes a user's full log. Used to populate steady-state
	// before running per-operation latency loops.
	Seed(ctx context.Context, userID string, imps []Impression) error

	// Write writes one impression. Returns latency by virtue of being timed
	// by the caller.
	Write(ctx context.Context, userID string, imp Impression) error

	// ReadAndCheck answers a single eligibility question.
	ReadAndCheck(ctx context.Context, userID string, fcapKeyHash uint64, rules []FrequencyRule, now int64) (capped bool, latestTS int64, err error)

	// ReadBatchCheck answers eligibility for many fcap_keys against the
	// same user log in one fetch.
	ReadBatchCheck(ctx context.Context, userID string, fcapKeyHashes []uint64, rules []FrequencyRule, now int64) (cappedByKey map[uint64]bool, err error)

	// Cleanup drops entries older than (now - window).
	Cleanup(ctx context.Context, userID string, now int64, window time.Duration) error

	// MemoryUsage returns the byte size of the user's log in valkey.
	MemoryUsage(ctx context.Context, userID string) (int64, error)

	// Reset deletes all bench data for this user.
	Reset(ctx context.Context, userID string) error
}
