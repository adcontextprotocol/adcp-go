package expbench

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/redis/go-redis/v9"
)

// Bucket-count variant: HASH per (user, UTC-day). Fields are fcap_key
// hashes (hex-encoded for HASH-friendly strings); values are integer
// counters incremented per impression. Models the "N per day" rule shape
// where N > 1.
//
//   Key:    user:exp:bc:{uid}:{utc_day}
//   Field:  hex(fcap_key_hash)
//   Value:  count
//   TTL:    25 hours on the whole HASH
//
// Eligibility under MaxCount=N:
//   - HGET for single-key read: capped iff count >= N
//   - HMGET for batch read: one RT, returns counts for all candidates
//
// Same period-bucketing pattern as bucket-day but supports counters
// instead of presence-only.

type BucketCountStore struct {
	rdb redis.Cmdable
}

func NewBucketCountStore(rdb redis.Cmdable) *BucketCountStore { return &BucketCountStore{rdb: rdb} }

func (s *BucketCountStore) Name() string { return "bucket-count" }

func bucketCountKey(userID string, dayEpoch int64) string {
	return fmt.Sprintf("user:exp:bc:%s:%d", userID, dayEpoch)
}

func fieldName(h uint64) string {
	return strconv.FormatUint(h, 16)
}

// Seed bulk-writes the user's history. Each impression's K fcap_keys are
// HINCRBY'd in the HASH keyed by that impression's UTC day, plus a single
// EXPIRE per affected day.
func (s *BucketCountStore) Seed(ctx context.Context, userID string, imps []Impression) error {
	if len(imps) == 0 {
		return nil
	}
	pipe := s.rdb.Pipeline()
	flushEvery := 0
	expiredKeys := make(map[string]struct{})
	for _, imp := range imps {
		day := utcDay(imp.Timestamp)
		key := bucketCountKey(userID, day)
		for _, k := range imp.FcapKeys {
			pipe.HIncrBy(ctx, key, fieldName(HashKey(k)), 1)
		}
		if _, set := expiredKeys[key]; !set {
			pipe.Expire(ctx, key, 25*time.Hour)
			expiredKeys[key] = struct{}{}
		}
		flushEvery++
		if flushEvery%1000 == 0 {
			if _, err := pipe.Exec(ctx); err != nil {
				return err
			}
			pipe = s.rdb.Pipeline()
		}
	}
	_, err := pipe.Exec(ctx)
	return err
}

// Write writes a single impression. K HINCRBY + 1 EXPIRE pipelined.
func (s *BucketCountStore) Write(ctx context.Context, userID string, imp Impression) error {
	day := utcDay(imp.Timestamp)
	key := bucketCountKey(userID, day)
	if len(imp.FcapKeys) == 0 {
		return nil
	}
	pipe := s.rdb.Pipeline()
	for _, k := range imp.FcapKeys {
		pipe.HIncrBy(ctx, key, fieldName(HashKey(k)), 1)
	}
	pipe.Expire(ctx, key, 25*time.Hour)
	_, err := pipe.Exec(ctx)
	return err
}

// ReadAndCheck answers "has the count for this fcap_key in today's bucket
// reached MaxCount?"
func (s *BucketCountStore) ReadAndCheck(ctx context.Context, userID string, fcapKeyHash uint64, rules []FrequencyRule, now int64) (capped bool, latestTS int64, err error) {
	day := utcDay(now)
	key := bucketCountKey(userID, day)
	val, err := s.rdb.HGet(ctx, key, fieldName(fcapKeyHash)).Int64()
	if err != nil {
		if errors.Is(err, redis.Nil) {
			return false, 0, nil
		}
		return false, 0, err
	}
	for _, rule := range rules {
		if val >= int64(rule.MaxCount) {
			return true, 0, nil
		}
	}
	return false, 0, nil
}

