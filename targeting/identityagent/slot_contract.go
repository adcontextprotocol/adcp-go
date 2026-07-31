package identityagent

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
//   - populated with a duplicate slot_id.
//
// Returns true iff the contract holds. The caller MUST NOT forward the
// provider's chunks into `tmpx_providers[provider_id]` when this returns
// false; the other providers on the same response are unaffected.
//
// The validation depends only on the emitted slot_id sequence — chunk
// values are irrelevant to the contract — so the caller extracts slot_ids
// from `[]tmproto.TmpxChunk` at the call site.
func enforceProviderSlotContract(registered, emitted []string) bool {
	if len(emitted) == 0 || len(emitted) > len(registered) {
		return false
	}
	for i, slotID := range emitted {
		if registered[i] != slotID {
			return false
		}
	}
	return true
}
