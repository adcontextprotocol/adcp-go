// Package tmpxdecoders converts an IdentityToken.UserToken string into the
// binary token TMPX packs into its encrypted plaintext, one decoder per
// UID type.
//
// Today, MAID and HashedEmail have real source-graph decoders; every other
// TMPX-encodable UID type uses a SHA-512-truncated stub that is NOT
// interoperable with any real buyer master. Stub decoders are placeholders
// for future per-type implementations and are flagged with a TODO at the
// point of definition.
//
// The TMPXSealer in targeting/identityagent declares an identically-shaped
// TmpxTokenDecoder interface; the concrete types here satisfy it via Go's
// structural typing, so identityagent does not need to import this package
// transitively through its test fakes.
package tmpxdecoders

import (
	"context"
	"crypto/sha512"
	"fmt"

	"github.com/adcontextprotocol/adcp-go/tmproto"
)

// Decoder is the per-UID-type adapter the registry maps to. Concrete decoders
// live in sibling files in this package.
//
// Decode receives the request's context.Context so HTTP-backed decoders
// (LiveRamp sidecar today; identity graph lookups tomorrow) can honor the
// caller's deadline. Stub and format-only decoders ignore it.
type Decoder interface {
	Decode(ctx context.Context, userToken string) ([]byte, error)
}

// sha512Truncate is the reference stub conversion shared by every UID type
// that does not yet have a real source-graph decoder. The output is
// deterministic for a given (token, size) pair so that retries within a
// session produce the same binary bytes, but it is NOT decodable by any
// real buyer master — the SHA-512 prefix bears no relationship to the
// underlying identifier.
func sha512Truncate(token string, size int) []byte {
	h := sha512.Sum512([]byte(token))
	out := make([]byte, size)
	copy(out, h[:size])
	return out
}

// StubbedUIDTypes returns the UID types whose decoder is currently the
// SHA-512-truncated stub. The order is undefined; callers that log it should
// sort for stable output. RampID / RampIDDerived are not included even when
// the operator has not configured a LiveRamp client — those types are
// "dropped, not stubbed" and surface via DroppedUIDTypes instead.
func StubbedUIDTypes() []tmproto.UIDType {
	out := make([]tmproto.UIDType, 0, len(stubDecoders))
	for k := range stubDecoders {
		out = append(out, k)
	}
	return out
}

// tokenSize returns TmpxTokenSize(typeID) or panics — used at package init
// time to bake the per-type stub size into the decoder. A panic here means
// the registry references a UID type whose TmpxTypeID is unknown to
// tmproto, which would be a build-time bug rather than a runtime one.
func tokenSize(typeID tmproto.TmpxTypeID) int {
	size, ok := tmproto.TmpxTokenSize(typeID)
	if !ok {
		panic(fmt.Sprintf("tmpxdecoders: unknown TmpxTypeID %d", typeID))
	}
	return size
}
