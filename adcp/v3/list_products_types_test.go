package adcp

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// list_products: a request, and a response that is ONE flat struct. The
// schema's oneOf only switches which top-level properties are required
// (`outcome` is the discriminator), so the generator emits a single
// ListProductsResponse with a typed Outcome and a generated Validate that
// states the per-outcome rules (adcp-go proposal in the issue on list_products
// types). Every wire fixture validates against its AdCP 3.2.2 schema first;
// decoding it into the generated type and encoding it back must reproduce it,
// so a dropped or renamed field fails the test. The fixtures carry an explicit
// empty list wherever one is valid.

const (
	listProductsRequestSchema  = "media-buy/list-products-request.json"
	listProductsResponseSchema = "media-buy/list-products-response.json"
)

func TestGeneratedListProductsRequestRoundTrip(t *testing.T) {
	cases := []struct {
		name    string
		fixture string
	}{
		{"no fields", `{}`},
		{"idempotent first page", `{"idempotency_key":"list-products-0001","max_results":25}`},
		{"field projection", `{"fields":["product_id","name","pricing_options","allowed_actions"],"max_results":1}`},
		{"every product field", `{"fields":["product_id","name","description","publisher_properties","channels","video_placement_types","audio_distribution_types","sponsored_placement_types","social_placement_surfaces","format_options","placements","delivery_type","exclusivity","pricing_options","forecast","reporting_capabilities","measurement_terms","performance_standards","catalog_types","signal_targeting_allowed","signal_targeting_rules","demographic_targeting","overlay_support","collections","collection_targeting_allowed","media_buy_support","audience_evidence","audience_evidence_selections","max_optimization_goals","catalog_match","list_applications","brief_relevance","targeting_resolution","acceptance_policy_profile_ids","identity","execution_requirements","expires_at","allowed_actions"]}`},
		{"conditional refresh", `{"if_feed_version":"feed-7","if_pricing_version":"price-3","cursor":"c2"}`},
		{"natural-key account", `{"account":{"brand":{"domain":"example.com"},"operator":"agency.example"}}`},
		{"every field", `{"adcp_version":"3.2","adcp_major_version":3,"idempotency_key":"list-products-0002","context_id":"ctx_1","context":{"trace":"t1"},"governance_context":"gov-ctx-1","push_notification_config":{"url":"https://buyer.example/hooks/tasks","token":"tok_0123456789abcdef","operation_id":"op_list_1"},"account":{"account_id":"acc_1"},"brand":{"domain":"example.com"},"criteria":{"product_ids":["prod_1","prod_2"],"policy_ids":["pol_1"],"ext":{"vendor":{"k":"v"}}},"fields":["product_id","name"],"cursor":"c1","max_results":100,"if_feed_version":"feed-6","if_pricing_version":"price-2"}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assertSchemaValid(t, listProductsRequestSchema, []byte(tc.fixture))
			var req ListProductsRequest
			require.NoError(t, json.Unmarshal([]byte(tc.fixture), &req))
			out, err := json.Marshal(req)
			require.NoError(t, err)
			assert.JSONEq(t, tc.fixture, string(out))
		})
	}
}

func TestListProductsRequestFieldTypes(t *testing.T) {
	typ := reflect.TypeFor[ListProductsRequest]()

	major, ok := typ.FieldByName("AdcpMajorVersion")
	require.True(t, ok)
	assert.Equal(t, reflect.TypeFor[*int](), major.Type, "like the other generated requests")

	fields, ok := typ.FieldByName("Fields")
	require.True(t, ok)
	assert.Equal(t, reflect.TypeFor[[]ProductField](), fields.Type)

	// The enum is the one the schema defines: media-buy/product-fields.json is an
	// array of it, so the helper must list exactly its items.
	raw, err := os.ReadFile(filepath.Join(schemaBundleDir, "media-buy", "product-fields.json"))
	if errors.Is(err, os.ErrNotExist) {
		t.Skip(errSchemaBundleMissing.Error())
	}
	require.NoError(t, err)
	var schema struct {
		Items struct {
			Enum []string `json:"enum"`
		} `json:"items"`
	}
	require.NoError(t, json.Unmarshal(raw, &schema))
	require.NotEmpty(t, schema.Items.Enum)
	assert.Equal(t, schema.Items.Enum, KnownProductFieldValues())
}

func TestListProductsRequestKeepsAnExplicitMajorVersion(t *testing.T) {
	// A pointer, so an absent version stays absent and a set one is sent.
	out, err := json.Marshal(ListProductsRequest{})
	require.NoError(t, err)
	assert.JSONEq(t, `{}`, string(out))

	one := 1
	out, err = json.Marshal(ListProductsRequest{AdcpMajorVersion: &one})
	require.NoError(t, err)
	assert.JSONEq(t, `{"adcp_major_version":1}`, string(out))
}

const (
	listProductsListedPage = `{"outcome":"listed","products":[{"product_id":"prod_1","name":"Homepage takeover"},{"product_id":"prod_2","name":"Run of site","description":"Display on every page","delivery_type":"non_guaranteed","exclusivity":"none"}],"next_cursor":"c2","feed_version":"feed-7","pricing_version":"price-3","cache_scope":"public","incomplete":[{"scope":"pricing","description":"Rate card still loading","estimated_wait":{"interval":30,"unit":"seconds"}},{"scope":"wholesale_feed","description":"Feed enumeration timed out"}],"replayed":true,"context":{"trace":"t1"},"ext":{"vendor":{"k":"v"}}}`
	listProductsEmptyPage  = `{"outcome":"listed","products":[],"feed_version":"feed-7","cache_scope":"account"}`
	listProductsUnchanged  = `{"outcome":"unchanged","feed_version":"feed-7","cache_scope":"public"}`
)

func TestGeneratedListProductsResponseRoundTrip(t *testing.T) {
	cases := []struct {
		name    string
		fixture string
		outcome ListProductsResponseOutcome
	}{
		{"listed page", listProductsListedPage, ListProductsResponseOutcomeListed},
		{"listed with no products", listProductsEmptyPage, ListProductsResponseOutcomeListed},
		{"listed, last page, account scope", `{"outcome":"listed","products":[{"product_id":"prod_9","name":"Sponsorship"}],"feed_version":"feed-8","cache_scope":"account"}`, ListProductsResponseOutcomeListed},
		{"unchanged", listProductsUnchanged, ListProductsResponseOutcomeUnchanged},
		{"unchanged with every optional field", `{"outcome":"unchanged","next_cursor":"c3","feed_version":"feed-7","pricing_version":"price-3","cache_scope":"account","incomplete":[{"scope":"forecast","description":"Forecasts pending"}],"replayed":true,"context":{"trace":"t2"},"ext":{"vendor":{"k":"v"}}}`, ListProductsResponseOutcomeUnchanged},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assertSchemaValid(t, listProductsResponseSchema, []byte(tc.fixture))
			var resp ListProductsResponse
			require.NoError(t, json.Unmarshal([]byte(tc.fixture), &resp))
			assert.Equal(t, tc.outcome, resp.Outcome)
			assert.Empty(t, resp.Validate(WithStrictEnums()), "a schema-valid fixture has no issues")
			out, err := json.Marshal(resp)
			require.NoError(t, err)
			assert.JSONEq(t, tc.fixture, string(out))

			// A pointer encodes the same way.
			out, err = json.Marshal(&resp)
			require.NoError(t, err)
			assert.JSONEq(t, tc.fixture, string(out))
		})
	}
}

// An empty page is `products: []` on the wire, and nil is no products key at
// all. The field is omitzero, not omitempty, so the two differ, as listed
// requires the key and unchanged forbids it.
func TestListProductsResponseKeepsAnEmptyProductsList(t *testing.T) {
	empty := ListProductsResponse{
		Outcome:     ListProductsResponseOutcomeListed,
		Products:    []CanonicalProduct{},
		FeedVersion: "feed-7",
		CacheScope:  "account",
	}
	out, err := json.Marshal(empty)
	require.NoError(t, err)
	assert.JSONEq(t, listProductsEmptyPage, string(out))
	assertSchemaValid(t, listProductsResponseSchema, out)

	var decoded ListProductsResponse
	require.NoError(t, json.Unmarshal(out, &decoded))
	assert.NotNil(t, decoded.Products, "[] decodes to a non-nil empty list")
	assert.Empty(t, decoded.Products)

	unchanged := ListProductsResponse{
		Outcome:     ListProductsResponseOutcomeUnchanged,
		FeedVersion: "feed-7",
		CacheScope:  "public",
	}
	out, err = json.Marshal(unchanged)
	require.NoError(t, err)
	assert.JSONEq(t, listProductsUnchanged, string(out))
	assert.NotContains(t, string(out), "products")
}

// The struct has no MarshalJSON, which an embedding type would promote and
// lose its own fields to. The empty-vs-nil difference is in the field's tag, so
// a type that embeds the response keeps both its own fields and products: [].
func TestListProductsResponseEmbeds(t *testing.T) {
	type wrapped struct {
		ListProductsResponse
		Extra string `json:"extra"`
	}
	assert.False(t, reflect.TypeFor[ListProductsResponse]().Implements(reflect.TypeFor[json.Marshaler]()))

	out, err := json.Marshal(wrapped{
		ListProductsResponse: ListProductsResponse{
			Outcome:     ListProductsResponseOutcomeListed,
			Products:    []CanonicalProduct{},
			FeedVersion: "feed-7",
			CacheScope:  "account",
		},
		Extra: "kept",
	})
	require.NoError(t, err)
	assert.JSONEq(t, `{"outcome":"listed","products":[],"feed_version":"feed-7","cache_scope":"account","extra":"kept"}`, string(out))

	out, err = json.Marshal(wrapped{
		ListProductsResponse: ListProductsResponse{
			Outcome: ListProductsResponseOutcomeUnchanged, FeedVersion: "feed-7", CacheScope: "public",
		},
		Extra: "kept",
	})
	require.NoError(t, err)
	assert.JSONEq(t, `{"outcome":"unchanged","feed_version":"feed-7","cache_scope":"public","extra":"kept"}`, string(out))
}

func TestListProductsResponseOutcomeIsATypedEnum(t *testing.T) {
	o := ListProductsResponseOutcomeListed
	assert.Equal(t, "listed", o)
	// The constants are the enum's type, so a function taking it accepts them.
	describe := func(ListProductsResponseOutcome) {}
	describe(o)
	assert.Equal(t, "unchanged", ListProductsResponseOutcomeUnchanged)
	assert.Equal(t,
		[]ListProductsResponseOutcome{ListProductsResponseOutcomeListed, ListProductsResponseOutcomeUnchanged},
		KnownListProductsResponseOutcomeValues(),
	)
	assert.True(t, IsKnownListProductsResponseOutcome("listed"))
	assert.False(t, IsKnownListProductsResponseOutcome("unlisted"))
	_, err := ParseListProductsResponseOutcome("unlisted")
	assert.Error(t, err)

	// The discriminator is required, so it is never dropped: the zero value
	// encodes as an empty outcome, which Validate reports.
	out, err := json.Marshal(ListProductsResponse{})
	require.NoError(t, err)
	assert.Contains(t, string(out), `"outcome":""`)
}

func TestListProductsResponseValidate(t *testing.T) {
	products := []CanonicalProduct{{ProductID: "prod_1", Name: "Homepage takeover"}}
	listed := func() ListProductsResponse {
		return ListProductsResponse{
			Outcome: ListProductsResponseOutcomeListed, Products: products,
			FeedVersion: "feed-7", CacheScope: "public",
		}
	}
	unchanged := func() ListProductsResponse {
		return ListProductsResponse{
			Outcome: ListProductsResponseOutcomeUnchanged, FeedVersion: "feed-7", CacheScope: "public",
		}
	}
	type issue struct{ field, code string }

	cases := []struct {
		name   string
		resp   ListProductsResponse
		strict bool
		want   []issue
		// schemaValid is what the schema says about the same value on the wire.
		schemaValid bool
		// skipSchema marks a case whose wire form the schema judges differently
		// on purpose. An empty feed_version: the schema only requires the key
		// (no minLength), Go cannot tell "" from absent, and an empty opaque
		// version is no version, so Validate counts it as missing. A cache_scope
		// outside the enum: only WithStrictEnums reports it.
		skipSchema bool
	}{
		{name: "listed", resp: listed(), schemaValid: true},
		{name: "listed with an empty list", resp: func() ListProductsResponse {
			r := listed()
			r.Products = []CanonicalProduct{}
			return r
		}(), schemaValid: true},
		{name: "unchanged", resp: unchanged(), schemaValid: true},
		{name: "listed without products", resp: func() ListProductsResponse {
			r := listed()
			r.Products = nil
			return r
		}(), want: []issue{{"products", "REQUIRED_FIELD"}}},
		{name: "unchanged with products", resp: func() ListProductsResponse {
			r := unchanged()
			r.Products = products
			return r
		}(), want: []issue{{"products", "UNSUPPORTED_FIELD"}}},
		{name: "unchanged with an empty products list", resp: func() ListProductsResponse {
			r := unchanged()
			r.Products = []CanonicalProduct{}
			return r
		}(), want: []issue{{"products", "UNSUPPORTED_FIELD"}}},
		{name: "listed without feed_version", resp: func() ListProductsResponse {
			r := listed()
			r.FeedVersion = ""
			return r
		}(), want: []issue{{"feed_version", "REQUIRED_FIELD"}}, skipSchema: true},
		{name: "listed without cache_scope", resp: func() ListProductsResponse {
			r := listed()
			r.CacheScope = ""
			return r
		}(), want: []issue{{"cache_scope", "REQUIRED_FIELD"}}},
		{name: "unchanged without feed_version", resp: func() ListProductsResponse {
			r := unchanged()
			r.FeedVersion = ""
			return r
		}(), want: []issue{{"feed_version", "REQUIRED_FIELD"}}, skipSchema: true},
		{name: "unchanged without cache_scope", resp: func() ListProductsResponse {
			r := unchanged()
			r.CacheScope = ""
			return r
		}(), want: []issue{{"cache_scope", "REQUIRED_FIELD"}}},
		{name: "every problem at once", resp: ListProductsResponse{
			Outcome: ListProductsResponseOutcomeListed,
		}, want: []issue{
			{"feed_version", "REQUIRED_FIELD"},
			{"cache_scope", "REQUIRED_FIELD"},
			{"products", "REQUIRED_FIELD"},
		}},
		{name: "no outcome", resp: ListProductsResponse{FeedVersion: "feed-7", CacheScope: "public"},
			want: []issue{{"outcome", "REQUIRED_FIELD"}}},
		{name: "unknown cache_scope is accepted by default", skipSchema: true, resp: func() ListProductsResponse {
			r := listed()
			r.CacheScope = "private"
			return r
		}()},
		{name: "unknown cache_scope under strict enums", strict: true, resp: func() ListProductsResponse {
			r := listed()
			r.CacheScope = "private"
			return r
		}(), want: []issue{{"cache_scope", "UNKNOWN_ENUM_VALUE"}}},
		{name: "known cache_scope under strict enums", strict: true, resp: func() ListProductsResponse {
			r := listed()
			r.CacheScope = "account"
			return r
		}(), schemaValid: true},
		{name: "unknown outcome is accepted by default", resp: func() ListProductsResponse {
			r := unchanged()
			r.Outcome = "partial"
			return r
		}()},
		{name: "unknown outcome under strict enums", strict: true, resp: func() ListProductsResponse {
			r := unchanged()
			r.Outcome = "partial"
			return r
		}(), want: []issue{{"outcome", "UNKNOWN_VARIANT"}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var opts []ValidationOption
			if tc.strict {
				opts = append(opts, WithStrictEnums())
			}
			got := tc.resp.Validate(opts...)
			var gotIssues []issue
			for _, i := range got {
				gotIssues = append(gotIssues, issue{i.Field, i.Code})
			}
			assert.Equal(t, tc.want, gotIssues)

			// Validate and the schema agree on every value whose wire form the
			// schema can judge: the unknown outcome is the schema's to reject
			// (it is not in the enum), the SDK's to tolerate.
			if tc.resp.Outcome != "partial" && !tc.skipSchema {
				wire, err := json.Marshal(tc.resp)
				require.NoError(t, err)
				err = validateAgainstSchema(listProductsResponseSchema, wire)
				if errors.Is(err, errSchemaBundleMissing) {
					return
				}
				if tc.schemaValid {
					assert.NoError(t, err, "wire form: %s", wire)
				} else {
					assert.Error(t, err, "Validate reports it, so the schema must reject it: %s", wire)
				}
			}
		})
	}
}

// The same issues, from a response decoded off the wire.
func TestListProductsResponseValidateOnDecodedJSON(t *testing.T) {
	for raw, want := range map[string][]string{
		`{"outcome":"listed","feed_version":"f","cache_scope":"public"}`:                                               {"products"},
		`{"outcome":"unchanged","products":[],"feed_version":"f","cache_scope":"public"}`:                              {"products"},
		`{"outcome":"unchanged","products":[{"product_id":"p","name":"n"}],"feed_version":"f","cache_scope":"public"}`: {"products"},
		`{"outcome":"listed","products":[],"cache_scope":"public"}`:                                                    {"feed_version"},
		`{"outcome":"listed","products":[],"feed_version":"f"}`:                                                        {"cache_scope"},
	} {
		var resp ListProductsResponse
		require.NoError(t, json.Unmarshal([]byte(raw), &resp))
		var fields []string
		for _, i := range resp.Validate() {
			fields = append(fields, i.Field)
		}
		assert.True(t, slices.Equal(want, fields), "%s: got %v, want %v", raw, fields, want)
		if err := validateAgainstSchema(listProductsResponseSchema, []byte(raw)); !errors.Is(err, errSchemaBundleMissing) {
			assert.Error(t, err, "the schema rejects it too: %s", raw)
		}
	}
}
