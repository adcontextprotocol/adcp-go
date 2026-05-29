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
		IoAcceptance: &IOAcceptance{
			IoID:        "io-1",
			AcceptedAt:  "2026-05-26T15:00:00Z",
			Signatory:   "agent:buyer.example",
			SignatureID: "sig-1",
		},
		ArtifactWebhook: &ArtifactWebhookConfig{
			URL:            "https://buyer.example/webhooks/artifacts",
			Authentication: LegacyWebhookAuthentication{Schemes: []string{"HMAC-SHA256"}, Credentials: "abcdef0123456789abcdef0123456789"},
			DeliveryMode:   "batched",
			BatchFrequency: "hourly",
			SamplingRate:   Ptr(0.0),
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
		`"io_acceptance":{"io_id":"io-1","accepted_at":"2026-05-26T15:00:00Z","signatory":"agent:buyer.example","signature_id":"sig-1"}`,
		`"artifact_webhook":{"url":"https://buyer.example/webhooks/artifacts","authentication":{"schemes":["HMAC-SHA256"],"credentials":"abcdef0123456789abcdef0123456789"},"delivery_mode":"batched","batch_frequency":"hourly","sampling_rate":0}`,
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
	if decoded.IoAcceptance == nil || decoded.IoAcceptance.IoID != "io-1" {
		t.Fatalf("io acceptance did not round-trip: %#v", decoded.IoAcceptance)
	}
	if decoded.ArtifactWebhook == nil || decoded.ArtifactWebhook.DeliveryMode != "batched" {
		t.Fatalf("artifact webhook did not round-trip: %#v", decoded.ArtifactWebhook)
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
			name: "get products proposals",
			value: GetProductsResponse{
				Products: []Product{},
				Proposals: []Proposal{{
					ProposalID:     "proposal-1",
					Name:           "Balanced launch plan",
					ProposalStatus: "committed",
					Allocations: []ProductAllocation{{
						ProductID:            "premium-display",
						AllocationPercentage: 60,
						PricingOptionID:      "pd-cpm",
						DaypartTargets:       []DaypartTarget{{Days: []string{"mon"}, StartHour: 9, EndHour: 17}},
					}},
					InsertionOrder: &InsertionOrder{
						IoID: "io-1",
						Terms: &InsertionOrderTerms{
							Advertiser:  "Acme",
							Publisher:   "Example Publisher",
							TotalBudget: Ptr(InsertionOrderBudget{Amount: 10000, Currency: "USD"}),
							FlightStart: "2026-06-01T00:00:00Z",
							FlightEnd:   "2026-06-30T00:00:00Z",
						},
						RequiresSignature: true,
					},
					TotalBudgetGuidance: &ProposalBudgetGuidance{
						Min:         5000,
						Recommended: 10000,
						Currency:    "USD",
					},
				}},
			},
			want: []string{
				`"proposals":[{"proposal_id":"proposal-1","name":"Balanced launch plan","allocations":[{"product_id":"premium-display","allocation_percentage":60,"pricing_option_id":"pd-cpm"`,
				`"daypart_targets":[{"days":["mon"],"start_hour":9,"end_hour":17}]`,
				`"insertion_order":{"io_id":"io-1","terms":{"advertiser":"Acme","publisher":"Example Publisher","total_budget":{"amount":10000,"currency":"USD"}`,
				`"requires_signature":true}`,
				`"total_budget_guidance":{"min":5000,"recommended":10000,"currency":"USD"}`,
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
			name: "delivery reporting request options",
			value: GetMediaBuyDeliveryRequest{
				MediaBuyIDs: []string{"mb-1"},
				AttributionWindow: &DeliveryAttributionWindow{
					PostClick: Ptr(Duration{Interval: 7, Unit: "days"}),
					PostView:  Ptr(Duration{Interval: 1, Unit: "days"}),
					Model:     "last_touch",
				},
				ReportingDimensions: &DeliveryReportingDimensions{
					Geo:        &DeliveryReportingGeoDimension{GeoLevel: "metro", System: "nielsen_dma", Limit: 10, SortBy: "spend"},
					DeviceType: &DeliveryReportingDimension{SortBy: "impressions"},
					Audience:   &DeliveryReportingDimension{Limit: 5},
				},
			},
			want: []string{
				`"attribution_window":{"post_click":{"interval":7,"unit":"days"},"post_view":{"interval":1,"unit":"days"},"model":"last_touch"}`,
				`"reporting_dimensions":{"geo":{"geo_level":"metro","system":"nielsen_dma","limit":10,"sort_by":"spend"},"device_type":{"sort_by":"impressions"},"audience":{"limit":5}}`,
			},
		},
		{
			name: "media buy status filter scalar",
			value: GetMediaBuysRequest{
				StatusFilter: NewMediaBuyStatusFilter(MediaBuyStatusActive),
			},
			want: []string{
				`"status_filter":"active"`,
			},
		},
		{
			name: "delivery status filter array",
			value: GetMediaBuyDeliveryRequest{
				MediaBuyIDs: []string{"mb-1"},
				StatusFilter: NewMediaBuyStatusFilter(
					MediaBuyStatusActive,
					MediaBuyStatusPaused,
				),
			},
			want: []string{
				`"status_filter":["active","paused"]`,
			},
		},
		{
			name: "package input optimization goals",
			value: PackageInput{
				ProductID:       "prod-1",
				PricingOptionID: "po-1",
				Budget:          1000,
				OptimizationGoals: []OptimizationGoal{{
					Kind:      "metric",
					Metric:    "reach",
					ReachUnit: "household",
					TargetFrequency: &OptimizationGoalTargetFrequency{
						Min:    1,
						Max:    3,
						Window: Duration{Interval: 7, Unit: "days"},
					},
					Target:   OptimizationGoalThresholdRateTarget{Value: 0.7},
					Priority: 1,
				}},
			},
			want: []string{
				`"optimization_goals":[{"kind":"metric","metric":"reach","priority":1,"reach_unit":"household","target":{"kind":"threshold_rate","value":0.7},"target_frequency":{"min":1,"max":3,"window":{"interval":7,"unit":"days"}}}]`,
			},
		},
		{
			name: "optimization goal preserves explicit zero value factor",
			value: PackageInput{
				ProductID:       "prod-1",
				PricingOptionID: "po-1",
				Budget:          1000,
				OptimizationGoals: []OptimizationGoal{{
					Kind: "event",
					EventSources: []OptimizationGoalEventSource{{
						EventSourceID: "pixel-1",
						EventType:     "purchase",
						ValueFactor:   Ptr(0.0),
					}},
				}},
			},
			want: []string{
				`"value_factor":0`,
			},
		},
		{
			name: "package optimization goals typed",
			value: Package{
				PackageID: "pkg-1",
				OptimizationGoals: []OptimizationGoal{{
					Kind:   "metric",
					Metric: "clicks",
				}},
			},
			want: []string{
				`"optimization_goals":[{"kind":"metric","metric":"clicks"}]`,
			},
		},
		{
			name: "package update optimization goals typed",
			value: PackageUpdate{
				PackageID: "pkg-1",
				OptimizationGoals: []OptimizationGoal{{
					Kind:   "metric",
					Metric: "views",
				}},
			},
			want: []string{
				`"optimization_goals":[{"kind":"metric","metric":"views"}]`,
			},
		},
		{
			name: "creative agent refs",
			value: ListCreativeFormatsResponse{
				Formats: []CreativeFormat{},
				CreativeAgents: []CreativeAgentRef{{
					AgentURL:     "https://creative.example/mcp",
					AgentName:    "Creative Agent",
					Capabilities: []string{"generation", "preview"},
				}},
			},
			want: []string{
				`"creative_agents":[{"agent_url":"https://creative.example/mcp","agent_name":"Creative Agent","capabilities":["generation","preview"]}]`,
			},
		},
		{
			name: "build creative preview inputs",
			value: BuildCreativeRequest{
				IdempotencyKey: "idem-preview-123",
				PreviewInputs: []BuildCreativePreviewInput{{
					Name:               "mobile",
					Macros:             map[string]string{"CITY": "Honolulu"},
					ContextDescription: "Sunny morning",
				}},
			},
			want: []string{
				`"preview_inputs":[{"name":"mobile","macros":{"CITY":"Honolulu"},"context_description":"Sunny morning"}]`,
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

func TestReportPlanOutcomeDeliveryPreservesExplicitZero(t *testing.T) {
	req := ReportPlanOutcomeRequest{
		PlanID:            "plan-1",
		IdempotencyKey:    "idem-1",
		Outcome:           "delivery",
		GovernanceContext: "ctx-1",
		Delivery: Ptr(ReportPlanOutcomeDelivery{
			ReportingPeriod: Ptr(ReportPlanOutcomeDeliveryReportingPeriod{
				Start: "2026-06-01T00:00:00Z",
				End:   "2026-06-01T01:00:00Z",
			}),
			Impressions:     Ptr(0),
			Spend:           Ptr(0.0),
			CPM:             Ptr(0.0),
			ViewabilityRate: Ptr(0.0),
			CompletionRate:  Ptr(0.0),
		}),
	}

	raw, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("marshal report plan outcome request: %v", err)
	}
	body := string(raw)
	for _, want := range []string{
		`"impressions":0`,
		`"spend":0`,
		`"cpm":0`,
		`"viewability_rate":0`,
		`"completion_rate":0`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("marshaled report plan outcome missing %s:\n%s", want, body)
		}
	}

	var decoded ReportPlanOutcomeRequest
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("unmarshal report plan outcome request: %v", err)
	}
	if decoded.Delivery == nil {
		t.Fatal("delivery did not round-trip")
	}
	if decoded.Delivery.Impressions == nil || *decoded.Delivery.Impressions != 0 {
		t.Fatalf("impressions = %v, want pointer to 0", decoded.Delivery.Impressions)
	}
	if decoded.Delivery.Spend == nil || *decoded.Delivery.Spend != 0 {
		t.Fatalf("spend = %v, want pointer to 0", decoded.Delivery.Spend)
	}
	if decoded.Delivery.CPM == nil || *decoded.Delivery.CPM != 0 {
		t.Fatalf("cpm = %v, want pointer to 0", decoded.Delivery.CPM)
	}
	if decoded.Delivery.ViewabilityRate == nil || *decoded.Delivery.ViewabilityRate != 0 {
		t.Fatalf("viewability_rate = %v, want pointer to 0", decoded.Delivery.ViewabilityRate)
	}
	if decoded.Delivery.CompletionRate == nil || *decoded.Delivery.CompletionRate != 0 {
		t.Fatalf("completion_rate = %v, want pointer to 0", decoded.Delivery.CompletionRate)
	}
}

func TestReportPlanOutcomeErrorRoundTrip(t *testing.T) {
	req := ReportPlanOutcomeRequest{
		PlanID:            "plan-1",
		IdempotencyKey:    "idem-1",
		Outcome:           "failed",
		GovernanceContext: "ctx-1",
		Error: &ReportPlanOutcomeError{
			Code:    "seller_timeout",
			Message: "seller timed out",
		},
	}

	raw, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("marshal report plan outcome request: %v", err)
	}
	body := string(raw)
	for _, want := range []string{
		`"error":{"code":"seller_timeout","message":"seller timed out"}`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("marshaled report plan outcome missing %s:\n%s", want, body)
		}
	}

	var decoded ReportPlanOutcomeRequest
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("unmarshal report plan outcome request: %v", err)
	}
	if decoded.Error == nil {
		t.Fatal("error did not round-trip")
	}
	if decoded.Error.Code != "seller_timeout" || decoded.Error.Message != "seller timed out" {
		t.Fatalf("error = %#v, want seller timeout", decoded.Error)
	}
}

