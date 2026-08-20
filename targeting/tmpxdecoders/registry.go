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
// default registry. Each opt-in lever is nil by default; when nil, the
// corresponding UID types are omitted from the registry and the agent's
// selectEntries silently drops them from the TMPX wire.
//
//   - LiveRampClient: enables RampID and RampIDDerived decoding.
//   - UID2Client:     enables UID2 advertising-token decryption.
//   - EUIDClient:     enables EUID advertising-token decryption.
//
// UID2 and EUID clients are separate because each is scope-bound (a UID2
// client cannot decrypt an EUID token and vice versa). A deployment that
// only accepts one scope leaves the other nil.
type RegistryOptions struct {
	LiveRampClient LiveRampClient
	UID2Client     UID2Client
	EUIDClient     UID2Client
}

// NewDefaultRegistry returns the canonical UID type → decoder map TMPX uses
// to convert IdentityToken.UserToken values into binary TMPX tokens. MAID,
// HashedEmail, and ID5 are format-only decoders that need no external
// dependency. RampID and RampIDDerived appear only when opts.LiveRampClient
// is non-nil. UID types without a registered decoder are silently dropped
// at decode time.
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
