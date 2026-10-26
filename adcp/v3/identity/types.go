package identity

import (
	"bytes"
	"encoding/json"
	"errors"
	"sort"

	"github.com/adcontextprotocol/adcp-go/adcp/v3"
	"github.com/adcontextprotocol/adcp-go/adcp/v3/signing"
)

// Types in this file mirror the experimental AdCP 3.3 identity schemas
// pinned in testdata/schemas/UPSTREAM.json. TestTypesMatchPinnedSchemas
// keeps their JSON members in step with those schemas.

// Profiles is an open, per-protocol profiles object: the adcp namespace is
// typed, every other namespace is kept verbatim (consumers ignore namespaces
// they do not implement).
type Profiles[T any] struct {
	Adcp  *T
	Other map[string]json.RawMessage
}

// MarshalJSON writes the adcp namespace and every other namespace.
func (p Profiles[T]) MarshalJSON() ([]byte, error) {
	out := map[string]json.RawMessage{}
	for k, v := range p.Other {
		out[k] = v
	}
	if p.Adcp != nil {
		b, err := json.Marshal(p.Adcp)
		if err != nil {
			return nil, err
		}
		out["adcp"] = b
	}
	keys := make([]string, 0, len(out))
	for k := range out {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var buf bytes.Buffer
	buf.WriteByte('{')
	for i, k := range keys {
		if i > 0 {
			buf.WriteByte(',')
		}
		kb, _ := json.Marshal(k)
		buf.Write(kb)
		buf.WriteByte(':')
		buf.Write(out[k])
	}
	buf.WriteByte('}')
	return buf.Bytes(), nil
}

// UnmarshalJSON splits the adcp namespace from the others.
func (p *Profiles[T]) UnmarshalJSON(data []byte) error {
	var all map[string]json.RawMessage
	if err := json.Unmarshal(data, &all); err != nil {
		return err
	}
	if all == nil {
		return errors.New("identity: profiles must be an object")
	}
	p.Adcp, p.Other = nil, nil
	for k, v := range all {
		if k == "adcp" {
			var t T
			if err := json.Unmarshal(v, &t); err != nil {
				return err
			}
			p.Adcp = &t
			continue
		}
		if p.Other == nil {
			p.Other = map[string]json.RawMessage{}
		}
		p.Other[k] = v
	}
	return nil
}

// TrustRecord is trust.json v1 (trust/v1/trust.json): either an
// authoritative-location pointer, or a record of the agents an organization
// runs, the grants it makes, and the grants it acknowledges. It carries no
// keys: an agent's keys are only in its origin's key directory. The
// organization a record speaks for is the host it was fetched from, never
// a value inside it.
type TrustRecord struct {
	Schema                string                            `json:"$schema,omitempty"`
	AuthoritativeLocation string                            `json:"authoritative_location,omitempty"`
	Version               string                            `json:"version,omitempty"`
	LastUpdated           string                            `json:"last_updated,omitempty"`
	Agents                []TrustAgent                      `json:"agents,omitempty"`
	Grants                []TrustGrant                      `json:"grants,omitempty"`
	ExclusiveGrants       *bool                             `json:"exclusive_grants,omitempty"`
	Acknowledgements      []TrustAcknowledgement            `json:"acknowledgements,omitempty"`
	AcknowledgementsURL   string                            `json:"acknowledgements_url,omitempty"`
	Profiles              *Profiles[TrustRecordAdcpProfile] `json:"profiles,omitempty"`
	Ext                   map[string]map[string]any         `json:"ext,omitempty"`
}

// TrustAgent is an agent the organization runs, identified by the canonical
// origin of URL: one agent per origin.
type TrustAgent struct {
	URL         string                           `json:"url"`
	Roles       []string                         `json:"roles"`
	Protocols   []string                         `json:"protocols,omitempty"`
	ID          string                           `json:"id,omitempty"`
	Description string                           `json:"description,omitempty"`
	Countries   []string                         `json:"countries,omitempty"`
	Profiles    *Profiles[TrustAgentAdcpProfile] `json:"profiles,omitempty"`
	LastUpdated string                           `json:"last_updated,omitempty"`
}

// TrustAgentAdcpProfile is an agent entry's adcp profile.
type TrustAgentAdcpProfile struct {
	AvailableUses []adcp.RightUse  `json:"available_uses,omitempty"`
	RightTypes    []adcp.RightType `json:"right_types,omitempty"`
	// Onboarding is a buying agent's posture toward sellers; it overrides
	// the record-level default.
	Onboarding *BuyerOnboardingPosture `json:"onboarding,omitempty"`
}

// GrantedAgent narrows a grant to one agent, matched by canonical origin.
type GrantedAgent struct {
	URL string `json:"url"`
}

// TrustGrant is a demand-side grant: the grantee (optionally only these
// agents) may act for the grantor for these scopes, in these countries,
// during this window. A grantee never extends a grant it received.
type TrustGrant struct {
	Grantee     string                           `json:"grantee"`
	Agents      []GrantedAgent                   `json:"agents,omitempty"`
	Scopes      []string                         `json:"scopes"`
	Countries   []string                         `json:"countries,omitempty"`
	ValidFrom   string                           `json:"valid_from,omitempty"`
	ValidUntil  string                           `json:"valid_until,omitempty"`
	Profiles    *Profiles[TrustGrantAdcpProfile] `json:"profiles,omitempty"`
	LastUpdated string                           `json:"last_updated,omitempty"`
}

// TrustGrantAdcpProfile is a grant's adcp profile.
type TrustGrantAdcpProfile struct {
	// Brands lists brand IDs from the grantor's brand.json; ["*"] or
	// omitted means every brand the grantor owns.
	Brands []string `json:"brands,omitempty"`
}

// TrustAcknowledgement records acceptance of a grant another organization
// published.
type TrustAcknowledgement struct {
	Grantor     string                                     `json:"grantor"`
	AgentURL    string                                     `json:"agent_url"`
	Via         string                                     `json:"via"`
	Profiles    *Profiles[TrustAcknowledgementAdcpProfile] `json:"profiles,omitempty"`
	LastUpdated string                                     `json:"last_updated,omitempty"`
}

// TrustAcknowledgementAdcpProfile is an acknowledgement's adcp profile.
type TrustAcknowledgementAdcpProfile struct {
	DelegationType string `json:"delegation_type,omitempty"`
}

// TrustRecordAdcpProfile is the record-level adcp profile.
type TrustRecordAdcpProfile struct {
	IdentityRelyingParties []IdentityRelyingParty `json:"identity_relying_parties,omitempty"`
	// Onboarding is the default buyer posture for every buying agent the
	// record lists.
	Onboarding *BuyerOnboardingPosture `json:"onboarding,omitempty"`
}

// IdentityRelyingParty is core/identity-relying-party.json.
type IdentityRelyingParty struct {
	Issuer         adcp.BrandReference `json:"issuer"`
	Scheme         string              `json:"scheme,omitempty"`
	RelyingPartyID string              `json:"relying_party_id"`
	Scope          string              `json:"scope,omitempty"`
}

// AdCP trust.json roles.
const (
	RoleBrand       = "adcp:brand"
	RoleRights      = "adcp:rights"
	RoleMeasurement = "adcp:measurement"
	RoleGovernance  = "adcp:governance"
	RoleCreative    = "adcp:creative"
	RoleSales       = "adcp:sales"
	RoleBuying      = "adcp:buying"
	RoleSignals     = "adcp:signals"
)

// AdCP grant scopes. ScopeAny covers every scope in every namespace.
const (
	ScopeAny                = "*"
	ScopeAll                = "adcp:all"
	ScopeMediaBuying        = "adcp:media_buying"
	ScopeCreativeGeneration = "adcp:creative_generation"
	ScopeRightsClearance    = "adcp:rights_clearance"
	ScopeGovernance         = "adcp:governance"
	ScopeMeasurement        = "adcp:measurement"
	ScopeAgentOperations    = "adcp:agent_operations"
	ScopeBrandIdentity      = "adcp:brand_identity"
)

// AgentOnboardingMode is enums/agent-onboarding-mode.json: how a seller
// onboards buyer agents for one billing party or for sandbox. Absent means
// undeclared, never invite_only.
type AgentOnboardingMode string

const (
	AgentOnboardingOpen        AgentOnboardingMode = "open"
	AgentOnboardingApplication AgentOnboardingMode = "application"
	AgentOnboardingInviteOnly  AgentOnboardingMode = "invite_only"
)

// AgentOnboardingRequirementKind is the extensible
// enums/agent-onboarding-requirement-kind.json. Readers treat an unknown
// kind as unsatisfiable by machine and fall back to the posture's message
// and URL.
type AgentOnboardingRequirementKind string

const (
	RequirementAgentIdentity       AgentOnboardingRequirementKind = "agent_identity"
	RequirementGrant               AgentOnboardingRequirementKind = "grant"
	RequirementBillingEntity       AgentOnboardingRequirementKind = "billing_entity"
	RequirementContract            AgentOnboardingRequirementKind = "contract"
	RequirementCreditApplication   AgentOnboardingRequirementKind = "credit_application" // #nosec G101 -- an enum value, not a credential.
	RequirementAAOVerified         AgentOnboardingRequirementKind = "aao_verified"
	RequirementStoryboard          AgentOnboardingRequirementKind = "storyboard"
	RequirementSandbox             AgentOnboardingRequirementKind = "sandbox"
	RequirementExperimentalFeature AgentOnboardingRequirementKind = "experimental_feature"
)

// KnownRequirementKind reports whether k is a kind this SDK's pinned schema
// defines.
func KnownRequirementKind(k AgentOnboardingRequirementKind) bool {
	switch k {
	case RequirementAgentIdentity, RequirementGrant, RequirementBillingEntity, RequirementContract,
		RequirementCreditApplication, RequirementAAOVerified, RequirementStoryboard, RequirementSandbox,
		RequirementExperimentalFeature:
		return true
	}
	return false
}

// BuyerOnboardingMode is enums/buyer-onboarding-mode.json.
type BuyerOnboardingMode string

const (
	BuyerOnboardingConsidersUnsolicited BuyerOnboardingMode = "considers_unsolicited"
	BuyerOnboardingApplication          BuyerOnboardingMode = "application"
	BuyerOnboardingInviteOnly           BuyerOnboardingMode = "invite_only"
)

// AgentOnboardingRequirement is core/agent-onboarding-requirement.json.
type AgentOnboardingRequirement struct {
	Kind              AgentOnboardingRequirementKind `json:"kind"`
	Role              string                         `json:"role,omitempty"`
	VerificationModes []string                       `json:"verification_modes,omitempty"`
	Specialisms       []string                       `json:"specialisms,omitempty"`
	AdcpVersion       string                         `json:"adcp_version,omitempty"`
	ID                string                         `json:"id,omitempty"`
	Ext               map[string]any                 `json:"ext,omitempty"`
}

// AgentOnboardingTier is core/agent-onboarding-tier.json.
type AgentOnboardingTier struct {
	Mode         AgentOnboardingMode          `json:"mode"`
	URL          string                       `json:"url,omitempty"`
	Requirements []AgentOnboardingRequirement `json:"requirements,omitempty"`
	Ext          map[string]any               `json:"ext,omitempty"`
}

// AgentOnboarding is core/agent-onboarding.json, the seller's
// account.agent_onboarding capability. Seller-wide and advisory.
type AgentOnboarding struct {
	Sandbox          *AgentOnboardingTier           `json:"sandbox,omitempty"`
	Billing          map[string]AgentOnboardingTier `json:"billing,omitempty"`
	URL              string                         `json:"url,omitempty"`
	DocumentationURL string                         `json:"documentation_url,omitempty"`
	Message          string                         `json:"message,omitempty"`
	Ext              map[string]any                 `json:"ext,omitempty"`
}

// BuyerOnboardingPosture is core/buyer-onboarding-posture.json.
type BuyerOnboardingPosture struct {
	Mode             BuyerOnboardingMode          `json:"mode"`
	Requirements     []AgentOnboardingRequirement `json:"requirements,omitempty"`
	URL              string                       `json:"url,omitempty"`
	DocumentationURL string                       `json:"documentation_url,omitempty"`
	Message          string                       `json:"message,omitempty"`
	Ext              map[string]any               `json:"ext,omitempty"`
}

// FeatureAgentOnboarding is the experimental_features id a seller that
// declares account.agent_onboarding must list.
const FeatureAgentOnboarding = "account.agent_onboarding"

// AccountCapabilities is adcp.AccountCapabilities plus the experimental
// agent_onboarding member.
type AccountCapabilities struct {
	adcp.AccountCapabilities
	AgentOnboarding *AgentOnboarding `json:"agent_onboarding,omitempty"`
}

// RequestSigningCapabilities is adcp.RequestSigningCapabilities plus the
// accepted profiles (#7894). Omitted profiles mean the RFC 9421 profile
// only.
type RequestSigningCapabilities struct {
	adcp.RequestSigningCapabilities
	Profiles []string `json:"profiles,omitempty"`
}

// WebhookSigningCapabilities is adcp.WebhookSigningCapabilities plus the
// profiles a seller can sign webhooks with (#7894).
type WebhookSigningCapabilities struct {
	adcp.WebhookSigningCapabilities
	Profiles []string `json:"profiles,omitempty"`
}

// AdvertisedRequestSigningProfiles reads request_signing.profiles from a
// get_adcp_capabilities response body. A response without the member
// advertises the RFC 9421 profile only; an explicit empty list advertises
// none. Use it to build
// signing.SignedHTTPClientOptions.ProfilesProvider.
func AdvertisedRequestSigningProfiles(capabilities []byte) ([]string, error) {
	var doc struct {
		RequestSigning *RequestSigningCapabilities `json:"request_signing"`
	}
	if err := json.Unmarshal(capabilities, &doc); err != nil {
		return nil, err
	}
	if doc.RequestSigning == nil || doc.RequestSigning.Profiles == nil {
		return []string{signing.ProfileTagRequestSigning}, nil
	}
	return doc.RequestSigning.Profiles, nil
}

// RelationshipStatus is principal-relationship.json status.
type RelationshipStatus string

const (
	RelationshipPending   RelationshipStatus = "pending"
	RelationshipActive    RelationshipStatus = "active"
	RelationshipSuspended RelationshipStatus = "suspended"
	RelationshipBlocked   RelationshipStatus = "blocked"
)

// PrincipalRelationship is core/principal-relationship.json, the
// relationship member get_principal returns only to the authenticated
// caller, on the unconfigured, recognized, and current results.
type PrincipalRelationship struct {
	Status    RelationshipStatus          `json:"status"`
	ExpiresAt string                      `json:"expires_at,omitempty"`
	Setup     *PrincipalRelationshipSetup `json:"setup,omitempty"`
	Ext       map[string]any              `json:"ext,omitempty"`
}

// PrincipalRelationshipSetup is the caller's next step while pending.
type PrincipalRelationshipSetup struct {
	URL       string `json:"url,omitempty"`
	Message   string `json:"message"`
	ExpiresAt string `json:"expires_at,omitempty"`
}

// principal.changed reasons added in 3.3.
const (
	PrincipalChangedRelationshipChanged  = "relationship_changed"
	PrincipalChangedRelationshipExpiring = "relationship_expiring"
)

// error-details/agent-permission-denied.json reasons.
const (
	PermissionDeniedSandboxOnly = "sandbox_only"
	PermissionDeniedGrantAbsent = "grant_absent"
)

// AgentPermissionDeniedDetails is error-details/agent-permission-denied.json.
// Emit it only once the signer's identity is established; otherwise
// PERMISSION_DENIED omits details (the cross-tenant oracle clamp).
type AgentPermissionDeniedDetails struct {
	Scope  string `json:"scope"`
	Reason string `json:"reason"`
}

// GrantAbsentDetails returns {scope: "agent", reason: "grant_absent"}.
func GrantAbsentDetails() AgentPermissionDeniedDetails {
	return AgentPermissionDeniedDetails{Scope: "agent", Reason: PermissionDeniedGrantAbsent}
}

// RevocationDetails is the revocation_details entry type, defined in
// signing beside the revocation list it belongs to.
type RevocationDetails = signing.RevocationDetails
