package adcp

import (
	"encoding/json"
	"fmt"
	"reflect"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// control_media_buy: a request with revision-checked controls and a response
// that is a union discriminated by status (completed, failed, submitted).
// Every wire fixture validates against its AdCP 3.2.2 schema, and decoding it into
// the generated type and encoding it back must reproduce it exactly, so a
// dropped or renamed field fails the test. The fixtures carry an explicit zero
// or false wherever one is valid.

var (
	_ ControlMediaBuyResponse = ControlApplied{}
	_ ControlMediaBuyResponse = &ControlApplied{}
	_ ControlMediaBuyResponse = ControlError{}
	_ ControlMediaBuyResponse = &ControlError{}
	_ ControlMediaBuyResponse = ControlSubmitted{}
	_ ControlMediaBuyResponse = &ControlSubmitted{}
)

const (
	controlRequestSchema  = "media-buy/control-media-buy-request.json"
	controlResponseSchema = "media-buy/control-media-buy-response.json"
)

// decodeControlMediaBuyResponse picks the variant by the union's discriminator,
// status. The generated union is an interface, so choosing the variant is the
// caller's job, as for the other generated response unions.
func decodeControlMediaBuyResponse(raw []byte) (ControlMediaBuyResponse, error) {
	var head struct {
		Status string `json:"status"`
	}
	if err := json.Unmarshal(raw, &head); err != nil {
		return nil, err
	}
	var out ControlMediaBuyResponse
	switch head.Status {
	case "completed":
		out = &ControlApplied{}
	case "failed":
		out = &ControlError{}
	case "submitted":
		out = &ControlSubmitted{}
	default:
		return nil, fmt.Errorf("unknown control_media_buy status %q", head.Status)
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return nil, err
	}

	return out, nil
}

const controlRequestFull = `{"adcp_version":"3.2","adcp_major_version":3,"idempotency_key":"control-mb-0000002","account":{"brand":{"domain":"example.com"},"operator":"agency.example"},"media_buy_id":"mb_1","revision":4,"name":"Autumn plan v2","paused":false,"total_budget":{"amount":0,"currency":"EUR"},"daily_budget_cap":0,"frequency_cap":{"max_impressions":3,"per":"devices","window":{"interval":1,"unit":"days"}},"budget_cap_timezone":"Europe/Berlin","budget_allocation":{"mode":"fixed"},"pacing":"front_loaded","bidding":{"automatic":true},"packages":[{"package_id":"pkg_1","budget":0,"daily_budget_cap":0,"impressions":0,"pacing":"asap","bidding":{"max_bid":4.5},"paused":false,"catalog_ids":["cat_1"],"keyword_targets_add":[{"keyword":"running shoes","match_type":"exact","bid_price":0},{"keyword":"trail","match_type":"phrase"}],"keyword_targets_remove":[{"keyword":"sandals","match_type":"broad"}],"negative_keywords_add":[{"keyword":"free","match_type":"broad"}],"negative_keywords_remove":[{"keyword":"cheap","match_type":"phrase"}],"optimization_goals":[{"kind":"metric","metric":"clicks"}]},{"package_id":"pkg_2","targeting_overlay":{"geo_countries":["DE"],"device_type":null}},{"package_id":"pkg_3","canceled":true,"cancellation_reason":"Replaced by pkg_1"}],"reporting_webhook":{"url":"https://buyer.example/hooks/reporting","authentication":{"schemes":["Bearer"],"credentials":"cxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx"},"reporting_frequency":"daily"},"governance_context":"gov-ctx-1","push_notification_config":{"url":"https://buyer.example/hooks/tasks","token":"tok_0123456789abcdef","operation_id":"op_control_1"},"context":{"trace":"t1"},"ext":{"vendor":{"k":"v"}}}`

func TestGeneratedControlMediaBuyRequestRoundTrip(t *testing.T) {
	cases := []struct {
		name    string
		fixture string
	}{
		{"pause with explicit false", `{"idempotency_key":"control-mb-0000001","account":{"account_id":"acc_1"},"media_buy_id":"mb_1","revision":1,"paused":false}`},
		{"every control field", controlRequestFull},
		{"media buy cancellation", `{"idempotency_key":"control-mb-0000003","account":{"account_id":"acc_1"},"media_buy_id":"mb_1","revision":2,"canceled":true,"cancellation_reason":"Campaign withdrawn"}`},
		{"cancellation without reason", `{"idempotency_key":"control-mb-0000004","account":{"account_id":"acc_1"},"media_buy_id":"mb_1","revision":2,"canceled":true}`},
		{"seller-optimized allocation with a zero spend target", `{"idempotency_key":"control-mb-0000009","account":{"account_id":"acc_1"},"media_buy_id":"mb_1","revision":2,"budget_allocation":{"mode":"seller_optimized","optimization_goals":[{"kind":"metric","metric":"clicks"}]},"packages":[{"package_id":"pkg_1","min_spend_target":0}]}`},
		{"package patch only", `{"idempotency_key":"control-mb-0000005","account":{"account_id":"acc_1"},"media_buy_id":"mb_1","revision":3,"packages":[{"package_id":"pkg_1","paused":false}]}`},
		{"reporting webhook only", `{"idempotency_key":"control-mb-0000006","account":{"account_id":"acc_1"},"media_buy_id":"mb_1","revision":3,"reporting_webhook":{"url":"https://buyer.example/hooks/reporting","authentication":{"schemes":["HMAC-SHA256"],"credentials":"cyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyy"},"reporting_frequency":"hourly"}}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assertSchemaValid(t, controlRequestSchema, []byte(tc.fixture))
			var req ControlMediaBuyRequest
			require.NoError(t, json.Unmarshal([]byte(tc.fixture), &req))
			out, err := json.Marshal(req)
			require.NoError(t, err)
			assert.JSONEq(t, tc.fixture, string(out))
		})
	}
}

func TestGeneratedControlMediaBuyResponsesRoundTrip(t *testing.T) {
	cases := []struct {
		name    string
		fixture string
	}{
		{"completed minimal", `{"status":"completed","media_buy_id":"mb_1","revision":2}`},
		{"completed full", `{"status":"completed","media_buy_id":"mb_1","revision":5,"media_buy_status":"active","implementation_date":"2026-10-24T00:00:00Z","affected_package_ids":["pkg_1","pkg_2"],"available_actions":[{"task":"control_media_buy","action":"pause","mode":"self_serve"},{"task":"refine_proposals","action":"extend_flight","mode":"requires_approval","sla":{"response_max":"PT4H","completion_max":"P1D"},"change_term_id":"term_1","terms_ref":"term_1","applicable_package_ids":["pkg_1","pkg_2"]},{"task":"sync_creatives","action":"replace_creative","mode":"seller_managed"}],"warnings":[{"code":"inventory_shortfall_forecast","message":"Forecast delivery is below the booked impressions","affected_resource":{"resource_type":"package","media_buy_id":"mb_1","package_id":"pkg_1"}}],"context":{"trace":"t1"},"ext":{"vendor":{"k":"v"}},"replayed":true}`},
		{"failed minimal", `{"status":"failed","errors":[{"code":"REQUOTE_REQUIRED","message":"The control is outside the accepted change terms","recovery":"correctable"}]}`},
		{"failed full", `{"status":"failed","errors":[{"code":"ACTION_NOT_ALLOWED","message":"Pacing cannot be changed on this media buy","recovery":"terminal","details":{"attempted_action":"update_pacing","reason":"not_supported_on_buy","currently_available_actions":[{"task":"control_media_buy","action":"pause","mode":"self_serve"}]}},{"code":"INVALID_REQUEST","message":"The frequency cap window is not an interval","field":"frequency_cap","recovery":"correctable"}],"context":{"trace":"t2"},"ext":{"vendor":{"k":"v"}},"replayed":true}`},
		{"submitted minimal", `{"status":"submitted","task_id":"task_1"}`},
		{"submitted full", `{"status":"submitted","task_id":"task_2","message":"Awaiting seller approval","errors":[{"code":"SERVICE_UNAVAILABLE","message":"The seller review queue is slow; the control is queued","recovery":"transient"}],"context":{"trace":"t3"},"ext":{"vendor":{"k":"v"}},"replayed":true}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assertSchemaValid(t, controlResponseSchema, []byte(tc.fixture))
			resp, err := decodeControlMediaBuyResponse([]byte(tc.fixture))
			require.NoError(t, err)
			out, err := json.Marshal(resp)
			require.NoError(t, err)
			assert.JSONEq(t, tc.fixture, string(out))
		})
	}
}

