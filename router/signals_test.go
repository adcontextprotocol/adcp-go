package router

import (
	"bytes"
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

// TestMergeSignals_SegmentsDeduplicated checks that a segment two providers
// both return reaches the ad server once rather than twice.
func TestMergeSignals_SegmentsDeduplicated(t *testing.T) {
	merged := mergedSignalsFor(t, nil,
		map[string]any{"segments": []any{"cooking", "shared"}},
		map[string]any{"segments": []any{"shared", "sustainability"}},
	)

	wire := decodeSignals(t, merged)
	assert.Equal(t, []any{"cooking", "shared", "sustainability"}, wire["segments"])
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

// TestMergeSignals_ExtensionKeyFirstProviderWins covers the keys the schema
// admits via additionalProperties but the spec defines no merge rule for. The
// first provider keeps the key and the conflict is logged rather than silently
// overwritten.
func TestMergeSignals_ExtensionKeyFirstProviderWins(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))

	merged := mergedSignalsFor(t, logger,
		map[string]any{"vendor_score": "high"},
		map[string]any{"vendor_score": "low"},
	)

	assert.Equal(t, "high", merged["vendor_score"], "first provider to supply the key keeps it")
	assert.Contains(t, buf.String(), "conflicting extension key in signals")
	assert.Contains(t, buf.String(), "first_provider=p1")
	assert.Contains(t, buf.String(), "conflicting_provider=p2")
}

// TestMergeSignals_MalformedEntrySkipped checks that a provider emitting the
// wrong shape for a spec-defined key loses only that key, without corrupting
// the merged list or dropping the other provider's contribution.
func TestMergeSignals_MalformedEntrySkipped(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))

	merged := mergedSignalsFor(t, logger,
		map[string]any{
			// segments as a bare string, and a kv entry missing `value`.
			"segments":      "cooking",
			"targeting_kvs": []any{map[string]any{"key": "sport"}},
		},
		map[string]any{"segments": []any{"sustainability"}},
	)

	wire := decodeSignals(t, merged)
	assert.Equal(t, []any{"sustainability"}, wire["segments"])
	assert.NotContains(t, wire, "targeting_kvs")
	assert.Contains(t, buf.String(), "skipping signals entry with unexpected shape")
}

// TestMergeSignals_AbsentWhenNoProviderContributes keeps `signals` omitted
// rather than emitting an empty object.
func TestMergeSignals_AbsentWhenNoProviderContributes(t *testing.T) {
	merged := mergedSignalsFor(t, nil, nil, map[string]any{})
	assert.Nil(t, merged)
}

// TestSignalStrings covers the shapes a segments list can arrive in: []any of
// string off the wire, []string when assembled in Go.
func TestSignalStrings(t *testing.T) {
	got, ok := signalStrings([]any{"a", "b"})
	require.True(t, ok)
	assert.Equal(t, []string{"a", "b"}, got)

	got, ok = signalStrings([]string{"a"})
	require.True(t, ok)
	assert.Equal(t, []string{"a"}, got)

	_, ok = signalStrings([]any{"a", 7})
	assert.False(t, ok, "a non-string element makes the whole list unusable")

	_, ok = signalStrings("a")
	assert.False(t, ok)
}

// TestSignalKVs covers the shapes a targeting_kvs list can arrive in.
func TestSignalKVs(t *testing.T) {
	got, ok := signalKVs([]any{map[string]any{"key": "k", "value": "v"}})
	require.True(t, ok)
	assert.Equal(t, []SignalKV{{Key: "k", Value: "v"}}, got)

	got, ok = signalKVs([]SignalKV{{Key: "k", Value: "v"}})
	require.True(t, ok)
	assert.Equal(t, []SignalKV{{Key: "k", Value: "v"}}, got)

	got, ok = signalKVs([]map[string]any{{"key": "k", "value": "v"}})
	require.True(t, ok)
	assert.Equal(t, []SignalKV{{Key: "k", Value: "v"}}, got)

	_, ok = signalKVs([]any{map[string]any{"key": "k"}})
	assert.False(t, ok, "value is required")

	_, ok = signalKVs([]any{map[string]any{"key": 1, "value": "v"}})
	assert.False(t, ok, "key must be a string")
}
