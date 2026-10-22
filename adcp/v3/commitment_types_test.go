package adcp

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// accept_proposal and buy_products are both answered by the media buy
// commitment union (media-buy/media-buy-commitment-response.json, behind an
// allOf/$ref in each tool's response schema). Every fixture validates against
// its AdCP 3.2.2 schema, and decoding it into the generated type and encoding
// it back must reproduce it exactly, so a dropped or renamed field fails the
// test. The fixtures carry an explicit zero or false wherever one is valid.

var (
	_ AcceptProposalResponse = CommittedMediaBuy{}
	_ AcceptProposalResponse = &CommittedMediaBuy{}
	_ AcceptProposalResponse = CommitmentError{}
	_ AcceptProposalResponse = &CommitmentError{}
	_ AcceptProposalResponse = CommitmentSubmitted{}
	_ AcceptProposalResponse = &CommitmentSubmitted{}
	_ BuyProductsResponse    = CommittedMediaBuy{}
	_ BuyProductsResponse    = &CommitmentError{}
	_ BuyProductsResponse    = CommitmentSubmitted{}
	// The two tools share one union type, as their schemas share one file.
	_ MediaBuyCommitmentResponse = AcceptProposalResponse(CommittedMediaBuy{})
	_ BuyProductsResponse        = AcceptProposalResponse(CommitmentError{})
)

// decodeCommitmentResponse picks the variant by the union's discriminator,
// status. The generated union is an interface, so choosing the variant is the
// caller's job, as for the other generated response unions.
func decodeCommitmentResponse(raw []byte) (MediaBuyCommitmentResponse, error) {
	var head struct {
		Status string `json:"status"`
	}
	if err := json.Unmarshal(raw, &head); err != nil {
		return nil, err
	}
	var out MediaBuyCommitmentResponse
	switch head.Status {
	case "completed":
		out = &CommittedMediaBuy{}
	case "failed":
		out = &CommitmentError{}
	case "submitted":
		out = &CommitmentSubmitted{}
	default:
		return nil, fmt.Errorf("unknown commitment status %q", head.Status)
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return nil, err
	}

	return out, nil
}

