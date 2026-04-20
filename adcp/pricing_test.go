package adcp

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Each variant of the signal-pricing oneOf must round-trip through
// SignalPricing without losing model-specific fields or spilling unrelated
// ones into the wire. Guards against silent drift when the spec adds another
// variant.
func TestSignalPricing_VariantRoundTrip(t *testing.T) {
	t.Run("cpm", func(t *testing.T) {
		p := SignalPricing{PricingOptionID: "po1", Model: "cpm", CPM: 2.50, Currency: "USD"}
		b, err := json.Marshal(p)
		require.NoError(t, err)
		assert.JSONEq(t, `{"pricing_option_id":"po1","model":"cpm","cpm":2.5,"currency":"USD"}`, string(b))
	})

	t.Run("percent_of_media with max_cpm", func(t *testing.T) {
		p := SignalPricing{PricingOptionID: "po2", Model: "percent_of_media", Percent: 15, MaxCPM: 5, Currency: "USD"}
		b, err := json.Marshal(p)
		require.NoError(t, err)
		assert.JSONEq(t, `{"pricing_option_id":"po2","model":"percent_of_media","percent":15,"max_cpm":5,"currency":"USD"}`, string(b))
	})

	t.Run("flat_fee", func(t *testing.T) {
		p := SignalPricing{PricingOptionID: "po3", Model: "flat_fee", Amount: 1000, Period: "monthly", Currency: "USD"}
		b, err := json.Marshal(p)
		require.NoError(t, err)
		assert.JSONEq(t, `{"pricing_option_id":"po3","model":"flat_fee","amount":1000,"period":"monthly","currency":"USD"}`, string(b))
	})

	t.Run("per_unit", func(t *testing.T) {
		p := SignalPricing{PricingOptionID: "po4", Model: "per_unit", Unit: "evaluation", UnitPrice: 0.001, Currency: "USD"}
		b, err := json.Marshal(p)
		require.NoError(t, err)
		assert.JSONEq(t, `{"pricing_option_id":"po4","model":"per_unit","unit":"evaluation","unit_price":0.001,"currency":"USD"}`, string(b))
	})

	t.Run("custom without currency", func(t *testing.T) {
		p := SignalPricing{
			PricingOptionID: "po5",
			Model:           "custom",
			Description:     "Outcome-based kicker on top of flat fee",
			Metadata: map[string]any{
				"summary_for_operator": "Base $5k/month plus $10 per attributed visit",
				"base_amount":          5000.0,
				"kicker_rate":          10.0,
			},
		}
		b, err := json.Marshal(p)
		require.NoError(t, err)

		var got map[string]any
		require.NoError(t, json.Unmarshal(b, &got))
		assert.Equal(t, "custom", got["model"])
		assert.Equal(t, "Outcome-based kicker on top of flat fee", got["description"])
		assert.NotNil(t, got["metadata"])
		// Schema makes currency optional for custom — must not appear when unset
		_, hasCurrency := got["currency"]
		assert.False(t, hasCurrency, "currency must be omitted when empty so custom variants validate")
	})
}

// VendorPricingOption is signal-pricing + pricing_option_id, so it must cover
// the same variants byte-for-byte. One spot check to prove they don't drift.
func TestVendorPricingOption_CustomVariant(t *testing.T) {
	p := VendorPricingOption{
		PricingOptionID: "vpo-1",
		Model:           "custom",
		Description:     "Hybrid volume/outcome pricing",
		Metadata:        map[string]any{"summary_for_operator": "see attached"},
	}
	b, err := json.Marshal(p)
	require.NoError(t, err)
	var got map[string]any
	require.NoError(t, json.Unmarshal(b, &got))
	assert.Equal(t, "custom", got["model"])
	assert.NotNil(t, got["metadata"])
	_, hasCurrency := got["currency"]
	assert.False(t, hasCurrency)
}
