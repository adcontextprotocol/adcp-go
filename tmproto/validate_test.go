package tmproto

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestValidateIdentityRequest(t *testing.T) {
	valid := func() IdentityMatchRequest {
		return IdentityMatchRequest{
			Type:           TypeIdentityMatchRequest,
			RequestID:      "id-1",
			SellerAgentURL: "https://seller.example.com/agent",
			Identities:     []IdentityToken{{UserToken: "tok", UIDType: UIDTypeUID2}},
		}
	}

	cases := []struct {
		name    string
		mutate  func(*IdentityMatchRequest)
		wantErr string
		wantNot string
	}{
		{name: "ok", mutate: func(*IdentityMatchRequest) {}},
		{
			name:    "missing type rejected",
			mutate:  func(r *IdentityMatchRequest) { r.Type = "" },
			wantErr: `type must be "identity_match_request"`,
		},
		{
			name:    "mismatched type rejected",
			mutate:  func(r *IdentityMatchRequest) { r.Type = TypeContextMatchRequest },
			wantErr: `type must be "identity_match_request"`,
		},
		{
			name:    "missing request_id rejected",
			mutate:  func(r *IdentityMatchRequest) { r.RequestID = "" },
			wantErr: "request_id is required",
		},
		{
			name:    "missing seller_agent_url rejected",
			mutate:  func(r *IdentityMatchRequest) { r.SellerAgentURL = "" },
			wantErr: "seller_agent_url is required",
		},
		{
			name:    "empty identities rejected",
			mutate:  func(r *IdentityMatchRequest) { r.Identities = nil },
			wantErr: "identities must not be empty",
		},
		{
			name: "too many identities rejected",
			mutate: func(r *IdentityMatchRequest) {
				r.Identities = []IdentityToken{
					{UserToken: "a", UIDType: UIDTypeUID2},
					{UserToken: "b", UIDType: UIDTypeID5},
					{UserToken: "c", UIDType: UIDTypeEUID},
					{UserToken: "d", UIDType: UIDTypeRampID},
				}
			},
			wantErr: "identities exceeds maximum of 3",
		},
		{
			name: "unknown uid_type rejected",
			mutate: func(r *IdentityMatchRequest) {
				r.Identities = []IdentityToken{{UserToken: "tok", UIDType: UIDType("made_up")}}
			},
			wantErr: "uid_type is not a recognized TMP identity type",
			wantNot: "made_up",
		},
		{
			name: "every spec uid_type accepted",
			mutate: func(r *IdentityMatchRequest) {
				r.Identities = []IdentityToken{
					{UserToken: "tok", UIDType: UIDTypeHashedEmail},
				}
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := valid()
			tc.mutate(&req)
			err := ValidateIdentityRequest(&req)
			if tc.wantErr == "" {
				require.NoError(t, err)
				return
			}
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.wantErr)
			if tc.wantNot != "" {
				assert.NotContains(t, err.Error(), tc.wantNot)
			}
		})
	}
}

func TestValidateIdentityRequest_AllValidUIDTypes(t *testing.T) {
	types := []UIDType{
		UIDTypeRampID, UIDTypeRampIDDerived, UIDTypeID5, UIDTypeUID2,
		UIDTypeEUID, UIDTypePairID, UIDTypeMAID, UIDTypeHashedEmail,
		UIDTypePublisherFirstParty, UIDTypeOther,
	}
	for _, ut := range types {
		t.Run(strings.ReplaceAll(string(ut), " ", "_"), func(t *testing.T) {
			req := IdentityMatchRequest{
				Type:           TypeIdentityMatchRequest,
				RequestID:      "id-1",
				SellerAgentURL: "https://seller.example.com/agent",
				Identities:     []IdentityToken{{UserToken: "tok", UIDType: ut}},
			}
			require.NoError(t, ValidateIdentityRequest(&req))
		})
	}
}