// Each status decodes into the variant that carries its fields. The variant
// is chosen by the test's own helper, as the generated union is an interface.
func TestControlMediaBuyResponseVariantsCarryTheirFields(t *testing.T) {
	for _, raw := range []string{
		`{"status":"completed","media_buy_id":"mb_1","revision":2}`,
		`{"status":"failed","errors":[{"code":"REQUOTE_REQUIRED","message":"outside bounds","recovery":"correctable"}]}`,
		`{"status":"submitted","task_id":"task_1"}`,
	} {
		assertSchemaValid(t, controlResponseSchema, []byte(raw))
	}

	applied, err := decodeControlMediaBuyResponse([]byte(`{"status":"completed","media_buy_id":"mb_1","revision":2}`))
	require.NoError(t, err)
	require.IsType(t, &ControlApplied{}, applied)
	assert.Equal(t, "mb_1", applied.(*ControlApplied).MediaBuyID)
	assert.Equal(t, 2, applied.(*ControlApplied).Revision)

	failed, err := decodeControlMediaBuyResponse([]byte(`{"status":"failed","errors":[{"code":"REQUOTE_REQUIRED","message":"outside bounds","recovery":"correctable"}]}`))
	require.NoError(t, err)
	require.IsType(t, &ControlError{}, failed)
	require.Len(t, failed.(*ControlError).Errors, 1)

	submitted, err := decodeControlMediaBuyResponse([]byte(`{"status":"submitted","task_id":"task_1"}`))
	require.NoError(t, err)
	require.IsType(t, &ControlSubmitted{}, submitted)
	assert.Equal(t, "task_1", submitted.(*ControlSubmitted).TaskID)

	withErrors, err := decodeControlMediaBuyResponse([]byte(`{"status":"submitted","task_id":"task_2","errors":[{"code":"SERVICE_UNAVAILABLE","message":"slow","recovery":"transient"}]}`))
	require.NoError(t, err)
	require.Len(t, withErrors.(*ControlSubmitted).Errors, 1)
	assert.Equal(t, "SERVICE_UNAVAILABLE", withErrors.(*ControlSubmitted).Errors[0]["code"])

	_, err = decodeControlMediaBuyResponse([]byte(`{"status":"working"}`))
	assert.Error(t, err)
}

