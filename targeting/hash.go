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

// HashPackageID returns a full SHA-256 hex digest of a package ID,
// used to derive storage keys so that audience data is only accessible
// to callers who know the package ID.
func HashPackageID(packageID string) string {
	h := sha256.Sum256([]byte(packageID))
	return hex.EncodeToString(h[:])
}

// HashURL returns a full SHA-256 hex digest of a lowercased URL.
func HashURL(url string) string {
	h := sha256.Sum256([]byte(strings.ToLower(url)))
	return hex.EncodeToString(h[:])
}
