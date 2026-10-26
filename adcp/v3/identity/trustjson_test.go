package identity

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestValidateTrustRecordExamples(t *testing.T) {
	files, _ := filepath.Glob("testdata/examples/*.json")
	for _, f := range files {
		if strings.Contains(f, "acknowledgements") {
			continue
		}
		data, err := os.ReadFile(f) // #nosec G304 -- test reads examples matched by a fixed glob under testdata.
		require.NoError(t, err)
		rec, err := ValidateTrustRecord(data, TrustRecordOptions{Mode: Authoring})
		require.NoError(t, err, f)
		require.NotNil(t, rec)
	}
}

// TestValidateTrustRecordRules ports the negative and positive cases of the
// spec's validator (specs/brand-identity-trust-split/validate.cjs).
func TestValidateTrustRecordRules(t *testing.T) {
	invalid := map[string]string{
		"both acknowledgements and acknowledgements_url":        `{"acknowledgements":[],"acknowledgements_url":"https://a.example/x"}`,
		"unknown top-level key":                                 `{"agents":[{"url":"https://a.example/mcp","roles":["adcp:sales"]}],"properties":[]}`,
		"grant carrying keys":                                   `{"grants":[{"grantee":"a.example","scopes":["adcp:governance"],"agents":[{"url":"https://a.example/x","jwks_uri":"https://a.example/j"}]}]}`,
		"empty record":                                          `{}`,
		"http agent url":                                        `{"agents":[{"url":"http://a.example/mcp","roles":["adcp:sales"]}]}`,
		"unknown adcp role":                                     `{"agents":[{"url":"https://a.example/mcp","roles":["adcp:seller"]}]}`,
		"unnamespaced role":                                     `{"agents":[{"url":"https://a.example/mcp","roles":["sales"]}]}`,
		"unknown adcp scope":                                    `{"grants":[{"grantee":"a.example","scopes":["adcp:everything"]}]}`,
		"acknowledgement without via":                           `{"acknowledgements":[{"grantor":"p.example","agent_url":"https://a.example/mcp"}]}`,
		"agent carrying jwks_uri":                               `{"agents":[{"url":"https://a.example/mcp","roles":["adcp:sales"],"jwks_uri":"https://a.example/.well-known/jwks.json"}]}`,
		"two agents sharing an origin":                          `{"agents":[{"url":"https://a.example/one","roles":["adcp:sales"]},{"url":"https://a.example/two","roles":["adcp:buying"]}]}`,
		"two agents sharing an origin through a root dot":       `{"agents":[{"url":"https://a.example./one","roles":["adcp:sales"]},{"url":"https://a.example/two","roles":["adcp:buying"]}]}`,
		"agent url with an empty DNS label":                     `{"agents":[{"url":"https://a.example../x","roles":["adcp:buying"]}]}`,
		"agent carrying a key pin":                              `{"agents":[{"url":"https://a.example/mcp","roles":["adcp:buying"],"key_thumbprints":["NzbLsXh8uDCcd-6MNwXF4W_7noWXFZAfHkxZsRGC9Xs"]}]}`,
		"signing_profiles is not part of the schema":            `{"agents":[{"url":"https://a.example/mcp","roles":["adcp:buying"],"profiles":{"adcp":{"signing_profiles":["web-bot-auth"]}}}]}`,
		"acknowledgement for an agent the record does not list": `{"agents":[{"url":"https://a.example/mcp","roles":["adcp:sales"]}],"acknowledgements":[{"grantor":"p.example","agent_url":"https://b.example/mcp","via":"adcp:adagents"}]}`,
		// Further schema rules.
		"pointer with extra members":           `{"authoritative_location":"https://h.example/t.json","agents":[]}`,
		"pointer over http":                    `{"authoritative_location":"http://h.example/t.json"}`,
		"empty roles":                          `{"agents":[{"url":"https://a.example/mcp","roles":[]}]}`,
		"duplicate roles":                      `{"agents":[{"url":"https://a.example/mcp","roles":["adcp:sales","adcp:sales"]}]}`,
		"bad protocol":                         `{"agents":[{"url":"https://a.example/mcp","roles":["adcp:sales"],"protocols":["grpc"]}]}`,
		"bad country":                          `{"agents":[{"url":"https://a.example/mcp","roles":["adcp:sales"],"countries":["usa"]}]}`,
		"bad profile namespace":                `{"agents":[{"url":"https://a.example/mcp","roles":["adcp:sales"],"profiles":{"Bad_NS":{}}}]}`,
		"unknown right use":                    `{"agents":[{"url":"https://a.example/mcp","roles":["adcp:rights"],"profiles":{"adcp":{"available_uses":["teleport"]}}}]}`,
		"grantee is not a domain":              `{"grants":[{"grantee":"https://a.example","scopes":["adcp:all"]}]}`,
		"grant valid_until not a date":         `{"grants":[{"grantee":"a.example","scopes":["adcp:all"],"valid_until":"soon"}]}`,
		"grant brands pattern":                 `{"grants":[{"grantee":"a.example","scopes":["adcp:all"],"profiles":{"adcp":{"brands":["Nova Motors"]}}}]}`,
		"grant agents empty":                   `{"grants":[{"grantee":"a.example","scopes":["adcp:all"],"agents":[]}]}`,
		"unknown grant source":                 `{"agents":[{"url":"https://a.example/mcp","roles":["adcp:sales"]}],"acknowledgements":[{"grantor":"p.example","agent_url":"https://a.example/mcp","via":"adcp:email"}]}`,
		"bad delegation type":                  `{"agents":[{"url":"https://a.example/mcp","roles":["adcp:sales"]}],"acknowledgements":[{"grantor":"p.example","agent_url":"https://a.example/mcp","via":"adcp:adagents","profiles":{"adcp":{"delegation_type":"resold"}}}]}`,
		"exclusive_grants not a boolean":       `{"grants":[],"exclusive_grants":"yes"}`,
		"ext member not an object":             `{"grants":[],"ext":{"vendor":1}}`,
		"onboarding without mode":              `{"profiles":{"adcp":{"onboarding":{"message":"hi"}}}}`,
		"onboarding with seller mode":          `{"profiles":{"adcp":{"onboarding":{"mode":"open"}}}}`,
		"unknown requirement kind (authoring)": `{"profiles":{"adcp":{"onboarding":{"mode":"application","requirements":[{"kind":"future_kind"}]}}}}`,
		"relying party missing issuer":         `{"profiles":{"adcp":{"identity_relying_parties":[{"relying_party_id":"rp_1"}]}}}`,
		"duplicate JSON key":                   `{"grants":[],"grants":[]}`,
		"not an object":                        `[]`,
	}
	for name, doc := range invalid {
		_, err := ValidateTrustRecord([]byte(doc), TrustRecordOptions{Mode: Authoring})
		require.Error(t, err, name)
		require.True(t, errors.Is(err, ErrInvalidTrustRecord), name)
	}
	valid := map[string]string{ // #nosec G101 -- trust.json fixtures, no credentials.
		"distinct origins, acknowledgement matched by origin": `{"agents":[{"url":"https://buyer.a.example/mcp","roles":["adcp:buying"]},{"url":"https://Gov.A.Example:443/mcp","roles":["adcp:governance"]}],"acknowledgements":[{"grantor":"p.example","agent_url":"https://gov.a.example/other-path","via":"adcp:adagents"}]}`,
		"pointer":                    `{"authoritative_location":"https://hosted.example/brand/trust.json","last_updated":"2026-10-01T00:00:00Z"}`,
		"non-adcp namespaced tokens": `{"agents":[{"url":"https://a.example/mcp","roles":["acme:relay"]}],"grants":[{"grantee":"b.example","scopes":["*","acme:thing"]}]}`,
		"buyer posture":              `{"agents":[{"url":"https://a.example/mcp","roles":["adcp:buying"],"profiles":{"adcp":{"onboarding":{"mode":"considers_unsolicited","requirements":[{"kind":"aao_verified","role":"media-buy"}],"extra":true}}}}]}`,
	}
	for name, doc := range valid {
		_, err := ValidateTrustRecord([]byte(doc), TrustRecordOptions{Mode: Authoring})
		require.NoError(t, err, name)
	}
}

