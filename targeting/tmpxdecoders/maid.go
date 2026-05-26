package tmpxdecoders

import (
	"context"
	"encoding/hex"
	"fmt"
	"strings"
)

// MAID decodes an IDFA / GAID / AAID-style mobile advertising identifier.
// The wire form is the UUID string (RFC 4122 / 8-4-4-4-12 hex with dashes)
// or the same hex digits without dashes; the binary form is the 16 raw
// bytes per the TMPX type registry.
//
// The decoder is intentionally tolerant: case-insensitive, accepts the
// dashed and undashed forms, but rejects anything else (URN prefix,
// braces, surrounding whitespace must be stripped upstream).
type MAID struct{}

// Decode parses a MAID string into 16 raw bytes. ctx is unused — MAID is a
// pure-format decoder.
func (MAID) Decode(_ context.Context, userToken string) ([]byte, error) {
	switch len(userToken) {
	case 36:
		// Dashed UUID: position-check the separators before stripping.
		if userToken[8] != '-' || userToken[13] != '-' || userToken[18] != '-' || userToken[23] != '-' {
			return nil, fmt.Errorf("maid: dashed UUID has misplaced separators in %q", userToken)
		}
		return decodeMAIDHex(strings.ReplaceAll(userToken, "-", ""))
	case 32:
		return decodeMAIDHex(userToken)
	default:
		return nil, fmt.Errorf("maid: expected 32 or 36 chars, got %d", len(userToken))
	}
}

func decodeMAIDHex(s string) ([]byte, error) {
	out, err := hex.DecodeString(s)
	if err != nil {
		return nil, fmt.Errorf("maid: invalid hex: %w", err)
	}
	if len(out) != 16 {
		return nil, fmt.Errorf("maid: decoded %d bytes, want 16", len(out))
	}
	return out, nil
}
