package targeting

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
)

// HashToken returns a truncated SHA-256 hex digest of a user token.
// Uses the first 16 bytes (32 hex characters) for compactness.
func HashToken(token string) string {
	h := sha256.Sum256([]byte(token))
	return hex.EncodeToString(h[:16])
}

// HashURL returns a full SHA-256 hex digest of a lowercased URL.
func HashURL(url string) string {
	h := sha256.Sum256([]byte(strings.ToLower(url)))
	return hex.EncodeToString(h[:])
}
