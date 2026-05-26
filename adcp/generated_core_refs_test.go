package adcp

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestGeneratedCoreRefsMarshalTypedFields(t *testing.T) {
	req := CreateMediaBuyRequest{
		IdempotencyKey: "idem-1",
		Account:        AccountReference{AccountID: "acct-1"},
		Brand:          BrandReference{Domain: "brand.example"},
		StartTime:      "2026-06-01T00:00:00Z",
		EndTime:        "2026-06-30T00:00:00Z",
		InvoiceRecipient: &BusinessEntity{
			LegalName: "Acme Corporation",
			TaxID:     "12-3456789",
			Address: &BusinessAddress{
				Street:     "1 Market St",
				City:       "San Francisco",
				PostalCode: "94105",
				Country:    "US",
			},
			Contacts: []BusinessContact{{Role: "billing", Email: "ap@example.com"}},
			Bank:     &BankAccount{AccountHolder: "Acme Corporation", AccountNumber: "1234"},
		},
		PushNotificationConfig: &PushNotificationConfig{
			URL:            "https://buyer.example/webhooks/tasks",
			Authentication: &LegacyWebhookAuthentication{Schemes: []string{"Bearer"}, Credentials: "0123456789abcdef0123456789abcdef"},
		},
		ReportingWebhook: &ReportingWebhook{
			URL:                "https://buyer.example/webhooks/reporting",
			Authentication:     LegacyWebhookAuthentication{Schemes: []string{"Bearer"}, Credentials: "fedcba9876543210fedcba9876543210"},
			ReportingFrequency: "daily",
		},
	}

	raw, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	body := string(raw)
	for _, want := range []string{
		`"address":{"street":"1 Market St","city":"San Francisco","postal_code":"94105","country":"US"}`,
		`"contacts":[{"role":"billing","email":"ap@example.com"}]`,
		`"bank":{"account_holder":"Acme Corporation","account_number":"1234"}`,
		`"push_notification_config":{"url":"https://buyer.example/webhooks/tasks","authentication":{"schemes":["Bearer"],"credentials":"0123456789abcdef0123456789abcdef"}}`,
		`"reporting_webhook":{"url":"https://buyer.example/webhooks/reporting"`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("marshaled request missing %s:\n%s", want, body)
		}
	}

	var decoded CreateMediaBuyRequest
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("unmarshal request: %v", err)
	}
	if decoded.InvoiceRecipient == nil || decoded.InvoiceRecipient.LegalName != "Acme Corporation" {
		t.Fatalf("invoice recipient did not round-trip: %#v", decoded.InvoiceRecipient)
	}
	if decoded.PushNotificationConfig == nil || decoded.PushNotificationConfig.URL == "" {
		t.Fatalf("push notification config did not round-trip: %#v", decoded.PushNotificationConfig)
	}
	if decoded.ReportingWebhook == nil || decoded.ReportingWebhook.ReportingFrequency != "daily" {
		t.Fatalf("reporting webhook did not round-trip: %#v", decoded.ReportingWebhook)
	}
}