func TestGeneratedMediaBuyStatusFilterUnmarshalScalarAndArray(t *testing.T) {
	if got := NewMediaBuyStatusFilter(); got != nil {
		t.Fatalf("empty status filter constructor returned %#v, want nil", got)
	}

	var listReq GetMediaBuysRequest
	if err := json.Unmarshal([]byte(`{"status_filter":"active"}`), &listReq); err != nil {
		t.Fatalf("unmarshal scalar status filter: %v", err)
	}
	if listReq.StatusFilter == nil || len(*listReq.StatusFilter) != 1 || (*listReq.StatusFilter)[0] != MediaBuyStatusActive {
		t.Fatalf("scalar status filter did not decode as one active status: %#v", listReq.StatusFilter)
	}

	var deliveryReq GetMediaBuyDeliveryRequest
	if err := json.Unmarshal([]byte(`{"status_filter":["active","paused"]}`), &deliveryReq); err != nil {
		t.Fatalf("unmarshal array status filter: %v", err)
	}
	if deliveryReq.StatusFilter == nil || len(*deliveryReq.StatusFilter) != 2 {
		t.Fatalf("array status filter did not decode two statuses: %#v", deliveryReq.StatusFilter)
	}
	if (*deliveryReq.StatusFilter)[0] != MediaBuyStatusActive || (*deliveryReq.StatusFilter)[1] != MediaBuyStatusPaused {
		t.Fatalf("array status filter decoded unexpected values: %#v", *deliveryReq.StatusFilter)
	}

	var forwardReq GetMediaBuysRequest
	if err := json.Unmarshal([]byte(`{"status_filter":"future_status"}`), &forwardReq); err != nil {
		t.Fatalf("unmarshal forward-compatible status filter: %v", err)
	}
	if forwardReq.StatusFilter == nil || len(*forwardReq.StatusFilter) != 1 || (*forwardReq.StatusFilter)[0] != MediaBuyStatus("future_status") {
		t.Fatalf("forward-compatible status filter did not preserve unknown value: %#v", forwardReq.StatusFilter)
	}

	emptyFilter := MediaBuyStatusFilter{}
	if _, err := json.Marshal(emptyFilter); err == nil {
		t.Fatal("marshal empty status filter succeeded, want error")
	}
	if err := json.Unmarshal([]byte(`null`), &emptyFilter); err == nil {
		t.Fatal("unmarshal null status filter succeeded, want error")
	}
	if err := json.Unmarshal([]byte(`{"status_filter":[]}`), &deliveryReq); err == nil {
		t.Fatal("unmarshal empty status filter succeeded, want error")
	}
}

