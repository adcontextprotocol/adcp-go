package tmpxdecoders

import (
	"context"
	"encoding/hex"
	"fmt"
)

// HashedEmail decodes a SHA-256-hashed email address. Per the AdCP and IAB
// Identity 2.0 conventions the wire form is the 64-character hex digest of
// the normalized lowercase email; the binary form is the underlying 32
// raw bytes.
//
// The decoder does not re-hash and does not normalize: callers are expected
// to deliver the hash already computed. Hex decoding is case-insensitive.
type HashedEmail struct{}

// Decode parses a 64-char hex SHA-256 string into its 32 raw bytes. ctx is
// unused — HashedEmail is a pure-format decoder.
func (HashedEmail) Decode(_ context.Context, userToken string) ([]byte, error) {
	if len(userToken) != 64 {
		return nil, fmt.Errorf("hashed_email: expected 64 hex chars (SHA-256), got %d", len(userToken))
	}
	out, err := hex.DecodeString(userToken)
	if err != nil {
		return nil, fmt.Errorf("hashed_email: invalid hex: %w", err)
	}
	if len(out) != 32 {
		return nil, fmt.Errorf("hashed_email: decoded %d bytes, want 32", len(out))
	}
	return out, nil
}
