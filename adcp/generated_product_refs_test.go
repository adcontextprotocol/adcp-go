package adcp

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestGeneratedProductRefsMarshalTypedFields(t *testing.T) {
	product := Product{
		ProductID:           "premium-display",
		Name:                "Premium Display",
		Description:         "High-impact display placements.",
		PublisherProperties: []PublisherPropertySelector{{PublisherDomain: "example.com", SelectionType: "all"}},
		FormatIDs:           []FormatRef{{AgentURL: "https://seller.example/mcp", ID: "display_300x250"}},
		DeliveryType:        "guaranteed",
		PricingOptions:      []PricingOption{{PricingOptionID: "pd-cpm", PricingModel: "cpm", FixedPrice: 15, Currency: "USD"}},
		Placements: []Placement{{
			PlacementID: "homepage",
			Name:        "Homepage",
			FormatIDs:   []FormatRef{{AgentURL: "https://seller.example/mcp", ID: "display_300x250"}},
		}},
		Forecast: &DeliveryForecast{
			Points: []ForecastPoint{{
				Budget: 1000,
				Metrics: map[string]ForecastRange{
					"impressions": {Mid: 100000},
				},
			}},
			Method:   "historical",
			Currency: "USD",
		},
		OutcomeMeasurement: &OutcomeMeasurement{
			Type:        "brand_lift",
			Attribution: "matched_market",
			Reporting:   "weekly",
		},
		ReportingCapabilities: ReportingCapabilities{
			AvailableReportingFrequencies: []string{"daily"},
			ExpectedDelayMinutes:          60,
			Timezone:                      "UTC",
			SupportsWebhooks:              true,
			AvailableMetrics:              []string{"impressions", "spend"},
			DateRangeSupport:              "date_range",
		},
		CreativePolicy: &CreativePolicy{
			CoBranding:         "optional",
			LandingPage:        "required",
			TemplatesAvailable: true,
		},
		MeasurementReadiness: &MeasurementReadiness{
			Status:             "ready",
			RequiredEventTypes: []string{"purchase"},
			Issues: []DiagnosticIssue{{
				Severity: "info",
				Message:  "purchase events are active",
			}},
		},
		Collections: []CollectionSelector{{
			PublisherDomain: "example.com",
			CollectionIDs:   []string{"sports"},
		}},
	}

	raw, err := json.Marshal(product)
	if err != nil {
		t.Fatalf("marshal product: %v", err)
	}
	body := string(raw)
	for _, want := range []string{
		`"placements":[{"placement_id":"homepage","name":"Homepage"`,
		`"forecast":{"points":[{"budget":1000,"metrics":{"impressions":{"mid":100000}}}],"method":"historical","currency":"USD"}`,
		`"outcome_measurement":{"type":"brand_lift","attribution":"matched_market","reporting":"weekly"}`,
		`"reporting_capabilities":{"available_reporting_frequencies":["daily"],"expected_delay_minutes":60`,
		`"creative_policy":{"co_branding":"optional","landing_page":"required","templates_available":true}`,
		`"measurement_readiness":{"status":"ready","required_event_types":["purchase"],"issues":[{"severity":"info","message":"purchase events are active"}]}`,
		`"collections":[{"publisher_domain":"example.com","collection_ids":["sports"]}]`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("marshaled product missing %s:\n%s", want, body)
		}
	}
}

func TestGeneratedPackagePriceBreakdown(t *testing.T) {
	pkg := Package{
		PackageID: "pkg-1",
		PriceBreakdown: &PriceBreakdown{
			ListPrice:   20,
			Adjustments: []any{map[string]any{"type": "discount", "amount": 5}},
		},
	}

	raw, err := json.Marshal(pkg)
	if err != nil {
		t.Fatalf("marshal package: %v", err)
	}
	if !strings.Contains(string(raw), `"price_breakdown":{"list_price":20,"adjustments":[{"amount":5,"type":"discount"}]}`) {
		t.Fatalf("price breakdown did not marshal as typed field: %s", raw)
	}
}
