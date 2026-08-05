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
