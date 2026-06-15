package fcap

import (
	"context"
	"fmt"
	"math"
	"sync"
	"time"

	"github.com/adcontextprotocol/adcp-go/targeting/identityhash"
)

const (
	// keyPrefix is the constant prefix for all fcap hash keys.
	keyPrefix = "fcap:"

	// fieldDelimiter joins SellerAgentURL and PackageID into the HSET field name.
	// The store never parses field names; reads and writes are symmetric, so a
	// literal colon is unambiguous in practice. Keep this stable: changing it
	// invalidates every existing field name on disk.
	fieldDelimiter = ":"

	// fieldValue is the constant value stored for every set field. HSETEX
	// requires a value; the field name carries the meaning.
	fieldValue = "1"
)

// CapBatch records caps for one user identity.
type CapBatch struct {
	UserIdentity string
	Fields       []Field
	ExpireAt     time.Time
}

// CapLookup describes a single cap check across users (one field per user).
type CapLookup struct {
	UserIdentity string
	Field        Field
}

// Service is the high-level frequency cap API. Callers pass raw user
// identities (id5, MAID, etc.); the Service hashes them and formats field
// names before delegating to Store.
type Service struct {
	store Store
}

// New constructs a Service backed by the provided Store.
func New(store Store) *Service {
	return &Service{store: store}
}

// RecordCap marks every field as capped for userIdentity until expireAt.
// All fields share the same expiration; HSETEX is preferred at the Store layer.
// A nil or empty fields slice is a no-op.
func (s *Service) RecordCap(ctx context.Context, userIdentity string, fields []Field, expireAt time.Time) error {
	if len(fields) == 0 {
		return nil
	}
	values := make(map[string]string, len(fields))
	for _, f := range fields {
		values[fieldString(f)] = fieldValue
	}
	return s.store.SetFields(ctx, identityKey(userIdentity), values, expireAt)
}

// RecordCapBatch records caps for multiple user identities. Each batch carries
// its own (fields, expireAt) pair; expireAt is shared within a batch but may
// differ between batches.
func (s *Service) RecordCapBatch(ctx context.Context, batches []CapBatch) error {
	if len(batches) == 0 {
		return nil
	}
	storeBatches := make([]FieldsBatch, 0, len(batches))
	for _, b := range batches {
		if len(b.Fields) == 0 {
			continue
		}
		values := make(map[string]string, len(b.Fields))
		for _, f := range b.Fields {
			values[fieldString(f)] = fieldValue
		}
		storeBatches = append(storeBatches, FieldsBatch{
			Key:      identityKey(b.UserIdentity),
			Fields:   values,
			ExpireAt: b.ExpireAt,
		})
	}
	if len(storeBatches) == 0 {
		return nil
	}
	return s.store.SetFieldsBatch(ctx, storeBatches)
}

// IsCapped reports whether the (userIdentity, field) tuple is currently capped.
func (s *Service) IsCapped(ctx context.Context, userIdentity string, field Field) (bool, error) {
	return s.store.FieldExists(ctx, identityKey(userIdentity), fieldString(field))
}

// IsCappedAny reports, per field, whether AT LEAST ONE of the supplied user
// identities is currently capped on that field. The result has length
// len(fields); cappedByField[i] corresponds to fields[i].
//
// Designed for the per-request hot path: identities × fields are sent as a
// single pipelined batch to the store, with the scratch buffer carrying the
// cross-product retrieved from a sync.Pool so the per-request allocation
// scales with the result slice (one per call) rather than N×M.
//
// Both input slices are read-only and must not be retained past return.
// Empty fields returns (nil, nil) without touching the store. Empty identities
// (with non-empty fields) returns an all-false result of length len(fields) —
// nothing can be capped without an identity — preserving the length-len(fields)
// contract callers index against.
//
// Goroutine-safe: each invocation uses its own scratch buffer and shares no
// state with concurrent callers. The store call inherits the supplied ctx
// and routes through the configured topology — cluster/shadow distribution
// is unchanged from IsCappedBatch.
func (s *Service) IsCappedAny(ctx context.Context, identities []string, fields []Field) ([]bool, error) {
	if len(fields) == 0 {
		return nil, nil
	}
	if len(identities) == 0 {
		// No identity can be capped, so nothing is capped. Return an all-false
		// result sized to fields (not nil) to preserve the length-len(fields)
		// contract; a nil result makes callers that index by field read out of
		// range.
		return make([]bool, len(fields)), nil
	}
	// Guard the cross-product against int overflow before it sizes the scratch
	// allocation. len(fields) is non-zero here (the early return above), so
	// bounding len(identities) by MaxInt/len(fields) proves the product stays
	// within int range; an oversized input fails closed instead of computing a
	// wrapped, corrupt allocation size.
	if len(identities) > math.MaxInt/len(fields) {
		return nil, fmt.Errorf("fcap: identity-field cross-product overflows (%d identities x %d fields)", len(identities), len(fields))
	}
	need := len(identities) * len(fields)

	bufPtr := fieldLookupBufPool.Get().(*[]FieldLookup)
	buf := *bufPtr
	if cap(buf) < need {
		buf = make([]FieldLookup, need)
	} else {
		buf = buf[:need]
	}
	defer func() {
		// Clear references so pooled buffers don't pin string heap.
		for i := range buf {
			buf[i] = FieldLookup{}
		}
		*bufPtr = buf[:0]
		fieldLookupBufPool.Put(bufPtr)
	}()

	for i, id := range identities {
		key := identityKey(id)
		base := i * len(fields)
		for j, f := range fields {
			buf[base+j] = FieldLookup{Key: key, Field: fieldString(f)}
		}
	}

	results, err := s.store.FieldExistsBatch(ctx, buf)
	if err != nil {
		return nil, err
	}

	out := make([]bool, len(fields))
	for i := range identities {
		base := i * len(fields)
		for j := range fields {
			if results[base+j] {
				out[j] = true
			}
		}
	}
	return out, nil
}

// fieldLookupBufPool reuses []FieldLookup scratch buffers across IsCappedAny
// calls. Pool entries are *[]FieldLookup so Get/Put avoid boxing the slice
// header into an interface{}. The Reset behavior clears each entry before
// returning a buffer to the pool so pooled storage doesn't pin string heap.
var fieldLookupBufPool = sync.Pool{
	New: func() any {
		s := make([]FieldLookup, 0, 64)
		return &s
	},
}

// IsCappedBatch reports cap state for many (user, field) tuples. The result
// is parallel to lookups: index i is the answer for lookups[i].
func (s *Service) IsCappedBatch(ctx context.Context, lookups []CapLookup) ([]bool, error) {
	if len(lookups) == 0 {
		return nil, nil
	}
	storeLookups := make([]FieldLookup, len(lookups))
	for i, l := range lookups {
		storeLookups[i] = FieldLookup{
			Key:   identityKey(l.UserIdentity),
			Field: fieldString(l.Field),
		}
	}
	return s.store.FieldExistsBatch(ctx, storeLookups)
}

// identityKey hashes userIdentity and prefixes it with keyPrefix.
func identityKey(userIdentity string) string {
	return keyPrefix + identityhash.Hash(userIdentity)
}

// fieldString joins SellerAgentURL and PackageID with fieldDelimiter.
func fieldString(f Field) string {
	return f.SellerAgentURL + fieldDelimiter + f.PackageID
}
