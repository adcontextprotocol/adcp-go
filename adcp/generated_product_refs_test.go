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
		PricingOptions:      []PricingOption{{PricingOptionID: "pd-cpm", PricingModel: "cpm", FixedPrice: Ptr(15.0), Currency: "USD"}},
		Placements: []Placement{{
			PlacementID: "homepage",
			Name:        "Homepage",
			Kind:        "publisher_ref",
			Mode:        "targetable",
			FormatIDs:   []FormatRef{{AgentURL: "https://seller.example/mcp", ID: "display_300x250"}},
		}},
		Forecast: &DeliveryForecast{
			Points: []ForecastPoint{{
				Budget: Ptr(1000.0),
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
		ProductCard: &ProductCard{
			Title:      "Premium Display",
			PriceLabel: "From $15 CPM",
		},
		ProductCardDetailed: &ProductCardDetailed{
			Title:       "Premium Display",
			Description: "High-impact display placements.",
			Specifications: []ProductCardSpecification{{
				Label: "Format",
				Value: "Display",
			}},
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
		MetricOptimization: &ProductMetricOptimization{
			SupportedMetrics: []string{"clicks", "reach"},
			SupportedTargets: []string{"cost_per"},
		},
		ConversionTracking: &ProductConversionTracking{
			ActionSources:    []string{"website"},
			SupportedTargets: []string{"cost_per"},
			PlatformManaged:  Ptr(true),
		},
		CatalogMatch: &ProductCatalogMatch{
			MatchedIDs:     []string{"sku-1"},
			MatchedCount:   1,
			SubmittedCount: 10,
		},
		TrustedMatch: &ProductTrustedMatch{
			ContextMatch:  true,
			ResponseTypes: []string{"activation"},
			Providers: []ProductTrustedMatchProvider{{
				AgentURL:     "https://tmp.example/mcp",
				ContextMatch: Ptr(true),
			}},
		},
		MaterialSubmission: &ProductMaterialSubmission{
			URL:          "https://seller.example/materials",
			Instructions: "Upload print-ready PDF.",
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
		Installments: []Installment{{
			InstallmentID:   "ep-1",
			CollectionID:    "sports",
			Name:            "Finals Preview",
			ScheduledAt:     "2026-06-15T20:00:00Z",
			Status:          "scheduled",
			DurationSeconds: 1800,
			FlexibleEnd:     Ptr(false),
			ContentRating:   &ContentRating{System: "tvpg", Rating: "TV-G"},
			Topics:          []string{"basketball", "playoffs"},
			Special: &Special{
				Name:     "Championship Week",
				Category: "sports",
				Starts:   "2026-06-14T00:00:00Z",
			},
			GuestTalent: []Talent{{
				Role: "host",
				Name: "Avery Smith",
			}},
			AdInventory: &AdInventoryConfig{
				ExpectedBreaks:       3,
				TotalAdSeconds:       360,
				MaxAdDurationSeconds: 30,
				UnplannedBreaks:      Ptr(false),
				SupportedFormats:     []string{"video"},
			},
			Deadlines: &InstallmentDeadlines{
				BookingDeadline: "2026-06-10T00:00:00Z",
				MaterialDeadlines: []MaterialDeadline{{
					Stage: "final",
					DueAt: "2026-06-12T00:00:00Z",
					Label: "Approved video creative",
				}},
			},
			DerivativeOf: &InstallmentDerivative{
				InstallmentID: "ep-0",
				Type:          "recap",
			},
		}},
	}

	raw, err := json.Marshal(product)
	if err != nil {
		t.Fatalf("marshal product: %v", err)
	}
	body := string(raw)
	for _, want := range []string{
		`"placements":[{"kind":"publisher_ref","placement_id":"homepage","name":"Homepage","mode":"targetable"`,
		`"forecast":{"points":[{"budget":1000,"metrics":{"impressions":{"mid":100000}}}],"method":"historical","currency":"USD"}`,
		`"outcome_measurement":{"type":"brand_lift","attribution":"matched_market","reporting":"weekly"}`,
		`"delivery_measurement":{"provider":"MRC-accredited display measurement","notes":"50% in-view for 1s"}`,
		`"product_card":{"title":"Premium Display","price_label":"From $15 CPM"}`,
		`"product_card_detailed":{"title":"Premium Display","description":"High-impact display placements.","specifications":[{"label":"Format","value":"Display"}]}`,
		`"reporting_capabilities":{"available_reporting_frequencies":["daily"],"expected_delay_minutes":60`,
		`"creative_policy":{"co_branding":"optional","landing_page":"required","templates_available":true}`,
		`"measurement_readiness":{"status":"ready","required_event_types":["purchase"],"issues":[{"severity":"info","message":"purchase events are active"}]}`,
		`"metric_optimization":{"supported_metrics":["clicks","reach"],"supported_targets":["cost_per"]}`,
		`"conversion_tracking":{"action_sources":["website"],"supported_targets":["cost_per"],"platform_managed":true}`,
		`"catalog_match":{"matched_ids":["sku-1"],"matched_count":1,"submitted_count":10}`,
		`"trusted_match":{"context_match":true,"response_types":["activation"],"providers":[{"agent_url":"https://tmp.example/mcp","context_match":true}]}`,
		`"material_submission":{"url":"https://seller.example/materials","instructions":"Upload print-ready PDF."}`,
		`"data_provider_signals":[{"data_provider_domain":"signals.example","selection_type":"signal_ids","signal_ids":["auto_intenders"]}]`,
		`"collections":[{"publisher_domain":"example.com","collection_ids":["sports"]}]`,
		`"installments":[{"installment_id":"ep-1","collection_id":"sports","name":"Finals Preview","scheduled_at":"2026-06-15T20:00:00Z","status":"scheduled","duration_seconds":1800,"flexible_end":false`,
		`"special":{"name":"Championship Week","category":"sports","starts":"2026-06-14T00:00:00Z"}`,
		`"guest_talent":[{"role":"host","name":"Avery Smith"}]`,
		`"ad_inventory":{"expected_breaks":3,"total_ad_seconds":360,"max_ad_duration_seconds":30,"unplanned_breaks":false,"supported_formats":["video"]}`,
		`"deadlines":{"booking_deadline":"2026-06-10T00:00:00Z","material_deadlines":[{"stage":"final","due_at":"2026-06-12T00:00:00Z","label":"Approved video creative"}]}`,
		`"derivative_of":{"installment_id":"ep-0","type":"recap"}`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("marshaled product missing %s:\n%s", want, body)
		}
	}
}

func TestOptionalNumericPointersPreserveExplicitZero(t *testing.T) {
	cases := []struct {
		name  string
		v     any
		want  []string
		deny  []string
		check func(t *testing.T, raw []byte)
	}{
		{
			name: "pricing option nil fixed price",
			v:    PricingOption{PricingOptionID: "auction", PricingModel: "cpm", Currency: "USD"},
			deny: []string{`"fixed_price"`},
		},
		{
			name: "pricing option zero fixed price",
			v:    PricingOption{PricingOptionID: "fixed-zero", PricingModel: "cpm", Currency: "USD", FixedPrice: Ptr(0.0)},
			want: []string{`"fixed_price":0`},
			check: func(t *testing.T, raw []byte) {
				var got PricingOption
				requireJSONRoundTrip(t, raw, &got)
				if got.FixedPrice == nil || *got.FixedPrice != 0 {
					t.Fatalf("fixed_price = %v, want pointer to 0", got.FixedPrice)
				}
			},
		},
		{
			name: "forecast point nil budget",
			v:    ForecastPoint{Metrics: map[string]ForecastRange{"impressions": {Mid: 1000}}},
			deny: []string{`"budget"`},
		},
		{
			name: "forecast point zero budget",
			v:    ForecastPoint{Budget: Ptr(0.0), Metrics: map[string]ForecastRange{"impressions": {Mid: 1000}}},
			want: []string{`"budget":0`},
			check: func(t *testing.T, raw []byte) {
				var got ForecastPoint
				requireJSONRoundTrip(t, raw, &got)
				if got.Budget == nil || *got.Budget != 0 {
					t.Fatalf("budget = %v, want pointer to 0", got.Budget)
				}
			},
		},
		{
			name: "audience selector nil bounds",
			v: AudienceSelector{
				Type:      "signal",
				SignalID:  &SignalID{AgentURL: "https://signals.example/mcp", ID: "score"},
				ValueType: "numeric",
			},
			deny: []string{`"min_value"`, `"max_value"`},
		},
		{
			name: "audience selector zero bounds",
			v: AudienceSelector{
				Type:      "signal",
				SignalID:  &SignalID{AgentURL: "https://signals.example/mcp", ID: "score"},
				ValueType: "numeric",
				MinValue:  Ptr(0.0),
				MaxValue:  Ptr(0.0),
			},
			want: []string{`"min_value":0`, `"max_value":0`},
			check: func(t *testing.T, raw []byte) {
				var got AudienceSelector
				requireJSONRoundTrip(t, raw, &got)
				if got.MinValue == nil || *got.MinValue != 0 {
					t.Fatalf("min_value = %v, want pointer to 0", got.MinValue)
				}
				if got.MaxValue == nil || *got.MaxValue != 0 {
					t.Fatalf("max_value = %v, want pointer to 0", got.MaxValue)
				}
			},
		},
		{
			name: "creative asset nil weight",
			v: CreativeAsset{
				CreativeID: "creative-1",
				Name:       "Creative",
				FormatID:   &FormatRef{AgentURL: "https://seller.example/mcp", ID: "display"},
				Assets:     map[string]any{"image": "asset-1"},
			},
			deny: []string{`"weight"`},
		},
		{
			name: "creative asset zero weight",
			v: CreativeAsset{
				CreativeID: "creative-1",
				Name:       "Creative",
				FormatID:   &FormatRef{AgentURL: "https://seller.example/mcp", ID: "display"},
				Assets:     map[string]any{"image": "asset-1"},
				Weight:     Ptr(0.0),
			},
			want: []string{`"weight":0`},
			check: func(t *testing.T, raw []byte) {
				var got CreativeAsset
				requireJSONRoundTrip(t, raw, &got)
				if got.Weight == nil || *got.Weight != 0 {
					t.Fatalf("weight = %v, want pointer to 0", got.Weight)
				}
			},
		},
		{
			name: "keyword target nil bid price",
			v:    KeywordTarget{Keyword: "running shoes", MatchType: "phrase"},
			deny: []string{`"bid_price"`},
		},
		{
			name: "keyword target zero bid price",
			v:    KeywordTarget{Keyword: "running shoes", MatchType: "phrase", BidPrice: Ptr(0.0)},
			want: []string{`"bid_price":0`},
			check: func(t *testing.T, raw []byte) {
				var got KeywordTarget
				requireJSONRoundTrip(t, raw, &got)
				if got.BidPrice == nil || *got.BidPrice != 0 {
					t.Fatalf("bid_price = %v, want pointer to 0", got.BidPrice)
				}
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			raw, err := json.Marshal(tc.v)
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			body := string(raw)
			for _, want := range tc.want {
				if !strings.Contains(body, want) {
					t.Fatalf("marshaled payload missing %s:\n%s", want, body)
				}
			}
			for _, deny := range tc.deny {
				if strings.Contains(body, deny) {
					t.Fatalf("marshaled payload unexpectedly contained %s:\n%s", deny, body)
				}
			}
			if tc.check != nil {
				tc.check(t, raw)
			}
		})
	}
}

func requireJSONRoundTrip(t *testing.T, raw []byte, v any) {
	t.Helper()
	if err := json.Unmarshal(raw, v); err != nil {
		t.Fatalf("unmarshal %s: %v", raw, err)
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
			Adjustments: []PriceAdjustment{{Kind: "discount", Name: "volume", Amount: 5}},
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
	if !strings.Contains(string(raw), `"price_breakdown":{"list_price":20,"adjustments":[{"kind":"discount","name":"volume","amount":5}]}`) {
		t.Fatalf("price breakdown did not marshal as typed field: %s", raw)
	}
	if !strings.Contains(string(raw), `"cancellation":{"canceled_at":"2026-06-01T00:00:00Z","canceled_by":"buyer","reason":"budget_cut"}`) {
		t.Fatalf("package cancellation did not marshal as typed field: %s", raw)
	}
}