func TestGeneratedCoreRefsAcrossSurfacesMarshalTypedFields(t *testing.T) {
	cases := []struct {
		name  string
		value any
		want  []string
	}{
		{
			name: "get products property list",
			value: GetProductsRequest{
				PropertyList: Ptr(PropertyListRef{AgentURL: "https://lists.example/mcp", ListID: "pl-123"}),
				TimeBudget:   Ptr(Duration{Interval: 5, Unit: "minutes"}),
			},
			want: []string{
				`"property_list":{"agent_url":"https://lists.example/mcp","list_id":"pl-123"}`,
				`"time_budget":{"interval":5,"unit":"minutes"}`,
			},
		},
		{
			name: "targeting typed refs",
			value: Targeting{
				DaypartTargets: []DaypartTarget{{Days: []string{"mon", "tue"}, StartHour: 9, EndHour: 17}},
				FrequencyCap:   Ptr(FrequencyCap{MaxImpressions: 3, Per: "user", Window: Ptr(Duration{Interval: 7, Unit: "days"})}),
				PropertyList:   Ptr(PropertyListRef{AgentURL: "https://lists.example/mcp", ListID: "pl-456"}),
				GeoMetros:      []GeoMetroTarget{{System: "nielsen_dma", Values: []string{"501"}}},
				StoreCatchments: []TargetingStoreCatchment{{
					CatalogID:    "stores",
					StoreIDs:     []string{"store-1"},
					CatchmentIDs: []string{"drive"},
				}},
				AgeRestriction: Ptr(AgeRestriction{Min: 21}),
				KeywordTargets: []KeywordTarget{{Keyword: "running shoes", MatchType: "phrase"}},
			},
			want: []string{
				`"daypart_targets":[{"days":["mon","tue"],"start_hour":9,"end_hour":17}]`,
				`"frequency_cap":{"max_impressions":3,"per":"user","window":{"interval":7,"unit":"days"}}`,
				`"property_list":{"agent_url":"https://lists.example/mcp","list_id":"pl-456"}`,
				`"geo_metros":[{"system":"nielsen_dma","values":["501"]}]`,
				`"store_catchments":[{"catalog_id":"stores","store_ids":["store-1"],"catchment_ids":["drive"]}]`,
				`"age_restriction":{"min":21}`,
				`"keyword_targets":[{"keyword":"running shoes","match_type":"phrase"}]`,
			},
		},
		{
			name: "catalog mapping",
			value: Catalog{
				CatalogID:         "cat-1",
				Name:              "Catalog",
				FeedFieldMappings: []CatalogFieldMapping{{FeedField: "sku", CatalogField: "id"}},
			},
			want: []string{
				`"feed_field_mappings":[{"feed_field":"sku","catalog_field":"id"}]`,
			},
		},
		{
			name: "event attribution",
			value: Event{
				EventID:   "evt-1",
				EventType: "purchase",
				EventTime: "2026-06-01T00:00:00Z",
				UserMatch: Ptr(UserMatch{
					HashedEmail: "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
				}),
				CustomData: Ptr(EventCustomData{Value: 42, Currency: "USD"}),
			},
			want: []string{
				`"user_match":{"hashed_email":"0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"}`,
				`"custom_data":{"value":42,"currency":"USD"}`,
			},
		},
		{
			name: "signal range",
			value: Signal{
				ID:        "affinity_score",
				Name:      "Affinity Score",
				ValueType: "numeric",
				Range:     Ptr(SignalRange{Min: 0, Max: 100, Unit: "score"}),
			},
			want: []string{
				`"range":{"min":0,"max":100,"unit":"score"}`,
			},
		},
		{
			name: "delivery metrics",
			value: DeliveryTotals{
				QuartileData:   Ptr(DeliveryQuartileData{Q1Views: 10, Q4Views: 4}),
				ByEventType:    []DeliveryEventTypeMetrics{{EventType: "purchase", Count: 2, Value: 42}},
				ByActionSource: []DeliveryActionSourceMetrics{{ActionSource: "website", Count: 2}},
				DoohMetrics:    Ptr(DeliveryDOOHMetrics{LoopPlays: 5, VenueBreakdown: []DeliveryDOOHVenueBreakdown{{VenueID: "venue-1", Impressions: 100}}}),
				Viewability:    Ptr(DeliveryViewability{MeasurableImpressions: 100, ViewableImpressions: 80, ViewableRate: 0.8, Standard: "mrc"}),
			},
			want: []string{
				`"quartile_data":{"q1_views":10,"q4_views":4}`,
				`"by_event_type":[{"count":2,"event_type":"purchase","value":42}]`,
				`"dooh_metrics":{"loop_plays":5,"venue_breakdown":[{"impressions":100,"venue_id":"venue-1"}]}`,
				`"viewability":{"measurable_impressions":100,"standard":"mrc","viewable_impressions":80,"viewable_rate":0.8}`,
				`"by_action_source":[{"action_source":"website","count":2}]`,
			},
		},
		{
			name: "proposal total budget",
			value: CreateMediaBuyRequest{
				IdempotencyKey: "idem-budget",
				Account:        AccountReference{AccountID: "acct-1"},
				ProposalID:     "proposal-1",
				TotalBudget:    Ptr(MediaBuyBudget{Amount: 10000, Currency: "USD"}),
				Brand:          BrandReference{Domain: "brand.example"},
			},
			want: []string{
				`"total_budget":{"amount":10000,"currency":"USD"}`,
			},
		},
		{
			name: "creative format disclosures",
			value: CreativeFormat{
				FormatID: FormatRef{AgentURL: "https://seller.example/mcp", ID: "display_300x250"},
				Name:     "Display",
				FormatCard: &CreativeFormatCard{
					FormatID: FormatRef{AgentURL: "https://seller.example/mcp", ID: "format_card_standard"},
					Manifest: map[string]any{"title": "Display"},
				},
				FormatCardDetailed: &CreativeFormatCardDetailed{
					FormatID: FormatRef{AgentURL: "https://seller.example/mcp", ID: "format_card_detailed"},
					Manifest: map[string]any{"sections": []any{"assets"}},
				},
				DisclosureCapabilities: []CreativeFormatDisclosureCapability{{
					Position:    "overlay_top_left",
					Persistence: []string{"initial", "persistent"},
				}},
			},
			want: []string{
				`"format_card":{"format_id":{"agent_url":"https://seller.example/mcp","id":"format_card_standard"},"manifest":{"title":"Display"}}`,
				`"disclosure_capabilities":[{"position":"overlay_top_left","persistence":["initial","persistent"]}]`,
				`"format_card_detailed":{"format_id":{"agent_url":"https://seller.example/mcp","id":"format_card_detailed"},"manifest":{"sections":["assets"]}}`,
			},
		},
		{
			name: "creative asset inputs",
			value: CreativeAsset{
				CreativeID: "creative-1",
				Name:       "Creative",
				FormatID:   FormatRef{AgentURL: "https://seller.example/mcp", ID: "gen_display"},
				Assets:     map[string]any{"headline": "Sale"},
				Inputs: []CreativeAssetInput{{
					Name:               "default",
					Macros:             map[string]string{"CITY": "Honolulu"},
					ContextDescription: "Warm-weather retail offer",
				}},
				Provenance: &Provenance{
					DigitalSourceType: "trained_algorithmic_media",
					AITool:            &ProvenanceAITool{Name: "Creative model", Provider: "Seller"},
					DeclaredBy:        &ProvenanceDeclaredBy{Role: "platform", AgentURL: "https://seller.example/mcp"},
					C2PA:              &ProvenanceC2PA{ManifestURL: "https://seller.example/c2pa/creative-1"},
					Disclosure: &ProvenanceDisclosure{
						Required: true,
						Jurisdictions: []ProvenanceDisclosureJurisdiction{{
							Country:    "US",
							Regulation: "ca_sb_942",
							RenderGuidance: &ProvenanceDisclosureRenderGuidance{
								Persistence:   "initial",
								MinDurationMs: 3000,
								Positions:     []string{"overlay_top_left"},
							},
						}},
					},
					Verification: []ProvenanceVerification{{VerifiedBy: "Verifier", Result: "ai_generated", Confidence: 0.95}},
				},
			},
			want: []string{
				`"inputs":[{"name":"default","macros":{"CITY":"Honolulu"},"context_description":"Warm-weather retail offer"}]`,
				`"provenance":{"digital_source_type":"trained_algorithmic_media","ai_tool":{"name":"Creative model","provider":"Seller"}`,
				`"c2pa":{"manifest_url":"https://seller.example/c2pa/creative-1"}`,
				`"disclosure":{"required":true,"jurisdictions":[{"country":"US","regulation":"ca_sb_942","render_guidance":{"persistence":"initial","min_duration_ms":3000,"positions":["overlay_top_left"]}}]}`,
				`"verification":[{"verified_by":"Verifier","result":"ai_generated","confidence":0.95}]`,
			},
		},
		{
			name: "delivery package breakdowns",
			value: PackageDelivery{
				PackageID:    "pkg-1",
				Spend:        10,
				PricingModel: "cpm",
				Rate:         5,
				Currency:     "USD",
				ByCatalogItem: []PackageCatalogItemDelivery{{
					ContentID:     "sku-1",
					ContentIDType: "sku",
					Impressions:   100,
					Spend:         10,
				}},
				ByCreative: []PackageCreativeDelivery{{
					CreativeID:   "creative-1",
					Impressions:  100,
					Spend:        10,
					QuartileData: Ptr(DeliveryQuartileData{Q4Views: 9}),
				}},
				DailyBreakdown: []PackageDailyBreakdown{{Date: "2026-06-01", Impressions: 100, Spend: 10}},
			},
			want: []string{
				`"by_catalog_item":[{"impressions":100,"spend":10,"content_id":"sku-1","content_id_type":"sku"}]`,
				`"by_creative":[{"impressions":100,"spend":10,"quartile_data":{"q4_views":9},"creative_id":"creative-1"}]`,
				`"daily_breakdown":[{"date":"2026-06-01","impressions":100,"spend":10}]`,
			},
		},
		{
			name: "media buy daily breakdown",
			value: MediaBuyDelivery{
				MediaBuyID:     "mb-1",
				Totals:         MediaBuyDeliveryTotals{Impressions: 100, Spend: 10},
				DailyBreakdown: []MediaBuyDailyBreakdown{{Date: "2026-06-01", Impressions: 100, Spend: 10}},
			},
			want: []string{
				`"daily_breakdown":[{"date":"2026-06-01","impressions":100,"spend":10}]`,
			},
		},
		{
			name: "performance feedback measurement period",
			value: ProvidePerformanceFeedbackRequest{
				MediaBuyID:        "mb-1",
				IdempotencyKey:    "idem-1",
				MeasurementPeriod: DatetimeRange{Start: "2026-06-01T00:00:00Z", End: "2026-06-30T23:59:59Z"},
				PerformanceIndex:  1,
				MetricType:        "roas",
				FeedbackSource:    "buyer",
			},
			want: []string{
				`"measurement_period":{"start":"2026-06-01T00:00:00Z","end":"2026-06-30T23:59:59Z"}`,
			},
		},
		{
			name: "create success planned delivery",
			value: CreateMediaBuySuccess{
				MediaBuyID:       "mb-1",
				Packages:         []Package{},
				PlannedDelivery:  Ptr(PlannedDelivery{TotalBudget: 1000, Currency: "USD"}),
				InvoiceRecipient: Ptr(BusinessEntity{LegalName: "Acme Corporation"}),
			},
			want: []string{
				`"invoice_recipient":{"legal_name":"Acme Corporation"}`,
				`"planned_delivery":{"total_budget":1000,"currency":"USD"}`,
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			raw, err := json.Marshal(tc.value)
			if err != nil {
				t.Fatalf("marshal %s: %v", tc.name, err)
			}
			body := string(raw)
			for _, want := range tc.want {
				if !strings.Contains(body, want) {
					t.Fatalf("marshaled %s missing %s:\n%s", tc.name, want, body)
				}
			}
		})
	}
}

