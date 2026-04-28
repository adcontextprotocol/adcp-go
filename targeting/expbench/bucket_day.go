package expbench

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/redis/go-redis/v9"
)

// Bucket-day variant: SET per (user, UTC-day). Members are fcap_key
// hashes the user has seen on that day. Models the "1 per day" rule shape
// from the AppNexus-style bucket model — no exposure log, no scan, no
// preagg index.
//
//   Key:    user:exp:bd:{uid}:{utc_day}
//   Member: fcap_key_hash (8 bytes)
//   TTL:    25 hours (set on every SADD; idempotent EXPIRE is cheap)
//
// Eligibility under MaxCount=1 (singleton-per-day):
//   - SISMEMBER for single-key read
//   - SMEMBERS + client-side intersect for batch read (one RT regardless
//     of batch size)
//
// MaxCount>1 ("5 per day") would use the same shape with HASH fields and
// HINCRBY; not implemented here. Keeping the comparison focused on the
// dominant singleton case.

type BucketDayStore struct {
	rdb redis.Cmdable
}

func NewBucketDayStore(rdb redis.Cmdable) *BucketDayStore { return &BucketDayStore{rdb: rdb} }

func (s *BucketDayStore) Name() string { return "bucket-day" }

func bucketDayKey(userID string, dayEpoch int64) string {
	return fmt.Sprintf("user:exp:bd:%s:%d", userID, dayEpoch)
}

// utcDay returns the UTC day index (days since epoch) for the given unix
// timestamp. Daily buckets are simple integer division.
func utcDay(unixSec int64) int64 {
	return unixSec / 86400
}

// memberBytes encodes an 8-byte hash as a SET member.
func memberBytes(h uint64) string {
	buf := make([]byte, 8)
	binary.LittleEndian.PutUint64(buf, h)
	return string(buf)
}

func decodeMemberBytes(m string) uint64 {
	if len(m) < 8 {
		return 0
	}
	return binary.LittleEndian.Uint64([]byte(m))
}

