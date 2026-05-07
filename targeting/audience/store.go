package audience

import "context"

// Store is the low-level backend for audience membership state. Implementations
// operate on raw hash keys; the Service layer hashes user identities and
// formats keys before calling Store.
type Store interface {
	// HSetBatch performs HSET for multiple (key, field, value) triples in a
	// single pipelined round-trip. Items targeting the same key are grouped
	// into one HSET command per implementation.
	HSetBatch(ctx context.Context, items []HSetItem) error

	// HExistsBatch checks one field per key for multiple (key, field) pairs.
	// Result order matches the input order.
	HExistsBatch(ctx context.Context, lookups []HLookup) ([]bool, error)

	// HGetAll returns all (field, value) pairs for key (HGETALL). Returns an
	// empty map when key does not exist.
	HGetAll(ctx context.Context, key string) (map[string]string, error)

	// HGetAllBatch returns HGETALL results for multiple keys, parallel to
	// the input slice order. Missing keys produce empty maps at their index.
	HGetAllBatch(ctx context.Context, keys []string) ([]map[string]string, error)

	// HDelBatch performs HDEL for multiple (key, fields) pairs in a single
	// pipelined round-trip.
	HDelBatch(ctx context.Context, items []HDelItem) error
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
