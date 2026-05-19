package tmpxdecoders

import "github.com/adcontextprotocol/adcp-go/tmproto"

// realDecoders holds the per-UID-type decoders that produce buyer-decodable
// binary tokens without any external dependency. New format-only real
// decoders move from stubDecoders into this map.
var realDecoders = map[tmproto.UIDType]Decoder{
	tmproto.UIDTypeMAID:        MAID{},
	tmproto.UIDTypeHashedEmail: HashedEmail{},
}

// RegistryOptions controls which TMPX-encodable UID types end up in the
// default registry. Today the only opt-in lever is the LiveRamp sidecar:
// when LiveRampClient is non-nil, RampID and RampIDDerived gain real
// decoders backed by the sidecar; when it is nil, those UID types are
// omitted from the registry entirely and the agent's selectEntries silently
// drops them from the TMPX wire (the operator-visible behavior is: "no
// LiveRamp config → RampIDs are ignored").
type RegistryOptions struct {
	LiveRampClient LiveRampClient
}

// NewDefaultRegistry returns the canonical UID type → decoder map TMPX uses
// to convert IdentityToken.UserToken values into binary TMPX tokens. MAID
// and HashedEmail are real implementations; UID2 / EUID / ID5 / PairID /
// PublisherFirstParty are SHA-512-truncated stubs (each flagged with a
// TODO at its definition site). RampID and RampIDDerived appear only when
// opts.LiveRampClient is non-nil.
//
// The returned map is freshly allocated on every call so callers can mutate
// it (e.g. swap in a custom decoder for tests) without affecting other
// callers.
func NewDefaultRegistry(opts RegistryOptions) map[tmproto.UIDType]Decoder {
	out := make(map[tmproto.UIDType]Decoder, len(realDecoders)+len(stubDecoders)+2)
	for k, v := range realDecoders {
		out[k] = v
	}
	for k, v := range stubDecoders {
		out[k] = v
	}
	if opts.LiveRampClient != nil {
		out[tmproto.UIDTypeRampID] = RampID{Client: opts.LiveRampClient}
		out[tmproto.UIDTypeRampIDDerived] = RampIDDerived{Client: opts.LiveRampClient}
	}
	return out
}

// RealUIDTypes returns the UID types whose decoder produces buyer-decodable
// canonical bytes (as opposed to a SHA-512 stub). Audience and frequency-
// cap lookups consume this list to decide which identities have a
// downstream-joinable form; types absent from this list still flow through
// the TMPX seal path (as stubs) but are ignored for non-TMPX lookups.
//
// Adding a real decoder is intentionally a single-file change: move the
// type from stubDecoders to realDecoders in this package and RealUIDTypes
// updates automatically.
func RealUIDTypes(liveRampEnabled bool) []tmproto.UIDType {
	out := make([]tmproto.UIDType, 0, len(realDecoders)+2)
	for k := range realDecoders {
		out = append(out, k)
	}
	if liveRampEnabled {
		out = append(out, tmproto.UIDTypeRampID, tmproto.UIDTypeRampIDDerived)
	}
	return out
}
