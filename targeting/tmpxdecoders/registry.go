package tmpxdecoders

import (
	"maps"

	"github.com/adcontextprotocol/adcp-go/tmproto"
)

// formatOnlyDecoders holds the per-UID-type decoders that produce
// buyer-decodable binary tokens by parsing the inbound user_token string
// directly, with no external dependency. Decoders that need an outside
// service (e.g. LiveRamp) are attached separately in NewDefaultRegistry.
var formatOnlyDecoders = map[tmproto.UIDType]Decoder{
	tmproto.UIDTypeMAID:        MAID{},
	tmproto.UIDTypeHashedEmail: HashedEmail{},
	tmproto.UIDTypeID5:         ID5{},
}

// RegistryOptions controls which TMPX-encodable UID types end up in the
// default registry. Every opt-in lever is a client for an external
// dependency the decoder needs to resolve inbound user tokens:
//
//   - LiveRampClient wires RampID and RampIDDerived via the LiveRamp
//     mapping sidecar.
//   - UID2Client wires UID2 via an operator adapter that resolves the
//     encrypted advertising token to the raw 32-byte UID2.
//   - EUIDClient wires EUID via an EU-jurisdiction operator adapter.
//     EUID and UID2 share the client interface but use distinct
//     endpoints and credentials, so they take separate fields.
//
// A nil field omits the corresponding UID type(s) from the registry
// and the identity-agent's selectEntries silently drops them from
// the TMPX wire (the operator-visible behavior is: "no client
// configured → that identity family is ignored").
type RegistryOptions struct {
	LiveRampClient LiveRampClient
	UID2Client     UID2Client
	EUIDClient     UID2Client
}

// NewDefaultRegistry returns the canonical UID type → decoder map TMPX uses
// to convert IdentityToken.UserToken values into binary TMPX tokens. MAID,
// HashedEmail, and ID5 are format-only decoders that need no external
// dependency. Each of RampID / RampIDDerived, UID2, and EUID appears only
// when its corresponding client on opts is non-nil. UID types without a
// registered decoder are silently dropped at decode time.
//
// WorldIDNullifier is deliberately absent: a World ID nullifier is
// verify-before-trust and must never be decoded from an inbound,
// sender-asserted IdentityToken. It reaches the wire only via the
// verified-identity stage, which encodes the verifier-derived nullifier
// directly (see TMPXSealer.verifiedIdentityEntries). An inbound
// world_id_nullifier token therefore has no decoder here and is dropped.
//
// The returned map is freshly allocated on every call so callers can mutate
// it (e.g. swap in a custom decoder for tests) without affecting other
// callers.
func NewDefaultRegistry(opts RegistryOptions) map[tmproto.UIDType]Decoder {
	out := make(map[tmproto.UIDType]Decoder, len(formatOnlyDecoders)+4)
	maps.Copy(out, formatOnlyDecoders)
	if opts.LiveRampClient != nil {
		out[tmproto.UIDTypeRampID] = RampID{Client: opts.LiveRampClient}
		out[tmproto.UIDTypeRampIDDerived] = RampIDDerived{Client: opts.LiveRampClient}
	}
	if opts.UID2Client != nil {
		out[tmproto.UIDTypeUID2] = UID2{Client: opts.UID2Client}
	}
	if opts.EUIDClient != nil {
		out[tmproto.UIDTypeEUID] = EUID{Client: opts.EUIDClient}
	}
	return out
}
