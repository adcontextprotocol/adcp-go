package adcp

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Each round-trip fixture validates against its AdCP 3.2.2 schema. The first
// two request fixtures and the two response fixtures that follow are the
// schemas' own examples. The others cover what the examples do not: every
// optional field of a usage record, an explicit zero in each optional quantity,
// and every field of the response envelope. Decoding into the generated type
// and encoding back must reproduce each exactly, so a dropped field or an
// omitted zero value fails the test.
func TestGeneratedReportUsageTypesRoundTrip(t *testing.T) {
	cases := []struct {
		name    string
		schema  string
		fixture string
		target  any
	}{
		{"report usage request example 0", "account/report-usage-request.json", `{"idempotency_key":"550e8400-e29b-41d4-a716-446655440000","reporting_period":{"start":"2025-03-01T00:00:00Z","end":"2025-03-31T23:59:59Z"},"usage":[{"account":{"account_id":"acct_pinnacle_signals"},"signal_agent_segment_id":"luxury_auto_intenders","pricing_option_id":"po_lux_auto_cpm","impressions":4200000,"media_spend":21000,"vendor_cost":2100,"currency":"USD"}]}`, &ReportUsageRequest{}},
		{"report usage request example 1", "account/report-usage-request.json", `{"idempotency_key":"8b7a9c2d-3456-4789-abcd-ef0123456789","reporting_period":{"start":"2025-03-01T00:00:00Z","end":"2025-03-31T23:59:59Z"},"usage":[{"account":{"account_id":"acct_pinnacle_signals"},"signal_agent_segment_id":"luxury_auto_intenders","pricing_option_id":"po_lux_auto_cpm","impressions":2100000,"vendor_cost":1050,"currency":"USD"},{"account":{"account_id":"acct_nova"},"signal_agent_segment_id":"eco_conscious_shoppers","pricing_option_id":"po_eco_cpm","impressions":800000,"vendor_cost":400,"currency":"USD"}]}`, &ReportUsageRequest{}},
		{"report usage request zero quantities and a preliminary record", "account/report-usage-request.json", `{"adcp_version":"3.2","adcp_major_version":3,"idempotency_key":"report-usage-zero-0001","reporting_period":{"start":"2026-10-01T00:00:00Z","end":"2026-10-01T23:59:59Z"},"usage":[{"account":{"account_id":"acct_1"},"media_buy_id":"mb_1","vendor_cost":0,"currency":"EUR","impressions":0,"media_spend":0,"conversions":0,"conversion_value":0,"commissionable_value":0,"final":false}]}`, &ReportUsageRequest{}},
		{"report usage request every record field", "account/report-usage-request.json", `{"idempotency_key":"report-usage-full-0001","reporting_period":{"start":"2026-09-01T00:00:00Z","end":"2026-09-30T23:59:59Z"},"usage":[{"account":{"brand":{"domain":"acme.example"},"operator":"agency.example"},"media_buy_id":"mb_2","vendor_cost":120.5,"currency":"USD","pricing_option_id":"po_share_10","impressions":900000,"media_spend":1500.25,"conversions":42.5,"conversion_value":6300.5,"commissionable_value":1205.75,"signal_agent_segment_id":"seg_1","standards_id":"std_1","rights_id":"rights_1","creative_id":"cr_1","build_variant_id":"bv_1","property_list_id":"pl_1","final":true,"finalized_at":"2026-10-05T08:00:00Z","measurement_window":"post_sivt"}],"context":{"correlation_id":"c-1"},"ext":{"vendor":{"note":"month-end close"}}}`, &ReportUsageRequest{}},
		{"report usage response all accepted", "account/report-usage-response.json", `{"status":"completed","accepted":2}`, &ReportUsageResponse{}},
		{"report usage response partial acceptance", "account/report-usage-response.json", `{"status":"completed","accepted":1,"errors":[{"code":"INVALID_PRICING_OPTION","message":"pricing_option_id 'po_unknown' does not exist on this account","field":"usage[1].pricing_option_id"}]}`, &ReportUsageResponse{}},
		{"report usage response none accepted in a sandbox", "account/report-usage-response.json", `{"status":"completed","accepted":0,"sandbox":true,"replayed":true,"task_id":"task_1","context_id":"ctx_1","message":"no usage stored","timestamp":"2026-10-06T09:00:00Z","adcp_version":"3.2","adcp_major_version":3,"context":{"correlation_id":"c-1"},"ext":{"vendor":{"note":"sandbox"}}}`, &ReportUsageResponse{}},
		{"report usage response failed with every envelope field", "account/report-usage-response.json", `{"status":"failed","accepted":0,"replayed":false,"governance_context":"gov-context-token","adcp_error":{"code":"INVALID_PRICING_OPTION","message":"pricing_option_id 'po_unknown' does not exist on this account"},"push_notification_config":{"url":"https://hooks.example/report-usage","token":"hook-token-0123456789"},"payload":{"detail":"none stored"}}`, &ReportUsageResponse{}},
		{"report usage response a sandbox flag set to false", "account/report-usage-response.json", `{"status":"completed","accepted":3,"sandbox":false}`, &ReportUsageResponse{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assertSchemaValid(t, tc.schema, []byte(tc.fixture))
			require.NoError(t, json.Unmarshal([]byte(tc.fixture), tc.target))
			out, err := json.Marshal(tc.target)
			require.NoError(t, err)
			assert.JSONEq(t, tc.fixture, string(out))
		})
	}
}