func TestControlMediaBuyRequestKeepsExplicitZeroAndFalse(t *testing.T) {
	assertSchemaValid(t, controlRequestSchema, []byte(controlRequestFull))
	var req ControlMediaBuyRequest
	require.NoError(t, json.Unmarshal([]byte(controlRequestFull), &req))

	require.NotNil(t, req.AdcpMajorVersion)
	assert.Equal(t, 3, *req.AdcpMajorVersion)
	require.NotNil(t, req.Paused)
	assert.False(t, *req.Paused)
	require.NotNil(t, req.TotalBudget)
	assert.Zero(t, req.TotalBudget.Amount)
	assert.Nil(t, req.Canceled)

	require.Len(t, req.Packages, 3)
	pkg := req.Packages[0]
	assert.Equal(t, []string{"cat_1"}, pkg.CatalogIDs)
	require.NotNil(t, pkg.Impressions)
	assert.Zero(t, *pkg.Impressions)
	require.NotNil(t, pkg.Paused)
	assert.False(t, *pkg.Paused)
	require.Len(t, pkg.KeywordTargetsAdd, 2)
	require.NotNil(t, pkg.KeywordTargetsAdd[0].BidPrice)
	assert.Zero(t, *pkg.KeywordTargetsAdd[0].BidPrice)
	assert.Nil(t, pkg.KeywordTargetsAdd[1].BidPrice)
	assert.Equal(t, MatchTypeExact, pkg.KeywordTargetsAdd[0].MatchType)
	require.NotNil(t, req.Packages[2].Canceled)
	assert.True(t, *req.Packages[2].Canceled)

	// Omitted optional values stay omitted.
	assert.Nil(t, req.Packages[1].Impressions)
	assert.Nil(t, req.Packages[1].Paused)
	out, err := json.Marshal(req.Packages[1])
	require.NoError(t, err)
	assert.JSONEq(t, `{"package_id":"pkg_2","targeting_overlay":{"geo_countries":["DE"],"device_type":null}}`, string(out))
}