func TestGeneratedOptimizationGoalUnmarshalNestedShapes(t *testing.T) {
	raw := []byte(`{
		"optimization_goals": [{
			"kind": "event",
			"event_sources": [{
				"event_source_id": "pixel-1",
				"event_type": "purchase",
				"value_field": "value",
				"value_factor": 1.5
			}],
			"target": {"kind": "per_ad_spend", "value": 4, "future_target_field": "ok"},
			"future_goal_field": {"keep": true},
			"attribution_window": {
				"post_click": {"interval": 7, "unit": "days"},
				"post_view": {"interval": 1, "unit": "days"}
			},
			"priority": 1
		}]
	}`)
	var req PackageInput
	if err := json.Unmarshal(raw, &req); err != nil {
		t.Fatalf("unmarshal optimization goal: %v", err)
	}
	if len(req.OptimizationGoals) != 1 {
		t.Fatalf("optimization_goals len = %d, want 1", len(req.OptimizationGoals))
	}
	goal := req.OptimizationGoals[0]
	if goal.Kind != "event" || goal.Priority != 1 {
		t.Fatalf("unexpected optimization goal: %#v", goal)
	}
	if len(goal.EventSources) != 1 || goal.EventSources[0].EventSourceID != "pixel-1" {
		t.Fatalf("event_sources not typed: %#v", goal.EventSources)
	}
	if goal.EventSources[0].ValueFactor == nil || *goal.EventSources[0].ValueFactor != 1.5 {
		t.Fatalf("value_factor did not preserve non-zero pointer value: %#v", goal.EventSources[0].ValueFactor)
	}
	if goal.AttributionWindow == nil || goal.AttributionWindow.PostClick.Interval != 7 || goal.AttributionWindow.PostView == nil {
		t.Fatalf("attribution_window not typed: %#v", goal.AttributionWindow)
	}
	target, ok := goal.Target.(*OptimizationGoalPerAdSpendTarget)
	if !ok || target.Kind != "per_ad_spend" || target.Value != 4 {
		t.Fatalf("target oneOf should decode as per_ad_spend, got %#v", goal.Target)
	}
	if target.Extra["future_target_field"] != "ok" {
		t.Fatalf("target extra field was not preserved: %#v", target.Extra)
	}
	extra, ok := goal.Extra["future_goal_field"].(map[string]any)
	if !ok || extra["keep"] != true {
		t.Fatalf("unknown top-level goal field was not preserved: %#v", goal.Extra)
	}

	roundTrip, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("marshal optimization goal round trip: %v", err)
	}
	if !strings.Contains(string(roundTrip), `"future_goal_field":{"keep":true}`) {
		t.Fatalf("round trip dropped unknown top-level goal field: %s", roundTrip)
	}
	if !strings.Contains(string(roundTrip), `"target":{"future_target_field":"ok","kind":"per_ad_spend","value":4}`) {
		t.Fatalf("round trip dropped unknown target field: %s", roundTrip)
	}

	collidingExtra, err := json.Marshal(OptimizationGoal{
		Extra: map[string]any{
			"priority":          99,
			"future_goal_field": "ok",
		},
	})
	if err != nil {
		t.Fatalf("marshal optimization goal with colliding extra: %v", err)
	}
	if strings.Contains(string(collidingExtra), `"priority":`) {
		t.Fatalf("known typed field from Extra should not override zero typed value: %s", collidingExtra)
	}
	if !strings.Contains(string(collidingExtra), `"future_goal_field":"ok"`) {
		t.Fatalf("non-colliding Extra field was not preserved: %s", collidingExtra)
	}
}

