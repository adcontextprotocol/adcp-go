package tmpxdecoders

import (
	"context"
	"errors"
	"fmt"
	"math/big"
	"strings"
)

// WorldIDNullifier encodes a verified World ID nullifier into its 32-byte
// TMPX token. The nullifier World returns is a relying-party-scoped field
// element (BN254 scalar, ≤254 bits) rendered as a hex string with an
// optional "0x" prefix; the binary form is that integer, big-endian, left-
// padded to the fixed 32-byte width the TMPX type registry assigns
// TmpxTypeWorldIDNullifier. Big-endian zero-padding is reversible by the
// buyer master, which keys frequency-cap / unique-human state on the same
// nullifier under its own relying-party scope.
//
// Verify-before-trust: this encoder is NOT part of NewDefaultRegistry, so it
// is unreachable from the inbound IdentityToken decode path. A nullifier
// reaches the wire ONLY after the verified-identity stage validated its
// proof against the World ID backend and derived the nullifier from World's
// authoritative response. An inbound, sender-asserted world_id_nullifier
// token has no registered decoder and is dropped at decode time — it can
// never be sealed.
type WorldIDNullifier struct{}

// worldIDNullifierBytes is the fixed TMPX token width for a World ID
// nullifier, matching TmpxTokenSize(TmpxTypeWorldIDNullifier).
const worldIDNullifierBytes = 32

// Decode parses the hex nullifier into its 32-byte big-endian form. ctx is
// unused — WorldIDNullifier is a pure-format encoder. A value wider than 32
// bytes, empty input, or non-hex content is an error (the caller drops the
// identity rather than emitting a malformed token).
func (WorldIDNullifier) Decode(_ context.Context, nullifier string) ([]byte, error) {
	hexDigits := strings.TrimSpace(nullifier)
	hexDigits = strings.TrimPrefix(hexDigits, "0x")
	hexDigits = strings.TrimPrefix(hexDigits, "0X")
	if hexDigits == "" {
		return nil, errors.New("world_id_nullifier: empty nullifier")
	}
	n, ok := new(big.Int).SetString(hexDigits, 16)
	if !ok {
		return nil, errors.New("world_id_nullifier: nullifier is not valid hex")
	}
	raw := n.Bytes()
	if len(raw) > worldIDNullifierBytes {
		return nil, fmt.Errorf("world_id_nullifier: nullifier is %d bytes, exceeds %d", len(raw), worldIDNullifierBytes)
	}
	out := make([]byte, worldIDNullifierBytes)
	copy(out[worldIDNullifierBytes-len(raw):], raw)
	return out, nil
}
