package tmpxdecoders

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"math/big"
	"strings"
)

// WorldIDNullifier encodes a verified World ID nullifier into its TMPX token.
// The nullifier World returns is a relying-party-scoped field element (BN254
// scalar, ≤254 bits) rendered as a hex string with an optional "0x" prefix;
// Decode renders it as a fixed-width big-endian integer. Token prepends a
// digest of the relying party so the sealed token is self-describing: the
// nullifier is meaningful only within the relying party it was minted for, and
// the exposure consumer keys frequency-cap / unique-human state on the
// (relying party, nullifier) pair.
//
// Verify-before-trust: this encoder is NOT part of NewDefaultRegistry, so it
// is unreachable from the inbound IdentityToken decode path. A nullifier
// reaches the wire ONLY after the verified-identity stage validated its
// proof against the World ID backend and derived the nullifier from World's
// authoritative response. An inbound, sender-asserted world_id_nullifier
// token has no registered decoder and is dropped at decode time — it can
// never be sealed.
type WorldIDNullifier struct{}

// worldIDNullifierBytes is the big-endian width of the nullifier component.
const worldIDNullifierBytes = 32

// worldIDRelyingPartyDigestBytes is the width of the relying-party component
// of the TMPX token: the leading bytes of SHA-256(relying_party_id). Together
// with the nullifier it forms TmpxTokenSize(TmpxTypeWorldIDNullifier).
const worldIDRelyingPartyDigestBytes = 16

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

// Token builds the relying-party-scoped TMPX token: the first
// worldIDRelyingPartyDigestBytes of SHA-256(relyingPartyID) followed by the
// Decode'd nullifier. relyingPartyID must be the receiver-confirmed rp_id the
// proof was verified against; an empty relyingPartyID or a malformed nullifier
// is an error so the caller drops the identity rather than sealing an
// unscoped or malformed token.
func (e WorldIDNullifier) Token(ctx context.Context, relyingPartyID, nullifier string) ([]byte, error) {
	if relyingPartyID == "" {
		return nil, errors.New("world_id_nullifier: empty relying_party_id")
	}
	nullifierBytes, err := e.Decode(ctx, nullifier)
	if err != nil {
		return nil, err
	}
	digest := sha256.Sum256([]byte(relyingPartyID))
	token := make([]byte, worldIDRelyingPartyDigestBytes+worldIDNullifierBytes)
	copy(token, digest[:worldIDRelyingPartyDigestBytes])
	copy(token[worldIDRelyingPartyDigestBytes:], nullifierBytes)
	return token, nil
}
