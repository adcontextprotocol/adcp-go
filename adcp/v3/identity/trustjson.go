package identity

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/adcontextprotocol/adcp-go/adcp/v3"
	"github.com/adcontextprotocol/adcp-go/adcp/v3/internal/psl"
	"github.com/adcontextprotocol/adcp-go/adcp/v3/internal/strictjson"
	"github.com/adcontextprotocol/adcp-go/adcp/v3/signing"
)

// TrustRecordMaxBytes is the trust-record body cap (security.mdx, Web Bot
// Auth identities).
const TrustRecordMaxBytes = 2 << 20

// ErrInvalidTrustRecord is wrapped by every trust record validation failure.
var ErrInvalidTrustRecord = errors.New("identity: invalid trust record")

// ValidationMode selects how strictly ValidateTrustRecord treats values the
// pinned schema marks as extensible.
type ValidationMode int

const (
	// Authoring rejects anything outside the pinned schema, including
	// onboarding requirement kinds this SDK does not know.
	Authoring ValidationMode = iota
	// Reading keeps unknown onboarding requirement kinds, which are
	// advisory and never machine-satisfiable, without relaxing any
	// authority-bearing member. One rule differs by mode: Authoring
	// requires every grantee to be a registrable domain, while Reading
	// keeps a record whose grantee is not one (a host, or a public
	// suffix). That grant covers no claim, and its scopes still count for
	// exclusive_grants (L2 Grants in trust.json).
	Reading
)

// TrustRecordOptions configures ValidateTrustRecord.
type TrustRecordOptions struct {
	Mode ValidationMode
	// Owner, when set, is the registrable domain (or a host under it) the
	// record speaks for: the host serving it. Every agent origin must then
	// be HTTPS under that registrable domain.
	Owner string
}

// ValidateTrustRecord strict-parses data (duplicate keys rejected, nesting
// at most 32) and validates it against the pinned trust/v1/trust.json
// schema, plus the rules a schema cannot express: one agent per canonical
// origin, no keys anywhere, every acknowledgement naming a listed agent, and
// with Owner set, origin ownership. It is the authoring and validation
// helper for publishers, and the reader for TrustRecordEvaluator.
func ValidateTrustRecord(data []byte, opts TrustRecordOptions) (*TrustRecord, error) {
	if len(data) > TrustRecordMaxBytes {
		return nil, fmt.Errorf("%w: larger than 2 MiB", ErrInvalidTrustRecord)
	}
	if err := strictjson.Validate(data, 0); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidTrustRecord, err)
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	var tree any
	if err := dec.Decode(&tree); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidTrustRecord, err)
	}
	v := &trustValidator{mode: opts.Mode}
	v.record(tree)
	if v.err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidTrustRecord, v.err)
	}
	if opts.Mode == Reading {
		// The validator may have trimmed unknown requirement kinds.
		var err error
		if data, err = json.Marshal(tree); err != nil {
			return nil, fmt.Errorf("%w: %v", ErrInvalidTrustRecord, err)
		}
	}
	var rec TrustRecord
	if err := json.Unmarshal(data, &rec); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidTrustRecord, err)
	}
	if err := rec.checkRules(opts.Owner, opts.Mode); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidTrustRecord, err)
	}
	return &rec, nil
}

// MarshalTrustRecord validates rec for owner in Authoring mode and returns
// its JSON, the bytes to publish at https://{owner}/.well-known/trust.json.
func MarshalTrustRecord(rec *TrustRecord, owner string) ([]byte, error) {
	data, err := json.Marshal(rec)
	if err != nil {
		return nil, err
	}
	if _, err := ValidateTrustRecord(data, TrustRecordOptions{Mode: Authoring, Owner: owner}); err != nil {
		return nil, err
	}
	return data, nil
}

