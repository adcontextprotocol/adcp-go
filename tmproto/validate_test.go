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
		{
			name: "duplicate uid_type+user_token rejected",
			mutate: func(r *IdentityMatchRequest) {
				r.Identities = []IdentityToken{
					{UserToken: "tok", UIDType: UIDTypeUID2},
					{UserToken: "tok", UIDType: UIDTypeUID2, Attestation: sampleAttestation()},
				}
			},
			wantErr: "duplicates an earlier (uid_type, user_token) pair",
		},
		{
			name: "same token different uid_type accepted",
			mutate: func(r *IdentityMatchRequest) {
				r.Identities = []IdentityToken{
					{UserToken: "tok", UIDType: UIDTypeUID2},
					{UserToken: "tok", UIDType: UIDTypeID5},
				}
			},
		},
		{
			name:   "valid attestation accepted",
			mutate: func(r *IdentityMatchRequest) { r.Identities[0].Attestation = sampleAttestation() },
		},
		{
			name: "attestation missing issuer rejected",
			mutate: func(r *IdentityMatchRequest) {
				a := sampleAttestation()
				a.Issuer = nil
				r.Identities[0].Attestation = a
			},
			wantErr: "attestation.issuer is required",
		},
		{
			name: "attestation missing scheme rejected",
			mutate: func(r *IdentityMatchRequest) {
				a := sampleAttestation()
				a.Scheme = ""
				r.Identities[0].Attestation = a
			},
			wantErr: "attestation.scheme is required",
		},
		{
			name: "attestation missing proof rejected",
			mutate: func(r *IdentityMatchRequest) {
				a := sampleAttestation()
				a.Proof = nil
				r.Identities[0].Attestation = a
			},
			wantErr: "attestation.proof is required",
		},
		{
			name: "attestation empty claims rejected",
			mutate: func(r *IdentityMatchRequest) {
				a := sampleAttestation()
				a.Claims = nil
				r.Identities[0].Attestation = a
			},
			wantErr: "attestation.claims must not be empty",
		},
		{
			name: "attestation too many claims rejected",
			mutate: func(r *IdentityMatchRequest) {
				a := sampleAttestation()
				a.Claims = make([]AttestationClaim, MaxAttestationClaims+1)
				for i := range a.Claims {
					a.Claims[i] = AttestationClaim("c" + string(rune('a'+i)))
				}
				r.Identities[0].Attestation = a
			},
			wantErr: "attestation.claims exceeds maximum of 16",
		},
		{
			name: "attestation duplicate claims rejected",
			mutate: func(r *IdentityMatchRequest) {
				a := sampleAttestation()
				a.Claims = []AttestationClaim{AttestationClaimUniqueHuman, AttestationClaimUniqueHuman}
				r.Identities[0].Attestation = a
			},
			wantErr: "duplicate",
		},
		{
			name: "attestation unrecognized claim accepted (additive set)",
			mutate: func(r *IdentityMatchRequest) {
				a := sampleAttestation()
				a.Claims = []AttestationClaim{AttestationClaim("age_over_25")}
				r.Identities[0].Attestation = a
			},
		},
		{
			name: "too many sealed_credentials rejected",
			mutate: func(r *IdentityMatchRequest) {
				r.SealedCredentials = make([]SealedCredential, MaxSealedCredentials+1)
				for i := range r.SealedCredentials {
					r.SealedCredentials[i] = SealedCredential{AudienceKID: "k" + string(rune('a'+i)), Payload: "p"}
				}
			},
			wantErr: "sealed_credentials exceeds maximum of 8",
		},
		{
			name: "sealed_credential missing audience_kid rejected",
			mutate: func(r *IdentityMatchRequest) {
				r.SealedCredentials = []SealedCredential{{Payload: "p"}}
			},
			wantErr: "audience_kid is required",
		},
		{
			name: "sealed_credential oversized audience_kid rejected",
			mutate: func(r *IdentityMatchRequest) {
				r.SealedCredentials = []SealedCredential{{AudienceKID: strings.Repeat("a", MaxAudienceKIDLength+1), Payload: "p"}}
			},
			wantErr: "audience_kid exceeds",
		},
		{
			name: "sealed_credential missing payload rejected",
			mutate: func(r *IdentityMatchRequest) {
				r.SealedCredentials = []SealedCredential{{AudienceKID: "k1"}}
			},
			wantErr: "payload is required",
		},
		{
			name: "sealed_credential oversized payload rejected",
			mutate: func(r *IdentityMatchRequest) {
				r.SealedCredentials = []SealedCredential{{AudienceKID: "k1", Payload: strings.Repeat("a", MaxSealedCredentialPayload+1)}}
			},
			wantErr: "payload exceeds",
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

func TestSafeRequestIDForEcho(t *testing.T) {
	assert.Equal(t, "req-1", SafeRequestIDForEcho("req-1"))
	assert.Empty(t, SafeRequestIDForEcho(""))
	assert.Empty(t, SafeRequestIDForEcho("bad/id"))
	assert.Empty(t, SafeRequestIDForEcho(strings.Repeat("a", MaxIDLength+1)))
}

// TestSafeRequestIDForEcho_RejectsC0AndDEL pins the contract that
// SafeRequestIDForEcho strips every C0 control (0x00–0x1F) and DEL
// (0x7F). slog's text handler does not escape control bytes, so a
// malicious request_id containing NUL / BEL / ANSI CSI escape sequences
// could otherwise hijack an operator's terminal when viewing raw logs.
func TestSafeRequestIDForEcho_RejectsC0AndDEL(t *testing.T) {
	cases := map[string]string{
		"NUL":       "\x00",
		"BEL":       "\x07",
		"backspace": "\x08",
		"VT":        "\x0B",
		"FF":        "\x0C",
		"SO":        "\x0E",
		"ANSI_CSI":  "\x1B[2J",
		"DEL":       "\x7F",
	}
	for name, sentinel := range cases {
		t.Run(name, func(t *testing.T) {
			assert.Empty(t, SafeRequestIDForEcho("req-"+sentinel+"id"),
				"control byte must blank the echo")
		})
	}
}

// ValidateContextRequest must enforce the ladder-level constraints the JSON
// Schema encodes on ContextSignals, ArtifactRefs, Artifact, and Geo — the
// audit called out that these Validate methods existed but were never wired
// into the request-entry path. Each case flips exactly one field so a
// regression that stops rejecting one dimension surfaces alone.
func TestValidateContextRequest_LadderConstraints(t *testing.T) {
	valid := func() ContextMatchRequest {
		return ContextMatchRequest{
			Type:           TypeContextMatchRequest,
			RequestID:      "req-1",
			PropertyRID:    "prop-1",
			PropertyType:   PropertyTypeWebsite,
			PlacementID:    "placement-1",
			SellerAgentURL: "https://seller.example.com/agent",
		}
	}

	oversizedTopics := make([]string, MaxTopics+1)
	for i := range oversizedTopics {
		oversizedTopics[i] = "t"
	}

	cases := []struct {
		name    string
		mutate  func(*ContextMatchRequest)
		wantErr string
	}{
		{name: "baseline ok", mutate: func(*ContextMatchRequest) {}},
		{
			name: "context_signals sentiment enum rejected",
			mutate: func(r *ContextMatchRequest) {
				r.ContextSignals = &ContextSignals{Sentiment: "wildly_positive"}
			},
			wantErr: "sentiment",
		},
		{
			name: "context_signals topics over cap rejected",
			mutate: func(r *ContextMatchRequest) {
				r.ContextSignals = &ContextSignals{Topics: oversizedTopics}
			},
			wantErr: "topics",
		},
		{
			name: "artifact_refs missing value rejected",
			mutate: func(r *ContextMatchRequest) {
				r.ArtifactRefs = []ArtifactRef{{Type: ArtifactRefTypeURL, Value: ""}}
			},
			wantErr: "artifact_ref.value",
		},
		{
			name: "artifact missing property_rid rejected",
			mutate: func(r *ContextMatchRequest) {
				r.Artifact = &Artifact{ArtifactID: "a-1"}
			},
			wantErr: "property_rid",
		},
		{
			name: "geo.country malformed rejected",
			mutate: func(r *ContextMatchRequest) {
				r.Geo = map[string]any{"country": "usa"}
			},
			wantErr: "geo.country",
		},
		{
			name: "geo.metro missing system rejected",
			mutate: func(r *ContextMatchRequest) {
				r.Geo = map[string]any{"metro": map[string]any{"value": "501"}}
			},
			wantErr: "geo.metro.system",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := valid()
			tc.mutate(&req)
			err := ValidateContextRequest(&req)
			if tc.wantErr == "" {
				require.NoError(t, err)
				return
			}
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.wantErr)
		})
	}
}

