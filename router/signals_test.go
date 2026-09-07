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
	return mergeContextResponses("ctx-signals", buildContextResults(providerSignals), logger).Signals
}

// mergedSignalsByProviderFor runs the same merge but returns the
// router-authored signals_by_provider bucket. AdCP 3.2 moved
// provider-local targeting_kvs off the flattened signals map into this
// per-provider structure, keyed by the publisher-assigned provider_id.
func mergedSignalsByProviderFor(t *testing.T, logger *slog.Logger, providerSignals ...map[string]any) map[string]map[string]any {
	t.Helper()
	return mergeContextResponses("ctx-signals", buildContextResults(providerSignals), logger).SignalsByProvider
}

func buildContextResults(providerSignals []map[string]any) []contextResult {
	results := make([]contextResult, 0, len(providerSignals))
	for i, sig := range providerSignals {
		results = append(results, contextResult{
			providerID: []string{"p1", "p2", "p3"}[i],
			response:   &tmproto.ProviderContextMatchResponse{Signals: sig},
		})
	}
	return results
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

// TestMergeSignals_TargetingKVsAttributedByProvider pins the AdCP 3.2
// change: provider-local targeting_kvs are attributed to the emitting
// provider_id and returned under signals_by_provider on the router→publisher
// response. Each provider's list is preserved unchanged (including
// same-key entries across providers) so publisher deployment configuration
// can resolve every (provider_id, key) tuple through targeting_kv_mapping.
func TestMergeSignals_TargetingKVsAttributedByProvider(t *testing.T) {
	byProvider := mergedSignalsByProviderFor(t, nil,
		map[string]any{"targeting_kvs": []any{
			map[string]any{"key": "sport", "value": "nfl"},
		}},
		map[string]any{"targeting_kvs": []any{
			map[string]any{"key": "sport", "value": "nba"},
			map[string]any{"key": "genre", "value": "news"},
		}},
	)

	assert.Equal(t, map[string]map[string]any{
		"p1": {"targeting_kvs": []any{
			map[string]any{"key": "sport", "value": "nfl"},
		}},
		"p2": {"targeting_kvs": []any{
			map[string]any{"key": "sport", "value": "nba"},
			map[string]any{"key": "genre", "value": "news"},
		}},
	}, byProvider, "each provider's key-values survive in its own attributed bucket")
}

// TestMergeSignals_TargetingKVsNeverFlattenedOnRouterHop enforces the
// AdCP 3.2 router-hop schema rule: signals.targeting_kvs is a
// provider-hop-only field and MUST NOT appear on the merged
// router→publisher response. The value moved to signals_by_provider;
// leaking it back into the flattened signals object would erase
// provider attribution and let the publisher accidentally treat
// provider-local keys as its own ad-server namespace.
func TestMergeSignals_TargetingKVsNeverFlattenedOnRouterHop(t *testing.T) {
	merged := mergedSignalsFor(t, nil,
		map[string]any{"targeting_kvs": []any{
			map[string]any{"key": "sport", "value": "nfl"},
		}},
	)
	if merged != nil {
		assert.NotContains(t, merged, "targeting_kvs",
			"router-hop signals MUST NOT carry flattened targeting_kvs")
	}
}

// TestMergeSignals_TargetingKVsMalformedEntryPreserved pins that a
// schema-invalid entry is the provider's defect to answer for, not
// something the router silently discards. The entry is preserved in
// the emitting provider's attributed bucket exactly as sent.
func TestMergeSignals_TargetingKVsMalformedEntryPreserved(t *testing.T) {
	byProvider := mergedSignalsByProviderFor(t, nil,
		map[string]any{"targeting_kvs": []any{
			map[string]any{"key": "sport", "value": "nfl"},
			map[string]any{"key": "broken"}, // missing `value`
		}},
		map[string]any{"targeting_kvs": []any{
			map[string]any{"key": "genre", "value": "news"},
		}},
	)

	assert.Equal(t, []any{
		map[string]any{"key": "sport", "value": "nfl"},
		map[string]any{"key": "broken"},
	}, byProvider["p1"]["targeting_kvs"], "one bad entry must not cost the provider its valid ones")
	assert.Equal(t, []any{
		map[string]any{"key": "genre", "value": "news"},
	}, byProvider["p2"]["targeting_kvs"])
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