// ReadBatchCheck answers eligibility for many fcap_keys. Single HGETALL
// returns all of the user's seen-counts for today; client-side intersects
// with the candidate keys and compares to MaxCount. This matches the
// shape of bucket-day's SMEMBERS+intersect pattern: one RT, response
// size scales with what's actually in the bucket, not the request batch.
func (s *BucketCountStore) ReadBatchCheck(ctx context.Context, userID string, fcapKeyHashes []uint64, rules []FrequencyRule, now int64) (cappedByKey map[uint64]bool, err error) {
	day := utcDay(now)
	key := bucketCountKey(userID, day)
	all, err := s.rdb.HGetAll(ctx, key).Result()
	if err != nil && !errors.Is(err, redis.Nil) {
		return nil, err
	}
	counts := make(map[uint64]int64, len(all))
	for f, v := range all {
		kh, err := strconv.ParseUint(f, 16, 64)
		if err != nil {
			continue
		}
		c, err := strconv.ParseInt(v, 10, 64)
		if err != nil {
			continue
		}
		counts[kh] = c
	}
	maxCount := 0
	for _, rule := range rules {
		if rule.MaxCount > maxCount {
			maxCount = rule.MaxCount
		}
	}
	cappedByKey = make(map[uint64]bool, len(fcapKeyHashes))
	for _, kh := range fcapKeyHashes {
		cappedByKey[kh] = counts[kh] >= int64(maxCount)
	}
	return cappedByKey, nil
}

// Cleanup is a no-op — TTL handles it. SCAN walk for symmetry with other
// variants' bench timing.
func (s *BucketCountStore) Cleanup(ctx context.Context, userID string, now int64, window time.Duration) error {
	cursor := uint64(0)
	pattern := "user:exp:bc:" + userID + ":*"
	for {
		_, next, err := s.rdb.Scan(ctx, cursor, pattern, 100).Result()
		if err != nil {
			return err
		}
		cursor = next
		if cursor == 0 {
			break
		}
	}
	return nil
}

// MemoryUsage sums valkey-reported memory for all of the user's per-day
// HASHes.
func (s *BucketCountStore) MemoryUsage(ctx context.Context, userID string) (int64, error) {
	pattern := "user:exp:bc:" + userID + ":*"
	var total int64
	cursor := uint64(0)
	for {
		keys, next, err := s.rdb.Scan(ctx, cursor, pattern, 100).Result()
		if err != nil {
			return total, err
		}
		for _, k := range keys {
			if u, err := s.rdb.MemoryUsage(ctx, k).Result(); err == nil {
				total += u
			}
		}
		cursor = next
		if cursor == 0 {
			break
		}
	}
	return total, nil
}

// Reset deletes all of the user's per-day HASHes.
func (s *BucketCountStore) Reset(ctx context.Context, userID string) error {
	pattern := "user:exp:bc:" + userID + ":*"
	cursor := uint64(0)
	for {
		keys, next, err := s.rdb.Scan(ctx, cursor, pattern, 100).Result()
		if err != nil {
			return err
		}
		if len(keys) > 0 {
			if err := s.rdb.Del(ctx, keys...).Err(); err != nil {
				return err
			}
		}
		cursor = next
		if cursor == 0 {
			break
		}
	}
	return nil
}

// --- Split bench support ---

// Fetch for the bucket-count variant: HGETALL on today's HASH. Re-encodes
// the result as redis.Z entries so the existing FetchResult plumbing works.
// Score = count, Member = hex(fcap_key_hash).
func (s *BucketCountStore) Fetch(ctx context.Context, userID string, window time.Duration, now int64) (FetchResult, error) {
	day := utcDay(now)
	key := bucketCountKey(userID, day)
	res, err := s.rdb.HGetAll(ctx, key).Result()
	if err != nil && !errors.Is(err, redis.Nil) {
		return FetchResult{}, err
	}
	zs := make([]redis.Z, 0, len(res))
	for f, v := range res {
		count, _ := strconv.ParseFloat(v, 64)
		zs = append(zs, redis.Z{Score: count, Member: f})
	}
	return FetchResult{ZMembers: zs}, nil
}

// Process for the bucket-count variant: count map → check against MaxCount.
func (s *BucketCountStore) Process(raw FetchResult, fcapKeyHashes []uint64, rules []FrequencyRule, now int64) map[uint64]bool {
	counts := make(map[uint64]int64, len(raw.ZMembers))
	for _, z := range raw.ZMembers {
		field, ok := z.Member.(string)
		if !ok {
			continue
		}
		kh, err := strconv.ParseUint(field, 16, 64)
		if err != nil {
			continue
		}
		counts[kh] = int64(z.Score)
	}
	maxCount := 0
	for _, rule := range rules {
		if rule.MaxCount > maxCount {
			maxCount = rule.MaxCount
		}
	}
	out := make(map[uint64]bool, len(fcapKeyHashes))
	for _, kh := range fcapKeyHashes {
		out[kh] = counts[kh] >= int64(maxCount)
	}
	return out
}

var _ SplitVariant = (*BucketCountStore)(nil)