func TestValidateIdentityRequest_Consent(t *testing.T) {
	base := func() IdentityMatchRequest {
		return IdentityMatchRequest{
			Type:           TypeIdentityMatchRequest,
			RequestID:      "id-1",
			SellerAgentURL: "https://seller.example.com/agent",
			Identities:     []IdentityToken{{UserToken: "tok", UIDType: UIDTypeUID2}},
		}
	}

	cases := []struct {
		name    string
		consent map[string]any
		wantErr string
	}{
		{name: "no consent object accepted", consent: nil},
		{name: "gdpr false accepted", consent: map[string]any{"gdpr": false}},
		{
			name:    "gdpr true with tcf accepted",
			consent: map[string]any{"gdpr": true, "tcf_consent": "CO..."},
		},
		{
			name:    "gdpr true with gpp accepted",
			consent: map[string]any{"gdpr": true, "gpp": "DBABMA~..."},
		},
		{
			name:    "gdpr true without consent string rejected",
			consent: map[string]any{"gdpr": true},
			wantErr: "consent.gdpr is true",
		},
		{
			name:    "gdpr true with empty tcf rejected",
			consent: map[string]any{"gdpr": true, "tcf_consent": ""},
			wantErr: "consent.gdpr is true",
		},
		{
			name:    "gdpr non-boolean rejected",
			consent: map[string]any{"gdpr": "yes"},
			wantErr: "consent.gdpr: must be boolean",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := base()
			req.Consent = tc.consent
			err := ValidateIdentityRequest(&req)
			if tc.wantErr == "" {
				require.NoError(t, err)
				return
			}
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.wantErr)
		})
	}
}

func TestValidateIdentityRequest_AllValidUIDTypes(t *testing.T) {
	types := []UIDType{
		UIDTypeRampID, UIDTypeRampIDDerived, UIDTypeID5, UIDTypeUID2,
		UIDTypeEUID, UIDTypePairID, UIDTypeMAID, UIDTypeHashedEmail,
		UIDTypePublisherFirstParty, UIDTypeWorldIDNullifier, UIDTypeOther,
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