func TestValidateTrustRecordModesAndOwner(t *testing.T) {
	unknownKind := `{"agents":[{"url":"https://buyer.example.com/mcp","roles":["adcp:buying"],"profiles":{"adcp":{"onboarding":{"mode":"application","requirements":[{"kind":"future_kind"}]}}}}]}`
	_, err := ValidateTrustRecord([]byte(unknownKind), TrustRecordOptions{Mode: Authoring})
	require.Error(t, err)
	rec, err := ValidateTrustRecord([]byte(unknownKind), TrustRecordOptions{Mode: Reading})
	require.NoError(t, err, "readers keep unknown, advisory requirement kinds")
	require.Equal(t, AgentOnboardingRequirementKind("future_kind"), rec.Agents[0].Profiles.Adcp.Onboarding.Requirements[0].Kind)
	oddFields := strings.Replace(unknownKind, `{"kind":"future_kind"}`, `{"kind":"future_kind","verification_modes":"not-an-array"}`, 1)
	_, err = ValidateTrustRecord([]byte(oddFields), TrustRecordOptions{Mode: Reading})
	require.NoError(t, err, "a reader does not judge an unknown kind's other members")
	_, err = ValidateTrustRecord([]byte(`{"agents":[{"url":"https://buyer.example.com","roles":["adcp:buying"],"Roles":["adcp:sales"]}]}`), TrustRecordOptions{Mode: Reading})
	require.Error(t, err, "keys that differ only in case are duplicates")

	owned := `{"agents":[{"url":"https://buyer.example.com/mcp","roles":["adcp:buying"]}],"grants":[{"grantee":"agency.com","scopes":["adcp:media_buying"]}]}`
	_, err = ValidateTrustRecord([]byte(owned), TrustRecordOptions{Owner: "example.com"})
	require.NoError(t, err)
	_, err = ValidateTrustRecord([]byte(owned), TrustRecordOptions{Owner: "www.example.com"})
	require.NoError(t, err, "the owner is compared as a registrable domain")
	for name, tc := range map[string]struct{ doc, owner string }{
		"agent under another operator":      {owned, "other.com"},
		"owner has no registrable domain":   {owned, "co.uk"},
		"grantee has no registrable domain": {`{"grants":[{"grantee":"github.io","scopes":["adcp:media_buying"]}]}`, "example.com"},
		"grantee is a host":                 {`{"grants":[{"grantee":"buy.agency.com","scopes":["adcp:media_buying"]}]}`, "example.com"},
		"agent on a platform suffix":        {`{"agents":[{"url":"https://tenant.github.io/mcp","roles":["adcp:buying"]}]}`, "github.io"},
	} {
		_, err := ValidateTrustRecord([]byte(tc.doc), TrustRecordOptions{Owner: tc.owner})
		require.Error(t, err, name)
	}
	// An author must write a registrable domain (the error says to narrow
	// with agents[]). A reader keeps a record
	// whose grantee is a host or a public suffix: that grant covers no
	// claim (L2 Grants in trust.json).
	for _, owner := range []string{"example.com", "www.example.com"} {
		for _, grantee := range []string{"buy.agency.com", "github.io"} {
			_, err = ValidateTrustRecord([]byte(`{"grants":[{"grantee":"`+grantee+`","scopes":["adcp:media_buying"]}]}`), TrustRecordOptions{Owner: owner})
			require.ErrorContains(t, err, "agents[]", "owner %q, grantee %s", owner, grantee)
			_, err = ValidateTrustRecord([]byte(`{"grants":[{"grantee":"`+grantee+`","scopes":["adcp:media_buying"]}]}`), TrustRecordOptions{Mode: Reading, Owner: owner})
			require.NoError(t, err, "a reader keeps the record: owner %q, grantee %s", owner, grantee)
		}
		_, err = ValidateTrustRecord([]byte(`{"grants":[{"grantee":"alice.github.io","scopes":["adcp:media_buying"]}]}`), TrustRecordOptions{Owner: owner})
		require.NoError(t, err, "alice.github.io is a registrable domain under a private-section suffix")
	}
	big := `{"grants":[],"ext":{"v":{"pad":"` + strings.Repeat("x", TrustRecordMaxBytes) + `"}}}`
	_, err = ValidateTrustRecord([]byte(big), TrustRecordOptions{})
	require.Error(t, err)
	deep := `{"ext":{"v":` + strings.Repeat(`{"a":`, 40) + `1` + strings.Repeat(`}`, 40) + `}}`
	_, err = ValidateTrustRecord([]byte(deep), TrustRecordOptions{})
	require.Error(t, err, "nesting deeper than 32")
}

