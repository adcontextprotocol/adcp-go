package expbench

import (
	"context"
	"encoding/binary"
	"errors"
	"time"

	"github.com/redis/go-redis/v9"
)

// Binary log entry layout (88 bytes, 8-byte aligned):
//
//	timestamp(8) + impressionHash(8) + keyCount(1) + padding(7) + fcapKeyHash[0..7](8 each)
//
// Header (4 bytes): version(uint16) + entrySize(uint16). Stored as a single
// byte string in valkey under user:exposures:{uid}.
const (
	binHeaderSize  = 4
	binEntrySize   = 88
	binVersion     = 1
	binTSOffset    = 0
	binImpOffset   = 8
	binCountOffset = 16
	binKeysOffset  = 24
)

// BinaryStore implements the binary-log variant: read-modify-write of a
// single byte slab per user. Generalized from the current
// `targeting/exposure_binary.go` to carry up to MaxKeysPerImpression
// fcap_key hashes per entry instead of fixed package/campaign slots.
type BinaryStore struct {
	rdb redis.Cmdable
}

// NewBinaryStore returns a BinaryStore backed by the given redis client.
func NewBinaryStore(rdb redis.Cmdable) *BinaryStore { return &BinaryStore{rdb: rdb} }

// Name is used in benchmark output.
func (s *BinaryStore) Name() string { return "binary" }

// Key for a user's exposure log.
func binaryKey(userID string) string { return "user:exposures:bin:" + userID }

func encodeEntry(buf []byte, imp Impression) {
	if len(imp.FcapKeys) > MaxKeysPerImpression {
		// Bench setup ensures K <= MaxKeysPerImpression; truncation here
		// makes a deterministic choice if violated.
		imp.FcapKeys = imp.FcapKeys[:MaxKeysPerImpression]
	}
	binary.LittleEndian.PutUint64(buf[binTSOffset:], uint64(imp.Timestamp)) //nolint:gosec
	binary.LittleEndian.PutUint64(buf[binImpOffset:], HashKey(imp.ImpressionID))
	buf[binCountOffset] = byte(len(imp.FcapKeys))
	// padding bytes 17..23 left zero
	for i, k := range imp.FcapKeys {
		off := binKeysOffset + i*8
		binary.LittleEndian.PutUint64(buf[off:], HashKey(k))
	}
	// Zero remaining slots so unused hashes don't accidentally match a real key.
	for i := len(imp.FcapKeys); i < MaxKeysPerImpression; i++ {
		off := binKeysOffset + i*8
		binary.LittleEndian.PutUint64(buf[off:], 0)
	}
}

func newBinaryBuf(numEntries int) []byte {
	buf := make([]byte, binHeaderSize, binHeaderSize+numEntries*binEntrySize)
	binary.LittleEndian.PutUint16(buf[0:], binVersion)
	binary.LittleEndian.PutUint16(buf[2:], binEntrySize)
	return buf
}

func entryCount(b []byte) int {
	if len(b) < binHeaderSize {
		return 0
	}
	return (len(b) - binHeaderSize) / binEntrySize
}

func entryAt(b []byte, i int) []byte {
	off := binHeaderSize + i*binEntrySize
	return b[off : off+binEntrySize]
}

// Seed bulk-writes a user's full log in one SET. Used by the bench harness
// to populate steady-state without paying the per-impression RMW cost.
func (s *BinaryStore) Seed(ctx context.Context, userID string, imps []Impression) error {
	buf := newBinaryBuf(len(imps))
	buf = buf[:binHeaderSize+len(imps)*binEntrySize]
	for i, imp := range imps {
		encodeEntry(entryAt(buf, i), imp)
	}
	return s.rdb.Set(ctx, binaryKey(userID), string(buf), 0).Err()
}

// Write performs read-modify-write for a single impression: fetches the
// existing log, appends the new entry, writes it back. This is the pattern
// `targeting/exposure_binary.go` uses today.
func (s *BinaryStore) Write(ctx context.Context, userID string, imp Impression) error {
	key := binaryKey(userID)
	cur, err := s.rdb.Get(ctx, key).Bytes()
	if err != nil && !errors.Is(err, redis.Nil) {
		return err
	}
	var n int
	if len(cur) >= binHeaderSize {
		n = entryCount(cur)
	}
	out := newBinaryBuf(n + 1)
	if n > 0 {
		out = append(out, cur[binHeaderSize:]...)
	}
	out = append(out, make([]byte, binEntrySize)...)
	encodeEntry(out[binHeaderSize+n*binEntrySize:], imp)
	return s.rdb.Set(ctx, key, string(out), 0).Err()
}