// The keyword rows of package-control.json come from core/keyword-target.json,
// not from the inline items of core/targeting.json, which KeywordTarget names.
// The two have the same fields; each type keeps its own schema's shape.
func TestCanonicalKeywordTargetIsNotTheTargetingKeywordTarget(t *testing.T) {
	row := `{"keyword":"running shoes","match_type":"exact","bid_price":0}`
	assertSchemaValid(t, "core/keyword-target.json", []byte(row))
	assertSchemaValid(t, "core/targeting.json#/properties/keyword_targets/items", []byte(row))

	var canonical CanonicalKeywordTarget
	require.NoError(t, json.Unmarshal([]byte(row), &canonical))
	var targeting KeywordTarget
	require.NoError(t, json.Unmarshal([]byte(row), &targeting))
	for _, v := range []any{canonical, targeting} {
		out, err := json.Marshal(v)
		require.NoError(t, err)
		assert.JSONEq(t, row, string(out))
	}
	assert.NotEqual(t, reflect.TypeOf(canonical), reflect.TypeOf(targeting))
	for _, rows := range []any{
		PackageControl{}.KeywordTargetsAdd, PackageControl{}.KeywordTargetsRemove,
		PackageControl{}.NegativeKeywordsAdd, PackageControl{}.NegativeKeywordsRemove,
	} {
		assert.IsType(t, []CanonicalKeywordTarget{}, rows)
	}
	assert.IsType(t, []KeywordTarget{}, Targeting{}.KeywordTargets)
}

