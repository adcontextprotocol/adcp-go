package router

import (
	"log/slog"
	"maps"
)

// Keys the spec defines inside a Context Match response's `signals` object
// (context-match-response.json §signals).
const (
	signalSegmentsKey     = "segments"
	signalTargetingKVsKey = "targeting_kvs"
)

// SignalKV is the KeyValuePair shape carried in a Context Match response's
// `signals.targeting_kvs` (context-match-response.json §KeyValuePair).
type SignalKV struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

// signalsMerger implements the enrichment-signal merge rules from
// docs/trusted-match/router-architecture.mdx §"Context Match fan-out" step 4:
// "Enrichment signals are concatenated. Segments from all providers are
// combined into a single list. Targeting key-values from different providers
// are namespaced to prevent collisions."
//
// Per key:
//
//   - `segments` — concatenated across providers in merge order, dropping
//     exact repeats so a segment two providers both return reaches the ad
//     server once.
//
//   - `targeting_kvs` — concatenated, with keys left exactly as the provider
//     sent them. The list is an array, so two providers both returning `sport`
//     yield two entries rather than one overwriting the other; nothing is lost
//     without renaming anything.
//
//     The spec sentence above adds "namespaced to prevent collisions", but it
//     pins no scheme — no separator, no format — so any prefix a router invented
//     would be unportable: a publisher's line items would break moving between
//     two conformant routers. It would also contradict how the spec handles the
//     same problem for TMPX, where destination naming is explicitly
//     publisher-owned and the router never mints a name in the publisher's
//     ad-server namespace. Renaming is therefore left to the publisher, who has
//     the mapping config; see adcontextprotocol/adcp#6252.
//
//   - anything else — `signals` is additionalProperties: true, so providers
//     may add their own keys and the spec defines no merge rule for them. The
//     first provider to supply a key keeps it, mirroring the offers path's
//     first-response-wins, and a later conflicting provider is logged instead
//     of silently overwriting.
//
// Concatenation order follows the order responses were merged, which is
// arrival order — the same nondeterminism the spec already accepts for offers
// ("the router keeps the first response received").
type signalsMerger struct {
	segments     []string
	seenSegments map[string]struct{}
	targetingKVs []SignalKV
	extra        map[string]any
	extraOwner   map[string]string
}

func newSignalsMerger() *signalsMerger {
	return &signalsMerger{
		seenSegments: make(map[string]struct{}),
		extra:        make(map[string]any),
		extraOwner:   make(map[string]string),
	}
}

// add folds one provider's signals object into the merge. providerID is used for
// attributing extension-key conflicts and shape warnings, not for rewriting any
// value the provider sent.
func (m *signalsMerger) add(providerID string, signals map[string]any, requestID string, logger *slog.Logger) {
	for key, value := range signals {
		switch key {
		case signalSegmentsKey:
			segments, ok := signalStrings(value)
			if !ok {
				logSignalShape(logger, requestID, providerID, key)
				continue
			}
			for _, s := range segments {
				if _, dup := m.seenSegments[s]; dup {
					continue
				}
				m.seenSegments[s] = struct{}{}
				m.segments = append(m.segments, s)
			}
		case signalTargetingKVsKey:
			kvs, ok := signalKVs(value)
			if !ok {
				logSignalShape(logger, requestID, providerID, key)
				continue
			}
			m.targetingKVs = append(m.targetingKVs, kvs...)
		default:
			if owner, taken := m.extraOwner[key]; taken {
				if owner != providerID && logger != nil {
					logger.Warn("conflicting extension key in signals — keeping the first provider's value",
						"request_id", requestID,
						"signal_key", key,
						"first_provider", owner,
						"conflicting_provider", providerID,
					)
				}
				continue
			}
			m.extraOwner[key] = providerID
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

// signalStrings reads a `segments` value. Responses arrive through
// json.Unmarshal so the wire shape is []any of string; the []string case
// covers signals assembled in Go (tests, embedders). Reports false for any
// other shape so the caller can skip it rather than emit a malformed list.
func signalStrings(value any) ([]string, bool) {
	switch v := value.(type) {
	case []string:
		return v, true
	case []any:
		out := make([]string, 0, len(v))
		for _, entry := range v {
			s, ok := entry.(string)
			if !ok {
				return nil, false
			}
			out = append(out, s)
		}
		return out, true
	default:
		return nil, false
	}
}

// signalKVs reads a `targeting_kvs` value. Handles the wire shape ([]any of
// map[string]any) plus the Go-assembled shapes. Both `key` and `value` are
// required strings per the schema; an entry missing either makes the whole
// value unusable, because partially emitting one provider's key-values would
// silently drop targeting the publisher expects.
func signalKVs(value any) ([]SignalKV, bool) {
	switch v := value.(type) {
	case []SignalKV:
		return v, true
	case []any:
		out := make([]SignalKV, 0, len(v))
		for _, entry := range v {
			kv, ok := signalKVFromAny(entry)
			if !ok {
				return nil, false
			}
			out = append(out, kv)
		}
		return out, true
	case []map[string]any:
		out := make([]SignalKV, 0, len(v))
		for _, entry := range v {
			kv, ok := signalKVFromMap(entry)
			if !ok {
				return nil, false
			}
			out = append(out, kv)
		}
		return out, true
	default:
		return nil, false
	}
}

func signalKVFromAny(entry any) (SignalKV, bool) {
	switch e := entry.(type) {
	case SignalKV:
		return e, true
	case map[string]any:
		return signalKVFromMap(e)
	default:
		return SignalKV{}, false
	}
}

func signalKVFromMap(entry map[string]any) (SignalKV, bool) {
	key, keyOK := entry["key"].(string)
	value, valueOK := entry["value"].(string)
	if !keyOK || !valueOK {
		return SignalKV{}, false
	}
	return SignalKV{Key: key, Value: value}, true
}

// logSignalShape reports a provider whose signals entry did not match the
// schema shape. DEBUG rather than WARN: the per-provider error counter is not
// incremented for a malformed sub-field of an otherwise valid response, and a
// provider stuck emitting the wrong shape would otherwise log on every
// request.
func logSignalShape(logger *slog.Logger, requestID, providerID, key string) {
	if logger == nil {
		return
	}
	logger.Debug("skipping signals entry with unexpected shape",
		"request_id", requestID,
		"provider", providerID,
		"signal_key", key,
	)
}
