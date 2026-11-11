package adcp

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

// Fields that lint's schema pairing found missing from hand-written types
// (adcontextprotocol/adcp-go#564, check 4). Each fixture is valid against the
// schema node the type is paired with, and carries a zero value so an omitted
// zero fails the round trip.

func requireRoundTrip[T any](t *testing.T, schemaRef, raw string) T {
	t.Helper()
	assertSchemaValid(t, schemaRef, []byte(raw))
	var v T
	require.NoError(t, json.Unmarshal([]byte(raw), &v))
	out, err := json.Marshal(v)
	require.NoError(t, err)
	require.JSONEq(t, raw, string(out))
	return v
}

func TestPlanBudgetAccountingModeRoundTrip(t *testing.T) {
	const ref = "governance/sync-plans-request.json#/properties/plans/items/properties/budget"
	for _, mode := range []string{"gross_commitment", "verified_net_cost"} {
		t.Run(mode, func(t *testing.T) {
			b := requireRoundTrip[PlanBudget](t, ref,
				`{"total":1000,"currency":"USD","accounting_mode":"`+mode+`","reallocation_threshold":0}`)
			require.Equal(t, mode, b.AccountingMode)
		})
	}

	t.Run("omitted stays omitted", func(t *testing.T) {
		b := requireRoundTrip[PlanBudget](t, ref, `{"total":1000,"currency":"USD","reallocation_unlimited":true}`)
		require.Empty(t, b.AccountingMode)
	})
}

func TestBillingMeasurementFinalizationDeadlineRoundTrip(t *testing.T) {
	const ref = "core/measurement-terms.json#/properties/billing_measurement"

	t.Run("explicit zero survives", func(t *testing.T) {
		m := requireRoundTrip[BillingMeasurement](t, ref,
			`{"vendor":{"domain":"admanager.google.com"},"measurement_window":"c7","finalization_deadline_hours":0}`)
		require.NotNil(t, m.FinalizationDeadlineHours)
		require.Zero(t, *m.FinalizationDeadlineHours)
	})

	t.Run("deadline", func(t *testing.T) {
		m := requireRoundTrip[BillingMeasurement](t, ref,
			`{"vendor":{"domain":"admanager.google.com"},"finalization_deadline_hours":240}`)
		require.NotNil(t, m.FinalizationDeadlineHours)
		require.Equal(t, 240, *m.FinalizationDeadlineHours)
	})

	t.Run("omitted stays omitted", func(t *testing.T) {
		m := requireRoundTrip[BillingMeasurement](t, ref, `{"vendor":{"domain":"admanager.google.com"}}`)
		require.Nil(t, m.FinalizationDeadlineHours)
	})
}

func TestReportingPeriodTimezoneRoundTrip(t *testing.T) {
	const ref = "media-buy/get-media-buy-delivery-response.json#/properties/reporting_period"

	p := requireRoundTrip[ReportingPeriod](t, ref,
		`{"start":"2026-04-15T04:00:00Z","end":"2026-04-16T04:00:00Z","timezone":"America/New_York"}`)
	require.Equal(t, "America/New_York", p.Timezone)

	p = requireRoundTrip[ReportingPeriod](t, ref, `{"start":"2026-04-15T00:00:00Z","end":"2026-04-16T00:00:00Z"}`)
	require.Empty(t, p.Timezone)
}

// The same fields reached through the types that hold them, the way a caller
// meets them.

func TestPlanBudgetAccountingModeThroughPlan(t *testing.T) {
	const ref = "governance/sync-plans-request.json#/properties/plans/items"
	p := requireRoundTrip[Plan](t, ref, `{
		"plan_id": "plan-1",
		"brand": {"domain": "nova.example"},
		"objectives": "Q4 awareness push",
		"budget": {"total": 1000, "currency": "USD", "accounting_mode": "verified_net_cost", "reallocation_threshold": 0},
		"flight": {"start": "2026-04-01T00:00:00Z", "end": "2026-06-30T23:59:59Z"},
		"approved_sellers": null
	}`)
	require.Equal(t, "verified_net_cost", p.Budget.AccountingMode)
}

func TestBillingMeasurementFinalizationDeadlineThroughMeasurementTerms(t *testing.T) {
	const ref = "core/measurement-terms.json"
	terms := requireRoundTrip[MeasurementTerms](t, ref, `{
		"billing_measurement": {
			"vendor": {"domain": "admanager.google.com"},
			"measurement_window": "c7",
			"finalization_deadline_hours": 0
		}
	}`)
	require.NotNil(t, terms.BillingMeasurement)
	require.NotNil(t, terms.BillingMeasurement.FinalizationDeadlineHours)
	require.Zero(t, *terms.BillingMeasurement.FinalizationDeadlineHours)
}

func TestReportingPeriodTimezoneThroughDeliveryData(t *testing.T) {
	// DeliveryData is the payload; the response on the wire also carries the
	// protocol envelope, whose status the schema requires.
	const payload = `{
		"reporting_period": {
			"start": "2026-04-15T04:00:00Z",
			"end": "2026-04-16T04:00:00Z",
			"timezone": "America/New_York"
		},
		"currency": "USD",
		"media_buy_deliveries": []
	}`
	var wire map[string]any
	require.NoError(t, json.Unmarshal([]byte(payload), &wire))
	wire["status"] = "completed"
	raw, err := json.Marshal(wire)
	require.NoError(t, err)
	assertSchemaValid(t, "media-buy/get-media-buy-delivery-response.json", raw)

	var d DeliveryData
	require.NoError(t, json.Unmarshal(raw, &d))
	require.Equal(t, "America/New_York", d.ReportingPeriod.Timezone)
	out, err := json.Marshal(d)
	require.NoError(t, err)
	require.JSONEq(t, payload, string(out))
}
