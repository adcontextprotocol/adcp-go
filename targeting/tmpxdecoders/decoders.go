// Package tmpxdecoders converts an IdentityToken.UserToken string into the
// binary token TMPX packs into its encrypted plaintext, one decoder per
// UID type.
//
// MAID, HashedEmail, and ID5 have format-only decoders. RampID and
// RampIDDerived are decoded via the LiveRamp sidecar when one is configured.
// UID types without a registered decoder are silently dropped from both the
// TMPX wire and the audience/fcap shadow request.
//
// The TMPXSealer in targeting/identityagent declares an identically-shaped
// TmpxTokenDecoder interface; the concrete types here satisfy it via Go's
// structural typing, so identityagent does not need to import this package
// transitively through its test fakes.
package tmpxdecoders

import (
	"context"
	"encoding/hex"

	"github.com/adcontextprotocol/adcp-go/tmproto"
)

// Decoder is the per-UID-type adapter the registry maps to. Concrete decoders
// live in sibling files in this package.
//
// Decode receives the request's context.Context so HTTP-backed decoders
// (LiveRamp sidecar today; identity graph lookups tomorrow) can honor the
// caller's deadline. Format-only decoders ignore it.
type Decoder interface {
	Decode(ctx context.Context, userToken string) ([]byte, error)
}

// Canonical returns the canonical lowercase-hex key form for a decoded
// identity token — the shape ExposureLog.user_token publishes on the
// wire and the shape fcap and audience markers are keyed by downstream.
//
// Output is lowercase because encoding/hex emits 0-9a-f. Changing the
// encoding here would silently invalidate every existing key in every
// downstream store, so this helper is the single contract anchor:
// callers that key on a user identity route through Canonical (directly
// or via DecodeHex), never through ad-hoc encoders.
func Canonical(b []byte) string {
	return hex.EncodeToString(b)
}

// DecodeHex decodes userToken via the registry's decoder for uidType
// and returns the Canonical form of the resulting bytes — i.e. the
// lowercase-hex key form ExposureLog.user_token uses.
//
// Returns:
//   - ("", ErrDropFromSeal) when the decoder asks the caller to drop
//     the identity (e.g. a LiveRamp miss).
//   - ("", nil) when uidType has no registered decoder. Same silent-
//     drop semantics the rest of the registry uses; callers that want
//     to surface unsupported types must check membership in the
//     registry themselves.
//   - ("", err) on any other decoder failure.
func DecodeHex(ctx context.Context, registry map[tmproto.UIDType]Decoder, uidType tmproto.UIDType, userToken string) (string, error) {
	dec, ok := registry[uidType]
	if !ok {
		return "", nil
	}
	b, err := dec.Decode(ctx, userToken)
	if err != nil {
		return "", err
	}
	return Canonical(b), nil
}
