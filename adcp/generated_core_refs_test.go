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
		},
		PushNotificationConfig: &PushNotificationConfig{
			URL: "https://buyer.example/webhooks/tasks",
		},
		ReportingWebhook: &ReportingWebhook{
			URL:                "https://buyer.example/webhooks/reporting",
			Authentication:     map[string]any{"schemes": []any{"Bearer"}, "credentials": "0123456789abcdef0123456789abcdef"},
			ReportingFrequency: "daily",
		},
	}

	raw, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	body := string(raw)
	for _, want := range []string{
		`"invoice_recipient":{"legal_name":"Acme Corporation","tax_id":"12-3456789"}`,
		`"push_notification_config":{"url":"https://buyer.example/webhooks/tasks"}`,
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
			},
			want: []string{
				`"daypart_targets":[{"days":["mon","tue"],"start_hour":9,"end_hour":17}]`,
				`"frequency_cap":{"max_impressions":3,"per":"user","window":{"interval":7,"unit":"days"}}`,
				`"property_list":{"agent_url":"https://lists.example/mcp","list_id":"pl-456"}`,
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