// Request properties whose schema allows null mean "clear it" (daily_budget_cap:
// "null removes it"). Their Go type is a reviewed `any` until adcp-go#536
// settles the three-state encoding, and `any` with omitempty does not keep a
// top-level null: it decodes to nil and the key is dropped on encode, as for a
// *float64 and as for UpdateMediaBuyRequest.frequency_cap. This pins that
// limitation so changing it is a deliberate act of #536. The expected output
// below is knowingly not a valid request (a package patch needs a control
// field): a proxy that decodes and re-encodes would drop the clear.
func TestControlMediaBuyRequestNullFieldsDecodeLikeOmission(t *testing.T) {
	requireSchemaBundle(t)
	raw := `{"idempotency_key":"control-mb-0000007","account":{"account_id":"acc_1"},"media_buy_id":"mb_1","revision":3,"daily_budget_cap":null,"frequency_cap":null,"budget_cap_timezone":null,"bidding":null,"packages":[{"package_id":"pkg_1","budget":null,"daily_budget_cap":null,"min_spend_target":null,"bidding":null}]}`
	assertSchemaValid(t, controlRequestSchema, []byte(raw))
	var req ControlMediaBuyRequest
	require.NoError(t, json.Unmarshal([]byte(raw), &req))

	assert.Nil(t, req.DailyBudgetCap)
	assert.Nil(t, req.FrequencyCap)
	assert.Nil(t, req.BudgetCapTimezone)
	assert.Nil(t, req.Bidding)
	pkg := req.Packages[0]
	assert.Nil(t, pkg.Budget)
	assert.Nil(t, pkg.DailyBudgetCap)
	assert.Nil(t, pkg.MinSpendTarget)
	assert.Nil(t, pkg.Bidding)

	out, err := json.Marshal(req)
	require.NoError(t, err)
	assert.JSONEq(t, `{"idempotency_key":"control-mb-0000007","account":{"account_id":"acc_1"},"media_buy_id":"mb_1","revision":3,"packages":[{"package_id":"pkg_1"}]}`, string(out))
	// The re-encoded request has lost its only control, so it is no longer valid.
	invalid := validateAgainstSchema(controlRequestSchema, out)
	require.Error(t, invalid)
	assert.NotErrorIs(t, invalid, errSchemaBundleMissing)

	// A caller sends a clear with ClearField (inputs.go), which marshals as null
	// in an `any` field with omitempty. That works only because these fields are
	// `any`; the typed encoding is #536's.
	cleared := ControlMediaBuyRequest{
		IdempotencyKey: "control-mb-0000008", Account: CanonicalAccountRef{AccountID: "acc_1"},
		MediaBuyID: "mb_1", Revision: 3,
		DailyBudgetCap: ClearField(), BudgetCapTimezone: ClearField(),
		FrequencyCap: ClearField(), Bidding: ClearField(),
		Packages: []PackageControl{{PackageID: "pkg_1", Budget: ClearField(), DailyBudgetCap: ClearField(), MinSpendTarget: ClearField(), Bidding: ClearField()}},
	}
	out, err = json.Marshal(cleared)
	require.NoError(t, err)
	assert.JSONEq(t, `{"idempotency_key":"control-mb-0000008","account":{"account_id":"acc_1"},"media_buy_id":"mb_1","revision":3,"daily_budget_cap":null,"budget_cap_timezone":null,"frequency_cap":null,"bidding":null,"packages":[{"package_id":"pkg_1","budget":null,"daily_budget_cap":null,"min_spend_target":null,"bidding":null}]}`, string(out))
	assertSchemaValid(t, controlRequestSchema, out)

	// A non-null value is kept, including a zero.
	var zero ControlMediaBuyRequest
	require.NoError(t, json.Unmarshal([]byte(`{"daily_budget_cap":0,"budget_cap_timezone":"UTC"}`), &zero))
	assert.Equal(t, float64(0), zero.DailyBudgetCap)
	assert.Equal(t, "UTC", zero.BudgetCapTimezone)
}

// implementation_date is optional and may be null. The field is a pointer, so
// null decodes to nil and is omitted on encode: presence of the key is not kept.
func TestControlAppliedNullImplementationDateDecodesToNil(t *testing.T) {
	raw := []byte(`{"status":"completed","media_buy_id":"mb_1","revision":2,"implementation_date":null}`)
	assertSchemaValid(t, controlResponseSchema, raw)
	var applied ControlApplied
	require.NoError(t, json.Unmarshal(raw, &applied))

	assert.Nil(t, applied.ImplementationDate)
	out, err := json.Marshal(applied)
	require.NoError(t, err)
	assert.JSONEq(t, `{"status":"completed","media_buy_id":"mb_1","revision":2}`, string(out))
}

