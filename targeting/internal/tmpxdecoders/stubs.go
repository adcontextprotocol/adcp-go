package tmpxdecoders

import (
	"context"

	"github.com/adcontextprotocol/adcp-go/tmproto"
)

// Stub decoders preserve the historical reference behavior: SHA-512 the
// supplied user_token and truncate to the type's binary size. The output is
// not decodable by any real buyer master. Each type gets its own struct so
// individual stubs can be replaced with a real source-graph decoder without
// touching the others.
//
// Per-type token sizes are resolved at construction via tokenSize(typeID),
// which is sourced from tmproto.TmpxTokenSize — keeping the stub aligned
// with whatever the spec declares for that type.
//
// RampID and RampIDDerived are intentionally absent from this file: those
// types are decoded via the LiveRamp sidecar (see rampid.go) and registered
// conditionally only when the operator wires a Client into NewDefaultRegistry.

// TODO: replace with a real UID2 advertising-token / raw_uid2 decoder. The
// UID2 spec defines raw_uid2 as a base64-encoded 32-byte value; the
// advertising token is a separately-encoded encrypted form. The right
// decoder depends on which form the upstream identity graph hands us.
type UID2Stub struct{}

func (UID2Stub) Decode(_ context.Context, userToken string) ([]byte, error) {
	return sha512Truncate(userToken, tokenSize(tmproto.TmpxTypeUID2)), nil
}

// TODO: replace with a real EUID decoder. EUID shares the UID2 wire format
// but has independent operational rules; gate the real implementation on
// confirming both forms decode identically before sharing code.
type EUIDStub struct{}

func (EUIDStub) Decode(_ context.Context, userToken string) ([]byte, error) {
	return sha512Truncate(userToken, tokenSize(tmproto.TmpxTypeEUID)), nil
}

// TODO: replace with a real Google PAIR ID decoder. PAIR's published binary
// form is documented; this stub stands in until a decoder lands.
type PairIDStub struct{}

func (PairIDStub) Decode(_ context.Context, userToken string) ([]byte, error) {
	return sha512Truncate(userToken, tokenSize(tmproto.TmpxTypePairID)), nil
}

// TODO: replace with a real publisher-first-party decoder. By definition
// the binary form is publisher-defined and so a single shared decoder may
// not be possible — this may end up as a pluggable per-tenant decoder
// rather than a single struct.
type PublisherFirstPartyStub struct{}

func (PublisherFirstPartyStub) Decode(_ context.Context, userToken string) ([]byte, error) {
	return sha512Truncate(userToken, tokenSize(tmproto.TmpxTypePublisherFirstParty)), nil
}

// stubDecoders enumerates the UID types currently served by a SHA-512 stub.
// RampID / RampIDDerived are excluded: when LiveRamp is configured they have
// real decoders, and when it is not they are intentionally absent so
// selectEntries drops them silently.
var stubDecoders = map[tmproto.UIDType]Decoder{
	tmproto.UIDTypeUID2:                UID2Stub{},
	tmproto.UIDTypeEUID:                EUIDStub{},
	tmproto.UIDTypePairID:              PairIDStub{},
	tmproto.UIDTypePublisherFirstParty: PublisherFirstPartyStub{},
}
