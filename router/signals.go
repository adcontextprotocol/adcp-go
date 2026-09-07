package router

import "maps"

// Keys the spec defines inside a Context Match response's `signals` object
// (context-match-response.json §signals).
const (
	signalSegmentsKey     = "segments"
	signalTargetingKVsKey = "targeting_kvs"
)

// signalsMerger implements the enrichment-signal merge from
// docs/trusted-match/router-architecture.mdx §"Context Match fan-out" step 4:
// non-attributed signals like `segments` concatenate into the merged
// response's `signals`; provider-local `targeting_kvs` do NOT — they are
// moved into `signals_by_provider[provider_id].targeting_kvs`, keyed by
// the publisher-assigned provider_id from provider registration.
//
// Per AdCP 3.2 the router-hop `context-match-response.json` FORBIDS
// flattened `signals.targeting_kvs` (even when the attributed bucket is
// absent) — so this merger strips that key from the router-facing
// signals output. Publishers resolve each (provider_id, key) tuple to
// a local ad-server destination via their `targeting_kv_mapping`
// deployment configuration.
//
// `segments` is an array, so concatenating it neither inspects nor
// rewrites anything a provider sent:
//
//   - No per-entry validation. An entry that does not match the schema is the
//     provider's defect. Validating would force a choice the spec does not
//     make — drop the entry, or drop that provider's whole list — and either
//     way the router would be discarding data the publisher was sent.
//   - No deduplication. The spec says "combined into a single list", not
//     deduplicated. If two providers return the same segment, the publisher
//     receives it twice and decides what that means.
//
// Any other key on `signals`: additionalProperties: true, no merge rule
// defined, so they keep a plain overwrite in merge order.
//
// Concatenation follows merge order, which is arrival order — the same
// nondeterminism the spec already accepts for offers ("the router keeps the
// first response received").
type signalsMerger struct {
	segments []any
	extra    map[string]any
	// byProvider accumulates the router-authored
	// signals_by_provider bucket. Keyed by the publisher-assigned
	// provider_id from provider registration; inner value carries at
	// minimum `targeting_kvs` when the provider emitted any. The
	// router derives the outer key from registration and MUST NOT
	// accept a provider-supplied bucket.
	byProvider map[string]map[string]any
}

func newSignalsMerger() *signalsMerger {
	return &signalsMerger{
		extra:      make(map[string]any),
		byProvider: make(map[string]map[string]any),
	}
}

// add folds one provider's signals object into the merge, moving
// provider-local targeting_kvs into the attributed bucket for
// providerID and dropping them from the flattened signals output.
func (m *signalsMerger) add(providerID string, signals map[string]any) {
	for key, value := range signals {
		switch key {
		case signalSegmentsKey:
			m.segments = appendSignalList(m.segments, value)
		case signalTargetingKVsKey:
			// Skip empty providerID: a fan-out result without a
			// registered provider_id cannot be attributed and MUST NOT
			// leak into an unkeyed bucket. This matches the identity-hop
			// tmpx guard at mergeIdentityResponses.
			if providerID == "" {
				continue
			}
			kvs := appendSignalList(nil, value)
			if len(kvs) == 0 {
				continue
			}
			bucket, ok := m.byProvider[providerID]
			if !ok {
				bucket = make(map[string]any, 1)
				m.byProvider[providerID] = bucket
			}
			existing, _ := bucket[signalTargetingKVsKey].([]any)
			bucket[signalTargetingKVsKey] = appendSignalList(existing, value)
		default:
			m.extra[key] = value
		}
	}
}

// result returns the merged non-keyed signals object, or nil when no
// provider contributed anything (so the field is omitted from the
// response). The router-authored provider buckets are returned
// separately via byProviderResult.
func (m *signalsMerger) result() map[string]any {
	out := make(map[string]any, len(m.extra)+1)
	maps.Copy(out, m.extra)
	if len(m.segments) > 0 {
		out[signalSegmentsKey] = m.segments
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// byProviderResult returns the router-authored signals_by_provider map
// or nil when no provider contributed any targeting_kvs.
func (m *signalsMerger) byProviderResult() map[string]map[string]any {
	if len(m.byProvider) == 0 {
		return nil
	}
	return m.byProvider
}

// appendSignalList concatenates one provider's array-valued signal onto the
// accumulator. Responses arrive through json.Unmarshal so the wire shape is
// []any; the []string case covers signals assembled in Go.
//
// A value that is not an array cannot be concatenated — the schema types both
// fields as arrays — so it contributes nothing. It is not allowed to replace
// what other providers sent, which is what the previous map-copy merge did.
func appendSignalList(dst []any, value any) []any {
	switch v := value.(type) {
	case []any:
		return append(dst, v...)
	case []string:
		for _, s := range v {
			dst = append(dst, s)
		}
		return dst
	default:
		return dst
	}
}
