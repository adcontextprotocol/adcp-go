package adcp

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGeneratedDeliveryTypesRoundTrip(t *testing.T) {
	paused := false
	isFinal := true
	resp := GetMediaBuyDeliveryResponse{
		ReportingPeriod: ReportingPeriod{
			Start: "2026-05-25T00:00:00Z",
			End:   "2026-05-25T23:59:59Z",
		},
		Currency: "USD",
		AggregatedTotals: &DeliveryAggregatedTotals{
			Impressions:   1000,
			Spend:         125,
			ReachUnit:     "households",
			MediaBuyCount: 1,
		},
		MediaBuyDeliveries: []MediaBuyDelivery{{
			MediaBuyID:   "mb-1",
			Status:       "active",
			PricingModel: "cpm",
			Totals: MediaBuyDeliveryTotals{
				Impressions:   1000,
				Spend:         125,
				Clicks:        25,
				EffectiveRate: 12.5,
			},
			ByPackage: []PackageDelivery{{
				PackageID:         "pkg-1",
				Impressions:       1000,
				Spend:             125,
				Clicks:            25,
				Ctr:               0.025,
				PricingModel:      "cpm",
				Rate:              12.5,
				Currency:          "USD",
				DeliveryStatus:    "delivering",
				Paused:            &paused,
				IsFinal:           &isFinal,
				MeasurementWindow: "c7",
				DailyBreakdown: []any{
					map[string]any{"date": "2026-05-25", "impressions": 1000, "spend": 125},
				},
			}},
		}},
	}

	b, err := json.Marshal(resp)
	require.NoError(t, err)

	var wire map[string]any
	require.NoError(t, json.Unmarshal(b, &wire))
	assert.IsType(t, map[string]any{}, wire["reporting_period"])
	assert.IsType(t, map[string]any{}, wire["aggregated_totals"])
	deliveries, ok := wire["media_buy_deliveries"].([]any)
	require.True(t, ok)
	require.Len(t, deliveries, 1)
	delivery, ok := deliveries[0].(map[string]any)
	require.True(t, ok)
	totals, ok := delivery["totals"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, float64(12.5), totals["effective_rate"])
	packages, ok := delivery["by_package"].([]any)
	require.True(t, ok)
	require.Len(t, packages, 1)
	pkg, ok := packages[0].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "pkg-1", pkg["package_id"])
	assert.Equal(t, "cpm", pkg["pricing_model"])
	assert.Equal(t, float64(12.5), pkg["rate"])
	assert.Equal(t, "USD", pkg["currency"])
	assert.Equal(t, false, pkg["paused"])
	assert.Equal(t, true, pkg["is_final"])

	var decoded GetMediaBuyDeliveryResponse
	require.NoError(t, json.Unmarshal(b, &decoded))
	require.Len(t, decoded.MediaBuyDeliveries, 1)
	assert.Equal(t, "mb-1", decoded.MediaBuyDeliveries[0].MediaBuyID)
	assert.Equal(t, 12.5, decoded.MediaBuyDeliveries[0].Totals.EffectiveRate)
	require.Len(t, decoded.MediaBuyDeliveries[0].ByPackage, 1)
	assert.Equal(t, "pkg-1", decoded.MediaBuyDeliveries[0].ByPackage[0].PackageID)
	assert.Equal(t, "c7", decoded.MediaBuyDeliveries[0].ByPackage[0].MeasurementWindow)
	require.NotNil(t, decoded.AggregatedTotals)
	assert.Equal(t, 1, decoded.AggregatedTotals.MediaBuyCount)
}

func TestPackageDeliveryRequiredZeroValuesMarshal(t *testing.T) {
	b, err := json.Marshal(PackageDelivery{PackageID: "pkg-zero"})
	require.NoError(t, err)

	assert.JSONEq(t, `{
		"package_id": "pkg-zero",
		"spend": 0,
		"pricing_model": "",
		"rate": 0,
		"currency": ""
	}`, string(b))
}