// ReadAndCheck fetches the user's log and answers a single eligibility
// check: does any rule's count of distinct impressions matching fcapKeyHash
// in its window exceed MaxCount? Also returns the latest matching timestamp
// for intent score.
func (s *BinaryStore) ReadAndCheck(ctx context.Context, userID string, fcapKeyHash uint64, rules []FrequencyRule, now int64) (capped bool, latestTS int64, err error) {
	b, err := s.rdb.Get(ctx, binaryKey(userID)).Bytes()
	if err != nil {
		if errors.Is(err, redis.Nil) {
			return false, 0, nil
		}
		return false, 0, err
	}
	n := entryCount(b)
	for _, rule := range rules {
		cutoff := now - int64(rule.Window.Seconds())
		seen := make(map[uint64]struct{})
		count := 0
		for i := 0; i < n; i++ {
			e := entryAt(b, i)
			ts := int64(binary.LittleEndian.Uint64(e[binTSOffset:])) //nolint:gosec
			if ts < cutoff {
				continue
			}
			kc := int(e[binCountOffset])
			match := false
			for j := 0; j < kc; j++ {
				if binary.LittleEndian.Uint64(e[binKeysOffset+j*8:]) == fcapKeyHash {
					match = true
					break
				}
			}
			if !match {
				continue
			}
			impHash := binary.LittleEndian.Uint64(e[binImpOffset:])
			if _, dup := seen[impHash]; dup {
				continue
			}
			seen[impHash] = struct{}{}
			count++
			if ts > latestTS {
				latestTS = ts
			}
		}
		if count >= rule.MaxCount {
			capped = true
		}
	}
	return capped, latestTS, nil
}

// ReadBatchCheck fetches once, scans once, and answers eligibility for many
// fcap_keys at once by bucketing entries per key. Mirrors the structure of
// `targeting/exposure_aggregate.go` (the PR #103 preagg).
func (s *BinaryStore) ReadBatchCheck(ctx context.Context, userID string, fcapKeyHashes []uint64, rules []FrequencyRule, now int64) (cappedByKey map[uint64]bool, err error) {
	b, err := s.rdb.Get(ctx, binaryKey(userID)).Bytes()
	if err != nil && !errors.Is(err, redis.Nil) {
		return nil, err
	}
	cappedByKey = make(map[uint64]bool, len(fcapKeyHashes))
	if len(b) < binHeaderSize {
		return cappedByKey, nil
	}
	n := entryCount(b)

	type aggEntry struct {
		impHash uint64
		ts      int64
	}
	// Pre-bucket entries by fcap_key hash. Same idea as exposure_aggregate.go.
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
	return cappedByKey, nil
}

// Cleanup drops entries with timestamps older than (now - window). Performs
// a full read-modify-write of the slab.
func (s *BinaryStore) Cleanup(ctx context.Context, userID string, now int64, window time.Duration) error {
	key := binaryKey(userID)
	b, err := s.rdb.Get(ctx, key).Bytes()
	if err != nil {
		if errors.Is(err, redis.Nil) {
			return nil
		}
		return err
	}
	cutoff := now - int64(window.Seconds())
	n := entryCount(b)
	// Find the first kept entry. Entries are append-ordered so timestamps
	// are roughly monotonic; we still scan to be safe.
	keepFrom := n
	for i := 0; i < n; i++ {
		ts := int64(binary.LittleEndian.Uint64(entryAt(b, i)[binTSOffset:])) //nolint:gosec
		if ts >= cutoff {
			keepFrom = i
			break
		}
	}
	if keepFrom == 0 {
		return nil
	}
	kept := n - keepFrom
	out := newBinaryBuf(kept)
	out = out[:binHeaderSize+kept*binEntrySize]
	copy(out[binHeaderSize:], b[binHeaderSize+keepFrom*binEntrySize:])
	return s.rdb.Set(ctx, key, string(out), 0).Err()
}

// MemoryUsage returns the byte size of the user's log in valkey.
func (s *BinaryStore) MemoryUsage(ctx context.Context, userID string) (int64, error) {
	return s.rdb.MemoryUsage(ctx, binaryKey(userID)).Result()
}

// Reset deletes all bench data for this user.
func (s *BinaryStore) Reset(ctx context.Context, userID string) error {
	return s.rdb.Del(ctx, binaryKey(userID)).Err()
}