func TestGeneratedOptionalAllOfRefsOmitWhenNil(t *testing.T) {
	req := GetProductsRequest{}
	raw, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("marshal empty get products request: %v", err)
	}
	if strings.Contains(string(raw), "time_budget") {
		t.Fatalf("nil time_budget should be omitted: %s", raw)
	}

	req.TimeBudget = Ptr(Duration{Interval: 5, Unit: "minutes"})
	raw, err = json.Marshal(req)
	if err != nil {
		t.Fatalf("marshal get products request with time budget: %v", err)
	}
	if !strings.Contains(string(raw), `"time_budget":{"interval":5,"unit":"minutes"}`) {
		t.Fatalf("time_budget did not marshal as typed duration: %s", raw)
	}

	cap := FrequencyCap{}
	raw, err = json.Marshal(cap)
	if err != nil {
		t.Fatalf("marshal empty frequency cap: %v", err)
	}
	if strings.Contains(string(raw), "suppress") || strings.Contains(string(raw), "window") {
		t.Fatalf("nil allOf duration fields should be omitted: %s", raw)
	}

	cap.Suppress = Ptr(Duration{Interval: 1, Unit: "hours"})
	raw, err = json.Marshal(cap)
	if err != nil {
		t.Fatalf("marshal frequency cap with suppress: %v", err)
	}
	if !strings.Contains(string(raw), `"suppress":{"interval":1,"unit":"hours"}`) {
		t.Fatalf("suppress did not marshal as typed duration: %s", raw)
	}
}