func TestGeneratedOptimizationGoalUnmarshalMetricShape(t *testing.T) {
	raw := []byte(`{
		"kind": "metric",
		"metric": "reach",
		"reach_unit": "household",
		"target_frequency": {
			"min": 2,
			"max": 5,
			"window": {"interval": 7, "unit": "days"}
		},
		"target": {"kind": "threshold_rate", "value": 0.65},
		"priority": 1
	}`)
	var goal OptimizationGoal
	if err := json.Unmarshal(raw, &goal); err != nil {
		t.Fatalf("unmarshal metric optimization goal: %v", err)
	}
	if goal.Kind != "metric" || goal.Metric != "reach" || goal.ReachUnit != "household" {
		t.Fatalf("unexpected metric optimization goal fields: %#v", goal)
	}
	if goal.TargetFrequency == nil || goal.TargetFrequency.Min != 2 || goal.TargetFrequency.Max != 5 {
		t.Fatalf("target_frequency not typed: %#v", goal.TargetFrequency)
	}
	if goal.TargetFrequency.Window.Interval != 7 || goal.TargetFrequency.Window.Unit != "days" {
		t.Fatalf("target_frequency window not typed: %#v", goal.TargetFrequency.Window)
	}
	target, ok := goal.Target.(*OptimizationGoalThresholdRateTarget)
	if !ok || target.Kind != "threshold_rate" || target.Value != 0.65 {
		t.Fatalf("target oneOf should decode as threshold_rate, got %#v", goal.Target)
	}
}

