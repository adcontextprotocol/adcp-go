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

// ZSet-per-keyed variant: one ZSET per (user, fcap_key) pair. Each
// impression produces K ZADDs, one per fcap_key, each into a different key.
// Member is just the impression hash (8 bytes) — the fcap_key is the
// valkey key, not part of the member.
//
// Win condition: single-key reads only fetch the data for that one key,
// not the user's whole 30-day log. Loss condition: batch reads need K
// fetches (pipelined); cleanup needs to find all of a user's per-key
// ZSETs.
//
// Cleanup story (resolved):
//   - We maintain an index `user:exp:idx:{uid}` (a regular SET) of all
//     fcap_key_hashes the user has any data for. ZADD'd to on every
//     write; cleaned up by the periodic janitor that runs
//     ZREMRANGEBYSCORE on each member key and SREM on emptied ones.
//   - Index write cost: 1 SADD per impression per fcap_key (K total),
//     pipelined with the ZADDs. SADD is idempotent, so a hot impression
//     for a known key is a single SADD command in valkey-side cost.

type ZSetPerKeyedStore struct {
	rdb redis.Cmdable
}

func NewZSetPerKeyedStore(rdb redis.Cmdable) *ZSetPerKeyedStore { return &ZSetPerKeyedStore{rdb: rdb} }

func (s *ZSetPerKeyedStore) Name() string { return "zset-perkeyed" }

func zsetPerKeyedKey(userID string, keyHash uint64) string {
	return fmt.Sprintf("user:exposures:zsek:%s:%016x", userID, keyHash)
}
func zsetPerKeyedIndex(userID string) string { return "user:exposures:zsek-idx:" + userID }

func encodePerKeyedMember(impHash uint64) string {
	buf := make([]byte, 8)
	binary.LittleEndian.PutUint64(buf, impHash)
	return string(buf)
}

func decodePerKeyedMember(m string) uint64 {
	if len(m) < 8 {
		return 0
	}
	return binary.LittleEndian.Uint64([]byte(m))
}

// Seed bulk-writes a user's full log. K members (one per fcap_key on each
// impression) plus K SADDs for the index, all pipelined.
func (s *ZSetPerKeyedStore) Seed(ctx context.Context, userID string, imps []Impression) error {
	if len(imps) == 0 {
		return nil
	}
	idx := zsetPerKeyedIndex(userID)
	pipe := s.rdb.Pipeline()
	indexAdded := make(map[uint64]struct{})

	for _, imp := range imps {
		impHash := HashKey(imp.ImpressionID)
		keys := imp.FcapKeys
		if len(keys) > MaxKeysPerImpression {
			keys = keys[:MaxKeysPerImpression]
		}
		for _, k := range keys {
			kh := HashKey(k)
			pipe.ZAdd(ctx, zsetPerKeyedKey(userID, kh), redis.Z{
				Score:  float64(imp.Timestamp),
				Member: encodePerKeyedMember(impHash),
			})
			if _, seen := indexAdded[kh]; !seen {
				pipe.SAdd(ctx, idx, strconv.FormatUint(kh, 16))
				indexAdded[kh] = struct{}{}
			}
		}
		// Flush in chunks to keep pipeline buffers reasonable on heavy seeds.
		if len(indexAdded)%500 == 0 {
			if _, err := pipe.Exec(ctx); err != nil {
				return err
			}
			pipe = s.rdb.Pipeline()
		}
	}
	_, err := pipe.Exec(ctx)
	return err
}

// Write writes a single impression. K ZADDs + K idempotent SADDs, pipelined.
func (s *ZSetPerKeyedStore) Write(ctx context.Context, userID string, imp Impression) error {
	keys := imp.FcapKeys
	if len(keys) > MaxKeysPerImpression {
		keys = keys[:MaxKeysPerImpression]
	}
	if len(keys) == 0 {
		return nil
	}
	impHash := HashKey(imp.ImpressionID)
	idx := zsetPerKeyedIndex(userID)
	pipe := s.rdb.Pipeline()
	for _, k := range keys {
		kh := HashKey(k)
		pipe.ZAdd(ctx, zsetPerKeyedKey(userID, kh), redis.Z{
			Score:  float64(imp.Timestamp),
			Member: encodePerKeyedMember(impHash),
		})
		pipe.SAdd(ctx, idx, strconv.FormatUint(kh, 16))
	}
	_, err := pipe.Exec(ctx)
	return err
}

