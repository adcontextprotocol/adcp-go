package audience

import "context"

// Store is the low-level backend for audience membership state. Implementations
// operate on raw hash and set keys; the Service layer hashes user identities
// and formats keys before calling Store.
type Store interface {
	// HSetBatch performs HSET for multiple (key, field, value) triples in a
	// single pipelined round-trip. Items targeting the same key are grouped
	// into one HSET command per implementation.
	HSetBatch(ctx context.Context, items []HSetItem) error

	// HExists reports whether field exists under key (HEXISTS).
	HExists(ctx context.Context, key, field string) (bool, error)

	// HExistsBatch checks one field per key for multiple (key, field) pairs.
	// Result order matches the input order.
	HExistsBatch(ctx context.Context, lookups []HLookup) ([]bool, error)

	// HGetAll returns all (field, value) pairs for key (HGETALL). Returns an
	// empty map when key does not exist.
	HGetAll(ctx context.Context, key string) (map[string]string, error)

	// HGetAllBatch returns HGETALL results for multiple keys, parallel to
	// the input slice order. Missing keys produce empty maps at their index.
	HGetAllBatch(ctx context.Context, keys []string) ([]map[string]string, error)

	// HDel removes the named fields from the hash at key (HDEL). Returns nil
	// when fields is empty.
	HDel(ctx context.Context, key string, fields []string) error

	// HDelBatch performs HDEL for multiple (key, fields) pairs in a single
	// pipelined round-trip.
	HDelBatch(ctx context.Context, items []HDelItem) error

	// SAdd adds members to the set at key (SADD). Returns nil when members
	// is empty.
	SAdd(ctx context.Context, key string, members []string) error

	// SRem removes members from the set at key (SREM). Returns nil when
	// members is empty.
	SRem(ctx context.Context, key string, members []string) error

	// SMembers returns all members of the set at key (SMEMBERS). Returns nil
	// when key does not exist.
	SMembers(ctx context.Context, key string) ([]string, error)

	// Del removes the key entirely (DEL).
	Del(ctx context.Context, key string) error
}

// HSetItem is one entry in a multi-key HSET batch.
type HSetItem struct {
	Key   string
	Field string
	Value string
}

// HLookup is one (key, field) pair for batch HEXISTS.
type HLookup struct {
	Key   string
	Field string
}

// HDelItem is one (key, fields) pair for batch HDEL.
type HDelItem struct {
	Key    string
	Fields []string
}
