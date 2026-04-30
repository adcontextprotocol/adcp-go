package fcap

import (
	"context"
	"time"
)

// Store is the low-level backend for fcap state. Implementations operate on
// raw Valkey hash keys and field names. The Service layer hashes user
// identities and formats field strings before calling into Store.
type Store interface {
	// SetFields creates or updates fields under key with a shared expiration
	// timestamp. Backed by HSETEX in Valkey 8+.
	SetFields(ctx context.Context, key string, fields map[string]string, expireAt time.Time) error

	// SetFieldsBatch performs SetFields for multiple (key, fields, expireAt)
	// triples, ideally in a single pipeline.
	SetFieldsBatch(ctx context.Context, batches []FieldsBatch) error

	// FieldExists reports whether field exists under key. Backed by HEXISTS.
	FieldExists(ctx context.Context, key, field string) (bool, error)

	// FieldExistsBatch checks one field per key for multiple keys.
	// Result order matches the input order.
	FieldExistsBatch(ctx context.Context, lookups []FieldLookup) ([]bool, error)
}

// FieldsBatch is one entry in a multi-key write batch.
type FieldsBatch struct {
	Key      string
	Fields   map[string]string
	ExpireAt time.Time
}

// FieldLookup is one entry in a multi-key read batch.
type FieldLookup struct {
	Key   string
	Field string
}