// checkRules applies the rules beyond the schema.
func (r *TrustRecord) checkRules(owner string, mode ValidationMode) error {
	if r.AuthoritativeLocation != "" {
		return nil
	}
	var operator string
	if owner != "" {
		var ok bool
		if operator, ok = psl.RegistrableDomain(owner); !ok {
			return errors.New("record owner has no registrable domain")
		}
	}
	origins := map[string]struct{}{}
	for i, a := range r.Agents {
		if u, err := url.Parse(a.URL); err != nil || u.User != nil {
			return fmt.Errorf("agents[%d].url must not carry userinfo", i)
		}
		origin, err := signing.CanonicalOrigin(a.URL)
		if err != nil {
			return fmt.Errorf("agents[%d].url has no canonical origin", i)
		}
		if _, dup := origins[origin]; dup {
			return fmt.Errorf("agents[%d]: a trust record lists one agent per origin", i)
		}
		origins[origin] = struct{}{}
		if operator != "" {
			if d, ok := psl.RegistrableDomain(origin); !ok || d != operator || !strings.HasPrefix(origin, "https://") {
				return fmt.Errorf("agents[%d].url is not under the record owner's registrable domain", i)
			}
		}
	}
	for i, ack := range r.Acknowledgements {
		origin, err := signing.CanonicalOrigin(ack.AgentURL)
		if _, ok := origins[origin]; err != nil || !ok {
			return fmt.Errorf("acknowledgements[%d].agent_url matches no agents[] entry", i)
		}
	}
	for i, g := range r.Grants {
		// A grantee MUST be a registrable domain (L2 Grants in trust.json).
		// An author writes the registrable domain and narrows with
		// agents[]. A reader keeps the record: a grant whose grantee is
		// not one covers no claim (TrustRecordEvaluator logs a warning),
		// and its scopes still count for exclusive_grants. Like the other
		// PSL rules, this needs an owner: the spec's examples use reserved
		// .example names, which have no registrable domain.
		if operator != "" && mode == Authoring {
			if !isRegistrableDomain(g.Grantee) {
				return fmt.Errorf("grants[%d].grantee must be a registrable domain; narrow the grant to specific agents with agents[]", i)
			}
		}
		for j, a := range g.Agents {
			if _, err := signing.CanonicalOrigin(a.URL); err != nil {
				return fmt.Errorf("grants[%d].agents[%d].url has no canonical origin", i, j)
			}
		}
	}
	return nil
}

// Agent returns the single agents[] entry whose canonical origin is origin
// and that carries role. Zero or several matches, or a match without the
// role, is an error: the record does not authorize the origin.
func (r *TrustRecord) Agent(origin, role string) (*TrustAgent, error) {
	want, err := signing.CanonicalOrigin(origin)
	if err != nil {
		return nil, err
	}
	var match *TrustAgent
	n := 0
	for i := range r.Agents {
		if o, err := signing.CanonicalOrigin(r.Agents[i].URL); err == nil && o == want {
			match = &r.Agents[i]
			n++
		}
	}
	if n != 1 || !containsStr(match.Roles, role) {
		return nil, errors.New("identity: the record must list exactly one matching agent with the required role")
	}
	return match, nil
}

