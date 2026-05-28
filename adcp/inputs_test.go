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
		PackageID:   "pkg-1",
		Paused:      Bool(false),
		Budget:      Float64(0),
		BidPrice:    Float64(0),
		Impressions: Float64(0),
		KeywordTargetsAdd: []KeywordTargetUpdate{{
			Keyword:   "running shoes",
			MatchType: "phrase",
			BidPrice:  Float64(0),
		}},
		CreativeAssignments: []CreativeAssignment{{
			CreativeID: "cr-1",
			Weight:     Float64(0),
		}},
	})
	require.NoError(t, err)

	var wire map[string]any
	require.NoError(t, json.Unmarshal(out, &wire))
	assert.Equal(t, false, wire["paused"])
	assert.Equal(t, float64(0), wire["budget"])
	assert.Equal(t, float64(0), wire["bid_price"])
	assert.Equal(t, float64(0), wire["impressions"])
	assert.NotContains(t, wire, "canceled")
	keywords, ok := wire["keyword_targets_add"].([]any)
	require.True(t, ok)
	require.Len(t, keywords, 1)
	keyword, ok := keywords[0].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, float64(0), keyword["bid_price"])
	assignments, ok := wire["creative_assignments"].([]any)
	require.True(t, ok)
	require.Len(t, assignments, 1)
	assignment, ok := assignments[0].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "cr-1", assignment["creative_id"])
	assert.Equal(t, float64(0), assignment["weight"])
}

func TestPackageInputOptionalZeroAndSchemaFieldsMarshal(t *testing.T) {
	out, err := json.Marshal(PackageInput{
		ProductID:       "prod-1",
		PricingOptionID: "price-1",
		Budget:          0,
		FormatIDs:       []FormatRef{{ID: "display-banner"}},
		Paused:          Bool(false),
		BidPrice:        Float64(0),
		Impressions:     Float64(0),
		Catalogs:        []Catalog{{Type: "products"}},
		CreativeAssignments: []CreativeAssignment{{
			CreativeID: "cr-1",
		}},
		Creatives: []CreativeAsset{{
			CreativeID: "cr-new-1",
			Name:       "New creative",
			FormatID:   FormatRef{ID: "display-banner"},
			Assets:     map[string]any{"image": map[string]any{"url": "https://example.com/image.png"}},
		}},
		Ext: map[string]any{"buyer_ref": "corr-1"},
	})
	require.NoError(t, err)

	var wire map[string]any
	require.NoError(t, json.Unmarshal(out, &wire))
	assert.Equal(t, float64(0), wire["budget"])
	assert.Equal(t, false, wire["paused"])
	assert.Equal(t, float64(0), wire["bid_price"])
	assert.Equal(t, float64(0), wire["impressions"])
	assert.Contains(t, wire, "format_ids")
	assert.Contains(t, wire, "catalogs")
	assert.Contains(t, wire, "creative_assignments")
	assert.Contains(t, wire, "creatives")
	assert.NotContains(t, wire, "buyer_ref")
	assert.Equal(t, map[string]any{"buyer_ref": "corr-1"}, wire["ext"])
}

func TestPackageOptionalNumericZeroRoundTrip(t *testing.T) {
	var input PackageInput
	require.NoError(t, json.Unmarshal([]byte(`{
		"product_id":"prod-1",
		"budget":0,
		"pricing_option_id":"price-1",
		"bid_price":0,
		"impressions":0
	}`), &input))
	require.NotNil(t, input.BidPrice)
	require.NotNil(t, input.Impressions)
	assert.Equal(t, float64(0), *input.BidPrice)
	assert.Equal(t, float64(0), *input.Impressions)

	var update PackageUpdate
	require.NoError(t, json.Unmarshal([]byte(`{
		"package_id":"pkg-1",
		"budget":0,
		"bid_price":0,
		"impressions":0
	}`), &update))
	require.NotNil(t, update.Budget)
	require.NotNil(t, update.BidPrice)
	require.NotNil(t, update.Impressions)
	assert.Equal(t, float64(0), *update.Budget)
	assert.Equal(t, float64(0), *update.BidPrice)
	assert.Equal(t, float64(0), *update.Impressions)

	var keyword KeywordTargetUpdate
	require.NoError(t, json.Unmarshal([]byte(`{
		"keyword":"running shoes",
		"match_type":"phrase",
		"bid_price":0
	}`), &keyword))
	require.NotNil(t, keyword.BidPrice)
	assert.Equal(t, float64(0), *keyword.BidPrice)
}

func TestPackageOptionalNumericNilOmitsFields(t *testing.T) {
	out, err := json.Marshal(PackageInput{
		ProductID:       "prod-1",
		Budget:          100,
		PricingOptionID: "price-1",
	})
	require.NoError(t, err)
	var wire map[string]any
	require.NoError(t, json.Unmarshal(out, &wire))
	assert.NotContains(t, wire, "bid_price")
	assert.NotContains(t, wire, "impressions")

	out, err = json.Marshal(PackageUpdate{
		PackageID: "pkg-1",
	})
	require.NoError(t, err)
	wire = nil
	require.NoError(t, json.Unmarshal(out, &wire))
	assert.NotContains(t, wire, "budget")
	assert.NotContains(t, wire, "bid_price")
	assert.NotContains(t, wire, "impressions")

	out, err = json.Marshal(KeywordTargetUpdate{
		Keyword:   "running shoes",
		MatchType: "phrase",
	})
	require.NoError(t, err)
	wire = nil
	require.NoError(t, json.Unmarshal(out, &wire))
	assert.NotContains(t, wire, "bid_price")
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
