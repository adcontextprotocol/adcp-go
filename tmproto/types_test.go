package tmproto

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
	for _, forbidden := range []string{"property_id", "property_type", "placement_id", "artifacts", "available_packages", "url", "domain", "topic_ids", "identities"} {
		if strings.Contains(s, forbidden) {
			t.Errorf("identity match request contains context field %q", forbidden)
		}
	}
}

func TestIdentityMatchResponse_RoundTrip(t *testing.T) {
	resp := &IdentityMatchResponse{
		RequestID:          "id-test-002",
		EligiblePackageIDs: []string{"pkg-1", "pkg-3"},
		TTLSec:             300,
	}

	data, err := json.Marshal(resp)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var got IdentityMatchResponse
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if len(got.EligiblePackageIDs) != 2 {
		t.Fatalf("eligible_package_ids: got %d, want 2", len(got.EligiblePackageIDs))
	}
	if got.EligiblePackageIDs[0] != "pkg-1" {
		t.Errorf("eligible_package_ids[0]: got %q, want pkg-1", got.EligiblePackageIDs[0])
	}
	if got.EligiblePackageIDs[1] != "pkg-3" {
		t.Errorf("eligible_package_ids[1]: got %q, want pkg-3", got.EligiblePackageIDs[1])
	}
	if got.TTLSec != 300 {
		t.Errorf("ttl_sec: got %d, want 300", got.TTLSec)
	}
}

func TestIdentityMatchRequest_Country(t *testing.T) {
	req := &IdentityMatchRequest{
		RequestID:  "id-country-001",
		UserToken:  "tok_uid2_abc",
		UIDType:    UIDTypeUID2,
		PackageIDs: []string{"pkg-1"},
		Country:    "US",
	}

	data, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	if !strings.Contains(string(data), `"country":"US"`) {
		t.Error("expected country in JSON output")
	}

	var got IdentityMatchRequest
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.Country != "US" {
		t.Errorf("country: got %q, want US", got.Country)
	}
}

func TestIdentityMatchRequest_CountryOmittedWhenEmpty(t *testing.T) {
	req := &IdentityMatchRequest{
		RequestID:  "id-omit-001",
		UserToken:  "tok_abc",
		PackageIDs: []string{"pkg-1"},
	}

	data, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	if strings.Contains(string(data), `"country"`) {
		t.Error("country should be omitted when empty")
	}
}

func TestIdentityMatchResponse_TMPX(t *testing.T) {
	resp := &IdentityMatchResponse{
		RequestID:          "id-tmpx-001",
		EligiblePackageIDs: []string{"pkg-1"},
		TTLSec:             60,
		Tmpx:               "k1.dGVzdC10b2tlbg",
	}

	data, err := json.Marshal(resp)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	if !strings.Contains(string(data), `"tmpx":"k1.dGVzdC10b2tlbg"`) {
		t.Error("expected tmpx in JSON output")
	}

	var got IdentityMatchResponse
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.Tmpx != "k1.dGVzdC10b2tlbg" {
		t.Errorf("tmpx: got %q, want k1.dGVzdC10b2tlbg", got.Tmpx)
	}
}