func TestGeneratedOptimizationGoalTargetVariants(t *testing.T) {
	cases := []struct {
		name  string
		value OptimizationGoalTarget
		want  string
	}{
		{
			name:  "cost per",
			value: OptimizationGoalCostPerTarget{Value: 2.5},
			want:  `{"kind":"cost_per","value":2.5}`,
		},
		{
			name:  "threshold rate",
			value: OptimizationGoalThresholdRateTarget{Value: 0.25},
			want:  `{"kind":"threshold_rate","value":0.25}`,
		},
		{
			name:  "per ad spend",
			value: OptimizationGoalPerAdSpendTarget{Value: 4},
			want:  `{"kind":"per_ad_spend","value":4}`,
		},
		{
			name:  "maximize value",
			value: OptimizationGoalMaximizeValueTarget{},
			want:  `{"kind":"maximize_value"}`,
		},
		{
			name: "maximize value drops colliding value extra",
			value: OptimizationGoalMaximizeValueTarget{
				Extra: map[string]any{
					"value":               99,
					"future_target_field": "ok",
				},
			},
			want: `{"future_target_field":"ok","kind":"maximize_value"}`,
		},
		{
			name: "target extra",
			value: OptimizationGoalThresholdRateTarget{
				Value: 0.5,
				Extra: map[string]any{
					"future_target_field": "ok",
					"kind":                "wrong",
					"value":               99,
				},
			},
			want: `{"future_target_field":"ok","kind":"threshold_rate","value":0.5}`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			raw, err := json.Marshal(tc.value)
			if err != nil {
				t.Fatalf("marshal target: %v", err)
			}
			if string(raw) != tc.want {
				t.Fatalf("target JSON = %s, want %s", raw, tc.want)
			}
		})
	}

	var unknown OptimizationGoal
	if err := json.Unmarshal([]byte(`{
		"kind": "event",
		"event_sources": [{"event_source_id": "pixel-1", "event_type": "purchase"}],
		"target": {"kind": "future_target", "future_target_field": true}
	}`), &unknown); err != nil {
		t.Fatalf("unmarshal unknown target: %v", err)
	}
	rawTarget, ok := unknown.Target.(*OptimizationGoalRawTarget)
	if !ok || rawTarget.Kind != "future_target" {
		t.Fatalf("unknown target did not decode as raw target: %#v", unknown.Target)
	}
	if rawTarget.Extra["future_target_field"] != true {
		t.Fatalf("raw target extra not preserved: %#v", rawTarget.Extra)
	}
	roundTrip, err := json.Marshal(unknown)
	if err != nil {
		t.Fatalf("marshal unknown target: %v", err)
	}
	if !strings.Contains(string(roundTrip), `"target":{"future_target_field":true,"kind":"future_target"}`) {
		t.Fatalf("unknown target did not round-trip: %s", roundTrip)
	}

	var nilCostPer *OptimizationGoalCostPerTarget
	withoutTarget, err := json.Marshal(OptimizationGoal{
		Kind:   "metric",
		Metric: "clicks",
		Target: nilCostPer,
	})
	if err != nil {
		t.Fatalf("marshal typed nil target: %v", err)
	}
	if strings.Contains(string(withoutTarget), `"target"`) {
		t.Fatalf("typed nil target should omit target: %s", withoutTarget)
	}
}

