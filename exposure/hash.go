package exposure

import (
	"crypto/sha256"
	"encoding/hex"
	"hash/fnv"
)

// HashToken returns a truncated SHA-256 hex digest of a user token.
// Uses the first 16 bytes (32 hex characters) for compactness.
// The digest is used to key per-user Store entries (exposure logs,
// frequency-cap sorted sets, intent timestamps, profiles).
func HashToken(token string) string {
	h := sha256.Sum256([]byte(token))
	return hex.EncodeToString(h[:16])
}

// HashString returns an FNV-1a 64-bit hash of s. It is used to encode
// package, campaign, impression and source IDs into fixed-width slots
// inside the binary exposure log. Collision probability is ~0.0003% at
// 10M unique strings (birthday bound). Acceptable for frequency cap
// counting where an occasional collision causes slight over/under-counting.
func HashString(s string) uint64 {
	h := fnv.New64a()
	_, _ = h.Write([]byte(s)) // fnv.Write never returns an error
	return h.Sum64()
}
