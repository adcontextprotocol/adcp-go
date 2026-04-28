// Package expbench compares three exposure-log storage shapes against valkey:
// a generalized fixed-stride binary log, a ZSET with fcap_keys array per
// member, and a ZSET with one member per fcap_key. The goal is empirical
// answers to "which shape is right for the per-user exposure log under the
// fcap_keys[] data model?"
package expbench

import (
	"hash/fnv"
	"time"
)

// Impression is one ad delivery to one user, carrying the fcap_keys the
// buyer wants to count this impression against.
type Impression struct {
	ImpressionID string
	Timestamp    int64
	FcapKeys     []string // e.g. {"campaign:42", "advertiser:13", "creative:8"}
}

// FrequencyRule limits exposures matching a single fcap_key in a window.
type FrequencyRule struct {
	MaxCount int
	Window   time.Duration
}

// MaxKeysPerImpression caps how many fcap_keys a single impression can
// carry in the binary-log variant. ZSET variants are unbounded.
const MaxKeysPerImpression = 8

// HashKey returns the 8-byte hash of a label string. Same FNV-1a as
// targeting.hashString — one hash function across all variants so the
// comparison isn't muddied by hash-function differences.
func HashKey(s string) uint64 {
	h := fnv.New64a()
	_, _ = h.Write([]byte(s))
	return h.Sum64()
}

// HashKeys hashes a slice of labels in order. Bench helper; production
// would hash at write time.
func HashKeys(keys []string) []uint64 {
	out := make([]uint64, len(keys))
	for i, k := range keys {
		out[i] = HashKey(k)
	}
	return out
}
