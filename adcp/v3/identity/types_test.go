package identity

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func loadSchema(t *testing.T, path string) map[string]any {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata/schemas", path)) // #nosec G304 -- fixed testdata paths.
	require.NoError(t, err)
	var m map[string]any
	require.NoError(t, json.Unmarshal(data, &m))
	return m
}

func dig(t *testing.T, m map[string]any, path ...any) map[string]any {
	t.Helper()
	var cur any = m
	for _, p := range path {
		switch k := p.(type) {
		case string:
			cur = cur.(map[string]any)[k]
		case int:
			cur = cur.([]any)[k]
		}
		require.NotNil(t, cur, "%v", path)
	}
	return cur.(map[string]any)
}

func propertyNames(t *testing.T, schema map[string]any) []string {
	t.Helper()
	props, ok := schema["properties"].(map[string]any)
	require.True(t, ok)
	var out []string
	for k := range props {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func jsonFields(typ reflect.Type) []string {
	var out []string
	for i := 0; i < typ.NumField(); i++ {
		f := typ.Field(i)
		if f.Anonymous {
			continue
		}
		name := strings.Split(f.Tag.Get("json"), ",")[0]
		if name != "" && name != "-" {
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out
}

// TestTypesMatchPinnedSchemas is the drift check for the hand-written
// identity types: each struct's JSON members equal its pinned schema's
// properties.
func TestTypesMatchPinnedSchemas(t *testing.T) {
	trust := loadSchema(t, "trust/v1/trust.json")
	defs := dig(t, trust, "definitions")
	union := func(a, b []string) []string {
		set := map[string]struct{}{}
		for _, s := range append(a, b...) {
			set[s] = struct{}{}
		}
		var out []string
		for s := range set {
			out = append(out, s)
		}
		sort.Strings(out)
		return out
	}
	withOnboarding := func(names []string) []string { return union(names, []string{"onboarding"}) }
	cases := []struct {
		name   string
		typ    reflect.Type
		schema []string
	}{
		{"TrustRecord", reflect.TypeOf(TrustRecord{}), union(propertyNames(t, dig(t, trust, "oneOf", 0)), propertyNames(t, dig(t, trust, "oneOf", 1)))},
		{"TrustAgent", reflect.TypeOf(TrustAgent{}), propertyNames(t, dig(t, defs, "agent"))},
		{"TrustGrant", reflect.TypeOf(TrustGrant{}), propertyNames(t, dig(t, defs, "grant"))},
		{"GrantedAgent", reflect.TypeOf(GrantedAgent{}), propertyNames(t, dig(t, defs, "granted_agent"))},
		{"TrustAcknowledgement", reflect.TypeOf(TrustAcknowledgement{}), propertyNames(t, dig(t, defs, "acknowledgement"))},
		// The onboarding slots are composed from PR #8170 (UPSTREAM.json).
		{"TrustAgentAdcpProfile", reflect.TypeOf(TrustAgentAdcpProfile{}), withOnboarding(propertyNames(t, dig(t, defs, "agent", "properties", "profiles", "allOf", 1, "properties", "adcp")))},
		{"TrustRecordAdcpProfile", reflect.TypeOf(TrustRecordAdcpProfile{}), withOnboarding(propertyNames(t, dig(t, defs, "record_profiles", "allOf", 1, "properties", "adcp")))},
		{"TrustGrantAdcpProfile", reflect.TypeOf(TrustGrantAdcpProfile{}), propertyNames(t, dig(t, defs, "grant", "properties", "profiles", "allOf", 1, "properties", "adcp"))},
		{"TrustAcknowledgementAdcpProfile", reflect.TypeOf(TrustAcknowledgementAdcpProfile{}), propertyNames(t, dig(t, defs, "acknowledgement", "properties", "profiles", "allOf", 1, "properties", "adcp"))},
		{"IdentityRelyingParty", reflect.TypeOf(IdentityRelyingParty{}), propertyNames(t, loadSchema(t, "core/identity-relying-party.json"))},
		{"AgentOnboarding", reflect.TypeOf(AgentOnboarding{}), propertyNames(t, loadSchema(t, "core/agent-onboarding.json"))},
		{"AgentOnboardingTier", reflect.TypeOf(AgentOnboardingTier{}), propertyNames(t, loadSchema(t, "core/agent-onboarding-tier.json"))},
		{"AgentOnboardingRequirement", reflect.TypeOf(AgentOnboardingRequirement{}), propertyNames(t, loadSchema(t, "core/agent-onboarding-requirement.json"))},
		{"BuyerOnboardingPosture", reflect.TypeOf(BuyerOnboardingPosture{}), propertyNames(t, loadSchema(t, "core/buyer-onboarding-posture.json"))},
		{"PrincipalRelationship", reflect.TypeOf(PrincipalRelationship{}), propertyNames(t, loadSchema(t, "core/principal-relationship.json"))},
		{"PrincipalRelationshipSetup", reflect.TypeOf(PrincipalRelationshipSetup{}), propertyNames(t, dig(t, loadSchema(t, "core/principal-relationship.json"), "properties", "setup"))},
		{"AgentPermissionDeniedDetails", reflect.TypeOf(AgentPermissionDeniedDetails{}), propertyNames(t, loadSchema(t, "error-details/agent-permission-denied.json"))},
	}
	for _, tc := range cases {
		require.Equal(t, tc.schema, jsonFields(tc.typ), tc.name)
	}

	// Enumerations.
	enum := func(path string) []string {
		var out []string
		for _, v := range loadSchema(t, path)["enum"].([]any) {
			out = append(out, v.(string))
		}
		sort.Strings(out)
		return out
	}
	str := func(vals ...string) []string { sort.Strings(vals); return vals }
	require.Equal(t, enum("enums/agent-onboarding-mode.json"), str(string(AgentOnboardingOpen), string(AgentOnboardingApplication), string(AgentOnboardingInviteOnly)))
	require.Equal(t, enum("enums/buyer-onboarding-mode.json"), str(string(BuyerOnboardingConsidersUnsolicited), string(BuyerOnboardingApplication), string(BuyerOnboardingInviteOnly)))
	var kinds []string
	for _, k := range enum("enums/agent-onboarding-requirement-kind.json") {
		require.True(t, KnownRequirementKind(AgentOnboardingRequirementKind(k)), k)
		kinds = append(kinds, k)
	}
	require.Len(t, kinds, 9)
	var reasons []string
	for _, v := range dig(t, loadSchema(t, "error-details/agent-permission-denied.json"), "properties", "reason")["enum"].([]any) {
		reasons = append(reasons, v.(string))
	}
	require.ElementsMatch(t, []string{PermissionDeniedSandboxOnly, PermissionDeniedGrantAbsent}, reasons)
	var statuses []string
	for _, v := range dig(t, loadSchema(t, "core/principal-relationship.json"), "properties", "status")["enum"].([]any) {
		statuses = append(statuses, v.(string))
	}
	require.ElementsMatch(t, []string{string(RelationshipPending), string(RelationshipActive), string(RelationshipSuspended), string(RelationshipBlocked)}, statuses)
	var changed []string
	for _, v := range dig(t, loadSchema(t, "core/principal-changed-webhook.json"), "properties", "reason")["enum"].([]any) {
		changed = append(changed, v.(string))
	}
	require.Contains(t, changed, PrincipalChangedRelationshipChanged)
	require.Contains(t, changed, PrincipalChangedRelationshipExpiring)
	roles := dig(t, defs, "role", "anyOf", 1)["enum"].([]any)
	require.Len(t, roles, len(adcpRoles))
	for _, r := range roles {
		require.Contains(t, adcpRoles, r.(string))
	}
	scopes := dig(t, defs, "scope", "anyOf", 2)["enum"].([]any)
	require.Len(t, scopes, len(adcpScopes))
	for _, s := range scopes {
		require.Contains(t, adcpScopes, s.(string))
	}
	// trust.json v1 carries no keys.
	require.NotContains(t, propertyNames(t, dig(t, defs, "agent")), "jwks_uri")
}

func TestTrustRecordExamplesRoundTrip(t *testing.T) {
	files, _ := filepath.Glob("testdata/examples/*.json")
	require.NotEmpty(t, files)
	for _, f := range files {
		if strings.Contains(f, "acknowledgements") {
			continue // a trust-acknowledgements.json document
		}
		data, err := os.ReadFile(f) // #nosec G304 -- test reads examples matched by a fixed glob under testdata.
		require.NoError(t, err)
		var rec TrustRecord
		require.NoError(t, json.Unmarshal(data, &rec), f)
		out, err := json.Marshal(rec)
		require.NoError(t, err)
		var a, b any
		require.NoError(t, json.Unmarshal(data, &a))
		require.NoError(t, json.Unmarshal(out, &b))
		require.Equal(t, a, b, f)
	}
}

func TestProfilesKeepOtherNamespaces(t *testing.T) {
	var p Profiles[TrustGrantAdcpProfile]
	require.NoError(t, json.Unmarshal([]byte(`{"adcp":{"brands":["nova"]},"x-proto":{"k":1}}`), &p))
	require.Equal(t, []string{"nova"}, p.Adcp.Brands)
	require.JSONEq(t, `{"k":1}`, string(p.Other["x-proto"]))
	out, err := json.Marshal(p)
	require.NoError(t, err)
	require.JSONEq(t, `{"adcp":{"brands":["nova"]},"x-proto":{"k":1}}`, string(out))
	require.Error(t, json.Unmarshal([]byte(`null`), &p))
}

func TestCapabilityExtensions(t *testing.T) {
	profiles, err := AdvertisedRequestSigningProfiles([]byte(`{"request_signing":{"supported":true,"profiles":["adcp/request-signing/v1","web-bot-auth"]}}`))
	require.NoError(t, err)
	require.Equal(t, []string{"adcp/request-signing/v1", "web-bot-auth"}, profiles)
	profiles, err = AdvertisedRequestSigningProfiles([]byte(`{"request_signing":{"supported":true}}`))
	require.NoError(t, err)
	require.Equal(t, []string{"adcp/request-signing/v1"}, profiles, "omitted means the RFC 9421 profile only")
	profiles, err = AdvertisedRequestSigningProfiles([]byte(`{"request_signing":{"supported":true,"profiles":[]}}`))
	require.NoError(t, err)
	require.Empty(t, profiles, "an explicit empty list advertises none")
	_, err = AdvertisedRequestSigningProfiles([]byte(`not json`))
	require.Error(t, err)

	var acct AccountCapabilities
	require.NoError(t, json.Unmarshal([]byte(`{"supported_billing":["operator"],"sandbox":true,"agent_onboarding":{"sandbox":{"mode":"open"},"billing":{"operator":{"mode":"application","url":"https://s.example.com/apply","requirements":[{"kind":"agent_identity"},{"kind":"future_kind"}]}},"message":"m"}}`), &acct))
	require.Equal(t, []string{"operator"}, acct.SupportedBilling)
	require.Equal(t, AgentOnboardingOpen, acct.AgentOnboarding.Sandbox.Mode)
	reqs := acct.AgentOnboarding.Billing["operator"].Requirements
	require.Equal(t, RequirementAgentIdentity, reqs[0].Kind)
	require.Equal(t, AgentOnboardingRequirementKind("future_kind"), reqs[1].Kind, "unknown kinds are kept for message/url fallback")
	require.False(t, KnownRequirementKind(reqs[1].Kind))
	out, _ := json.Marshal(acct)
	require.Contains(t, string(out), `"agent_onboarding"`)
	require.Contains(t, string(out), `"supported_billing"`)

	b, _ := json.Marshal(GrantAbsentDetails())
	require.JSONEq(t, `{"scope":"agent","reason":"grant_absent"}`, string(b))
	var rel PrincipalRelationship
	require.NoError(t, json.Unmarshal([]byte(`{"status":"pending","setup":{"message":"sign the IO","url":"https://s.example.com/io"},"expires_at":"2027-01-01T00:00:00Z"}`), &rel))
	require.Equal(t, RelationshipPending, rel.Status)
	require.Equal(t, "sign the IO", rel.Setup.Message)
}
