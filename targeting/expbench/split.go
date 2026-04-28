package expbench

import (
	"context"
	"encoding/binary"
	"errors"
	"strconv"
	"time"

	"github.com/redis/go-redis/v9"
)

// FetchResult carries the raw, pre-Go-processing data from a single fetch.
// It's variant-specific behind an opaque type so the bench can time the
// fetch (valkey-side cost + network + Go-side deserialization of the wire
// protocol) separately from the in-process eligibility scan.
type FetchResult struct {
	BinaryBlob []byte    // BinaryStore
	ZMembers   []redis.Z // ZSet variants
}

// Fetcher pulls raw data for a window from valkey. Implemented by each
// variant alongside its existing Read* methods so the bench can split
// "wait on valkey" from "scan in Go".
type Fetcher interface {
	Fetch(ctx context.Context, userID string, window time.Duration, now int64) (FetchResult, error)
}

// Processor runs the Go-side eligibility scan against pre-fetched data.
// No I/O — pure CPU.
type Processor interface {
	Process(raw FetchResult, fcapKeyHashes []uint64, rules []FrequencyRule, now int64) (cappedByKey map[uint64]bool)
}

// --- BinaryStore ---

func (s *BinaryStore) Fetch(ctx context.Context, userID string, window time.Duration, now int64) (FetchResult, error) {
	b, err := s.rdb.Get(ctx, binaryKey(userID)).Bytes()
	if err != nil && !errors.Is(err, redis.Nil) {
		return FetchResult{}, err
	}
	return FetchResult{BinaryBlob: b}, nil
}

func (s *BinaryStore) Process(raw FetchResult, fcapKeyHashes []uint64, rules []FrequencyRule, now int64) map[uint64]bool {
	b := raw.BinaryBlob
	cappedByKey := make(map[uint64]bool, len(fcapKeyHashes))
	if len(b) < binHeaderSize {
		return cappedByKey
	}
	n := entryCount(b)
	type aggEntry struct {
		impHash uint64
		ts      int64
	}
	wantedSet := make(map[uint64]struct{}, len(fcapKeyHashes))
	for _, k := range fcapKeyHashes {
		wantedSet[k] = struct{}{}
	}
	byKey := make(map[uint64][]aggEntry, len(fcapKeyHashes))
	for i := 0; i < n; i++ {
		e := entryAt(b, i)
		ts := int64(binary.LittleEndian.Uint64(e[binTSOffset:])) //nolint:gosec
		impHash := binary.LittleEndian.Uint64(e[binImpOffset:])
		kc := int(e[binCountOffset])
		for j := 0; j < kc; j++ {
			kh := binary.LittleEndian.Uint64(e[binKeysOffset+j*8:])
			if _, want := wantedSet[kh]; !want {
				continue
			}
			byKey[kh] = append(byKey[kh], aggEntry{impHash, ts})
		}
	}
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

// --- ZSetArrayStore ---

func (s *ZSetArrayStore) Fetch(ctx context.Context, userID string, window time.Duration, now int64) (FetchResult, error) {
	cutoff := now - int64(window.Seconds())
	res, err := s.rdb.ZRangeByScoreWithScores(ctx, zsetArrayKey(userID), &redis.ZRangeBy{
		Min: strconv.FormatInt(cutoff, 10),
		Max: "+inf",
	}).Result()
	if err != nil && !errors.Is(err, redis.Nil) {
		return FetchResult{}, err
	}
	return FetchResult{ZMembers: res}, nil
}

func (s *ZSetArrayStore) Process(raw FetchResult, fcapKeyHashes []uint64, rules []FrequencyRule, now int64) map[uint64]bool {
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
		impHash, keyHashes := decodeArrayMember(z.Member.(string))
		ts := int64(z.Score)
		for _, kh := range keyHashes {
			if _, want := wantedSet[kh]; !want {
				continue
			}
			byKey[kh] = append(byKey[kh], entry{impHash, ts})
		}
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

// --- ZSetPerKeyStore ---

func (s *ZSetPerKeyStore) Fetch(ctx context.Context, userID string, window time.Duration, now int64) (FetchResult, error) {
	cutoff := now - int64(window.Seconds())
	res, err := s.rdb.ZRangeByScoreWithScores(ctx, zsetPerKeyKey(userID), &redis.ZRangeBy{
		Min: strconv.FormatInt(cutoff, 10),
		Max: "+inf",
	}).Result()
	if err != nil && !errors.Is(err, redis.Nil) {
		return FetchResult{}, err
	}
	return FetchResult{ZMembers: res}, nil
}

func (s *ZSetPerKeyStore) Process(raw FetchResult, fcapKeyHashes []uint64, rules []FrequencyRule, now int64) map[uint64]bool {
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

// SplitVariant combines Fetcher + Processor + the Variant identity needed
// for bench iteration.
type SplitVariant interface {
	Variant
	Fetcher
	Processor
}

// SizeOfFetch is a rough indicator of how many bytes/members came back —
// useful for understanding why one variant's fetch costs more than another.
func SizeOfFetch(r FetchResult) (bytes int, members int) {
	bytes = len(r.BinaryBlob)
	members = len(r.ZMembers)
	for _, z := range r.ZMembers {
		bytes += len(z.Member.(string))
	}
	return bytes, members
}