func TestGeneratedCommitmentRequestsRoundTrip(t *testing.T) {
	cases := []struct {
		name    string
		schema  string
		fixture string
		target  any
	}{
		{"accept_proposal request minimal", "media-buy/accept-proposal-request.json", `{"idempotency_key":"accept-prop-0000001","account":{"account_id":"acc_1"},"proposal_id":"prop_1","proposal_terms_digest":"sha256:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"}`, &AcceptProposalRequest{}},
		{"accept_proposal request full", "media-buy/accept-proposal-request.json", `{"adcp_major_version":3,"idempotency_key":"accept-prop-0000002","name":"Autumn plan","account":{"brand":{"domain":"example.com"},"operator":"agency.example"},"proposal_id":"prop_1","proposal_terms_digest":"sha256:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA","total_budget":{"amount":0,"currency":"EUR"},"daily_budget_cap":0,"budget_cap_timezone":"Europe/Berlin","io_acceptance":{"io_id":"io_1","accepted_at":"2026-10-22T09:00:00Z","signatory":"Jo Buyer","signature_id":"sig_1"},"purchase_order_ref":"PO-1","governance_context":"gov-ctx-1","push_notification_config":{"url":"https://buyer.example/hooks/tasks","token":"tok_0123456789abcdef"},"reporting_webhook":{"url":"https://buyer.example/hooks/reporting","authentication":{"schemes":["Bearer"],"credentials":"cxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx"},"reporting_frequency":"daily"},"opportunity":{"opportunity_id":"opp_1","status":"closed","close_reason":"accepted_with_seller"},"context":{"trace":"t1"},"ext":{"vendor":{"k":"v"}}}`, &AcceptProposalRequest{}},
		{"buy_products request minimal", "media-buy/buy-products-request.json", `{"idempotency_key":"buy-products-000001","account":{"account_id":"acc_1"},"brand":{"domain":"example.com"},"feed_version":"feed_1","purchases":[{"product_id":"prod_1","pricing_option_id":"po_1"}],"start_time":"2026-11-01T00:00:00Z","end_time":"2026-11-30T00:00:00Z"}`, &BuyProductsRequest{}},
		{"buy_products request full", "media-buy/buy-products-request.json", `{"adcp_version":"3.2","adcp_major_version":3,"idempotency_key":"buy-products-000002","name":"Autumn buy","account":{"brand":{"domain":"example.com","brand_id":"outdoor"},"operator":"agency.example"},"advertiser_industry":"retail","feed_version":"feed_1","pricing_version":"price_1","purchases":[{"product_id":"prod_1","pricing_option_id":"po_1","pricing":{"pricing_option_id":"po_1","pricing_model":"cpm","fixed_price":5,"currency":"EUR"},"format_option_refs":[{"scope":"publisher","publisher_domain":"pub.example","format_option_id":"fo_1"},{"scope":"product","format_option_id":"fo_2"}],"catalog_ids":["cat_1"],"budget":0,"daily_budget_cap":0,"min_spend_target":0,"impressions":0,"start_time":"2026-11-01T00:00:00Z","end_time":"2026-11-30T00:00:00Z","pacing":"even","bidding":{"max_bid":4.5},"targeting_overlay":{"geo_countries":["DE"],"device_type":null},"optimization_goals":[{"kind":"metric","metric":"clicks"}],"audience_evidence_requirements":{"requirement_mode":"required","evidence_presence":"when_available"},"audience_evidence_pins":[{"evidence_id":"ev_1","snapshot_id":"snap_1","version":"1","content_digest":"sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}],"agency_estimate_number":"EST-1","context":{"line":1},"ext":{"vendor":{"k":"v"}},"measurement_terms":{},"performance_standards":[{"metric":"viewability","threshold":0,"standard":"mrc","vendor":{"domain":"ias.example"}}]},{"product_id":"prod_2","pricing_option_id":"po_2","budget":2500.5,"impressions":100000}],"total_budget":{"amount":5000,"currency":"EUR"},"daily_budget_cap":0,"frequency_cap":{"max_impressions":3,"per":"devices","window":{"interval":1,"unit":"days"}},"budget_cap_timezone":"Europe/Berlin","budget_allocation":{"mode":"fixed"},"start_time":"2026-11-01T00:00:00Z","end_time":"2026-11-30T00:00:00Z","pacing":"asap","bidding":{"automatic":true},"paused":false,"purchase_order_ref":"PO-2","agency_estimate_number":"EST-2","invoice_recipient":{"legal_name":"Agency GmbH"},"governance_context":"gov-ctx-2","push_notification_config":{"url":"https://buyer.example/hooks/tasks"},"reporting_webhook":{"url":"https://buyer.example/hooks/reporting","authentication":{"schemes":["HMAC-SHA256"],"credentials":"cyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyy"},"reporting_frequency":"hourly"},"opportunity":{"opportunity_id":"opp_2"},"context":{"line":2},"ext":{"vendor":{"k":"v"}}}`, &BuyProductsRequest{}},
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

func TestGeneratedCommitmentResponsesRoundTrip(t *testing.T) {
	cases := []struct {
		name    string
		schema  string
		fixture string
	}{
		{"accept_proposal response committed full", "media-buy/accept-proposal-response.json", `{"status":"completed","media_buy_id":"mb_1","name":"Autumn plan","revision":1,"media_buy_status":"pending_creatives","confirmed_at":"2026-10-22T09:00:05Z","accepted_proposal":{"proposal_id":"prop_1","proposal_kind":"new_media_buy","media_buy_id":"mb_1","proposal_status":"accepted","accepted_at":"2026-10-22T09:00:00Z","name":"Autumn plan","commercial_terms":{"brand":{"domain":"example.com"},"start_time":"2026-11-01T00:00:00Z","end_time":"2026-11-30T00:00:00Z","purchases":[{"product_id":"prod_1","pricing_option_id":"po_1","pricing":{"pricing_option_id":"po_1","pricing_model":"cpm","fixed_price":5,"currency":"EUR"},"start_time":"2026-11-01T00:00:00Z","end_time":"2026-11-30T00:00:00Z"},{"product_id":"prod_2","pricing_option_id":"po_1","pricing":{"pricing_option_id":"po_1","pricing_model":"cpm","fixed_price":5,"currency":"EUR"},"start_time":"2026-11-01T00:00:00Z","end_time":"2026-11-30T00:00:00Z"}]},"terms_digest":"sha256:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"},"purchase_bindings":[{"purchase_index":0,"product_id":"prod_1","package_id":"pkg_1"},{"purchase_index":1,"product_id":"prod_2","package_id":"pkg_2"}],"available_actions":[{"task":"control_media_buy","action":"pause","mode":"self_serve"},{"task":"refine_proposals","action":"extend_flight","mode":"requires_approval","sla":{"response_max":"PT4H","completion_max":"P1D"},"change_term_id":"term_1","terms_ref":"legacy_terms_1","applicable_package_ids":["pkg_1","pkg_2"]},{"task":"sync_creatives","action":"replace_creative","mode":"seller_managed"}],"warnings":[{"code":"inventory_shortfall_forecast","message":"Forecast delivery is below the booked impressions","affected_resource":{"resource_type":"package","media_buy_id":"mb_1","package_id":"pkg_1"}}],"context":{"trace":"t1"},"ext":{"vendor":{"k":"v"}},"replayed":true}`},
		{"accept_proposal response committed minimal", "media-buy/accept-proposal-response.json", `{"status":"completed","media_buy_id":"mb_2","revision":1,"accepted_proposal":{"proposal_id":"prop_1","proposal_kind":"new_media_buy","media_buy_id":"mb_1","proposal_status":"accepted","accepted_at":"2026-10-22T09:00:00Z","name":"Autumn plan","commercial_terms":{"brand":{"domain":"example.com"},"start_time":"2026-11-01T00:00:00Z","end_time":"2026-11-30T00:00:00Z","purchases":[{"product_id":"prod_1","pricing_option_id":"po_1","pricing":{"pricing_option_id":"po_1","pricing_model":"cpm","fixed_price":5,"currency":"EUR"},"start_time":"2026-11-01T00:00:00Z","end_time":"2026-11-30T00:00:00Z"},{"product_id":"prod_2","pricing_option_id":"po_1","pricing":{"pricing_option_id":"po_1","pricing_model":"cpm","fixed_price":5,"currency":"EUR"},"start_time":"2026-11-01T00:00:00Z","end_time":"2026-11-30T00:00:00Z"}]},"terms_digest":"sha256:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"},"purchase_bindings":[{"purchase_index":0,"product_id":"prod_1","package_id":"pkg_1"}],"available_actions":[]}`},
		{"accept_proposal response failed", "media-buy/accept-proposal-response.json", `{"status":"failed","errors":[{"code":"PROPOSAL_EXPIRED","message":"Proposal prop_1 has expired"}],"context":{"trace":"t2"},"ext":{"vendor":{"k":"v"}},"replayed":true}`},
		{"accept_proposal response failed minimal", "media-buy/accept-proposal-response.json", `{"status":"failed","errors":[{"code":"PROPOSAL_EXPIRED","message":"Proposal prop_1 has expired"}]}`},
		{"accept_proposal response submitted", "media-buy/accept-proposal-response.json", `{"status":"submitted","task_id":"task_1","message":"Awaiting seller approval","errors":[{"code":"APPROVAL_PENDING","message":"Awaiting approval"}],"context":{"trace":"t3"},"ext":{"vendor":{"k":"v"}},"replayed":true}`},
		{"accept_proposal response submitted minimal", "media-buy/accept-proposal-response.json", `{"status":"submitted","task_id":"task_2"}`},
		{"buy_products response committed full", "media-buy/buy-products-response.json", `{"status":"completed","media_buy_id":"mb_1","name":"Autumn plan","revision":1,"media_buy_status":"pending_creatives","confirmed_at":"2026-10-22T09:00:05Z","accepted_proposal":{"proposal_id":"prop_1","proposal_kind":"new_media_buy","media_buy_id":"mb_1","proposal_status":"accepted","accepted_at":"2026-10-22T09:00:00Z","name":"Autumn plan","commercial_terms":{"brand":{"domain":"example.com"},"start_time":"2026-11-01T00:00:00Z","end_time":"2026-11-30T00:00:00Z","purchases":[{"product_id":"prod_1","pricing_option_id":"po_1","pricing":{"pricing_option_id":"po_1","pricing_model":"cpm","fixed_price":5,"currency":"EUR"},"start_time":"2026-11-01T00:00:00Z","end_time":"2026-11-30T00:00:00Z"},{"product_id":"prod_2","pricing_option_id":"po_1","pricing":{"pricing_option_id":"po_1","pricing_model":"cpm","fixed_price":5,"currency":"EUR"},"start_time":"2026-11-01T00:00:00Z","end_time":"2026-11-30T00:00:00Z"}]},"terms_digest":"sha256:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"},"purchase_bindings":[{"purchase_index":0,"product_id":"prod_1","package_id":"pkg_1"},{"purchase_index":1,"product_id":"prod_2","package_id":"pkg_2"}],"available_actions":[{"task":"control_media_buy","action":"pause","mode":"self_serve"},{"task":"refine_proposals","action":"extend_flight","mode":"requires_approval","sla":{"response_max":"PT4H","completion_max":"P1D"},"change_term_id":"term_1","terms_ref":"legacy_terms_1","applicable_package_ids":["pkg_1","pkg_2"]},{"task":"sync_creatives","action":"replace_creative","mode":"seller_managed"}],"warnings":[{"code":"inventory_shortfall_forecast","message":"Forecast delivery is below the booked impressions","affected_resource":{"resource_type":"package","media_buy_id":"mb_1","package_id":"pkg_1"}}],"context":{"trace":"t1"},"ext":{"vendor":{"k":"v"}},"replayed":true}`},
		{"buy_products response committed minimal", "media-buy/buy-products-response.json", `{"status":"completed","media_buy_id":"mb_2","revision":1,"accepted_proposal":{"proposal_id":"prop_1","proposal_kind":"new_media_buy","media_buy_id":"mb_1","proposal_status":"accepted","accepted_at":"2026-10-22T09:00:00Z","name":"Autumn plan","commercial_terms":{"brand":{"domain":"example.com"},"start_time":"2026-11-01T00:00:00Z","end_time":"2026-11-30T00:00:00Z","purchases":[{"product_id":"prod_1","pricing_option_id":"po_1","pricing":{"pricing_option_id":"po_1","pricing_model":"cpm","fixed_price":5,"currency":"EUR"},"start_time":"2026-11-01T00:00:00Z","end_time":"2026-11-30T00:00:00Z"},{"product_id":"prod_2","pricing_option_id":"po_1","pricing":{"pricing_option_id":"po_1","pricing_model":"cpm","fixed_price":5,"currency":"EUR"},"start_time":"2026-11-01T00:00:00Z","end_time":"2026-11-30T00:00:00Z"}]},"terms_digest":"sha256:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"},"purchase_bindings":[{"purchase_index":0,"product_id":"prod_1","package_id":"pkg_1"}],"available_actions":[]}`},
		{"buy_products response failed", "media-buy/buy-products-response.json", `{"status":"failed","errors":[{"code":"PROPOSAL_EXPIRED","message":"Proposal prop_1 has expired"}],"context":{"trace":"t2"},"ext":{"vendor":{"k":"v"}},"replayed":true}`},
		{"buy_products response failed minimal", "media-buy/buy-products-response.json", `{"status":"failed","errors":[{"code":"PROPOSAL_EXPIRED","message":"Proposal prop_1 has expired"}]}`},
		{"buy_products response submitted", "media-buy/buy-products-response.json", `{"status":"submitted","task_id":"task_1","message":"Awaiting seller approval","errors":[{"code":"APPROVAL_PENDING","message":"Awaiting approval"}],"context":{"trace":"t3"},"ext":{"vendor":{"k":"v"}},"replayed":true}`},
		{"buy_products response submitted minimal", "media-buy/buy-products-response.json", `{"status":"submitted","task_id":"task_2"}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assertSchemaValid(t, tc.schema, []byte(tc.fixture))
			resp, err := decodeCommitmentResponse([]byte(tc.fixture))
			require.NoError(t, err)
			out, err := json.Marshal(resp)
			require.NoError(t, err)
			assert.JSONEq(t, tc.fixture, string(out))
		})
	}
}

func TestCommitmentResponseVariantFollowsStatus(t *testing.T) {
	cases := []struct {
		raw  string
		want MediaBuyCommitmentResponse
	}{
		{`{"status":"completed","media_buy_id":"mb_2","revision":1,"accepted_proposal":{"proposal_id":"prop_1","proposal_kind":"new_media_buy","media_buy_id":"mb_1","proposal_status":"accepted","accepted_at":"2026-10-22T09:00:00Z","name":"Autumn plan","commercial_terms":{"brand":{"domain":"example.com"},"start_time":"2026-11-01T00:00:00Z","end_time":"2026-11-30T00:00:00Z","purchases":[{"product_id":"prod_1","pricing_option_id":"po_1","pricing":{"pricing_option_id":"po_1","pricing_model":"cpm","fixed_price":5,"currency":"EUR"},"start_time":"2026-11-01T00:00:00Z","end_time":"2026-11-30T00:00:00Z"},{"product_id":"prod_2","pricing_option_id":"po_1","pricing":{"pricing_option_id":"po_1","pricing_model":"cpm","fixed_price":5,"currency":"EUR"},"start_time":"2026-11-01T00:00:00Z","end_time":"2026-11-30T00:00:00Z"}]},"terms_digest":"sha256:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"},"purchase_bindings":[{"purchase_index":0,"product_id":"prod_1","package_id":"pkg_1"}],"available_actions":[]}`, &CommittedMediaBuy{}},
		{`{"status":"failed","errors":[{"code":"PROPOSAL_EXPIRED","message":"expired"}]}`, &CommitmentError{}},
		{`{"status":"submitted","task_id":"task_2"}`, &CommitmentSubmitted{}},
	}
	for _, tc := range cases {
		got, err := decodeCommitmentResponse([]byte(tc.raw))
		require.NoError(t, err)
		assert.IsType(t, tc.want, got)
	}

	_, err := decodeCommitmentResponse([]byte(`{"status":"working"}`))
	assert.Error(t, err)
}

func TestCommittedMediaBuyAvailableActionsAreObjects(t *testing.T) {
	raw := []byte(`{"status":"completed","media_buy_id":"mb_1","name":"Autumn plan","revision":1,"media_buy_status":"pending_creatives","confirmed_at":"2026-10-22T09:00:05Z","accepted_proposal":{"proposal_id":"prop_1","proposal_kind":"new_media_buy","media_buy_id":"mb_1","proposal_status":"accepted","accepted_at":"2026-10-22T09:00:00Z","name":"Autumn plan","commercial_terms":{"brand":{"domain":"example.com"},"start_time":"2026-11-01T00:00:00Z","end_time":"2026-11-30T00:00:00Z","purchases":[{"product_id":"prod_1","pricing_option_id":"po_1","pricing":{"pricing_option_id":"po_1","pricing_model":"cpm","fixed_price":5,"currency":"EUR"},"start_time":"2026-11-01T00:00:00Z","end_time":"2026-11-30T00:00:00Z"},{"product_id":"prod_2","pricing_option_id":"po_1","pricing":{"pricing_option_id":"po_1","pricing_model":"cpm","fixed_price":5,"currency":"EUR"},"start_time":"2026-11-01T00:00:00Z","end_time":"2026-11-30T00:00:00Z"}]},"terms_digest":"sha256:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"},"purchase_bindings":[{"purchase_index":0,"product_id":"prod_1","package_id":"pkg_1"},{"purchase_index":1,"product_id":"prod_2","package_id":"pkg_2"}],"available_actions":[{"task":"control_media_buy","action":"pause","mode":"self_serve"},{"task":"refine_proposals","action":"extend_flight","mode":"requires_approval","sla":{"response_max":"PT4H","completion_max":"P1D"},"change_term_id":"term_1","terms_ref":"legacy_terms_1","applicable_package_ids":["pkg_1","pkg_2"]},{"task":"sync_creatives","action":"replace_creative","mode":"seller_managed"}],"warnings":[{"code":"inventory_shortfall_forecast","message":"Forecast delivery is below the booked impressions","affected_resource":{"resource_type":"package","media_buy_id":"mb_1","package_id":"pkg_1"}}],"context":{"trace":"t1"},"ext":{"vendor":{"k":"v"}},"replayed":true}`)
	assertSchemaValid(t, "media-buy/accept-proposal-response.json", raw)
	var committed CommittedMediaBuy
	require.NoError(t, json.Unmarshal(raw, &committed))

	require.Len(t, committed.AvailableActions, 3)
	assert.Equal(t, CanonicalAvailableAction{
		Task: "control_media_buy", Action: "pause", Mode: CanonicalMediaBuyActionModeSelfServe,
	}, committed.AvailableActions[0])
	refine := committed.AvailableActions[1]
	assert.Equal(t, "refine_proposals", refine.Task)
	assert.Equal(t, "extend_flight", refine.Action)
	assert.Equal(t, CanonicalMediaBuyActionModeRequiresApproval, refine.Mode)
	require.NotNil(t, refine.Sla)
	assert.Equal(t, "PT4H", refine.Sla.ResponseMax)
	assert.Equal(t, "term_1", refine.ChangeTermID)
	assert.Equal(t, []string{"pkg_1", "pkg_2"}, refine.ApplicablePackageIDs)
	assert.Equal(t, "sync_creatives", committed.AvailableActions[2].Task)

	// The string enum keeps its name and values.
	assert.Equal(t, "pause", CanonicalMediaBuyActionPause)
	assert.True(t, IsKnownCanonicalMediaBuyAction("extend_flight"))
}

func TestCommitmentRequestsKeepExplicitZeroAndFalse(t *testing.T) {
	var buy BuyProductsRequest
	require.NoError(t, json.Unmarshal([]byte(`{"adcp_version":"3.2","adcp_major_version":3,"idempotency_key":"buy-products-000002","name":"Autumn buy","account":{"brand":{"domain":"example.com","brand_id":"outdoor"},"operator":"agency.example"},"advertiser_industry":"retail","feed_version":"feed_1","pricing_version":"price_1","purchases":[{"product_id":"prod_1","pricing_option_id":"po_1","pricing":{"pricing_option_id":"po_1","pricing_model":"cpm","fixed_price":5,"currency":"EUR"},"format_option_refs":[{"scope":"publisher","publisher_domain":"pub.example","format_option_id":"fo_1"},{"scope":"product","format_option_id":"fo_2"}],"catalog_ids":["cat_1"],"budget":0,"daily_budget_cap":0,"min_spend_target":0,"impressions":0,"start_time":"2026-11-01T00:00:00Z","end_time":"2026-11-30T00:00:00Z","pacing":"even","bidding":{"max_bid":4.5},"targeting_overlay":{"geo_countries":["DE"],"device_type":null},"optimization_goals":[{"kind":"metric","metric":"clicks"}],"audience_evidence_requirements":{"requirement_mode":"required","evidence_presence":"when_available"},"audience_evidence_pins":[{"evidence_id":"ev_1","snapshot_id":"snap_1","version":"1","content_digest":"sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}],"agency_estimate_number":"EST-1","context":{"line":1},"ext":{"vendor":{"k":"v"}},"measurement_terms":{},"performance_standards":[{"metric":"viewability","threshold":0,"standard":"mrc","vendor":{"domain":"ias.example"}}]},{"product_id":"prod_2","pricing_option_id":"po_2","budget":2500.5,"impressions":100000}],"total_budget":{"amount":5000,"currency":"EUR"},"daily_budget_cap":0,"frequency_cap":{"max_impressions":3,"per":"devices","window":{"interval":1,"unit":"days"}},"budget_cap_timezone":"Europe/Berlin","budget_allocation":{"mode":"fixed"},"start_time":"2026-11-01T00:00:00Z","end_time":"2026-11-30T00:00:00Z","pacing":"asap","bidding":{"automatic":true},"paused":false,"purchase_order_ref":"PO-2","agency_estimate_number":"EST-2","invoice_recipient":{"legal_name":"Agency GmbH"},"governance_context":"gov-ctx-2","push_notification_config":{"url":"https://buyer.example/hooks/tasks"},"reporting_webhook":{"url":"https://buyer.example/hooks/reporting","authentication":{"schemes":["HMAC-SHA256"],"credentials":"cyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyy"},"reporting_frequency":"hourly"},"opportunity":{"opportunity_id":"opp_2"},"context":{"line":2},"ext":{"vendor":{"k":"v"}}}`), &buy))

	require.NotNil(t, buy.DailyBudgetCap)
	assert.Zero(t, *buy.DailyBudgetCap)
	require.NotNil(t, buy.Paused)
	assert.False(t, *buy.Paused)
	require.NotNil(t, buy.TotalBudget)
	require.Len(t, buy.Purchases, 2)
	for _, field := range []*float64{
		buy.Purchases[0].Budget, buy.Purchases[0].DailyBudgetCap,
		buy.Purchases[0].MinSpendTarget, buy.Purchases[0].Impressions,
	} {
		require.NotNil(t, field)
		assert.Zero(t, *field)
	}
	// Omitted optional numbers stay omitted.
	assert.Nil(t, buy.Purchases[1].DailyBudgetCap)
	assert.Nil(t, buy.Purchases[1].MinSpendTarget)
	out, err := json.Marshal(buy.Purchases[1])
	require.NoError(t, err)
	assert.JSONEq(t, `{"product_id":"prod_2","pricing_option_id":"po_2","budget":2500.5,"impressions":100000}`, string(out))

	var accept AcceptProposalRequest
	require.NoError(t, json.Unmarshal([]byte(`{"adcp_major_version":3,"idempotency_key":"accept-prop-0000002","name":"Autumn plan","account":{"brand":{"domain":"example.com"},"operator":"agency.example"},"proposal_id":"prop_1","proposal_terms_digest":"sha256:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA","total_budget":{"amount":0,"currency":"EUR"},"daily_budget_cap":0,"budget_cap_timezone":"Europe/Berlin","io_acceptance":{"io_id":"io_1","accepted_at":"2026-10-22T09:00:00Z","signatory":"Jo Buyer","signature_id":"sig_1"},"purchase_order_ref":"PO-1","governance_context":"gov-ctx-1","push_notification_config":{"url":"https://buyer.example/hooks/tasks","token":"tok_0123456789abcdef"},"reporting_webhook":{"url":"https://buyer.example/hooks/reporting","authentication":{"schemes":["Bearer"],"credentials":"cxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx"},"reporting_frequency":"daily"},"opportunity":{"opportunity_id":"opp_1","status":"closed","close_reason":"accepted_with_seller"},"context":{"trace":"t1"},"ext":{"vendor":{"k":"v"}}}`), &accept))
	require.NotNil(t, accept.DailyBudgetCap)
	assert.Zero(t, *accept.DailyBudgetCap)
	require.NotNil(t, accept.AdcpMajorVersion)
	assert.Equal(t, 3, *accept.AdcpMajorVersion)
}

// targeting_overlay carries a null (clear a dimension) inside the object. Its
// Go type is a reviewed `any` until adcp-go#536 settles the encoding, so the
// null survives a round trip.
func TestBuyProductsTargetingOverlayKeepsNullDimension(t *testing.T) {
	raw := `{"idempotency_key":"buy-products-000003","account":{"account_id":"acc_1"},"brand":{"domain":"example.com"},"feed_version":"feed_1","purchases":[{"product_id":"prod_1","pricing_option_id":"po_1","targeting_overlay":{"device_type":null,"geo_countries":["DE"]}}],"start_time":"2026-11-01T00:00:00Z","end_time":"2026-11-30T00:00:00Z"}`
	assertSchemaValid(t, "media-buy/buy-products-request.json", []byte(raw))
	var req BuyProductsRequest
	require.NoError(t, json.Unmarshal([]byte(raw), &req))
	out, err := json.Marshal(req)
	require.NoError(t, err)
	assert.JSONEq(t, raw, string(out))
}

// confirmed_at is optional and may be null. The field is a pointer, so null
// decodes to nil and is omitted on encode: presence of the key is not kept.
func TestCommittedMediaBuyNullConfirmedAtDecodesToNil(t *testing.T) {
	raw := []byte(`{"status":"completed","media_buy_id":"mb_3","revision":2,"confirmed_at":null,"accepted_proposal":{"proposal_id":"prop_1","proposal_kind":"new_media_buy","media_buy_id":"mb_1","proposal_status":"accepted","accepted_at":"2026-10-22T09:00:00Z","name":"Autumn plan","commercial_terms":{"brand":{"domain":"example.com"},"start_time":"2026-11-01T00:00:00Z","end_time":"2026-11-30T00:00:00Z","purchases":[{"product_id":"prod_1","pricing_option_id":"po_1","pricing":{"pricing_option_id":"po_1","pricing_model":"cpm","fixed_price":5,"currency":"EUR"},"start_time":"2026-11-01T00:00:00Z","end_time":"2026-11-30T00:00:00Z"},{"product_id":"prod_2","pricing_option_id":"po_1","pricing":{"pricing_option_id":"po_1","pricing_model":"cpm","fixed_price":5,"currency":"EUR"},"start_time":"2026-11-01T00:00:00Z","end_time":"2026-11-30T00:00:00Z"}]},"terms_digest":"sha256:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"},"purchase_bindings":[{"purchase_index":0,"product_id":"prod_1","package_id":"pkg_1"}],"available_actions":[]}`)
	assertSchemaValid(t, "media-buy/accept-proposal-response.json", raw)
	var committed CommittedMediaBuy
	require.NoError(t, json.Unmarshal(raw, &committed))

	assert.Nil(t, committed.ConfirmedAt)
	out, err := json.Marshal(committed)
	require.NoError(t, err)
	assert.NotContains(t, string(out), "confirmed_at")
}
