package adcp

import (
	"encoding/json"
	"testing"
)

// --- Discriminated union constructor tests ---

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
	// Must not leak fields from other variants
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
	ids, ok := m["collection_ids"].([]any)
	if !ok || len(ids) != 2 {
		t.Fatalf("expected 2 collection_ids, got %v", m["collection_ids"])
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
	if m["publisher_domain"] != "roku.com" {
		t.Fatalf("expected publisher_domain=roku.com, got %v", m["publisher_domain"])
	}
	if m["genre_taxonomy"] != "iab_content_3.0" {
		t.Fatalf("expected genre_taxonomy=iab_content_3.0, got %v", m["genre_taxonomy"])
	}
	genres, ok := m["genres"].([]any)
	if !ok || len(genres) != 2 {
		t.Fatalf("expected 2 genres, got %v", m["genres"])
	}
	if _, ok := m["identifiers"]; ok {
		t.Fatal("publisher_genres source should not have identifiers")
	}
	if _, ok := m["collection_ids"]; ok {
		t.Fatal("publisher_genres source should not have collection_ids")
	}
}

// --- Round-trip deserialization tests ---

func TestCreateCollectionListRequestRoundTrip(t *testing.T) {
	raw := `{
		"name": "Premium Shows",
		"base_collections": [
			{"selection_type": "distribution_ids", "identifiers": [{"type": "imdb_id", "value": "tt1234"}]},
			{"selection_type": "publisher_genres", "publisher_domain": "hulu.com", "genres": ["comedy"], "genre_taxonomy": "iab_content_3.0"}
		],
		"filters": {"content_ratings_exclude": [{"system": "us_tv", "rating": "TV-MA"}]},
		"adcp_major_version": 3
	}`
	var input CreateCollectionListRequest
	if err := json.Unmarshal([]byte(raw), &input); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if input.Name != "Premium Shows" {
		t.Fatalf("expected name=Premium Shows, got %s", input.Name)
	}
	if len(input.BaseCollections) != 2 {
		t.Fatalf("expected 2 base_collections, got %d", len(input.BaseCollections))
	}
	if input.BaseCollections[0].SelectionType != "distribution_ids" {
		t.Fatal("first source should be distribution_ids")
	}
	if input.BaseCollections[1].SelectionType != "publisher_genres" {
		t.Fatal("second source should be publisher_genres")
	}
	if input.Filters == nil {
		t.Fatal("expected filters to be set")
	}
	if len(input.Filters.ContentRatingsExclude) != 1 {
		t.Fatalf("expected 1 content_ratings_exclude, got %d", len(input.Filters.ContentRatingsExclude))
	}
	if input.Filters.ContentRatingsExclude[0].System != "us_tv" {
		t.Fatalf("expected system=us_tv, got %s", input.Filters.ContentRatingsExclude[0].System)
	}
}

func TestGetCollectionListRequestResolveDefault(t *testing.T) {
	// When resolve is absent, it should be nil (server defaults to true)
	raw := `{"list_id": "list-1"}`
	var input GetCollectionListRequest
	if err := json.Unmarshal([]byte(raw), &input); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if input.ListID != "list-1" {
		t.Fatalf("expected list_id=list-1, got %s", input.ListID)
	}
	if input.Resolve != nil {
		t.Fatal("expected resolve to be nil when absent")
	}

	// When resolve is explicitly false
	raw = `{"list_id": "list-1", "resolve": false}`
	if err := json.Unmarshal([]byte(raw), &input); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if input.Resolve == nil || *input.Resolve != false {
		t.Fatal("expected resolve=false")
	}
}

func TestUpdateCollectionListRequestRoundTrip(t *testing.T) {
	raw := `{
		"list_id": "list-42",
		"name": "Updated Name",
		"base_collections": [
			{"selection_type": "publisher_collections", "publisher_domain": "nbc.com", "collection_ids": ["today-show"]}
		],
		"webhook_url": "https://example.com/webhook"
	}`
	var input UpdateCollectionListRequest
	if err := json.Unmarshal([]byte(raw), &input); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if input.ListID != "list-42" {
		t.Fatalf("expected list_id=list-42, got %s", input.ListID)
	}
	if input.Name != "Updated Name" {
		t.Fatalf("expected name=Updated Name, got %s", input.Name)
	}
	if len(input.BaseCollections) != 1 {
		t.Fatalf("expected 1 base_collection, got %d", len(input.BaseCollections))
	}
	if input.WebhookURL != "https://example.com/webhook" {
		t.Fatalf("expected webhook_url, got %s", input.WebhookURL)
	}
}

