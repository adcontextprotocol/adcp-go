package router

import "github.com/adcontextprotocol/adcp-go/tmproto"

// enforceProviderSlotContract implements the router MUST introduced by
// adcontextprotocol/adcp#5971: a provider's emitted `tmpx_chunks` sequence
// must be an exact non-empty ordered prefix of that provider's registered
// `tmpx_slots` list from provider-registration.json.
//
// The router MUST drop the provider's chunks atomically — never partially
// trim, never forward — when the emitted slot_id sequence is:
//   - empty (no chunks to place),
//   - longer than the registered slot list,
//   - reordered relative to the registered list,
//   - sparse (skips a registered slot),
//   - populated with a slot_id not in the registered list, or
//   - populated with a duplicate slot_id (caught by the pointwise
//     comparison against the registered list, which is unique per the
//     schema's `uniqueItems: true` on `tmpx_slots`).
//
// Returns true iff the contract holds. The caller MUST NOT forward the
// provider's chunks into `tmpx_providers[provider_id]` when this returns
// false; the other providers on the same response are unaffected.
func enforceProviderSlotContract(registered []string, chunks []tmproto.TmpxChunk) bool {
	if len(chunks) == 0 || len(chunks) > len(registered) {
		return false
	}
	for i, c := range chunks {
		if registered[i] != c.SlotID {
			return false
		}
	}
	return true
}
