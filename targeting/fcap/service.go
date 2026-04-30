package fcap

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"time"
)

// keyPrefix is the constant prefix for all fcap hash keys.
const keyPrefix = "fcap:"

// fieldValue is the constant value stored for every set field. HSETEX requires
// a value; the field name carries the meaning.
const fieldValue = "1"

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

// identityKey hashes userIdentity (SHA-256, first 16 bytes hex) and prefixes it.
// Matches the existing user-token hashing in the targeting package.
func identityKey(userIdentity string) string {
	h := sha256.Sum256([]byte(userIdentity))
	return keyPrefix + hex.EncodeToString(h[:16])
}

// fieldString joins seller_agent_url and package_id with a literal colon.
// The store never parses field names, so colons inside the URL component
// are unambiguous on read (write/read are symmetric).
func fieldString(f Field) string {
	return f.SellerAgentURL + ":" + f.PackageID
}