// Seed bulk-writes the user's history. Each impression's K fcap_keys are
// SADDed to the SET keyed by that impression's UTC day. Older days are
// included even though TTL would have expired them in production — for
// the bench we want all days populated to measure realistic membership
// counts. (This means the bench measures "fully-populated 30 days of
// buckets," matching how the ZSET variants are seeded.)
func (s *BucketDayStore) Seed(ctx context.Context, userID string, imps []Impression) error {
	if len(imps) == 0 {
		return nil
	}
	pipe := s.rdb.Pipeline()
	flushEvery := 0
	for _, imp := range imps {
		day := utcDay(imp.Timestamp)
		key := bucketDayKey(userID, day)
		members := make([]any, 0, len(imp.FcapKeys))
		for _, k := range imp.FcapKeys {
			members = append(members, memberBytes(HashKey(k)))
		}
		pipe.SAdd(ctx, key, members...)
		pipe.Expire(ctx, key, 25*time.Hour)
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

// Write writes a single impression to today's bucket. K hashes SADDed +
// 25h EXPIRE, all in one RT.
func (s *BucketDayStore) Write(ctx context.Context, userID string, imp Impression) error {
	day := utcDay(imp.Timestamp)
	key := bucketDayKey(userID, day)
	members := make([]any, 0, len(imp.FcapKeys))
	for _, k := range imp.FcapKeys {
		members = append(members, memberBytes(HashKey(k)))
	}
	if len(members) == 0 {
		return nil
	}
	pipe := s.rdb.Pipeline()
	pipe.SAdd(ctx, key, members...)
	pipe.Expire(ctx, key, 25*time.Hour)
	_, err := pipe.Exec(ctx)
	return err
}

// ReadAndCheck answers "is this fcap_key in today's bucket?" Treats the
// rule as a singleton-per-day rule: capped iff member is present.
// MaxCount > 1 is not modeled here.
func (s *BucketDayStore) ReadAndCheck(ctx context.Context, userID string, fcapKeyHash uint64, rules []FrequencyRule, now int64) (capped bool, latestTS int64, err error) {
	day := utcDay(now)
	key := bucketDayKey(userID, day)
	present, err := s.rdb.SIsMember(ctx, key, memberBytes(fcapKeyHash)).Result()
	if err != nil {
		if errors.Is(err, redis.Nil) {
			return false, 0, nil
		}
		return false, 0, err
	}
	// latestTS is irrelevant in bucket model — bucket only carries presence,
	// not timestamps. Return 0; intent score in production would derive from
	// a separate signal if needed.
	if present {
		return true, 0, nil
	}
	return false, 0, nil
}

// ReadBatchCheck answers eligibility for many fcap_keys with a single
// SMEMBERS + client-side intersect. Single RT regardless of batch size.
func (s *BucketDayStore) ReadBatchCheck(ctx context.Context, userID string, fcapKeyHashes []uint64, rules []FrequencyRule, now int64) (cappedByKey map[uint64]bool, err error) {
	day := utcDay(now)
	key := bucketDayKey(userID, day)
	res, err := s.rdb.SMembers(ctx, key).Result()
	if err != nil && !errors.Is(err, redis.Nil) {
		return nil, err
	}
	seen := make(map[uint64]struct{}, len(res))
	for _, m := range res {
		seen[decodeMemberBytes(m)] = struct{}{}
	}
	cappedByKey = make(map[uint64]bool, len(fcapKeyHashes))
	for _, kh := range fcapKeyHashes {
		_, present := seen[kh]
		cappedByKey[kh] = present
	}
	return cappedByKey, nil
}

// Cleanup is a no-op in bucket-day — TTL handles it server-side. Kept on
// the interface for symmetry; the call costs essentially nothing.
func (s *BucketDayStore) Cleanup(ctx context.Context, userID string, now int64, window time.Duration) error {
	// SCAN + DEL would forcibly remove old buckets; in practice TTL handles
	// it. Run a no-op SCAN so the bench has something to measure for the
	// "cleanup" column.
	cursor := uint64(0)
	pattern := "user:exp:bd:" + userID + ":*"
	for {
		keys, next, err := s.rdb.Scan(ctx, cursor, pattern, 100).Result()
		if err != nil {
			return err
		}
		_ = keys // we don't actually delete; TTL handles it
		cursor = next
		if cursor == 0 {
			break
		}
	}
	return nil
}

// MemoryUsage sums valkey-reported memory for all of the user's per-day
// SETs (discovered via SCAN).
func (s *BucketDayStore) MemoryUsage(ctx context.Context, userID string) (int64, error) {
	pattern := "user:exp:bd:" + userID + ":*"
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

// Reset deletes all of the user's per-day buckets.
func (s *BucketDayStore) Reset(ctx context.Context, userID string) error {
	pattern := "user:exp:bd:" + userID + ":*"
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

// Fetch for the bucket variant: SMEMBERS on today's bucket. Returns the
// members in FetchResult.ZMembers (re-using the field; score is unused).
func (s *BucketDayStore) Fetch(ctx context.Context, userID string, window time.Duration, now int64) (FetchResult, error) {
	day := utcDay(now)
	key := bucketDayKey(userID, day)
	res, err := s.rdb.SMembers(ctx, key).Result()
	if err != nil && !errors.Is(err, redis.Nil) {
		return FetchResult{}, err
	}
	zs := make([]redis.Z, len(res))
	for i, m := range res {
		zs[i] = redis.Z{Member: m}
	}
	return FetchResult{ZMembers: zs}, nil
}

// Process for the bucket variant: build a hash set from members,
// intersect with candidates.
func (s *BucketDayStore) Process(raw FetchResult, fcapKeyHashes []uint64, rules []FrequencyRule, now int64) map[uint64]bool {
	seen := make(map[uint64]struct{}, len(raw.ZMembers))
	for _, z := range raw.ZMembers {
		seen[decodeMemberBytes(z.Member.(string))] = struct{}{}
	}
	out := make(map[uint64]bool, len(fcapKeyHashes))
	for _, kh := range fcapKeyHashes {
		_, present := seen[kh]
		out[kh] = present
	}
	return out
}

// Compile-time check.
var _ SplitVariant = (*BucketDayStore)(nil)

// avoid unused import in linters
var _ = strconv.Itoa
