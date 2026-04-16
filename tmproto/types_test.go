package tmproto

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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
	require.NoError(t, err, "marshal")

	var got ContextMatchRequest
	err = json.Unmarshal(data, &got)
	require.NoError(t, err, "unmarshal")

	assert.Equal(t, req.RequestID, got.RequestID, "request_id")
	assert.Equal(t, req.PropertyType, got.PropertyType, "property_type")
	assert.Len(t, got.AvailablePkgs, 2, "available_packages")
	assert.Len(t, got.AvailablePkgs[1].Catalogs, 1, "catalogs")
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
	require.NoError(t, err, "marshal")

	s := string(data)
	for _, forbidden := range []string{"user_token", "uid_type", "user_id", "device_id", "ip_address"} {
		assert.NotContains(t, s, forbidden, "context match request contains identity field %q", forbidden)
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
	require.NoError(t, err, "marshal")

	s := string(data)
	for _, forbidden := range []string{"property_id", "property_type", "placement_id", "artifacts", "available_packages", "url", "domain", "topic_ids", "identities"} {
		assert.NotContains(t, s, forbidden, "identity match request contains context field %q", forbidden)
	}
}

func TestIdentityMatchResponse_RoundTrip(t *testing.T) {
	resp := &IdentityMatchResponse{
		RequestID:          "id-test-002",
		EligiblePackageIDs: []string{"pkg-1", "pkg-3"},
		TTLSec:             300,
	}

	data, err := json.Marshal(resp)
	require.NoError(t, err, "marshal")

	var got IdentityMatchResponse
	err = json.Unmarshal(data, &got)
	require.NoError(t, err, "unmarshal")

	require.Len(t, got.EligiblePackageIDs, 2, "eligible_package_ids")
	assert.Equal(t, "pkg-1", got.EligiblePackageIDs[0], "eligible_package_ids[0]")
	assert.Equal(t, "pkg-3", got.EligiblePackageIDs[1], "eligible_package_ids[1]")
	assert.Equal(t, 300, got.TTLSec, "ttl_sec")
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
	require.NoError(t, err, "marshal")

	assert.Contains(t, string(data), `"country":"US"`, "expected country in JSON output")

	var got IdentityMatchRequest
	err = json.Unmarshal(data, &got)
	require.NoError(t, err, "unmarshal")
	assert.Equal(t, "US", got.Country, "country")
}

func TestIdentityMatchRequest_CountryOmittedWhenEmpty(t *testing.T) {
	req := &IdentityMatchRequest{
		RequestID:  "id-omit-001",
		UserToken:  "tok_abc",
		PackageIDs: []string{"pkg-1"},
	}

	data, err := json.Marshal(req)
	require.NoError(t, err, "marshal")

	assert.NotContains(t, string(data), `"country"`, "country should be omitted when empty")
}

func TestIdentityMatchResponse_TMPX(t *testing.T) {
	resp := &IdentityMatchResponse{
		RequestID:          "id-tmpx-001",
		EligiblePackageIDs: []string{"pkg-1"},
		TTLSec:             60,
		Tmpx:               "k1.dGVzdC10b2tlbg",
	}

	data, err := json.Marshal(resp)
	require.NoError(t, err, "marshal")

	assert.Contains(t, string(data), `"tmpx":"k1.dGVzdC10b2tlbg"`, "expected tmpx in JSON output")

	var got IdentityMatchResponse
	err = json.Unmarshal(data, &got)
	require.NoError(t, err, "unmarshal")
	assert.Equal(t, "k1.dGVzdC10b2tlbg", got.Tmpx, "tmpx")
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
	require.NoError(t, err, "marshal")

	var got IdentityMatchResponse
	err = json.Unmarshal(data, &got)
	require.NoError(t, err, "unmarshal")
	assert.Equal(t, "k1.acme-token", got.TmpxProviders["acme"], "acme")
	assert.Equal(t, "k2.scope3-token", got.TmpxProviders["scope3"], "scope3")
}

func TestIdentityMatchResponse_TMPXOmittedWhenEmpty(t *testing.T) {
	resp := &IdentityMatchResponse{
		RequestID:          "id-omit-002",
		EligiblePackageIDs: []string{"pkg-1"},
		TTLSec:             60,
	}

	data, err := json.Marshal(resp)
	require.NoError(t, err, "marshal")

	assert.NotContains(t, string(data), `"tmpx"`, "tmpx should be omitted when empty")
	assert.NotContains(t, string(data), `"tmpx_providers"`, "tmpx_providers should be omitted when empty")
}

func TestOffer_SimpleActivation(t *testing.T) {
	offer := &Offer{PackageID: "pkg-display-001"}

	data, err := json.Marshal(offer)
	require.NoError(t, err, "marshal")

	s := string(data)
	assert.Contains(t, s, `"package_id":"pkg-display-001"`, "expected package_id in output")
	assert.NotContains(t, s, `"brand"`, "simple offer should not contain brand")
	assert.NotContains(t, s, `"price"`, "simple offer should not contain price")
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
	require.NoError(t, err, "marshal")

	var got Offer
	err = json.Unmarshal(data, &got)
	require.NoError(t, err, "unmarshal")

	require.NotNil(t, got.Brand, "brand should not be nil")
	assert.Equal(t, "Acme Corp", got.Brand.Name, "brand name")
	require.NotNil(t, got.Price, "price should not be nil")
	assert.Equal(t, 12.50, got.Price.Amount, "price amount")
	assert.Equal(t, PriceModelCPM, got.Price.Model, "price model")
	assert.Equal(t, "https://track.example.com/c/123", got.Macros["click_url"], "macros click_url")
}

func TestErrorResponse_RoundTrip(t *testing.T) {
	errResp := &ErrorResponse{
		RequestID: "ctx-err-001",
		Code:      ErrorCodeRateLimited,
		Message:   "Too many requests",
	}

	data, err := json.Marshal(errResp)
	require.NoError(t, err, "marshal")

	var got ErrorResponse
	err = json.Unmarshal(data, &got)
	require.NoError(t, err, "unmarshal")

	assert.Equal(t, ErrorCodeRateLimited, got.Code, "code")
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
	require.NoError(t, err, "marshal")

	var got ContextMatchRequest
	err = UnmarshalJSON(data, &got)
	require.NoError(t, err, "unmarshal")

	assert.Equal(t, PropertyTypeAIAssistant, got.PropertyType, "property_type")
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
			if tt.wantErr {
				assert.Error(t, err, "ValidateExposeRequest() expected error")
			} else {
				assert.NoError(t, err, "ValidateExposeRequest() unexpected error")
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
	require.NoError(t, err, "marshal")
	assert.Contains(t, string(data), `"source_id":"aao-agent-cnn-identity-v2"`, "source_id missing from JSON output")

	var got ExposeRequest
	err = json.Unmarshal(data, &got)
	require.NoError(t, err, "unmarshal")
	assert.Equal(t, "aao-agent-cnn-identity-v2", got.SourceID, "source_id")
}
