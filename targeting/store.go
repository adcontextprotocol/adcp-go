package targeting

import (
	"context"
	"time"
)

// Store is a storage backend for the targeting engine.
// Implementations wrap Valkey or an in-memory mock.
type Store interface {
	// SetIsMember checks if member is in the set at key.
	SetIsMember(ctx context.Context, key, member string) (bool, error)

	// SetIntersect returns the intersection of sets at the given keys.
	SetIntersect(ctx context.Context, keys ...string) ([]string, error)

	// Get returns a string value. The bool is false if the key does not exist.
	Get(ctx context.Context, key string) (string, bool, error)

	// Set stores a string value with an optional TTL. Zero TTL means no expiry.
	Set(ctx context.Context, key, value string, ttl time.Duration) error

	// Exists checks if a key exists.
	Exists(ctx context.Context, key string) (bool, error)

	// SetMembers returns all members of the set at key. Returns nil if the key does not exist.
	SetMembers(ctx context.Context, key string) ([]string, error)

	// MGet returns the values for the given keys. Missing keys return "" at their index.
	MGet(ctx context.Context, keys ...string) ([]string, error)

	// MSet stores multiple key-value pairs with an optional TTL. Zero TTL means no expiry.
	// With a non-zero TTL, atomicity is implementation-defined; callers must not
	// assume that all keys land together.
	MSet(ctx context.Context, kvs map[string]string, ttl time.Duration) error
}
