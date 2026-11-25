package adcp

import (
	"encoding/json"
	"testing"
)

// TestProductDiscoveryCriteriaOfferFiltersRoundTrip covers adcp-go#573:
// criteria.offer_filters used to be generated as `any`, so the AdCP 3.2
// property_list filter (and every other offer filter) had no typed home.
// The whole criteria document must survive a marshal/unmarshal round trip
// with the offer filters intact, and both the criteria and the nested
// offer_filters document must validate against their schemas.
func TestProductDiscoveryCriteriaOfferFiltersRoundTrip(t *testing.T) {
	criteria := ProductDiscoveryCriteria{
		OfferFilters: &ProductOfferFilters{
			DeliveryType:        "guaranteed",
			IsFixedPrice:        Ptr(true),
			PricingCurrencies:   []string{"USD"},
			Countries:           []string{"US", "CA"},
			AvailabilityHorizon: &ProductOfferFilterAvailabilityHorizon{StartTime: "2027-01-01T00:00:00Z", EndTime: "2027-02-01T00:00:00Z"},
			Metros:              []ProductOfferFilterMetro{{System: "nielsen_dma", Code: "501"}},
			PropertyList:        &PropertyListRef{AgentURL: "https://lists.example/mcp", ListID: "pl-123"},
			TrustedMatch: &ProductOfferFilterTrustedMatch{
				Providers:     []ProductOfferFilterTrustedMatchProvider{{AgentURL: "https://tmp.example", ContextMatch: Ptr(true)}},
				ResponseTypes: []ResponseType{"activation"},
			},
			RequiredFeatures: &CanonicalMediaBuyFeatures{PropertyFiltering: Ptr(true)},
			RequiredPerformanceStandards: []ProductOfferFilterPerformanceStandard{
				{Metric: "viewability", Threshold: 0.7, Standard: "mrc", Vendor: BrandKey{Domain: "doubleverify.com"}},
			},
			RequiredVendorMetrics: []ProductOfferFilterVendorMetric{
				{Vendor: &BrandKey{Domain: "vendor.example"}, MetricID: "attention_score"},
			},
			AudienceEvidenceRequirements: &ProductAudienceEvidenceRequirements{
				RequirementMode:       "required",
				EvidencePresence:      "required",
				AcceptedMethodologies: []AudienceEvidenceMethodology{"observed"},
			},
		},
	}

	raw, err := json.Marshal(criteria)
	if err != nil {
		t.Fatalf("marshal criteria: %v", err)
	}
	assertSchemaValid(t, "media-buy/product-discovery-criteria.json", raw)

	var doc map[string]json.RawMessage
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("unmarshal criteria document: %v", err)
	}
	offerRaw, ok := doc["offer_filters"]
	if !ok {
		t.Fatalf("offer_filters missing from marshaled criteria: %s", raw)
	}
	assertSchemaValid(t, "core/product-offer-filters.json", offerRaw)

	var back ProductDiscoveryCriteria
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatalf("unmarshal criteria: %v", err)
	}
	filters := back.OfferFilters
	if filters == nil {
		t.Fatal("offer_filters lost on round trip")
	}
	if filters.PropertyList == nil || filters.PropertyList.ListID != "pl-123" {
		t.Errorf("property_list did not survive round trip: %+v", filters.PropertyList)
	}
	if len(filters.Metros) != 1 || filters.Metros[0].Code != "501" {
		t.Errorf("metros did not survive round trip: %+v", filters.Metros)
	}
	if filters.AvailabilityHorizon == nil || filters.AvailabilityHorizon.EndTime != "2027-02-01T00:00:00Z" {
		t.Errorf("availability_horizon did not survive round trip: %+v", filters.AvailabilityHorizon)
	}
	if filters.TrustedMatch == nil || len(filters.TrustedMatch.Providers) != 1 ||
		filters.TrustedMatch.Providers[0].AgentURL != "https://tmp.example" {
		t.Errorf("trusted_match did not survive round trip: %+v", filters.TrustedMatch)
	}
	if len(filters.RequiredPerformanceStandards) != 1 ||
		filters.RequiredPerformanceStandards[0].Vendor.Domain != "doubleverify.com" {
		t.Errorf("required_performance_standards did not survive round trip: %+v", filters.RequiredPerformanceStandards)
	}
	if len(filters.RequiredVendorMetrics) != 1 || filters.RequiredVendorMetrics[0].MetricID != "attention_score" {
		t.Errorf("required_vendor_metrics did not survive round trip: %+v", filters.RequiredVendorMetrics)
	}
	if filters.AudienceEvidenceRequirements == nil ||
		filters.AudienceEvidenceRequirements.RequirementMode != "required" {
		t.Errorf("audience_evidence_requirements did not survive round trip: %+v", filters.AudienceEvidenceRequirements)
	}
}