func TestMarshalTrustRecordAuthoring(t *testing.T) {
	exclusive := true
	rec := &TrustRecord{
		Agents: []TrustAgent{{URL: "https://buyer.example.com/mcp", Roles: []string{RoleBuying}, Profiles: &Profiles[TrustAgentAdcpProfile]{
			Adcp: &TrustAgentAdcpProfile{Onboarding: &BuyerOnboardingPosture{Mode: BuyerOnboardingConsidersUnsolicited}},
		}}},
		Grants: []TrustGrant{{Grantee: "agency.com", Scopes: []string{ScopeMediaBuying},
			Agents:   []GrantedAgent{{URL: "https://buy.agency.com/mcp"}},
			Profiles: &Profiles[TrustGrantAdcpProfile]{Adcp: &TrustGrantAdcpProfile{Brands: []string{"nova"}}}}},
		ExclusiveGrants: &exclusive,
	}
	data, err := MarshalTrustRecord(rec, "example.com")
	require.NoError(t, err)
	back, err := ValidateTrustRecord(data, TrustRecordOptions{Owner: "example.com"})
	require.NoError(t, err)
	require.Equal(t, rec.Grants[0].Agents, back.Grants[0].Agents)

	rec.Agents = append(rec.Agents, TrustAgent{URL: "https://BUYER.example.com:443/other", Roles: []string{RoleSales}})
	_, err = MarshalTrustRecord(rec, "example.com")
	require.Error(t, err, "one agent per origin")
}

func TestTrustRecordAgentMatch(t *testing.T) {
	rec, err := ValidateTrustRecord([]byte(`{"agents":[{"url":"https://buyer.example.com/mcp","roles":["adcp:buying"]},{"url":"https://gov.example.com","roles":["adcp:governance"]}]}`), TrustRecordOptions{})
	require.NoError(t, err)
	a, err := rec.Agent("https://BUYER.example.com:443/anything", RoleBuying)
	require.NoError(t, err)
	require.Equal(t, "https://buyer.example.com/mcp", a.URL)
	_, err = rec.Agent("https://buyer.example.com", RoleSales)
	require.Error(t, err, "wrong role")
	_, err = rec.Agent("https://other.example.com", RoleBuying)
	require.Error(t, err, "not listed")
	_, err = rec.Agent("https://buyer.example.com:8443", RoleBuying)
	require.Error(t, err, "a different port is a different origin")
}