// The arrays of ControlApplied and ControlSubmitted are optional, so an empty
// one is valid on the wire but is omitted on encode, like the other generated
// optional arrays.
func TestControlAppliedEmptyOptionalArraysAreOmitted(t *testing.T) {
	raw := []byte(`{"status":"completed","media_buy_id":"mb_1","revision":2,"affected_package_ids":[],"available_actions":[]}`)
	assertSchemaValid(t, controlResponseSchema, raw)
	var applied ControlApplied
	require.NoError(t, json.Unmarshal(raw, &applied))

	out, err := json.Marshal(applied)
	require.NoError(t, err)
	assert.JSONEq(t, `{"status":"completed","media_buy_id":"mb_1","revision":2}`, string(out))

	submittedRaw := []byte(`{"status":"submitted","task_id":"task_1","errors":[]}`)
	assertSchemaValid(t, controlResponseSchema, submittedRaw)
	var submitted ControlSubmitted
	require.NoError(t, json.Unmarshal(submittedRaw, &submitted))
	out, err = json.Marshal(submitted)
	require.NoError(t, err)
	assert.JSONEq(t, `{"status":"submitted","task_id":"task_1"}`, string(out))
}

func TestControlAppliedAvailableActionsAreObjects(t *testing.T) {
	raw := []byte(`{"status":"completed","media_buy_id":"mb_1","revision":5,"affected_package_ids":["pkg_1","pkg_2"],"available_actions":[{"task":"control_media_buy","action":"resume","mode":"self_serve"},{"task":"refine_proposals","action":"extend_flight","mode":"requires_approval","applicable_package_ids":["pkg_1"]}]}`)
	assertSchemaValid(t, controlResponseSchema, raw)
	var applied ControlApplied
	require.NoError(t, json.Unmarshal(raw, &applied))

	assert.Equal(t, []string{"pkg_1", "pkg_2"}, applied.AffectedPackageIDs)
	require.Len(t, applied.AvailableActions, 2)
	assert.Equal(t, CanonicalAvailableAction{
		Task: "control_media_buy", Action: "resume", Mode: CanonicalMediaBuyActionModeSelfServe,
	}, applied.AvailableActions[0])
	assert.Equal(t, []string{"pkg_1"}, applied.AvailableActions[1].ApplicablePackageIDs)
}

// Required properties are never omitted on encode, whatever their value, so a
// variant built from its zero value still names every field the schema requires.
// The zero values below are not valid requests or responses (a nil required
// array encodes as null, like the other generated required arrays); only the
// presence of each key is the point.
func TestControlMediaBuyRequiredPropertiesAreNeverOmitted(t *testing.T) {
	cases := []struct {
		name string
		v    any
		want string
	}{
		{"request", ControlMediaBuyRequest{}, `{"idempotency_key":"","account":{},"media_buy_id":"","revision":0}`},
		{"completed", ControlApplied{}, `{"status":"","media_buy_id":"","revision":0}`},
		{"failed", ControlError{}, `{"status":"","errors":null}`},
		{"submitted", ControlSubmitted{}, `{"status":"","task_id":""}`},
		{"package control", PackageControl{}, `{"package_id":""}`},
		{"keyword row", CanonicalKeywordTarget{}, `{"keyword":"","match_type":""}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out, err := json.Marshal(tc.v)
			require.NoError(t, err)
			assert.JSONEq(t, tc.want, string(out))
		})
	}
}

// core/error.json leaves details open, so a response fixture alone cannot show
// that an ACTION_NOT_ALLOWED error carries the details its schema asks for.
func TestControlMediaBuyActionNotAllowedDetailsMatchTheirSchema(t *testing.T) {
	details := `{"attempted_action":"update_pacing","reason":"not_supported_on_buy","currently_available_actions":[{"task":"control_media_buy","action":"pause","mode":"self_serve"}]}`
	assertSchemaValid(t, "error-details/action-not-allowed.json", []byte(details))
	assertSchemaValid(t, controlResponseSchema, []byte(`{"status":"failed","errors":[{"code":"ACTION_NOT_ALLOWED","message":"Pacing cannot be changed on this media buy","recovery":"terminal","details":`+details+`}]}`))
}
