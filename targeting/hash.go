package targeting

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
)

// HashURL returns the SHA-256 hex digest of strings.ToLower(url).
//
// Interop contract for writer pipelines that populate the
// url:blocklist:* / url:allowlist:* keysets:
//
//   - Input is treated as an opaque string. Only ASCII-case folding is
//     applied (strings.ToLower). No URL canonicalization is performed:
//     scheme casing, default-port stripping, percent-encoding
//     normalization, trailing-slash trimming, fragment removal, query
//     reordering, IDN/punycode mapping, and Unicode normalization are
//     all the producer's responsibility.
//   - Producers and consumers MUST agree on the canonical form BEFORE
//     hashing or every block silently misses. Today the producer and
//     consumer are the same code path so the contract is dormant; the
//     moment external publisher tooling writes against this key shape
//     the canonical form needs to be specified there too (or this
//     function needs to start canonicalizing).
//   - Output is a 64-character lowercase hex string; the URL-list
//     storage layer (urlliststore) does not re-hash and stores
//     whatever this function returns.
//
// Changing the hash function or the canonical form is a breaking
// change to every url:blocklist:* / url:allowlist:* key on disk and
// requires coordinated re-hashing of every existing entry.
func HashURL(url string) string {
	h := sha256.Sum256([]byte(strings.ToLower(url)))
	return hex.EncodeToString(h[:])
}
