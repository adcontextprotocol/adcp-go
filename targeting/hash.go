package targeting

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
)

// HashURL returns a full SHA-256 hex digest of a lowercased URL.
func HashURL(url string) string {
	h := sha256.Sum256([]byte(strings.ToLower(url)))
	return hex.EncodeToString(h[:])
}
