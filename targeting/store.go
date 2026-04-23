package targeting

import (
	"context"
	"time"
)

// Store key prefixes.
const (
	keyPrefixPackageAudience = "audience:"
	keyPrefixUserExposures   = "user:exposures:"
	keyPrefixTopicsArtifact  = "topics:artifact:"
	keyPrefixTopicsPackage   = "topics:package:"
	keyPrefixURLBlocklist    = "url:blocklist:"
	keyPrefixURLAllowlist    = "url:allowlist:"
	keyPrefixMediaBuySeller  = "mediabuy:seller:"
	keyPrefixMediaBuy        = "mediabuy:"
	keyPrefixConfigPkg       = "config:pkg:"
	keyPrefixConfigCampaign  = "config:campaign:"
)

// Store is a storage backend for the targeting engine.
// Implementations wrap Valkey/Redis or an in-memory mock.
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

	// ZAdd adds a member with the given score to a sorted set.
	ZAdd(ctx context.Context, key string, score float64, member string) error

	// ZCount returns the number of members with scores in [min, max].
	ZCount(ctx context.Context, key string, min, max float64) (int64, error)

	// ZExpire sets a TTL on a sorted set key. Zero TTL means no expiry.
	ZExpire(ctx context.Context, key string, ttl time.Duration) error

	// ZRemRangeByScore removes members with scores in [min, max] from a sorted set.
	ZRemRangeByScore(ctx context.Context, key string, min, max float64) error

	// SetMembers returns all members of the set at key. Returns nil if the key does not exist.
	SetMembers(ctx context.Context, key string) ([]string, error)

	// MGet returns the values for the given keys. Missing keys return "" at their index.
	MGet(ctx context.Context, keys ...string) ([]string, error)

	// MSet stores multiple key-value pairs with an optional TTL. Zero TTL means no expiry.
	MSet(ctx context.Context, kvs map[string]string, ttl time.Duration) error

	// Del removes a key. It is a no-op if the key does not exist.
	Del(ctx context.Context, key string) error

	// MDel removes multiple keys. It is a no-op for keys that do not exist.
	MDel(ctx context.Context, keys ...string) error

	// HSet sets a single field in a hash.
	HSet(ctx context.Context, key, field, value string) error

	// HMSet sets multiple fields in a hash.
	HMSet(ctx context.Context, key string, fields map[string]string) error

	// HGet returns the value of a hash field. The bool is false if the field does not exist.
	HGet(ctx context.Context, key, field string) (string, bool, error)

	// HMGet returns the values of multiple hash fields. Missing fields return "" at their index.
	HMGet(ctx context.Context, key string, fields ...string) ([]string, error)

	// HMGetBatch returns the values of the given fields for each key.
	// The result slice is always len(keys) long; each entry is len(fields) long ("" for missing fields).
	// In Valkey this is a single pipelined round-trip.
	HMGetBatch(ctx context.Context, keys []string, fields []string) ([][]string, error)

	// HDel removes fields from a hash. It is a no-op for fields that do not exist.
	HDel(ctx context.Context, key string, fields ...string) error
}