// --- Response builder tests ---

func TestCreateCollectionListResponseFields(t *testing.T) {
	list := &CollectionList{ListID: "list-1", Name: "Test List"}
	result, out, err := CreateCollectionListResponse(list, "token-abc")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result == nil {
		t.Fatal("expected non-nil result")
	}
	if result.StructuredContent == nil {
		t.Fatal("expected StructuredContent to be set")
	}

	m, ok := out.(map[string]any)
	if !ok {
		t.Fatalf("expected map[string]any, got %T", out)
	}
	if m["auth_token"] != "token-abc" {
		t.Fatalf("expected auth_token=token-abc, got %v", m["auth_token"])
	}
	if m["list"] == nil {
		t.Fatal("expected list to be set")
	}
}

func TestGetCollectionListResponseWithCollections(t *testing.T) {
	list := &CollectionList{ListID: "list-1", Name: "Shows"}
	collections := []ResolvedCollection{
		{Name: "Breaking Bad", Kind: "series"},
		{Name: "The Office", Kind: "series"},
	}
	pagination := &PaginationResponse{HasMore: true, Cursor: "cursor-2"}
	result, out, err := GetCollectionListResponse(list, collections, pagination)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result == nil {
		t.Fatal("expected non-nil result")
	}

	m := out.(map[string]any)
	colls, ok := m["collections"].([]ResolvedCollection)
	if !ok {
		t.Fatalf("expected []ResolvedCollection, got %T", m["collections"])
	}
	if len(colls) != 2 {
		t.Fatalf("expected 2 collections, got %d", len(colls))
	}
	if m["pagination"] == nil {
		t.Fatal("expected pagination to be set")
	}
}

func TestGetCollectionListResponseMetadataOnly(t *testing.T) {
	list := &CollectionList{ListID: "list-1", Name: "Shows"}
	_, out, _ := GetCollectionListResponse(list, nil, nil)

	m := out.(map[string]any)
	if _, ok := m["collections"]; ok {
		t.Fatal("expected no collections key when nil")
	}
	if _, ok := m["pagination"]; ok {
		t.Fatal("expected no pagination key when nil")
	}
}

func TestDeleteCollectionListResponseFields(t *testing.T) {
	_, out, _ := DeleteCollectionListResponse("list-99")
	m := out.(map[string]any)
	if m["deleted"] != true {
		t.Fatalf("expected deleted=true, got %v", m["deleted"])
	}
	if m["list_id"] != "list-99" {
		t.Fatalf("expected list_id=list-99, got %v", m["list_id"])
	}
}

func TestListCollectionListsResponseEmpty(t *testing.T) {
	// Empty list must produce {"lists": [...]}, not {"lists": null}
	_, out, _ := ListCollectionListsResponse([]CollectionList{}, nil)
	m := out.(map[string]any)
	lists, ok := m["lists"].([]CollectionList)
	if !ok {
		t.Fatalf("expected []CollectionList, got %T", m["lists"])
	}
	if lists == nil {
		t.Fatal("expected empty slice, not nil")
	}
	if len(lists) != 0 {
		t.Fatalf("expected 0 lists, got %d", len(lists))
	}
}

func TestListCollectionListsResponseWithPagination(t *testing.T) {
	lists := []CollectionList{
		{ListID: "list-1", Name: "Shows"},
		{ListID: "list-2", Name: "Podcasts"},
	}
	pagination := &PaginationResponse{HasMore: false, TotalCount: 2}
	_, out, _ := ListCollectionListsResponse(lists, pagination)

	m := out.(map[string]any)
	if m["pagination"] == nil {
		t.Fatal("expected pagination to be set")
	}
}

// --- Schema generation tests ---