func containsStr(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

// The validator below mirrors trust/v1/trust.json at the pinned commit.

var (
	domainPattern          = regexp.MustCompile(`^(?:[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?\.)+[a-z]{2,63}$`)
	namespacedTokenPattern = regexp.MustCompile(`^[a-z0-9-]+:[a-z0-9_.-]+$`)
	countryPattern         = regexp.MustCompile(`^[A-Z]{2}$`)
	profileKeyPattern      = regexp.MustCompile(`^[a-z0-9-]+$`)
	brandIDPattern         = regexp.MustCompile(`^([a-z0-9_]+|\*)$`)
	brandRefDomainPattern  = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]*[a-z0-9])?(\.[a-z0-9]([a-z0-9-]*[a-z0-9])?)*$`)

	adcpRoles  = set(RoleBrand, RoleRights, RoleMeasurement, RoleGovernance, RoleCreative, RoleSales, RoleBuying, RoleSignals)
	adcpScopes = set(ScopeAll, ScopeMediaBuying, ScopeCreativeGeneration, ScopeRightsClearance, ScopeGovernance,
		ScopeMeasurement, ScopeAgentOperations, ScopeBrandIdentity)
	adcpGrantSources = set("adcp:adagents", "adcp:trust")
)

func set(vals ...string) map[string]struct{} {
	m := make(map[string]struct{}, len(vals))
	for _, v := range vals {
		m[v] = struct{}{}
	}
	return m
}

type trustValidator struct {
	mode ValidationMode
	err  error
}

func (v *trustValidator) fail(path, msg string) {
	if v.err == nil {
		v.err = fmt.Errorf("%s: %s", path, msg)
	}
}

func (v *trustValidator) object(path string, x any, allowed map[string]struct{}, required ...string) map[string]any {
	m, ok := x.(map[string]any)
	if !ok {
		v.fail(path, "must be an object")
		return nil
	}
	if allowed != nil {
		keys := make([]string, 0, len(m))
		for k := range m {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			if _, ok := allowed[k]; !ok {
				v.fail(path+"."+k, "is not allowed")
			}
		}
	}
	for _, k := range required {
		if _, ok := m[k]; !ok {
			v.fail(path+"."+k, "is required")
		}
	}
	return m
}

func (v *trustValidator) str(path string, x any) (string, bool) {
	s, ok := x.(string)
	if !ok {
		v.fail(path, "must be a string")
	}
	return s, ok
}

func (v *trustValidator) optStr(path string, m map[string]any, key string) {
	if x, ok := m[key]; ok {
		v.str(path+"."+key, x)
	}
}

func (v *trustValidator) dateTime(path string, m map[string]any, key string) {
	if x, ok := m[key]; ok {
		if s, ok := v.str(path+"."+key, x); ok {
			if _, err := time.Parse(time.RFC3339Nano, s); err != nil {
				v.fail(path+"."+key, "must be an RFC 3339 date-time")
			}
		}
	}
}

func (v *trustValidator) uri(path string, x any) {
	if s, ok := v.str(path, x); ok {
		if u, err := url.Parse(s); err != nil || u.Scheme == "" {
			v.fail(path, "must be an absolute URI")
		}
	}
}

func (v *trustValidator) httpsURL(path string, x any) {
	if s, ok := v.str(path, x); ok {
		if !strings.HasPrefix(s, "https://") {
			v.fail(path, "must be an https URL")
			return
		}
		if u, err := url.Parse(s); err != nil || u.Host == "" {
			v.fail(path, "must be an absolute URI")
		}
	}
}

func (v *trustValidator) array(path string, x any, minItems int, unique bool) []any {
	a, ok := x.([]any)
	if !ok {
		v.fail(path, "must be an array")
		return nil
	}
	if len(a) < minItems {
		v.fail(path, fmt.Sprintf("must hold at least %d item(s)", minItems))
	}
	if unique {
		seen := map[string]struct{}{}
		for _, e := range a {
			b, _ := json.Marshal(e)
			if _, dup := seen[string(b)]; dup {
				v.fail(path, "items must be unique")
			}
			seen[string(b)] = struct{}{}
		}
	}
	return a
}

func (v *trustValidator) stringArray(path string, m map[string]any, key string, minItems int, unique bool, check func(string, string)) {
	x, ok := m[key]
	if !ok {
		return
	}
	for i, e := range v.array(path+"."+key, x, minItems, unique) {
		p := fmt.Sprintf("%s.%s[%d]", path, key, i)
		if s, ok := v.str(p, e); ok && check != nil {
			check(p, s)
		}
	}
}

func (v *trustValidator) pattern(re *regexp.Regexp, what string) func(string, string) {
	return func(p, s string) {
		if !re.MatchString(s) {
			v.fail(p, "must be "+what)
		}
	}
}

// namespaced checks a role, scope, or grant source: a token in a non-adcp
// namespace, or one of the adcp values.
func (v *trustValidator) namespaced(adcpValues map[string]struct{}, allowStar bool) func(string, string) {
	return func(p, s string) {
		if allowStar && s == "*" {
			return
		}
		if _, ok := adcpValues[s]; ok {
			return
		}
		if namespacedTokenPattern.MatchString(s) && !strings.HasPrefix(s, "adcp:") {
			return
		}
		v.fail(p, "is not a valid namespaced token")
	}
}

func (v *trustValidator) record(x any) {
	m, ok := x.(map[string]any)
	if !ok {
		v.fail("$", "must be an object")
		return
	}
	if _, pointer := m["authoritative_location"]; pointer {
		m = v.object("$", m, set("$schema", "authoritative_location", "last_updated"), "authoritative_location")
		if m == nil {
			return
		}
		v.optStr("$", m, "$schema")
		v.httpsURL("$.authoritative_location", m["authoritative_location"])
		v.dateTime("$", m, "last_updated")
		return
	}
	m = v.object("$", m, set("$schema", "version", "last_updated", "agents", "grants", "exclusive_grants",
		"acknowledgements", "acknowledgements_url", "profiles", "ext"))
	if m == nil {
		return
	}
	any1 := false
	for _, k := range []string{"agents", "grants", "acknowledgements", "acknowledgements_url", "profiles"} {
		if _, ok := m[k]; ok {
			any1 = true
		}
	}
	if !any1 {
		v.fail("$", "must carry agents, grants, acknowledgements, acknowledgements_url, or profiles")
	}
	_, a := m["acknowledgements"]
	_, au := m["acknowledgements_url"]
	if a && au {
		v.fail("$", "acknowledgements and acknowledgements_url are mutually exclusive")
	}
	v.optStr("$", m, "$schema")
	v.optStr("$", m, "version")
	v.dateTime("$", m, "last_updated")
	if x, ok := m["agents"]; ok {
		for i, e := range v.array("$.agents", x, 0, false) {
			v.agent(fmt.Sprintf("$.agents[%d]", i), e)
		}
	}
	if x, ok := m["grants"]; ok {
		for i, e := range v.array("$.grants", x, 0, false) {
			v.grant(fmt.Sprintf("$.grants[%d]", i), e)
		}
	}
	if x, ok := m["exclusive_grants"]; ok {
		if _, ok := x.(bool); !ok {
			v.fail("$.exclusive_grants", "must be a boolean")
		}
	}
	if x, ok := m["acknowledgements"]; ok {
		for i, e := range v.array("$.acknowledgements", x, 0, false) {
			v.acknowledgement(fmt.Sprintf("$.acknowledgements[%d]", i), e)
		}
	}
	if x, ok := m["acknowledgements_url"]; ok {
		v.httpsURL("$.acknowledgements_url", x)
	}
	if x, ok := m["profiles"]; ok {
		if adcpProfile := v.profiles("$.profiles", x); adcpProfile != nil {
			p := v.object("$.profiles.adcp", adcpProfile, set("identity_relying_parties", "onboarding"))
			if p != nil {
				if rp, ok := p["identity_relying_parties"]; ok {
					for i, e := range v.array("$.profiles.adcp.identity_relying_parties", rp, 0, false) {
						v.relyingParty(fmt.Sprintf("$.profiles.adcp.identity_relying_parties[%d]", i), e)
					}
				}
				if o, ok := p["onboarding"]; ok {
					v.buyerPosture("$.profiles.adcp.onboarding", o)
				}
			}
		}
	}
	if x, ok := m["ext"]; ok {
		if ext := v.object("$.ext", x, nil); ext != nil {
			for k, e := range ext {
				v.object("$.ext."+k, e, nil)
			}
		}
	}
}

// profiles validates an open profiles object and returns its adcp member.
func (v *trustValidator) profiles(path string, x any) any {
	m := v.object(path, x, nil)
	if m == nil {
		return nil
	}
	for k, e := range m {
		if !profileKeyPattern.MatchString(k) {
			v.fail(path+"."+k, "is not a valid profile namespace")
		}
		v.object(path+"."+k, e, nil)
	}
	return m["adcp"]
}

func (v *trustValidator) agent(path string, x any) {
	m := v.object(path, x, set("url", "roles", "protocols", "id", "description", "countries", "profiles", "last_updated"), "url", "roles")
	if m == nil {
		return
	}
	v.httpsURL(path+".url", m["url"])
	v.stringArray(path, m, "roles", 1, true, v.namespaced(adcpRoles, false))
	v.stringArray(path, m, "protocols", 0, true, func(p, s string) {
		if s != "mcp" && s != "a2a" {
			v.fail(p, "must be mcp or a2a")
		}
	})
	v.optStr(path, m, "id")
	v.optStr(path, m, "description")
	v.stringArray(path, m, "countries", 0, false, v.pattern(countryPattern, "an ISO 3166-1 alpha-2 code"))
	v.dateTime(path, m, "last_updated")
	if x, ok := m["profiles"]; ok {
		if adcpProfile := v.profiles(path+".profiles", x); adcpProfile != nil {
			p := v.object(path+".profiles.adcp", adcpProfile, set("available_uses", "right_types", "onboarding"))
			if p != nil {
				v.stringArray(path+".profiles.adcp", p, "available_uses", 1, false, func(q, s string) {
					if !adcp.IsKnownRightUse(adcp.RightUse(s)) {
						v.fail(q, "is not a known right use")
					}
				})
				v.stringArray(path+".profiles.adcp", p, "right_types", 1, false, func(q, s string) {
					if !adcp.IsKnownRightType(adcp.RightType(s)) {
						v.fail(q, "is not a known right type")
					}
				})
				if o, ok := p["onboarding"]; ok {
					v.buyerPosture(path+".profiles.adcp.onboarding", o)
				}
			}
		}
	}
}

func (v *trustValidator) grant(path string, x any) {
	m := v.object(path, x, set("grantee", "agents", "scopes", "countries", "valid_from", "valid_until", "profiles", "last_updated"), "grantee", "scopes")
	if m == nil {
		return
	}
	if s, ok := v.str(path+".grantee", m["grantee"]); ok && (len(s) > 253 || !domainPattern.MatchString(s)) {
		v.fail(path+".grantee", "must be a lowercase DNS name")
	}
	if x, ok := m["agents"]; ok {
		for i, e := range v.array(path+".agents", x, 1, false) {
			p := fmt.Sprintf("%s.agents[%d]", path, i)
			if a := v.object(p, e, set("url"), "url"); a != nil {
				v.httpsURL(p+".url", a["url"])
			}
		}
	}
	v.stringArray(path, m, "scopes", 1, true, v.namespaced(adcpScopes, true))
	v.stringArray(path, m, "countries", 0, false, v.pattern(countryPattern, "an ISO 3166-1 alpha-2 code"))
	v.dateTime(path, m, "valid_from")
	v.dateTime(path, m, "valid_until")
	v.dateTime(path, m, "last_updated")
	if x, ok := m["profiles"]; ok {
		if adcpProfile := v.profiles(path+".profiles", x); adcpProfile != nil {
			if p := v.object(path+".profiles.adcp", adcpProfile, set("brands")); p != nil {
				v.stringArray(path+".profiles.adcp", p, "brands", 1, false, v.pattern(brandIDPattern, "a brand id or *"))
			}
		}
	}
}

func (v *trustValidator) acknowledgement(path string, x any) {
	m := v.object(path, x, set("grantor", "agent_url", "via", "profiles", "last_updated"), "grantor", "agent_url", "via")
	if m == nil {
		return
	}
	if s, ok := v.str(path+".grantor", m["grantor"]); ok && (len(s) > 253 || !domainPattern.MatchString(s)) {
		v.fail(path+".grantor", "must be a lowercase DNS name")
	}
	v.httpsURL(path+".agent_url", m["agent_url"])
	if s, ok := v.str(path+".via", m["via"]); ok {
		v.namespaced(adcpGrantSources, false)(path+".via", s)
	}
	v.dateTime(path, m, "last_updated")
	if x, ok := m["profiles"]; ok {
		if adcpProfile := v.profiles(path+".profiles", x); adcpProfile != nil {
			if p := v.object(path+".profiles.adcp", adcpProfile, set("delegation_type")); p != nil {
				if d, ok := p["delegation_type"]; ok {
					if s, ok := v.str(path+".profiles.adcp.delegation_type", d); ok && s != "direct" && s != "delegated" && s != "ad_network" {
						v.fail(path+".profiles.adcp.delegation_type", "must be direct, delegated, or ad_network")
					}
				}
			}
		}
	}
}

func (v *trustValidator) relyingParty(path string, x any) {
	m := v.object(path, x, set("issuer", "scheme", "relying_party_id", "scope"), "issuer", "relying_party_id")
	if m == nil {
		return
	}
	if issuer := v.object(path+".issuer", m["issuer"], nil, "domain"); issuer != nil {
		if s, ok := v.str(path+".issuer.domain", issuer["domain"]); ok && !brandRefDomainPattern.MatchString(s) {
			v.fail(path+".issuer.domain", "must be a lowercase domain")
		}
	}
	v.optStr(path, m, "scheme")
	v.str(path+".relying_party_id", m["relying_party_id"])
	if s, ok := m["scope"]; ok {
		if str, ok := v.str(path+".scope", s); ok && str != "entity" && str != "property" {
			v.fail(path+".scope", "must be entity or property")
		}
	}
}

func (v *trustValidator) buyerPosture(path string, x any) {
	m := v.object(path, x, nil, "mode")
	if m == nil {
		return
	}
	if s, ok := v.str(path+".mode", m["mode"]); ok {
		switch BuyerOnboardingMode(s) {
		case BuyerOnboardingConsidersUnsolicited, BuyerOnboardingApplication, BuyerOnboardingInviteOnly:
		default:
			v.fail(path+".mode", "is not a buyer onboarding mode")
		}
	}
	if r, ok := m["requirements"]; ok {
		for i, e := range v.array(path+".requirements", r, 0, false) {
			v.requirement(fmt.Sprintf("%s.requirements[%d]", path, i), e)
		}
	}
	if u, ok := m["url"]; ok {
		v.uri(path+".url", u)
	}
	if u, ok := m["documentation_url"]; ok {
		v.uri(path+".documentation_url", u)
	}
	v.optStr(path, m, "message")
	if e, ok := m["ext"]; ok {
		v.object(path+".ext", e, nil)
	}
}

func (v *trustValidator) requirement(path string, x any) {
	m := v.object(path, x, nil, "kind")
	if m == nil {
		return
	}
	if s, ok := v.str(path+".kind", m["kind"]); ok && !KnownRequirementKind(AgentOnboardingRequirementKind(s)) {
		if v.mode == Authoring {
			v.fail(path+".kind", "is not a known requirement kind")
		}
		// A reader keeps an unknown kind (the posture's message and url are
		// the fallback) and, like the Python SDK, does not judge its other
		// members; it keeps only the kind, so the typed form can hold it.
		for k := range m {
			if k != "kind" {
				delete(m, k)
			}
		}
		return
	}
	v.optStr(path, m, "role")
	v.stringArray(path, m, "verification_modes", 1, false, nil)
	v.stringArray(path, m, "specialisms", 1, false, nil)
	v.optStr(path, m, "adcp_version")
	v.optStr(path, m, "id")
	if e, ok := m["ext"]; ok {
		v.object(path+".ext", e, nil)
	}
}