// ReadAndCheck answers a single eligibility check. Single ZSET fetch.
func (s *ZSetPerKeyedStore) ReadAndCheck(ctx context.Context, userID string, fcapKeyHash uint64, rules []FrequencyRule, now int64) (capped bool, latestTS int64, err error) {
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
	res, err := s.rdb.ZRangeByScoreWithScores(ctx, zsetPerKeyedKey(userID, fcapKeyHash), &redis.ZRangeBy{
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
	matched := make([]entry, 0, len(res))
	for _, z := range res {
		impHash := decodePerKeyedMember(z.Member.(string))
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

// ReadBatchCheck answers eligibility for many fcap_keys. Pipelines a
// ZRANGEBYSCORE per key, processes results in parallel.
func (s *ZSetPerKeyedStore) ReadBatchCheck(ctx context.Context, userID string, fcapKeyHashes []uint64, rules []FrequencyRule, now int64) (cappedByKey map[uint64]bool, err error) {
	maxWindow := time.Duration(0)
	for _, r := range rules {
		if r.Window > maxWindow {
			maxWindow = r.Window
		}
	}
	cutoff := now - int64(maxWindow.Seconds())
	pipe := s.rdb.Pipeline()
	cmds := make([]*redis.ZSliceCmd, len(fcapKeyHashes))
	for i, kh := range fcapKeyHashes {
		cmds[i] = pipe.ZRangeByScoreWithScores(ctx, zsetPerKeyedKey(userID, kh), &redis.ZRangeBy{
			Min: strconv.FormatInt(cutoff, 10),
			Max: "+inf",
		})
	}
	if _, err := pipe.Exec(ctx); err != nil && !errors.Is(err, redis.Nil) {
		return nil, err
	}

	cappedByKey = make(map[uint64]bool, len(fcapKeyHashes))
	for i, kh := range fcapKeyHashes {
		res, err := cmds[i].Result()
		if err != nil && !errors.Is(err, redis.Nil) {
			return nil, err
		}
		capped := false
		for _, rule := range rules {
			ruleCutoff := now - int64(rule.Window.Seconds())
			seen := make(map[uint64]struct{})
			count := 0
			for _, z := range res {
				ts := int64(z.Score)
				if ts < ruleCutoff {
					continue
				}
				impHash := decodePerKeyedMember(z.Member.(string))
				if _, dup := seen[impHash]; dup {
					continue
				}
				seen[impHash] = struct{}{}
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

// Cleanup walks the per-user index and runs ZREMRANGEBYSCORE on each
// fcap_key ZSET. Empty ZSETs get their index entries removed.
func (s *ZSetPerKeyedStore) Cleanup(ctx context.Context, userID string, now int64, window time.Duration) error {
	cutoff := now - int64(window.Seconds())
	idxKey := zsetPerKeyedIndex(userID)
	members, err := s.rdb.SMembers(ctx, idxKey).Result()
	if err != nil {
		if errors.Is(err, redis.Nil) {
			return nil
		}
		return err
	}
	pipe := s.rdb.Pipeline()
	remCmds := make([]*redis.IntCmd, len(members))
	cardCmds := make([]*redis.IntCmd, len(members))
	for i, m := range members {
		kh, err := strconv.ParseUint(m, 16, 64)
		if err != nil {
			continue
		}
		key := zsetPerKeyedKey(userID, kh)
		remCmds[i] = pipe.ZRemRangeByScore(ctx, key, "-inf", "("+strconv.FormatInt(cutoff, 10))
		cardCmds[i] = pipe.ZCard(ctx, key)
	}
	if _, err := pipe.Exec(ctx); err != nil {
		return err
	}
	// Second pass: SREM index entries for keys that are now empty.
	pipe = s.rdb.Pipeline()
	for i, m := range members {
		if cardCmds[i] == nil {
			continue
		}
		card, err := cardCmds[i].Result()
		if err != nil {
			continue
		}
		if card == 0 {
			pipe.SRem(ctx, idxKey, m)
		}
	}
	_, err = pipe.Exec(ctx)
	return err
}

// MemoryUsage sums valkey-reported memory for the index plus all per-key
// ZSETs the user has data in.
func (s *ZSetPerKeyedStore) MemoryUsage(ctx context.Context, userID string) (int64, error) {
	idxKey := zsetPerKeyedIndex(userID)
	members, err := s.rdb.SMembers(ctx, idxKey).Result()
	if err != nil && !errors.Is(err, redis.Nil) {
		return 0, err
	}
	var total int64
	if u, err := s.rdb.MemoryUsage(ctx, idxKey).Result(); err == nil {
		total += u
	}
	for _, m := range members {
		kh, err := strconv.ParseUint(m, 16, 64)
		if err != nil {
			continue
		}
		if u, err := s.rdb.MemoryUsage(ctx, zsetPerKeyedKey(userID, kh)).Result(); err == nil {
			total += u
		}
	}
	return total, nil
}

// Reset deletes the index plus all per-key ZSETs.
func (s *ZSetPerKeyedStore) Reset(ctx context.Context, userID string) error {
	idxKey := zsetPerKeyedIndex(userID)
	members, err := s.rdb.SMembers(ctx, idxKey).Result()
	if err != nil && !errors.Is(err, redis.Nil) {
		return err
	}
	pipe := s.rdb.Pipeline()
	pipe.Del(ctx, idxKey)
	for _, m := range members {
		kh, err := strconv.ParseUint(m, 16, 64)
		if err != nil {
			continue
		}
		pipe.Del(ctx, zsetPerKeyedKey(userID, kh))
	}
	_, err = pipe.Exec(ctx)
	return err
}

// --- Split bench support ---

// Fetch for the perkeyed variant: when a single fcap_key is in scope the
// fetch is a single ZRANGEBYSCORE; for batch reads the bench will call
// ReadBatchCheck directly (the pipelined fetch is part of that path).
//
// This Fetch implementation aggregates members across ALL fcap_keys in the
// user's index — useful for the split bench's "Process given pre-fetched
// data" model. In production, the win is that single-key reads skip this
// aggregation entirely.
func (s *ZSetPerKeyedStore) Fetch(ctx context.Context, userID string, window time.Duration, now int64) (FetchResult, error) {
	cutoff := now - int64(window.Seconds())
	idxKey := zsetPerKeyedIndex(userID)
	members, err := s.rdb.SMembers(ctx, idxKey).Result()
	if err != nil && !errors.Is(err, redis.Nil) {
		return FetchResult{}, err
	}
	pipe := s.rdb.Pipeline()
	cmds := make([]*redis.ZSliceCmd, 0, len(members))
	keyHashes := make([]uint64, 0, len(members))
	for _, m := range members {
		kh, err := strconv.ParseUint(m, 16, 64)
		if err != nil {
			continue
		}
		keyHashes = append(keyHashes, kh)
		cmds = append(cmds, pipe.ZRangeByScoreWithScores(ctx, zsetPerKeyedKey(userID, kh), &redis.ZRangeBy{
			Min: strconv.FormatInt(cutoff, 10),
			Max: "+inf",
		}))
	}
	if _, err := pipe.Exec(ctx); err != nil && !errors.Is(err, redis.Nil) {
		return FetchResult{}, err
	}
	// Re-shape into FetchResult.ZMembers with a synthetic member encoding
	// of {impHash, keyHash} so the existing Process step can decode it the
	// same way as the per-key variant.
	all := make([]redis.Z, 0)
	for i, kh := range keyHashes {
		zs, err := cmds[i].Result()
		if err != nil && !errors.Is(err, redis.Nil) {
			continue
		}
		for _, z := range zs {
			impHash := decodePerKeyedMember(z.Member.(string))
			all = append(all, redis.Z{
				Score:  z.Score,
				Member: encodePerKeyMember(impHash, kh),
			})
		}
	}
	return FetchResult{ZMembers: all}, nil
}

// Process for the perkeyed variant: identical decoding to the per-key
// variant since Fetch reshapes into {impHash, keyHash} 16-byte members.
func (s *ZSetPerKeyedStore) Process(raw FetchResult, fcapKeyHashes []uint64, rules []FrequencyRule, now int64) map[uint64]bool {
	wantedSet := make(map[uint64]struct{}, len(fcapKeyHashes))
	for _, k := range fcapKeyHashes {
		wantedSet[k] = struct{}{}
	}
	type entry struct {
		impHash uint64
		ts      int64
	}
	byKey := make(map[uint64][]entry, len(fcapKeyHashes))
	for _, z := range raw.ZMembers {
		impHash, keyHash := decodePerKeyMember(z.Member.(string))
		if _, want := wantedSet[keyHash]; !want {
			continue
		}
		byKey[keyHash] = append(byKey[keyHash], entry{impHash, int64(z.Score)})
	}
	cappedByKey := make(map[uint64]bool, len(fcapKeyHashes))
	for _, kh := range fcapKeyHashes {
		bucket := byKey[kh]
		capped := false
		for _, rule := range rules {
			cutoff := now - int64(rule.Window.Seconds())
			seen := make(map[uint64]struct{})
			count := 0
			for _, e := range bucket {
				if e.ts < cutoff {
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
	return cappedByKey
}
