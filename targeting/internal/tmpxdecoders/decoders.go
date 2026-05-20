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

import "context"

// Decoder is the per-UID-type adapter the registry maps to. Concrete decoders
// live in sibling files in this package.
//
// Decode receives the request's context.Context so HTTP-backed decoders
// (LiveRamp sidecar today; identity graph lookups tomorrow) can honor the
// caller's deadline. Format-only decoders ignore it.
type Decoder interface {
	Decode(ctx context.Context, userToken string) ([]byte, error)
}
