package tmp

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestContextMatchRequest_RoundTrip(t *testing.T) {
	req := &ContextMatchRequest{
		ProtocolVersion: "1.0",
		RequestID:       "ctx-test-001",
		PropertyID:      "oakwood-publishing",
		PropertyType:    PropertyTypeWebsite,
		PlacementID:     "article-sidebar-300x250",
		Artifacts:       []string{"article:sustainable-kitchen"},
		AvailablePkgs: []AvailablePackage{
			{
				PackageID:  "pkg-display-001",
				MediaBuyID: "mb-acme-q1",
				FormatIDs:  []string{"display_300x250"},
			},
			{
				PackageID:  "pkg-native-002",
				MediaBuyID: "mb-nova-q1",
				FormatIDs:  []string{"native_infeed"},
				Catalogs: []Catalog{
					{CatalogID: "cat-nova-products", Type: "product", GTINs: []string{"gtin-001", "gtin-002"}},
				},
			},
		},
	}

	data, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var got ContextMatchRequest
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if got.RequestID != req.RequestID {
		t.Errorf("request_id: got %q, want %q", got.RequestID, req.RequestID)
	}
	if got.PropertyType != req.PropertyType {
		t.Errorf("property_type: got %q, want %q", got.PropertyType, req.PropertyType)
	}
	if len(got.AvailablePkgs) != 2 {
		t.Errorf("available_packages: got %d, want 2", len(got.AvailablePkgs))
	}
	if len(got.AvailablePkgs[1].Catalogs) != 1 {
		t.Errorf("catalogs: got %d, want 1", len(got.AvailablePkgs[1].Catalogs))
	}
}

func TestContextMatchRequest_NoIdentityFields(t *testing.T) {
	req := &ContextMatchRequest{
		RequestID:    "ctx-test-002",
		PropertyID:   "test-pub",
		PropertyType: PropertyTypeWebsite,
		PlacementID:  "sidebar",
		AvailablePkgs: []AvailablePackage{
			{PackageID: "pkg-1", MediaBuyID: "mb-1"},
		},
	}

	data, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	s := string(data)
	for _, forbidden := range []string{"user_token", "uid_type", "user_id", "device_id", "ip_address"} {
		if strings.Contains(s, forbidden) {
			t.Errorf("context match request contains identity field %q", forbidden)
		}
	}
}

func TestIdentityMatchRequest_NoContextFields(t *testing.T) {
	req := &IdentityMatchRequest{
		RequestID:  "id-test-001",
		UserToken:  "tok_uid2_abc123",
		UIDType:    UIDTypeUID2,
		PackageIDs: []string{"pkg-1", "pkg-2", "pkg-3"},
	}

	data, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	s := string(data)
	for _, forbidden := range []string{"property_id", "property_type", "placement_id", "artifacts", "available_packages", "url", "domain", "topic_ids"} {
		if strings.Contains(s, forbidden) {
			t.Errorf("identity match request contains context field %q", forbidden)
		}
	}
}

func TestIdentityMatchResponse_RoundTrip(t *testing.T) {
	score := 0.82
	resp := &IdentityMatchResponse{
		RequestID: "id-test-002",
		Eligibility: []PackageEligibility{
			{PackageID: "pkg-1", Eligible: true, IntentScore: &score},
			{PackageID: "pkg-2", Eligible: false},
			{PackageID: "pkg-3", Eligible: true},
		},
	}

	data, err := json.Marshal(resp)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var got IdentityMatchResponse
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if len(got.Eligibility) != 3 {
		t.Fatalf("eligibility: got %d, want 3", len(got.Eligibility))
	}
	if !got.Eligibility[0].Eligible {
		t.Error("pkg-1 should be eligible")
	}
	if got.Eligibility[0].IntentScore == nil || *got.Eligibility[0].IntentScore != 0.82 {
		t.Error("pkg-1 intent_score should be 0.82")
	}
	if got.Eligibility[1].Eligible {
		t.Error("pkg-2 should not be eligible")
	}
	if got.Eligibility[1].IntentScore != nil {
		t.Error("pkg-2 should have no intent_score")
	}
}

func TestOffer_SimpleActivation(t *testing.T) {
	offer := &Offer{PackageID: "pkg-display-001"}

	data, err := json.Marshal(offer)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	s := string(data)
	if !strings.Contains(s, `"package_id":"pkg-display-001"`) {
		t.Errorf("expected package_id in output: %s", s)
	}
	// Simple offer should not contain optional fields
	if strings.Contains(s, `"brand"`) {
		t.Error("simple offer should not contain brand")
	}
	if strings.Contains(s, `"price"`) {
		t.Error("simple offer should not contain price")
	}
}

func TestOffer_RichResponse(t *testing.T) {
	offer := &Offer{
		PackageID: "pkg-reco-001",
		Brand:     &BrandRef{Name: "Acme Corp", AdvertiserDomain: "acme.example.com"},
		Price:     &OfferPrice{Amount: 12.50, Currency: "USD", Model: PriceModelCPM},
		Summary:   "Acme product recommendation for cooking context",
		Macros:    map[string]string{"click_url": "https://track.example.com/c/123"},
	}

	data, err := json.Marshal(offer)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var got Offer
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if got.Brand == nil || got.Brand.Name != "Acme Corp" {
		t.Error("brand should be Acme Corp")
	}
	if got.Price == nil || got.Price.Amount != 12.50 {
		t.Error("price should be 12.50")
	}
	if got.Price.Model != PriceModelCPM {
		t.Errorf("price model: got %q, want cpm", got.Price.Model)
	}
	if got.Macros["click_url"] != "https://track.example.com/c/123" {
		t.Error("macros should contain click_url")
	}
}

func TestErrorResponse_RoundTrip(t *testing.T) {
	err := &ErrorResponse{
		RequestID: "ctx-err-001",
		Code:      ErrorCodeRateLimited,
		Message:   "Too many requests",
	}

	data, marshalErr := json.Marshal(err)
	if marshalErr != nil {
		t.Fatalf("marshal: %v", marshalErr)
	}

	var got ErrorResponse
	if unmarshalErr := json.Unmarshal(data, &got); unmarshalErr != nil {
		t.Fatalf("unmarshal: %v", unmarshalErr)
	}

	if got.Code != ErrorCodeRateLimited {
		t.Errorf("code: got %q, want rate_limited", got.Code)
	}
}

func TestJSONCodec_RoundTrip(t *testing.T) {
	codec := &JSONCodec{}

	req := &ContextMatchRequest{
		RequestID:    "ctx-codec-001",
		PropertyID:   "test-pub",
		PropertyType: PropertyTypeAIAssistant,
		PlacementID:  "chat-inline",
		AvailablePkgs: []AvailablePackage{
			{PackageID: "pkg-1", MediaBuyID: "mb-1"},
		},
	}

	data, err := codec.MarshalContextRequest(req)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	got, err := codec.UnmarshalContextRequest(data)
	if err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if got.PropertyType != PropertyTypeAIAssistant {
		t.Errorf("property_type: got %q, want ai_assistant", got.PropertyType)
	}
}