// An absent optional quantity decodes to nil and stays off the wire, and an
// explicit zero decodes to a pointer to zero and stays on it: the receiver
// reads "no impressions" and "not reported" differently.
func TestReportUsageRecordDistinguishesZeroFromAbsent(t *testing.T) {
	absent := []byte(`{"account":{"account_id":"acct_1"},"vendor_cost":10,"currency":"USD"}`)
	assertSchemaValid(t, "account/report-usage-request.json#/properties/usage/items", absent)
	var rec ReportUsageRecord
	require.NoError(t, json.Unmarshal(absent, &rec))
	assert.Nil(t, rec.Impressions)
	assert.Nil(t, rec.MediaSpend)
	assert.Nil(t, rec.Conversions)
	assert.Nil(t, rec.ConversionValue)
	assert.Nil(t, rec.CommissionableValue)
	assert.Nil(t, rec.Final)
	out, err := json.Marshal(rec)
	require.NoError(t, err)
	assert.JSONEq(t, string(absent), string(out))

	zero := []byte(`{"account":{"account_id":"acct_1"},"vendor_cost":0,"currency":"USD","impressions":0,"media_spend":0,"conversions":0,"conversion_value":0,"commissionable_value":0,"final":false}`)
	assertSchemaValid(t, "account/report-usage-request.json#/properties/usage/items", zero)
	var rec0 ReportUsageRecord
	require.NoError(t, json.Unmarshal(zero, &rec0))
	require.NotNil(t, rec0.Impressions)
	require.NotNil(t, rec0.MediaSpend)
	require.NotNil(t, rec0.Conversions)
	require.NotNil(t, rec0.ConversionValue)
	require.NotNil(t, rec0.CommissionableValue)
	require.NotNil(t, rec0.Final)
	assert.Zero(t, *rec0.Impressions)
	assert.False(t, *rec0.Final)
	out0, err := json.Marshal(rec0)
	require.NoError(t, err)
	assert.JSONEq(t, string(zero), string(out0))
}

// The schema requires idempotency_key, reporting_period and usage on the
// request and accepted and status on the response, so a zero value of each is
// still written, an unset usage list included.
func TestReportUsageRequiredFieldsAreAlwaysEncoded(t *testing.T) {
	req, err := json.Marshal(ReportUsageRequest{Usage: []ReportUsageRecord{{}}})
	require.NoError(t, err)
	assert.JSONEq(t, `{"idempotency_key":"","reporting_period":{"start":"","end":""},"usage":[{"account":{},"vendor_cost":0,"currency":""}]}`, string(req))

	empty, err := json.Marshal(ReportUsageRequest{})
	require.NoError(t, err)
	assert.JSONEq(t, `{"idempotency_key":"","reporting_period":{"start":"","end":""},"usage":null}`, string(empty))

	resp, err := json.Marshal(ReportUsageResponse{})
	require.NoError(t, err)
	assert.JSONEq(t, `{"status":"","accepted":0}`, string(resp))
}

// adcp_major_version is deprecated and optional: a request without it decodes
// to nil, so a handler can tell a sender that left it off from one that sent a
// version.
func TestReportUsageRequestMajorVersionAbsentIsNil(t *testing.T) {
	fixture := []byte(`{"idempotency_key":"report-usage-nomajor-01","reporting_period":{"start":"2026-10-01T00:00:00Z","end":"2026-10-31T23:59:59Z"},"usage":[{"account":{"account_id":"acct_1"},"vendor_cost":1,"currency":"EUR"}]}`)
	assertSchemaValid(t, "account/report-usage-request.json", fixture)
	var req ReportUsageRequest
	require.NoError(t, json.Unmarshal(fixture, &req))
	assert.Nil(t, req.AdcpMajorVersion)

	withMajor := []byte(`{"adcp_major_version":3,"idempotency_key":"report-usage-nomajor-01","reporting_period":{"start":"2026-10-01T00:00:00Z","end":"2026-10-31T23:59:59Z"},"usage":[{"account":{"account_id":"acct_1"},"vendor_cost":1,"currency":"EUR"}]}`)
	var reqMajor ReportUsageRequest
	require.NoError(t, json.Unmarshal(withMajor, &reqMajor))
	require.NotNil(t, reqMajor.AdcpMajorVersion)
	assert.Equal(t, 3, *reqMajor.AdcpMajorVersion)
}

// The request and each usage record are open objects (additionalProperties:
// true), so a sender on a later AdCP version may add keys this SDK does not
// know. Decoding such a payload succeeds and leaves every known field as it
// would be without the extra keys.
func TestReportUsageOpenObjectsAcceptUnknownKeys(t *testing.T) {
	known := []byte(`{"idempotency_key":"report-usage-open-0001","reporting_period":{"start":"2026-10-01T00:00:00Z","end":"2026-10-31T23:59:59Z"},"usage":[{"account":{"account_id":"acct_1"},"vendor_cost":5,"currency":"EUR","impressions":10}]}`)
	extra := []byte(`{"idempotency_key":"report-usage-open-0001","reporting_period":{"start":"2026-10-01T00:00:00Z","end":"2026-10-31T23:59:59Z"},"future_request_field":{"a":1},"usage":[{"account":{"account_id":"acct_1"},"vendor_cost":5,"currency":"EUR","impressions":10,"future_record_field":"x"}]}`)
	assertSchemaValid(t, "account/report-usage-request.json", known)
	assertSchemaValid(t, "account/report-usage-request.json", extra)
	var want, got ReportUsageRequest
	require.NoError(t, json.Unmarshal(known, &want))
	require.NoError(t, json.Unmarshal(extra, &got))
	assert.Equal(t, want, got)
}
