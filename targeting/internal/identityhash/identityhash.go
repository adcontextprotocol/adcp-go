// Package identityhash holds the canonical user-identity hash used by every
// targeting sub-package that keys storage on a user token. SHA-256 truncated
// to the first 16 bytes (32 hex characters) — short enough to keep keys
// compact and long enough that collisions are not a concern at our scale.
package identityhash

import (
	"crypto/sha256"
	"encoding/hex"
)

// Hash returns the truncated hex digest of token.
func Hash(token string) string {
	h := sha256.Sum256([]byte(token))
	return hex.EncodeToString(h[:16])
}
