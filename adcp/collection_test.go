package adcp

import (
	"encoding/json"
	"testing"
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
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var m map[string]any
	json.Unmarshal(b, &m)

	if m["selection_type"] != "distribution_ids" {
		t.Fatalf("expected selection_type=distribution_ids, got %v", m["selection_type"])
	}
	ids, ok := m["identifiers"].([]any)
	if !ok || len(ids) != 2 {
		t.Fatalf("expected 2 identifiers, got %v", m["identifiers"])
	}
	if _, ok := m["publisher_domain"]; ok {
		t.Fatal("distribution_ids source should not have publisher_domain")
	}
	if _, ok := m["collection_ids"]; ok {
		t.Fatal("distribution_ids source should not have collection_ids")
	}
	if _, ok := m["genres"]; ok {
		t.Fatal("distribution_ids source should not have genres")
	}
}

func TestByPublisherCollectionsJSON(t *testing.T) {
	source := ByPublisherCollections("hulu.com", []string{"comedy-originals", "drama-catalog"})
	b, _ := json.Marshal(source)
	var m map[string]any
	json.Unmarshal(b, &m)

	if m["selection_type"] != "publisher_collections" {
		t.Fatalf("expected selection_type=publisher_collections, got %v", m["selection_type"])
	}
	if m["publisher_domain"] != "hulu.com" {
		t.Fatalf("expected publisher_domain=hulu.com, got %v", m["publisher_domain"])
	}
	if _, ok := m["identifiers"]; ok {
		t.Fatal("publisher_collections source should not have identifiers")
	}
}

func TestByPublisherGenresJSON(t *testing.T) {
	source := ByPublisherGenres("roku.com", []string{"Comedy", "Drama"}, "iab_content_3.0")
	b, _ := json.Marshal(source)
	var m map[string]any
	json.Unmarshal(b, &m)

	if m["selection_type"] != "publisher_genres" {
		t.Fatalf("expected selection_type=publisher_genres, got %v", m["selection_type"])
	}
	if m["genre_taxonomy"] != "iab_content_3.0" {
		t.Fatalf("expected genre_taxonomy=iab_content_3.0, got %v", m["genre_taxonomy"])
	}
	if _, ok := m["identifiers"]; ok {
		t.Fatal("publisher_genres source should not have identifiers")
	}
	if _, ok := m["collection_ids"]; ok {
		t.Fatal("publisher_genres source should not have collection_ids")
	}
}

// --- Generated type pointer semantics ---
// Verifies the generator produces *bool for optional booleans so callers
// can distinguish "absent" from "false".

func TestGetCollectionListRequestResolveDefault(t *testing.T) {
	raw := `{"list_id": "list-1"}`
	var input GetCollectionListRequest
	if err := json.Unmarshal([]byte(raw), &input); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if input.Resolve != nil {
		t.Fatal("expected resolve to be nil when absent")
	}

	raw = `{"list_id": "list-1", "resolve": false}`
	if err := json.Unmarshal([]byte(raw), &input); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if input.Resolve == nil || *input.Resolve != false {
		t.Fatal("expected resolve=false")
	}
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
	if err := json.Unmarshal([]byte(raw), &input); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if input.Brand.Domain != "novamotors.com" {
		t.Fatal("expected brand.domain=novamotors.com")
	}
	if len(input.Packages) != 1 {
		t.Fatalf("expected 1 package, got %d", len(input.Packages))
	}
	pkg := input.Packages[0]
	if pkg.MeasurementTerms == nil {
		t.Fatal("expected measurement_terms on package")
	}
	if pkg.MeasurementTerms.BillingMeasurement.MeasurementWindow != "c7" {
		t.Fatalf("expected measurement_window=c7, got %s", pkg.MeasurementTerms.BillingMeasurement.MeasurementWindow)
	}
}

// --- Empty slice serialization ---
// Verifies [] not null — a recurring bug pattern in ad tech APIs.

func TestListCollectionListsResponseEmpty(t *testing.T) {
	_, out, _ := ListCollectionListsResponse([]CollectionList{}, nil)
	m := out.(map[string]any)
	lists, ok := m["lists"].([]CollectionList)
	if !ok {
		t.Fatalf("expected []CollectionList, got %T", m["lists"])
	}
	if lists == nil {
		t.Fatal("expected empty slice, not nil")
	}
}

// --- Schema generation ---
// Verifies permissiveSchemaFor produces useful schemas for generated types.

func TestCollectionRequestSchemaGeneration(t *testing.T) {
	schema := permissiveSchemaFor[CreateCollectionListRequest]()
	if schema.Type != "object" {
		t.Fatalf("expected type=object, got %s", schema.Type)
	}
	if _, ok := schema.Properties["name"]; !ok {
		t.Fatal("expected 'name' property in schema")
	}
	if _, ok := schema.Properties["base_collections"]; !ok {
		t.Fatal("expected 'base_collections' property in schema")
	}
	if schema.AdditionalProperties != nil {
		t.Fatal("expected AdditionalProperties to be nil (permissive)")
	}
}