func TestGeneratedOptimizationGoalCostPerTargetSharedAcrossGoalKinds(t *testing.T) {
	raw := []byte(`{
		"optimization_goals": [
			{"kind": "metric", "metric": "clicks", "target": {"kind": "cost_per", "value": 1.25}},
			{
				"kind": "event",
				"event_sources": [{"event_source_id": "pixel-1", "event_type": "purchase"}],
				"target": {"kind": "cost_per", "value": 8.5}
			}
		]
	}`)
	var req PackageInput
	if err := json.Unmarshal(raw, &req); err != nil {
		t.Fatalf("unmarshal cost_per targets: %v", err)
	}
	if len(req.OptimizationGoals) != 2 {
		t.Fatalf("optimization_goals len = %d, want 2", len(req.OptimizationGoals))
	}
	metricTarget, ok := req.OptimizationGoals[0].Target.(*OptimizationGoalCostPerTarget)
	if !ok || metricTarget.Value != 1.25 {
		t.Fatalf("metric cost_per target = %#v", req.OptimizationGoals[0].Target)
	}
	eventTarget, ok := req.OptimizationGoals[1].Target.(*OptimizationGoalCostPerTarget)
	if !ok || eventTarget.Value != 8.5 {
		t.Fatalf("event cost_per target = %#v", req.OptimizationGoals[1].Target)
	}
}

func TestGeneratedEnumHelpers(t *testing.T) {
	values := KnownMediaBuyStatusValues()
	known := map[MediaBuyStatus]bool{}
	for _, value := range values {
		known[value] = true
	}
	for _, want := range []MediaBuyStatus{MediaBuyStatusActive, MediaBuyStatusPaused, MediaBuyStatusCanceled} {
		if !known[want] {
			t.Fatalf("KnownMediaBuyStatusValues missing %q from %#v", want, values)
		}
		if !IsKnownMediaBuyStatus(want) {
			t.Fatalf("%q media buy status should be known", want)
		}
	}
	if IsKnownMediaBuyStatus(MediaBuyStatus("future_status")) {
		t.Fatal("future media buy status should not be known")
	}
	parsed, err := ParseMediaBuyStatus("paused")
	if err != nil {
		t.Fatalf("ParseMediaBuyStatus paused: %v", err)
	}
	if parsed != MediaBuyStatusPaused {
		t.Fatalf("ParseMediaBuyStatus parsed %q, want %q", parsed, MediaBuyStatusPaused)
	}
	if _, err := ParseMediaBuyStatus("future_status"); err == nil {
		t.Fatal("ParseMediaBuyStatus future_status succeeded, want error")
	} else if strings.Contains(err.Error(), "future_status") {
		t.Fatalf("ParseMediaBuyStatus error echoed rejected value: %q", err.Error())
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
