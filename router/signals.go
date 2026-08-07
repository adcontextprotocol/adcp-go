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
// "Enrichment signals are concatenated. Segments from all providers are
// combined into a single list. Targeting key-values from different providers
// are namespaced to prevent collisions."
//
// `segments` and `targeting_kvs` are both arrays, so concatenating them neither
// inspects nor rewrites anything a provider sent. What that rules out is as
// much the point as what it does:
//
//   - No per-entry validation. An entry that does not match the schema is the
//     provider's defect. Validating would force a choice the spec does not
//     make — drop the entry, or drop that provider's whole list — and either
//     way the router would be discarding targeting the publisher was sent.
//     Malformed entries pass through exactly as they did before this merge
//     existed.
//   - No deduplication. The spec says "combined into a single list", not
//     deduplicated. If two providers return the same segment, the publisher
//     receives it twice and decides what that means.
//   - No key namespacing, despite the sentence above. It pins no scheme, so a
//     router-invented prefix would not be portable between implementations,
//     and it would put the router inside the publisher's ad-server namespace —
//     which the spec's TMPX design explicitly forbids. See
//     adcontextprotocol/adcp#6252.
//
// Any other key: `signals` is additionalProperties: true and the spec defines
// no merge rule for those, so they keep a plain overwrite in merge order.
//
// Concatenation follows merge order, which is arrival order — the same
// nondeterminism the spec already accepts for offers ("the router keeps the
// first response received").
type signalsMerger struct {
	segments     []any
	targetingKVs []any
	extra        map[string]any
}

func newSignalsMerger() *signalsMerger {
	return &signalsMerger{extra: make(map[string]any)}
}

// add folds one provider's signals object into the merge.
func (m *signalsMerger) add(signals map[string]any) {
	for key, value := range signals {
		switch key {
		case signalSegmentsKey:
			m.segments = appendSignalList(m.segments, value)
		case signalTargetingKVsKey:
			m.targetingKVs = appendSignalList(m.targetingKVs, value)
		default:
			m.extra[key] = value
		}
	}
}

// result returns the merged signals object, or nil when no provider
// contributed anything (so the field is omitted from the response).
func (m *signalsMerger) result() map[string]any {
	out := make(map[string]any, len(m.extra)+2)
	maps.Copy(out, m.extra)
	if len(m.segments) > 0 {
		out[signalSegmentsKey] = m.segments
	}
	if len(m.targetingKVs) > 0 {
		out[signalTargetingKVsKey] = m.targetingKVs
	}
	if len(out) == 0 {
		return nil
	}
	return out
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
