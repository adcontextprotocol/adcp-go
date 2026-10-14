package adcp

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Each fixture validates against its AdCP 3.2.2 schema and covers a branch of
// the task: every response outcome, the brand and the account request paths,
// and a decline with a detail. Decoding into the generated type and encoding
// back must reproduce it exactly, so a dropped or renamed field fails the test.
func TestGeneratedProposalLifecycleTypesRoundTrip(t *testing.T) {
	cases := []struct {
		name    string
		fixture string
		target  any
	}{
		{"request proposals request", `{"idempotency_key":"req-proposals-0001","brief":"Autumn awareness for an outdoor brand","brand":{"domain":"example.com"}}`, &RequestProposalsRequest{}},
		{"request proposals response rejected", `{"outcome":"rejected","reason":"No inventory matches the requested markets"}`, &RequestProposalsResponse{}},
		{"request proposals response submitted", `{"status":"submitted","task_id":"task_1"}`, &RequestProposalsResponse{}},
		{"decline proposals request", `{"idempotency_key":"decline-props-0001","declines":[{"proposal_id":"prop_1","reason":"price"}]}`, &DeclineProposalsRequest{}},
		{"decline proposals response", `{"results":[{"proposal_id":"prop_1","outcome":"declined"},{"proposal_id":"prop_2","outcome":"unable","reason":"already accepted"}]}`, &DeclineProposalsResponse{}},
		{"decline proposals response submitted", `{"status":"submitted","task_id":"task_2"}`, &DeclineProposalsResponse{}},
		{"request proposals response proposed", `{"outcome":"proposed","status":"completed","proposals":[{"proposal_id":"prop_1","proposal_kind":"new_media_buy","proposal_status":"draft","expires_at":"2026-10-20T00:00:00Z","name":"Autumn plan","commercial_terms":{"brand":{"domain":"example.com"},"start_time":"2026-11-01T00:00:00Z","end_time":"2026-11-30T00:00:00Z","purchases":[{"product_id":"prod_1","pricing_option_id":"po_1","pricing":{"pricing_option_id":"po_1","pricing_model":"cpm","fixed_price":5,"currency":"EUR"},"start_time":"2026-11-01T00:00:00Z","end_time":"2026-11-30T00:00:00Z","budget":0}]},"terms_digest":"sha256:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"}],"products":[{"product_id":"prod_1","name":"Homepage takeover"}],"incomplete":[{"scope":"forecast","description":"Forecast still computing","estimated_wait":{"interval":30,"unit":"seconds"}}],"replayed":true}`, &RequestProposalsResponse{}},
		{"request proposals response products_available", `{"outcome":"products_available","products":[{"product_id":"prod_1","name":"Homepage takeover"}],"purchase_continuation":{"kind":"legacy_create","continuation_token":"tok_abcdefghijklmnop","continuation_expires_at":"2026-10-14T01:00:00Z","source_adcp_version":"3.1","product_ids":["prod_1"],"losses":["feed_version_not_atomic","pricing_version_not_atomic"],"requires_explicit_acceptance":true}}`, &RequestProposalsResponse{}},
		{"decline proposals request other", `{"adcp_major_version":3,"idempotency_key":"decline-props-0002","declines":[{"proposal_id":"prop_3","reason":"other","detail":"Campaign postponed"}]}`, &DeclineProposalsRequest{}},
		{"request proposals request account", `{"idempotency_key":"req-proposals-0002","brief":"Autumn awareness","account":{"brand":{"domain":"example.com"},"operator":"agency.example"},"opportunity":{"opportunity_id":"opp_1","status":"open"}}`, &RequestProposalsRequest{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			require.NoError(t, json.Unmarshal([]byte(tc.fixture), tc.target))
			out, err := json.Marshal(tc.target)
			require.NoError(t, err)
			assert.JSONEq(t, tc.fixture, string(out))
		})
	}
}

func TestGeneratedDeclineProposalResultOmitsReasonWhenDeclined(t *testing.T) {
	var resp DeclineProposalsResponse
	require.NoError(t, json.Unmarshal([]byte(`{"results":[{"proposal_id":"prop_1","outcome":"declined"}]}`), &resp))
	require.Len(t, resp.Results, 1)
	assert.Empty(t, resp.Results[0].Reason)
	out, err := json.Marshal(resp.Results[0])
	require.NoError(t, err)
	assert.JSONEq(t, `{"proposal_id":"prop_1","outcome":"declined"}`, string(out))
}
