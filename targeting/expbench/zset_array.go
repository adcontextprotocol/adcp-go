package expbench

import (
	"context"
	"encoding/binary"
	"errors"
	"strconv"
	"time"

	"github.com/redis/go-redis/v9"
)

// ZSet-array variant: one ZSET per user. Each ZADD writes one member that
// encodes a single impression with all of its fcap_key hashes inline.
//
// Member layout: impHash(8) + keyCount(1) + fcapKeyHash[0..keyCount-1](8 each)
// Score: timestamp (unix seconds, stored as float64).
//
// Read uses ZRANGEBYSCORE for server-side window filtering.

type ZSetArrayStore struct {
	rdb redis.Cmdable
}

func NewZSetArrayStore(rdb redis.Cmdable) *ZSetArrayStore { return &ZSetArrayStore{rdb: rdb} }

func (s *ZSetArrayStore) Name() string { return "zset-array" }

func zsetArrayKey(userID string) string { return "user:exposures:zsa:" + userID }

func encodeArrayMember(imp Impression) string {
	keys := imp.FcapKeys
	if len(keys) > MaxKeysPerImpression {
		keys = keys[:MaxKeysPerImpression]
	}
	buf := make([]byte, 9+len(keys)*8)
	binary.LittleEndian.PutUint64(buf[0:], HashKey(imp.ImpressionID))
	buf[8] = byte(len(keys))
	for i, k := range keys {
		binary.LittleEndian.PutUint64(buf[9+i*8:], HashKey(k))
	}
	return string(buf)
}

func decodeArrayMember(m string) (impHash uint64, keyHashes []uint64) {
	if len(m) < 9 {
		return 0, nil
	}
	b := []byte(m)
	impHash = binary.LittleEndian.Uint64(b[0:])
	kc := int(b[8])
	if 9+kc*8 > len(b) {
		return impHash, nil
	}
	keyHashes = make([]uint64, kc)
	for i := 0; i < kc; i++ {
		keyHashes[i] = binary.LittleEndian.Uint64(b[9+i*8:])
	}
	return impHash, keyHashes
}

// Seed bulk-writes a user's full log via a single pipelined ZADD batch.
func (s *ZSetArrayStore) Seed(ctx context.Context, userID string, imps []Impression) error {
	if len(imps) == 0 {
		return nil
	}
	key := zsetArrayKey(userID)
	zs := make([]redis.Z, 0, len(imps))
	for _, imp := range imps {
		zs = append(zs, redis.Z{
			Score:  float64(imp.Timestamp),
			Member: encodeArrayMember(imp),
		})
	}
	return s.rdb.ZAdd(ctx, key, zs...).Err()
}

// Write writes a single impression. One ZADD round-trip.
func (s *ZSetArrayStore) Write(ctx context.Context, userID string, imp Impression) error {
	return s.rdb.ZAdd(ctx, zsetArrayKey(userID), redis.Z{
		Score:  float64(imp.Timestamp),
		Member: encodeArrayMember(imp),
	}).Err()
}

// ReadAndCheck answers a single eligibility check. Server-side window
// filter via ZRANGEBYSCORE; client iterates returned members.
func (s *ZSetArrayStore) ReadAndCheck(ctx context.Context, userID string, fcapKeyHash uint64, rules []FrequencyRule, now int64) (capped bool, latestTS int64, err error) {
	if len(rules) == 0 {
		return false, 0, nil
	}
	// Use the widest rule window for the fetch; smaller windows filter on the client.
	maxWindow := rules[0].Window
	for _, r := range rules[1:] {
		if r.Window > maxWindow {
			maxWindow = r.Window
		}
	}
	cutoff := now - int64(maxWindow.Seconds())
	res, err := s.rdb.ZRangeByScoreWithScores(ctx, zsetArrayKey(userID), &redis.ZRangeBy{
		Min: strconv.FormatInt(cutoff, 10),
		Max: "+inf",
	}).Result()
	if err != nil {
		if errors.Is(err, redis.Nil) {
			return false, 0, nil
		}
		return false, 0, err
	}

	type entry struct {
		impHash uint64
		ts      int64
	}
	matched := make([]entry, 0, len(res)/4)
	for _, z := range res {
		impHash, keyHashes := decodeArrayMember(z.Member.(string))
		for _, kh := range keyHashes {
			if kh == fcapKeyHash {
				ts := int64(z.Score)
				matched = append(matched, entry{impHash, ts})
				if ts > latestTS {
					latestTS = ts
				}
				break
			}
		}
	}
	for _, rule := range rules {
		ruleCutoff := now - int64(rule.Window.Seconds())
		seen := make(map[uint64]struct{})
		count := 0
		for _, e := range matched {
			if e.ts < ruleCutoff {
				continue
			}
			if _, dup := seen[e.impHash]; dup {
				continue
			}
			seen[e.impHash] = struct{}{}
			count++
		}
		if count >= rule.MaxCount {
			capped = true
		}
	}
	return capped, latestTS, nil
}

