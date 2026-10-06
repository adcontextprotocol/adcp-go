package idempotency

import (
	"context"
	"time"
)

// Entry is the persisted record for one idempotency key.
// Response holds the encoded inner handler response. The envelope
// `replayed` flag is injected at response time, not stored.
type Entry struct {
	Hash      string
	Response  []byte
	CreatedAt time.Time
	ExpiresAt time.Time
}

// Backend is the pluggable storage surface for the idempotency middleware.
// Implementations MUST be safe for concurrent use.
//
// Scope is a namespace under which keys are unique. The middleware passes a
// per-principal (or principal+session) scope so keys from different
// principals cannot collide.
type Backend interface {
	// Get returns the entry for (scope, key), or (nil, nil) on miss.
	// Expired entries MAY be returned; the middleware checks TTL.
	Get(ctx context.Context, scope, key string) (*Entry, error)

	// PutIfAbsent stores entry only if (scope, key) has no existing record.
	// On race, it returns the winning entry and stored=false so the caller can
	// hash-compare without an extra round trip.
	PutIfAbsent(ctx context.Context, scope, key string, entry *Entry) (existing *Entry, stored bool, err error)
}

// ClaimBackend is a Backend that can fence an in-flight claim. Store.Wrap
// writes a claim before running the handler when the backend implements
// this, so concurrent duplicates never execute twice. MemoryBackend and
// PgBackend implement it; custom backends without it keep the legacy
// execute-then-store behavior.
type ClaimBackend interface {
	Backend
	// ReplaceIfHash atomically replaces (scope, key) only when its current
	// Hash equals oldHash. It reports whether the row was replaced. The
	// store uses it to record, release and reclaim claims.
	ReplaceIfHash(ctx context.Context, scope, key, oldHash string, entry *Entry) (bool, error)
}