func TestIdentityMatchResponse_TmpxProviders(t *testing.T) {
	resp := &IdentityMatchResponse{
		RequestID:          "id-tmpx-map",
		EligiblePackageIDs: []string{"pkg-1"},
		TTLSec:             120,
		TmpxProviders: map[string]string{
			"acme":   "k1.acme-token",
			"scope3": "k2.scope3-token",
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
	if got.TmpxProviders["acme"] != "k1.acme-token" {
		t.Errorf("acme: got %q, want k1.acme-token", got.TmpxProviders["acme"])
	}
	if got.TmpxProviders["scope3"] != "k2.scope3-token" {
		t.Errorf("scope3: got %q, want k2.scope3-token", got.TmpxProviders["scope3"])
	}
}

func TestIdentityMatchResponse_TMPXOmittedWhenEmpty(t *testing.T) {
	resp := &IdentityMatchResponse{
		RequestID:          "id-omit-002",
		EligiblePackageIDs: []string{"pkg-1"},
		TTLSec:             60,
	}

	data, err := json.Marshal(resp)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	if strings.Contains(string(data), `"tmpx"`) {
		t.Error("tmpx should be omitted when empty")
	}
	if strings.Contains(string(data), `"tmpx_providers"`) {
		t.Error("tmpx_providers should be omitted when empty")
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

func TestMarshalJSON_RoundTrip(t *testing.T) {
	req := &ContextMatchRequest{
		RequestID:    "ctx-codec-001",
		PropertyID:   "test-pub",
		PropertyType: PropertyTypeAIAssistant,
		PlacementID:  "chat-inline",
		AvailablePkgs: []AvailablePackage{
			{PackageID: "pkg-1", MediaBuyID: "mb-1"},
		},
	}

	data, err := MarshalJSON(req)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var got ContextMatchRequest
	if err := UnmarshalJSON(data, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if got.PropertyType != PropertyTypeAIAssistant {
		t.Errorf("property_type: got %q, want ai_assistant", got.PropertyType)
	}
}

func TestValidateExposeRequest(t *testing.T) {
	tests := []struct {
		name    string
		req     ExposeRequest
		wantErr bool
	}{
		{
			name:    "valid with source_id",
			req:     ExposeRequest{UserToken: "u1", PackageID: "pkg-1", SourceID: "agent-cnn"},
			wantErr: false,
		},
		{
			name:    "valid without source_id",
			req:     ExposeRequest{UserToken: "u1", PackageID: "pkg-1"},
			wantErr: false,
		},
		{
			name:    "missing user_token and identities",
			req:     ExposeRequest{PackageID: "pkg-1"},
			wantErr: true,
		},
		{
			name:    "missing package_id",
			req:     ExposeRequest{UserToken: "u1"},
			wantErr: true,
		},
		{
			name:    "source_id with colon",
			req:     ExposeRequest{UserToken: "u1", PackageID: "pkg-1", SourceID: "bad:id"},
			wantErr: true,
		},
		{
			name:    "source_id with slash",
			req:     ExposeRequest{UserToken: "u1", PackageID: "pkg-1", SourceID: "bad/id"},
			wantErr: true,
		},
		{
			name:    "campaign_id with colon",
			req:     ExposeRequest{UserToken: "u1", PackageID: "pkg-1", CampaignID: "bad:camp"},
			wantErr: true,
		},
		{
			name:    "package_id with newline",
			req:     ExposeRequest{UserToken: "u1", PackageID: "pkg\n1"},
			wantErr: true,
		},
		{
			name: "valid with identities instead of user_token",
			req: ExposeRequest{
				PackageID:  "pkg-1",
				Identities: []UserIdentity{{UserToken: "u1", UIDType: UIDTypeUID2}},
			},
			wantErr: false,
		},
		{
			name:    "impression_id with colon",
			req:     ExposeRequest{UserToken: "u1", PackageID: "pkg-1", ImpressionID: "bad:imp"},
			wantErr: true,
		},
		{
			name:    "impression_id with newline",
			req:     ExposeRequest{UserToken: "u1", PackageID: "pkg-1", ImpressionID: "bad\nimp"},
			wantErr: true,
		},
		{
			name:    "source_id exceeds max length",
			req:     ExposeRequest{UserToken: "u1", PackageID: "pkg-1", SourceID: strings.Repeat("a", MaxIDLength+1)},
			wantErr: true,
		},
		{
			name:    "source_id at max length is ok",
			req:     ExposeRequest{UserToken: "u1", PackageID: "pkg-1", SourceID: strings.Repeat("a", MaxIDLength)},
			wantErr: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateExposeRequest(&tt.req)
			if (err != nil) != tt.wantErr {
				t.Errorf("ValidateExposeRequest() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestExposeRequest_SourceID_RoundTrip(t *testing.T) {
	req := ExposeRequest{
		SourceID:     "aao-agent-cnn-identity-v2",
		UserToken:    "uid2-abc123",
		PackageID:    "pkg-display-001",
		ImpressionID: "imp-12345",
		CampaignID:   "campaign-acme",
	}
	data, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.Contains(string(data), `"source_id":"aao-agent-cnn-identity-v2"`) {
		t.Error("source_id missing from JSON output")
	}

	var got ExposeRequest
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.SourceID != "aao-agent-cnn-identity-v2" {
		t.Errorf("source_id: got %q, want aao-agent-cnn-identity-v2", got.SourceID)
	}
}