// ReadBatchCheck answers eligibility for many fcap_keys with a single
// ZRANGEBYSCORE fetch. Buckets matching members per key.
func (s *ZSetArrayStore) ReadBatchCheck(ctx context.Context, userID string, fcapKeyHashes []uint64, rules []FrequencyRule, now int64) (cappedByKey map[uint64]bool, err error) {
	maxWindow := time.Duration(0)
	for _, r := range rules {
		if r.Window > maxWindow {
			maxWindow = r.Window
		}
	}
	cutoff := now - int64(maxWindow.Seconds())
	res, err := s.rdb.ZRangeByScoreWithScores(ctx, zsetArrayKey(userID), &redis.ZRangeBy{
		Min: strconv.FormatInt(cutoff, 10),
		Max: "+inf",
	}).Result()
	if err != nil && !errors.Is(err, redis.Nil) {
		return nil, err
	}

	wantedSet := make(map[uint64]struct{}, len(fcapKeyHashes))
	for _, k := range fcapKeyHashes {
		wantedSet[k] = struct{}{}
	}
	type entry struct {
		impHash uint64
		ts      int64
	}
	byKey := make(map[uint64][]entry, len(fcapKeyHashes))
	for _, z := range res {
		impHash, keyHashes := decodeArrayMember(z.Member.(string))
		ts := int64(z.Score)
		for _, kh := range keyHashes {
			if _, want := wantedSet[kh]; !want {
				continue
			}
			byKey[kh] = append(byKey[kh], entry{impHash, ts})
		}
	}

	cappedByKey = make(map[uint64]bool, len(fcapKeyHashes))
	for _, kh := range fcapKeyHashes {
		bucket := byKey[kh]
		capped := false
		for _, rule := range rules {
			ruleCutoff := now - int64(rule.Window.Seconds())
			seen := make(map[uint64]struct{})
			count := 0
			for _, e := range bucket {
				if e.ts < ruleCutoff {
					continue
				}
				if _, dup := seen[e.impHash]; dup {
					continue
				}
				seen[e.impHash] = struct{}{}
				count++
			}
			if count >= rule.MaxCount {
				capped = true
				break
			}
		}
		cappedByKey[kh] = capped
	}
	return cappedByKey, nil
}

// Cleanup drops entries older than (now - window) via a single
// ZREMRANGEBYSCORE. Server-side, no read-modify-write.
func (s *ZSetArrayStore) Cleanup(ctx context.Context, userID string, now int64, window time.Duration) error {
	cutoff := now - int64(window.Seconds())
	return s.rdb.ZRemRangeByScore(ctx, zsetArrayKey(userID), "-inf", "("+strconv.FormatInt(cutoff, 10)).Err()
}

// MemoryUsage returns the byte size of the user's ZSET in valkey.
func (s *ZSetArrayStore) MemoryUsage(ctx context.Context, userID string) (int64, error) {
	return s.rdb.MemoryUsage(ctx, zsetArrayKey(userID)).Result()
}

// Reset deletes all bench data for this user.
func (s *ZSetArrayStore) Reset(ctx context.Context, userID string) error {
	return s.rdb.Del(ctx, zsetArrayKey(userID)).Err()
}
