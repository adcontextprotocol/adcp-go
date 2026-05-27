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

func TestUpdateMediaBuyOptionalFlagsMarshal(t *testing.T) {
	out, err := json.Marshal(UpdateMediaBuyRequest{
		IdempotencyKey: "idem-123456789012",
		Account:        AccountReference{AccountID: "acct-1"},
		MediaBuyID:     "mb-1",
		Paused:         Bool(true),
	})
	require.NoError(t, err)

	var wire map[string]any
	require.NoError(t, json.Unmarshal(out, &wire))
	assert.Equal(t, true, wire["paused"])
	assert.NotContains(t, wire, "canceled")

	out, err = json.Marshal(UpdateMediaBuyRequest{
		IdempotencyKey: "idem-123456789013",
		Account:        AccountReference{AccountID: "acct-1"},
		MediaBuyID:     "mb-1",
		Canceled:       Bool(false),
	})
	require.NoError(t, err)

	wire = nil
	require.NoError(t, json.Unmarshal(out, &wire))
	assert.Equal(t, false, wire["canceled"])
	assert.NotContains(t, wire, "paused")
}

func TestPackageUpdateOptionalFlagsAndCreativeAssignmentsMarshal(t *testing.T) {
	out, err := json.Marshal(PackageUpdate{
		PackageID: "pkg-1",
		Paused:    Bool(false),
		CreativeAssignments: []CreativeAssignment{{
			CreativeID: "cr-1",
			Weight:     Float64(0),
		}},
	})
	require.NoError(t, err)

	var wire map[string]any
	require.NoError(t, json.Unmarshal(out, &wire))
	assert.Equal(t, false, wire["paused"])
	assert.NotContains(t, wire, "canceled")
	assignments, ok := wire["creative_assignments"].([]any)
	require.True(t, ok)
	require.Len(t, assignments, 1)
	assignment, ok := assignments[0].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "cr-1", assignment["creative_id"])
	assert.Equal(t, float64(0), assignment["weight"])
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
