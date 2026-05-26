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
		DeliveryMeasurement: &ProductDeliveryMeasurement{
			Provider: "MRC-accredited display measurement",
			Notes:    "50% in-view for 1s",
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
		CatalogMatch: &ProductCatalogMatch{
			MatchedIDs:     []string{"sku-1"},
			MatchedCount:   1,
			SubmittedCount: 10,
		},
		DataProviderSignals: []DataProviderSignalSelector{{
			DataProviderDomain: "signals.example",
			SelectionType:      "signal_ids",
			SignalIDs:          []string{"auto_intenders"},
		}},
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
		`"delivery_measurement":{"provider":"MRC-accredited display measurement","notes":"50% in-view for 1s"}`,
		`"reporting_capabilities":{"available_reporting_frequencies":["daily"],"expected_delay_minutes":60`,
		`"creative_policy":{"co_branding":"optional","landing_page":"required","templates_available":true}`,
		`"measurement_readiness":{"status":"ready","required_event_types":["purchase"],"issues":[{"severity":"info","message":"purchase events are active"}]}`,
		`"catalog_match":{"matched_ids":["sku-1"],"matched_count":1,"submitted_count":10}`,
		`"data_provider_signals":[{"data_provider_domain":"signals.example","selection_type":"signal_ids","signal_ids":["auto_intenders"]}]`,
		`"collections":[{"publisher_domain":"example.com","collection_ids":["sports"]}]`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("marshaled product missing %s:\n%s", want, body)
		}
	}
}

func TestGeneratedOneOfRefsMarshalFlattenedFields(t *testing.T) {
	req := GetSignalsRequest{
		SignalSpec: "auto intenders",
		Destinations: []Destination{{
			Type:     "platform",
			Platform: "the-trade-desk",
		}},
	}
	plan := PlannedDelivery{
		AudienceTargeting: []AudienceSelector{{
			Type:        "description",
			Description: "likely auto intenders",
		}},
	}

	rawReq, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("marshal get signals request: %v", err)
	}
	if !strings.Contains(string(rawReq), `"destinations":[{"type":"platform","platform":"the-trade-desk"}]`) {
		t.Fatalf("destinations did not marshal as typed field: %s", rawReq)
	}

	rawPlan, err := json.Marshal(plan)
	if err != nil {
		t.Fatalf("marshal planned delivery: %v", err)
	}
	if !strings.Contains(string(rawPlan), `"audience_targeting":[{"type":"description","description":"likely auto intenders"}]`) {
		t.Fatalf("audience targeting did not marshal as typed field: %s", rawPlan)
	}
}

func TestGeneratedPackagePriceBreakdown(t *testing.T) {
	canceled := true
	pkg := Package{
		PackageID: "pkg-1",
		PriceBreakdown: &PriceBreakdown{
			ListPrice:   20,
			Adjustments: []any{map[string]any{"type": "discount", "amount": 5}},
		},
		Canceled: Ptr(canceled),
		Cancellation: &PackageCancellation{
			CanceledAt: "2026-06-01T00:00:00Z",
			CanceledBy: "buyer",
			Reason:     "budget_cut",
		},
	}

	raw, err := json.Marshal(pkg)
	if err != nil {
		t.Fatalf("marshal package: %v", err)
	}
	if !strings.Contains(string(raw), `"price_breakdown":{"list_price":20,"adjustments":[{"amount":5,"type":"discount"}]}`) {
		t.Fatalf("price breakdown did not marshal as typed field: %s", raw)
	}
	if !strings.Contains(string(raw), `"cancellation":{"canceled_at":"2026-06-01T00:00:00Z","canceled_by":"buyer","reason":"budget_cut"}`) {
		t.Fatalf("package cancellation did not marshal as typed field: %s", raw)
	}
}
