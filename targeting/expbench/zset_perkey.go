package expbench

import (
	"context"
	"encoding/binary"
	"errors"
	"strconv"
	"time"

	"github.com/redis/go-redis/v9"
)

// ZSet-per-key variant: one ZSET per user, but each impression produces
// K members (one per fcap_key). Member is fixed 16 bytes.
//
// Member layout: impHash(8) + fcapKeyHash(8)
// Score: timestamp.
//
// Trade vs. ZSetArrayStore:
//   - K× write amplification (K ZADDs per impression, pipelined into one RT)
//   - K× more entries per ZSET → K× the memory
//   - Read-side: no per-member deserialization beyond the 8/8 byte split;
//     impression-level dedup needs a client-side seen-set (one impression
//     produces multiple matching members under different fcap_keys).

type ZSetPerKeyStore struct {
	rdb redis.Cmdable
}

func NewZSetPerKeyStore(rdb redis.Cmdable) *ZSetPerKeyStore { return &ZSetPerKeyStore{rdb: rdb} }

func (s *ZSetPerKeyStore) Name() string { return "zset-perkey" }

func zsetPerKeyKey(userID string) string { return "user:exposures:zsk:" + userID }

func encodePerKeyMember(impHash, keyHash uint64) string {
	buf := make([]byte, 16)
	binary.LittleEndian.PutUint64(buf[0:], impHash)
	binary.LittleEndian.PutUint64(buf[8:], keyHash)
	return string(buf)
}

func decodePerKeyMember(m string) (impHash, keyHash uint64) {
	if len(m) < 16 {
		return 0, 0
	}
	b := []byte(m)
	return binary.LittleEndian.Uint64(b[0:]), binary.LittleEndian.Uint64(b[8:])
}

// Seed bulk-writes a user's full log. K members emitted per impression.
func (s *ZSetPerKeyStore) Seed(ctx context.Context, userID string, imps []Impression) error {
	if len(imps) == 0 {
		return nil
	}
	key := zsetPerKeyKey(userID)
	zs := make([]redis.Z, 0, len(imps)*4)
	for _, imp := range imps {
		impHash := HashKey(imp.ImpressionID)
		keys := imp.FcapKeys
		if len(keys) > MaxKeysPerImpression {
			keys = keys[:MaxKeysPerImpression]
		}
		for _, k := range keys {
			zs = append(zs, redis.Z{
				Score:  float64(imp.Timestamp),
				Member: encodePerKeyMember(impHash, HashKey(k)),
			})
		}
	}
	// ZAdd in chunks to avoid oversized commands at extreme load.
	const chunk = 5000
	for i := 0; i < len(zs); i += chunk {
		end := i + chunk
		if end > len(zs) {
			end = len(zs)
		}
		if err := s.rdb.ZAdd(ctx, key, zs[i:end]...).Err(); err != nil {
			return err
		}
	}
	return nil
}

// Write writes a single impression. K ZADDs pipelined into one RT.
func (s *ZSetPerKeyStore) Write(ctx context.Context, userID string, imp Impression) error {
	keys := imp.FcapKeys
	if len(keys) > MaxKeysPerImpression {
		keys = keys[:MaxKeysPerImpression]
	}
	if len(keys) == 0 {
		return nil
	}
	impHash := HashKey(imp.ImpressionID)
	zs := make([]redis.Z, 0, len(keys))
	for _, k := range keys {
		zs = append(zs, redis.Z{
			Score:  float64(imp.Timestamp),
			Member: encodePerKeyMember(impHash, HashKey(k)),
		})
	}
	return s.rdb.ZAdd(ctx, zsetPerKeyKey(userID), zs...).Err()
}

// ReadAndCheck answers a single eligibility check.
func (s *ZSetPerKeyStore) ReadAndCheck(ctx context.Context, userID string, fcapKeyHash uint64, rules []FrequencyRule, now int64) (capped bool, latestTS int64, err error) {
	if len(rules) == 0 {
		return false, 0, nil
	}
	maxWindow := rules[0].Window
	for _, r := range rules[1:] {
		if r.Window > maxWindow {
			maxWindow = r.Window
		}
	}
	cutoff := now - int64(maxWindow.Seconds())
	res, err := s.rdb.ZRangeByScoreWithScores(ctx, zsetPerKeyKey(userID), &redis.ZRangeBy{
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
		impHash, keyHash := decodePerKeyMember(z.Member.(string))
		if keyHash != fcapKeyHash {
			continue
		}
		ts := int64(z.Score)
		matched = append(matched, entry{impHash, ts})
		if ts > latestTS {
			latestTS = ts
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

// ReadBatchCheck answers eligibility for many fcap_keys.
func (s *ZSetPerKeyStore) ReadBatchCheck(ctx context.Context, userID string, fcapKeyHashes []uint64, rules []FrequencyRule, now int64) (cappedByKey map[uint64]bool, err error) {
	maxWindow := time.Duration(0)
	for _, r := range rules {
		if r.Window > maxWindow {
			maxWindow = r.Window
		}
	}
	cutoff := now - int64(maxWindow.Seconds())
	res, err := s.rdb.ZRangeByScoreWithScores(ctx, zsetPerKeyKey(userID), &redis.ZRangeBy{
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
		impHash, keyHash := decodePerKeyMember(z.Member.(string))
		if _, want := wantedSet[keyHash]; !want {
			continue
		}
		byKey[keyHash] = append(byKey[keyHash], entry{impHash, int64(z.Score)})
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

// Cleanup drops entries older than (now - window).
func (s *ZSetPerKeyStore) Cleanup(ctx context.Context, userID string, now int64, window time.Duration) error {
	cutoff := now - int64(window.Seconds())
	return s.rdb.ZRemRangeByScore(ctx, zsetPerKeyKey(userID), "-inf", "("+strconv.FormatInt(cutoff, 10)).Err()
}

// MemoryUsage returns the byte size of the user's ZSET in valkey.
func (s *ZSetPerKeyStore) MemoryUsage(ctx context.Context, userID string) (int64, error) {
	return s.rdb.MemoryUsage(ctx, zsetPerKeyKey(userID)).Result()
}

// Reset deletes all bench data for this user.
func (s *ZSetPerKeyStore) Reset(ctx context.Context, userID string) error {
	return s.rdb.Del(ctx, zsetPerKeyKey(userID)).Err()
}
