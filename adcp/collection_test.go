package adcp

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- Discriminated union constructor tests ---
// These verify custom logic: constructors must set selection_type and
// must NOT leak fields from other variants (omitempty on zero values).

func TestByDistributionIDsJSON(t *testing.T) {
	source := ByDistributionIDs([]DistributionID{
		{Type: "imdb_id", Value: "tt1234567"},
		{Type: "gracenote_id", Value: "SH01234"},
	})
	b, err := json.Marshal(source)
	require.NoError(t, err, "marshal")
	var m map[string]any
	require.NoError(t, json.Unmarshal(b, &m))

	require.Equal(t, "distribution_ids", m["selection_type"])
	ids, ok := m["identifiers"].([]any)
	require.True(t, ok, "expected identifiers to be []any")
	require.Len(t, ids, 2)
	assert.NotContains(t, m, "publisher_domain", "distribution_ids source should not have publisher_domain")
	assert.NotContains(t, m, "collection_ids", "distribution_ids source should not have collection_ids")
	assert.NotContains(t, m, "genres", "distribution_ids source should not have genres")
}

func TestByPublisherCollectionsJSON(t *testing.T) {
	source := ByPublisherCollections("hulu.com", []string{"comedy-originals", "drama-catalog"})
	b, err := json.Marshal(source)
	require.NoError(t, err)
	var m map[string]any
	require.NoError(t, json.Unmarshal(b, &m))

	require.Equal(t, "publisher_collections", m["selection_type"])
	require.Equal(t, "hulu.com", m["publisher_domain"])
	assert.NotContains(t, m, "identifiers", "publisher_collections source should not have identifiers")
}

func TestByPublisherGenresJSON(t *testing.T) {
	source := ByPublisherGenres("roku.com", []string{"Comedy", "Drama"}, "iab_content_3.0")
	b, err := json.Marshal(source)
	require.NoError(t, err)
	var m map[string]any
	require.NoError(t, json.Unmarshal(b, &m))

	require.Equal(t, "publisher_genres", m["selection_type"])
	require.Equal(t, "iab_content_3.0", m["genre_taxonomy"])
	assert.NotContains(t, m, "identifiers", "publisher_genres source should not have identifiers")
	assert.NotContains(t, m, "collection_ids", "publisher_genres source should not have collection_ids")
}

// --- Generated type pointer semantics ---
// Verifies the generator produces *bool for optional booleans so callers
// can distinguish "absent" from "false".

func TestGetCollectionListRequestResolveDefault(t *testing.T) {
	raw := `{"list_id": "list-1"}`
	var input GetCollectionListRequest
	require.NoError(t, json.Unmarshal([]byte(raw), &input), "unmarshal")
	require.Nil(t, input.Resolve, "expected resolve to be nil when absent")

	raw = `{"list_id": "list-1", "resolve": false}`
	require.NoError(t, json.Unmarshal([]byte(raw), &input), "unmarshal")
	require.NotNil(t, input.Resolve)
	require.Equal(t, false, *input.Resolve, "expected resolve=false")
}

// --- Storyboard wire format ---
// Verifies the broadcast storyboard's create_media_buy payload deserializes
// into the generated types. This is a cross-system compat test, not a json test.

func TestBroadcastCreateMediaBuyWireFormat(t *testing.T) {
	raw := `{
		"account": {"brand": {"domain": "novamotors.com"}, "operator": "pinnacle-agency.com"},
		"brand": {"domain": "novamotors.com"},
		"agency_estimate_number": "PNNL-NM-2026-Q4-0847",
		"start_time": "2026-10-01T00:00:00Z",
		"end_time": "2026-12-31T23:59:59Z",
		"packages": [
			{
				"product_id": "primetime_30s_mf",
				"budget": 280000,
				"pricing_option_id": "unit_primetime_30",
				"measurement_terms": {
					"billing_measurement": {
						"vendor": {"domain": "videoamp.com"},
						"measurement_window": "c7",
						"max_variance_percent": 10
					}
				}
			}
		]
	}`
	var input CreateMediaBuyRequest
	require.NoError(t, json.Unmarshal([]byte(raw), &input), "unmarshal")
	require.Equal(t, "novamotors.com", input.Brand.Domain)
	require.Len(t, input.Packages, 1)
	pkg := input.Packages[0]
	require.NotNil(t, pkg.MeasurementTerms, "expected measurement_terms on package")
	require.Equal(t, "c7", pkg.MeasurementTerms.BillingMeasurement.MeasurementWindow)
}

// --- Empty slice serialization ---
// Verifies [] not null — a recurring bug pattern in ad tech APIs.

func TestListCollectionListsResponseEmpty(t *testing.T) {
	_, out, _ := ListCollectionListsResponse([]CollectionList{}, nil)
	m := out.(map[string]any)
	lists, ok := m["lists"].([]CollectionList)
	require.True(t, ok, "expected []CollectionList, got %T", m["lists"])
	require.NotNil(t, lists, "expected empty slice, not nil")
}

// --- Schema generation ---
// Verifies permissiveSchemaFor produces useful schemas for generated types.

func TestCollectionRequestSchemaGeneration(t *testing.T) {
	schema := permissiveSchemaFor[CreateCollectionListRequest]()
	require.Equal(t, "object", schema.Type)
	assert.Contains(t, schema.Properties, "name", "expected 'name' property in schema")
	assert.Contains(t, schema.Properties, "base_collections", "expected 'base_collections' property in schema")
	assert.Nil(t, schema.AdditionalProperties, "expected AdditionalProperties to be nil (permissive)")
}
