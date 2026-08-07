package router

import (
	"encoding/json"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/adcontextprotocol/adcp-go/tmproto"
)

// mergedSignalsFor runs the context merge over the given per-provider signals
// and returns the merged `signals` object. Provider order is the merge order,
// which the concatenation rules preserve.
func mergedSignalsFor(t *testing.T, logger *slog.Logger, providerSignals ...map[string]any) map[string]any {
	t.Helper()
	results := make([]contextResult, 0, len(providerSignals))
	for i, sig := range providerSignals {
		results = append(results, contextResult{
			providerID: []string{"p1", "p2", "p3"}[i],
			response:   &tmproto.ContextMatchResponse{Signals: sig},
		})
	}
	return mergeContextResponses("ctx-signals", results, logger).Signals
}

// decodeSignals round-trips the merged object through JSON, which is what a
// publisher actually receives. Asserting on the wire form catches a merge that
// builds a Go value the encoder renders wrong.
func decodeSignals(t *testing.T, signals map[string]any) map[string]any {
	t.Helper()
	raw, err := json.Marshal(signals)
	require.NoError(t, err)
	var out map[string]any
	require.NoError(t, json.Unmarshal(raw, &out))
	return out
}

// TestMergeSignals_SegmentsConcatenated pins the spec rule from
// docs/trusted-match/router-architecture.mdx §"Context Match fan-out" step 4:
// "Segments from all providers are combined into a single list." Before this
// was implemented the merge was a map copy, so the second provider's segments
// silently replaced the first's.
func TestMergeSignals_SegmentsConcatenated(t *testing.T) {
	merged := mergedSignalsFor(t, nil,
		map[string]any{"segments": []any{"cooking", "recipes"}},
		map[string]any{"segments": []any{"sustainability"}},
	)

	wire := decodeSignals(t, merged)
	assert.Equal(t, []any{"cooking", "recipes", "sustainability"}, wire["segments"],
		"every provider's segments must survive the merge, in merge order")
}

// TestMergeSignals_SegmentsNotDeduplicated pins that a segment two providers
// both return appears twice. The spec says "combined into a single list", not
// deduplicated — collapsing repeats would be the router deciding something the
// publisher is entitled to decide.
func TestMergeSignals_SegmentsNotDeduplicated(t *testing.T) {
	merged := mergedSignalsFor(t, nil,
		map[string]any{"segments": []any{"cooking", "shared"}},
		map[string]any{"segments": []any{"shared", "sustainability"}},
	)

	wire := decodeSignals(t, merged)
	assert.Equal(t, []any{"cooking", "shared", "shared", "sustainability"}, wire["segments"])
}

// TestMergeSignals_TargetingKVsConcatenatedVerbatim pins the other half of step
// 4. Two providers returning the same key both survive — targeting_kvs is an
// array, so nothing has to be renamed to avoid losing one. Keys are passed
// through exactly as sent: the spec's "namespaced to prevent collisions" pins no
// scheme, and a router-invented prefix would be unportable across
// implementations and would put the router in the publisher's ad-server
// namespace, which the spec's TMPX design explicitly forbids.
func TestMergeSignals_TargetingKVsConcatenatedVerbatim(t *testing.T) {
	merged := mergedSignalsFor(t, nil,
		map[string]any{"targeting_kvs": []any{
			map[string]any{"key": "sport", "value": "nfl"},
		}},
		map[string]any{"targeting_kvs": []any{
			map[string]any{"key": "sport", "value": "nba"},
			map[string]any{"key": "genre", "value": "news"},
		}},
	)

	wire := decodeSignals(t, merged)
	assert.Equal(t, []any{
		map[string]any{"key": "sport", "value": "nfl"},
		map[string]any{"key": "sport", "value": "nba"},
		map[string]any{"key": "genre", "value": "news"},
	}, wire["targeting_kvs"], "every provider's key-values survive, unrenamed")
}

// TestMergeSignals_MalformedEntryForwardedNotDropped pins that a schema-invalid
// entry is the provider's defect to answer for, not something the router
// silently discards. Dropping it — or worse, dropping that provider's whole
// list because one entry was bad — would mean the router deciding to withhold
// targeting the publisher was sent, which the spec nowhere asks for.
func TestMergeSignals_MalformedEntryForwardedNotDropped(t *testing.T) {
	merged := mergedSignalsFor(t, nil,
		map[string]any{"targeting_kvs": []any{
			map[string]any{"key": "sport", "value": "nfl"},
			map[string]any{"key": "broken"}, // missing `value`
		}},
		map[string]any{"targeting_kvs": []any{
			map[string]any{"key": "genre", "value": "news"},
		}},
	)

	wire := decodeSignals(t, merged)
	assert.Equal(t, []any{
		map[string]any{"key": "sport", "value": "nfl"},
		map[string]any{"key": "broken"},
		map[string]any{"key": "genre", "value": "news"},
	}, wire["targeting_kvs"], "one bad entry must not cost the provider its valid ones")
}

// TestMergeSignals_NonArrayCannotDisplaceOthers covers the one shape the merge
// cannot concatenate. A provider sending `segments` as a bare string contributes
// nothing, but must not replace what other providers sent — which is what the
// previous map-copy merge did.
func TestMergeSignals_NonArrayCannotDisplaceOthers(t *testing.T) {
	merged := mergedSignalsFor(t, nil,
		map[string]any{"segments": "cooking"},
		map[string]any{"segments": []any{"sustainability"}},
	)

	wire := decodeSignals(t, merged)
	assert.Equal(t, []any{"sustainability"}, wire["segments"])
}

// TestMergeSignals_AbsentWhenNoProviderContributes keeps `signals` omitted
// rather than emitting an empty object.
func TestMergeSignals_AbsentWhenNoProviderContributes(t *testing.T) {
	merged := mergedSignalsFor(t, nil, nil, map[string]any{})
	assert.Nil(t, merged)
}