func TestCollectionInputSchemaGeneration(t *testing.T) {
	schema := permissiveSchemaFor[CreateCollectionListRequest]()
	if schema.Type != "object" {
		t.Fatalf("expected type=object, got %s", schema.Type)
	}
	if schema.Properties == nil {
		t.Fatal("expected properties to be set")
	}
	if _, ok := schema.Properties["name"]; !ok {
		t.Fatal("expected 'name' property in schema")
	}
	if _, ok := schema.Properties["base_collections"]; !ok {
		t.Fatal("expected 'base_collections' property in schema")
	}
	// AdditionalProperties should be permissive
	if schema.AdditionalProperties != nil {
		t.Fatal("expected AdditionalProperties to be nil (permissive)")
	}
}

// --- Business terms type tests ---

func TestCancellationPolicyJSON(t *testing.T) {
	policy := CancellationPolicy{
		NoticePeriod:    Duration{Interval: 30, Unit: "days"},
		CancellationFee: CancellationFee{Type: "percent_remaining", Rate: 0.5},
	}
	b, err := json.Marshal(policy)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var m map[string]any
	json.Unmarshal(b, &m)

	np, ok := m["notice_period"].(map[string]any)
	if !ok {
		t.Fatal("expected notice_period to be object")
	}
	if np["interval"] != float64(30) {
		t.Fatalf("expected interval=30, got %v", np["interval"])
	}
	if np["unit"] != "days" {
		t.Fatalf("expected unit=days, got %v", np["unit"])
	}

	fee, ok := m["cancellation_fee"].(map[string]any)
	if !ok {
		t.Fatal("expected cancellation_fee to be object")
	}
	if fee["type"] != "percent_remaining" {
		t.Fatalf("expected type=percent_remaining, got %v", fee["type"])
	}
	if fee["rate"] != 0.5 {
		t.Fatalf("expected rate=0.5, got %v", fee["rate"])
	}
}

func TestPerformanceStandardJSON(t *testing.T) {
	ps := PerformanceStandard{
		Metric:    "viewability",
		Threshold: 0.70,
		Standard:  "mrc",
		Vendor:    &BrandReference{Domain: "doubleverify.com"},
	}
	b, _ := json.Marshal(ps)
	var m map[string]any
	json.Unmarshal(b, &m)

	if m["metric"] != "viewability" {
		t.Fatalf("expected metric=viewability, got %v", m["metric"])
	}
	if m["threshold"] != 0.70 {
		t.Fatalf("expected threshold=0.70, got %v", m["threshold"])
	}
	if m["standard"] != "mrc" {
		t.Fatalf("expected standard=mrc, got %v", m["standard"])
	}
	vendor, ok := m["vendor"].(map[string]any)
	if !ok {
		t.Fatal("expected vendor to be object")
	}
	if vendor["domain"] != "doubleverify.com" {
		t.Fatalf("expected vendor.domain=doubleverify.com, got %v", vendor["domain"])
	}
}

func TestProductWithBusinessTermsJSON(t *testing.T) {
	product := Product{
		ProductID:    "premium-ctv",
		Name:         "Premium CTV",
		Description:  "Connected TV inventory",
		DeliveryType: "guaranteed",
		PricingOptions: []PricingOption{
			{PricingOptionID: "cpm-1", PricingModel: "cpm", FixedPrice: 25.0, Currency: "USD"},
		},
		PublisherProperties: []string{},
		FormatIDs:           []FormatRef{{AgentURL: "http://localhost:3001/mcp", ID: "video-30s"}},
		CancellationPolicy: &CancellationPolicy{
			NoticePeriod:    Duration{Interval: 14, Unit: "days"},
			CancellationFee: CancellationFee{Type: "percent_remaining", Rate: 0.25},
		},
		MeasurementTerms: &MeasurementTerms{
			BillingMeasurement: &BillingMeasurement{
				Vendor:            &BrandReference{Domain: "nielsen.com"},
				MeasurementWindow: "c3",
			},
			MakegoodPolicy: &MakegoodPolicy{
				AvailableRemedies: []string{"additional_delivery", "credit"},
			},
		},
		PerformanceStandards: []PerformanceStandard{
			{Metric: "viewability", Threshold: 0.70, Standard: "mrc", Vendor: &BrandReference{Domain: "doubleverify.com"}},
			{Metric: "ivt", Threshold: 0.05, Vendor: &BrandReference{Domain: "doubleverify.com"}},
		},
	}

	b, err := json.Marshal(product)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	// Round-trip back
	var rt Product
	if err := json.Unmarshal(b, &rt); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if rt.CancellationPolicy == nil {
		t.Fatal("expected cancellation_policy to survive round-trip")
	}
	if rt.CancellationPolicy.NoticePeriod.Interval != 14 {
		t.Fatal("expected notice_period.interval=14")
	}
	if rt.MeasurementTerms == nil {
		t.Fatal("expected measurement_terms to survive round-trip")
	}
	if rt.MeasurementTerms.BillingMeasurement.MeasurementWindow != "c3" {
		t.Fatal("expected measurement_window=c3")
	}
	if len(rt.PerformanceStandards) != 2 {
		t.Fatalf("expected 2 performance_standards, got %d", len(rt.PerformanceStandards))
	}
}

