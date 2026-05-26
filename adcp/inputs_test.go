package adcp

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCreativeAssignmentWeightZeroAndExtraRoundTrip(t *testing.T) {
	in := []byte(`{"creative_id":"cr-1","weight":0,"placement_ids":["hero"],"vendor_hint":"x"}`)

	var assignment CreativeAssignment
	require.NoError(t, json.Unmarshal(in, &assignment))
	require.NotNil(t, assignment.Weight)
	assert.Equal(t, float64(0), *assignment.Weight)
	assert.Equal(t, map[string]any{"vendor_hint": "x"}, assignment.Extra)

	out, err := json.Marshal(assignment)
	require.NoError(t, err)
	var wire map[string]any
	require.NoError(t, json.Unmarshal(out, &wire))
	assert.Equal(t, "cr-1", wire["creative_id"])
	assert.Equal(t, float64(0), wire["weight"])
	assert.Equal(t, []any{"hero"}, wire["placement_ids"])
	assert.Equal(t, "x", wire["vendor_hint"])
}

func TestPtr(t *testing.T) {
	duration := Ptr(Duration{Interval: 5, Unit: "minutes"})
	require.NotNil(t, duration)
	assert.Equal(t, Duration{Interval: 5, Unit: "minutes"}, *duration)
}

func TestCreativeAssignmentTypedFieldsOverrideExtraCollisions(t *testing.T) {
	out, err := json.Marshal(CreativeAssignment{
		CreativeID: "cr-typed",
		Weight:     Float64(0),
		Extra: map[string]any{
			"creative_id":   "cr-extra",
			"weight":        99,
			"placement_ids": []string{"extra"},
			"vendor_hint":   "x",
		},
	})
	require.NoError(t, err)

	var wire map[string]any
	require.NoError(t, json.Unmarshal(out, &wire))
	assert.Equal(t, "cr-typed", wire["creative_id"])
	assert.Equal(t, float64(0), wire["weight"])
	assert.NotContains(t, wire, "placement_ids")
	assert.Equal(t, "x", wire["vendor_hint"])
}