func TestBroadcastPackageInputRoundTrip(t *testing.T) {
	// Matches the broadcast storyboard's create_media_buy sample_request
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
	if input.AgencyEstimateNumber != "PNNL-NM-2026-Q4-0847" {
		t.Fatalf("expected agency_estimate_number, got %s", input.AgencyEstimateNumber)
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
	if pkg.MeasurementTerms.BillingMeasurement.Vendor.Domain != "videoamp.com" {
		t.Fatal("expected vendor.domain=videoamp.com")
	}
}

func TestPackageWithPerformanceStandardsJSON(t *testing.T) {
	pkg := Package{
		PackageID:       "pkg-1",
		ProductID:       "primetime-30s",
		PricingOptionID: "unit-1",
		Budget:          100000,
		MeasurementTerms: &MeasurementTerms{
			BillingMeasurement: &BillingMeasurement{
				Vendor:            &BrandReference{Domain: "nielsen.com"},
				MeasurementWindow: "c7",
			},
		},
		PerformanceStandards: []PerformanceStandard{
			{Metric: "viewability", Threshold: 0.70, Standard: "mrc", Vendor: &BrandReference{Domain: "doubleverify.com"}},
		},
		AgencyEstimateNumber: "EST-123",
	}
	b, _ := json.Marshal(pkg)
	var rt Package
	if err := json.Unmarshal(b, &rt); err != nil {
		t.Fatalf("roundtrip: %v", err)
	}
	if rt.MeasurementTerms == nil || rt.MeasurementTerms.BillingMeasurement.MeasurementWindow != "c7" {
		t.Fatal("measurement_terms should survive round-trip")
	}
	if len(rt.PerformanceStandards) != 1 {
		t.Fatal("performance_standards should survive round-trip")
	}
	if rt.AgencyEstimateNumber != "EST-123" {
		t.Fatal("agency_estimate_number should survive round-trip")
	}
}

func TestMeasurementTermsJSON(t *testing.T) {
	terms := MeasurementTerms{
		BillingMeasurement: &BillingMeasurement{
			Vendor:             &BrandReference{Domain: "nielsen.com"},
			MaxVariancePercent: 10.0,
			MeasurementWindow:  "c3",
		},
		MakegoodPolicy: &MakegoodPolicy{
			AvailableRemedies: []string{"additional_delivery", "credit"},
		},
	}
	b, _ := json.Marshal(terms)
	var roundTripped MeasurementTerms
	if err := json.Unmarshal(b, &roundTripped); err != nil {
		t.Fatalf("roundtrip unmarshal: %v", err)
	}
	if roundTripped.BillingMeasurement.Vendor.Domain != "nielsen.com" {
		t.Fatal("expected vendor domain to survive round-trip")
	}
	if roundTripped.BillingMeasurement.MeasurementWindow != "c3" {
		t.Fatal("expected measurement_window=c3 to survive round-trip")
	}
	if len(roundTripped.MakegoodPolicy.AvailableRemedies) != 2 {
		t.Fatal("expected 2 available_remedies to survive round-trip")
	}
}
