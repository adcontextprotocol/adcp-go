// Package adcp provides helpers for building AdCP MCP servers in Go.
//
// Generated enum aliases preserve unknown wire values for forward-compatible
// JSON decoding. Use helpers such as KnownMediaBuyStatusValues,
// IsKnownMediaBuyStatus, and ParseMediaBuyStatus when a handler needs strict
// validation against the current schema values.
package adcp

import "encoding/json"

// CapabilitiesData is the typed get_adcp_capabilities response. Per the 3.0
// schema, adcp (with idempotency) and supported_protocols are required; all
// other blocks are optional and set only when the relevant protocol is
// supported.
type CapabilitiesData struct {
	ADCP                  *ADCPVersion                   `json:"adcp"`
	SupportedProtocols    []string                       `json:"supported_protocols"`
	Account               *AccountCapabilities           `json:"account,omitempty"`
	MediaBuy              *MediaBuyCapabilities          `json:"media_buy,omitempty"`
	Signals               *SignalsCapabilities           `json:"signals,omitempty"`
	Governance            *GovernanceCapabilities        `json:"governance,omitempty"`
	SponsoredIntelligence *SICapabilities                `json:"sponsored_intelligence,omitempty"`
	Brand                 *BrandCapabilities             `json:"brand,omitempty"`
	Creative              *CreativeCapabilities          `json:"creative,omitempty"`
	RequestSigning        *RequestSigningCapabilities    `json:"request_signing,omitempty"`
	WebhookSigning        *WebhookSigningCapabilities    `json:"webhook_signing,omitempty"`
	Identity              *IdentityCapabilities          `json:"identity,omitempty"`
	ComplianceTesting     *ComplianceTestingCapabilities `json:"compliance_testing,omitempty"`
	Specialisms           []string                       `json:"specialisms,omitempty"`
	ExtensionsSupported   []string                       `json:"extensions_supported,omitempty"`
	ExperimentalFeatures  []string                       `json:"experimental_features,omitempty"`
	LastUpdated           string                         `json:"last_updated,omitempty"`
	Errors                []AdcpError                    `json:"errors,omitempty"`
	Context               any                            `json:"context,omitempty"`
	Ext                   any                            `json:"ext,omitempty"`
}

type ADCPVersion struct {
	MajorVersions []int           `json:"major_versions"`
	Idempotency   IdempotencyCaps `json:"idempotency"`
}

// IdempotencyCaps declares the seller's replay window for idempotency_key.
// Minimum 3600 (1h); recommended 86400 (24h); maximum 604800 (7d).
type IdempotencyCaps struct {
	Supported         bool  `json:"supported"`
	ReplayTTLSeconds  int   `json:"replay_ttl_seconds"`
	AccountIDIsOpaque *bool `json:"account_id_is_opaque,omitempty"`
}

// AccountCapabilities describes how accounts are established and billed.
// supported_billing is required when present.
type AccountCapabilities struct {
	RequireOperatorAuth   *bool    `json:"require_operator_auth,omitempty"`
	AuthorizationEndpoint string   `json:"authorization_endpoint,omitempty"`
	SupportedBilling      []string `json:"supported_billing"`
	RequiredForProducts   *bool    `json:"required_for_products,omitempty"`
	AccountFinancials     *bool    `json:"account_financials,omitempty"`
	Sandbox               *bool    `json:"sandbox,omitempty"`
}

// MediaBuyCapabilities is the media_buy protocol capability block.
type MediaBuyCapabilities struct {
	SupportedPricingModels   []string                `json:"supported_pricing_models,omitempty"`
	ReportingDeliveryMethods []string                `json:"reporting_delivery_methods,omitempty"`
	OfflineDeliveryProtocols []string                `json:"offline_delivery_protocols,omitempty"`
	Features                 map[string]any          `json:"features,omitempty"`
	Execution                *MediaBuyExecution      `json:"execution,omitempty"`
	AudienceTargeting        *AudienceTargetingCaps  `json:"audience_targeting,omitempty"`
	ConversionTracking       *ConversionTrackingCaps `json:"conversion_tracking,omitempty"`
	ContentStandards         *ContentStandardsCaps   `json:"content_standards,omitempty"`
	Portfolio                *PortfolioCaps          `json:"portfolio,omitempty"`
}

// MediaBuyExecution describes technical execution capabilities for media buying.
type MediaBuyExecution struct {
	TrustedMatch    *TrustedMatchCaps  `json:"trusted_match,omitempty"`
	AxeIntegrations []string           `json:"axe_integrations,omitempty"`
	CreativeSpecs   *CreativeSpecsCaps `json:"creative_specs,omitempty"`
	Targeting       *TargetingCaps     `json:"targeting,omitempty"`
}

type TrustedMatchCaps struct {
	Surfaces []string `json:"surfaces,omitempty"`
}

type CreativeSpecsCaps struct {
	VASTVersions  []string `json:"vast_versions,omitempty"`
	MRAIDVersions []string `json:"mraid_versions,omitempty"`
	VPAID         *bool    `json:"vpaid,omitempty"`
	SIMID         *bool    `json:"simid,omitempty"`
}

// TargetingCaps declares which targeting dimensions the seller honors. Presence
// of a boolean/object indicates support; buyers can then send matching fields
// in targeting_overlay.
type TargetingCaps struct {
	GeoCountries     *bool               `json:"geo_countries,omitempty"`
	GeoRegions       *bool               `json:"geo_regions,omitempty"`
	GeoMetros        *GeoMetrosCaps      `json:"geo_metros,omitempty"`
	GeoPostalAreas   *GeoPostalAreasCaps `json:"geo_postal_areas,omitempty"`
	GeoProximity     *GeoProximityCaps   `json:"geo_proximity,omitempty"`
	AgeRestriction   *AgeRestrictionCaps `json:"age_restriction,omitempty"`
	Language         *bool               `json:"language,omitempty"`
	KeywordTargets   *KeywordMatchCaps   `json:"keyword_targets,omitempty"`
	NegativeKeywords *KeywordMatchCaps   `json:"negative_keywords,omitempty"`
}

type GeoMetrosCaps struct {
	NielsenDMA    *bool `json:"nielsen_dma,omitempty"`
	UKITL1        *bool `json:"uk_itl1,omitempty"`
	UKITL2        *bool `json:"uk_itl2,omitempty"`
	EurostatNUTS2 *bool `json:"eurostat_nuts2,omitempty"`
}

type GeoPostalAreasCaps struct {
	USZip         *bool `json:"us_zip,omitempty"`
	USZipPlusFour *bool `json:"us_zip_plus_four,omitempty"`
	GBOutward     *bool `json:"gb_outward,omitempty"`
	GBFull        *bool `json:"gb_full,omitempty"`
	CAFSA         *bool `json:"ca_fsa,omitempty"`
	CAFull        *bool `json:"ca_full,omitempty"`
	DEPLZ         *bool `json:"de_plz,omitempty"`
	FRCodePostal  *bool `json:"fr_code_postal,omitempty"`
	AUPostcode    *bool `json:"au_postcode,omitempty"`
	CHPLZ         *bool `json:"ch_plz,omitempty"`
	ATPLZ         *bool `json:"at_plz,omitempty"`
}

type GeoProximityCaps struct {
	Radius         *bool    `json:"radius,omitempty"`
	TravelTime     *bool    `json:"travel_time,omitempty"`
	Geometry       *bool    `json:"geometry,omitempty"`
	TransportModes []string `json:"transport_modes,omitempty"`
}

type AgeRestrictionCaps struct {
	Supported           *bool    `json:"supported,omitempty"`
	VerificationMethods []string `json:"verification_methods,omitempty"`
}

// KeywordMatchCaps declares which match types a seller honors for keyword or
// negative-keyword targeting. supported_match_types is required when present.
type KeywordMatchCaps struct {
	SupportedMatchTypes []string `json:"supported_match_types"`
}

// AudienceTargetingCaps describes audience matching capabilities.
// supported_identifier_types and minimum_audience_size are required when present.
type AudienceTargetingCaps struct {
	SupportedIdentifierTypes   []string              `json:"supported_identifier_types"`
	SupportsPlatformCustomerID *bool                 `json:"supports_platform_customer_id,omitempty"`
	SupportedUIDTypes          []string              `json:"supported_uid_types,omitempty"`
	MinimumAudienceSize        int                   `json:"minimum_audience_size"`
	MatchingLatencyHours       *MatchingLatencyRange `json:"matching_latency_hours,omitempty"`
}

type MatchingLatencyRange struct {
	Min *int `json:"min,omitempty"`
	Max *int `json:"max,omitempty"`
}

// ConversionTrackingCaps describes seller-level conversion event capabilities.
// AttributionWindows (plural) lists window options a buyer can choose from —
// distinct from the singular AttributionWindow in core/attribution-window.json
// used on an optimization goal.
type ConversionTrackingCaps struct {
	MultiSourceEventDedup      *bool                     `json:"multi_source_event_dedup,omitempty"`
	SupportedEventTypes        []string                  `json:"supported_event_types,omitempty"`
	SupportedUIDTypes          []string                  `json:"supported_uid_types,omitempty"`
	SupportedHashedIdentifiers []string                  `json:"supported_hashed_identifiers,omitempty"`
	SupportedActionSources     []string                  `json:"supported_action_sources,omitempty"`
	AttributionWindows         []AttributionWindowOption `json:"attribution_windows,omitempty"`
}

// AttributionWindowOption describes one attribution-window configuration a
// buyer can pick. post_click is required when present.
type AttributionWindowOption struct {
	EventType string     `json:"event_type,omitempty"`
	PostClick []Duration `json:"post_click"`
	PostView  []Duration `json:"post_view,omitempty"`
}

// AttributionWindow is the singular attribution config applied to a specific
// optimization goal or delivery response. Mirrors core/attribution-window.json
// and is distinct from AttributionWindowOption (plural capability options).
type AttributionWindow struct {
	PostClick *Duration `json:"post_click,omitempty"`
	PostView  *Duration `json:"post_view,omitempty"`
	Model     string    `json:"model"`
}

// ContentStandardsCaps describes content-standards evaluation and delivery.
type ContentStandardsCaps struct {
	SupportsLocalEvaluation *bool    `json:"supports_local_evaluation,omitempty"`
	SupportedChannels       []string `json:"supported_channels,omitempty"`
	SupportsWebhookDelivery *bool    `json:"supports_webhook_delivery,omitempty"`
}

// PortfolioCaps describes the seller's inventory portfolio.
// publisher_domains is required when present.
type PortfolioCaps struct {
	PublisherDomains    []string `json:"publisher_domains"`
	PrimaryChannels     []string `json:"primary_channels,omitempty"`
	PrimaryCountries    []string `json:"primary_countries,omitempty"`
	Description         string   `json:"description,omitempty"`
	AdvertisingPolicies string   `json:"advertising_policies,omitempty"`
}

// SignalsCapabilities is the signals protocol capability block.
type SignalsCapabilities struct {
	DataProviderDomains []string        `json:"data_provider_domains,omitempty"`
	Features            map[string]bool `json:"features,omitempty"`
}

// GovernanceCapabilities is the governance protocol capability block.
type GovernanceCapabilities struct {
	PropertyFeatures      []GovernanceFeature `json:"property_features,omitempty"`
	CreativeFeatures      []GovernanceFeature `json:"creative_features,omitempty"`
	AggregationWindowDays int                 `json:"aggregation_window_days,omitempty"`
}

// GovernanceFeature describes a score/rating/certification the agent provides.
type GovernanceFeature struct {
	FeatureID      string        `json:"feature_id"`
	Type           string        `json:"type"`
	Range          *FeatureRange `json:"range,omitempty"`
	Categories     []string      `json:"categories,omitempty"`
	Description    string        `json:"description,omitempty"`
	MethodologyURL string        `json:"methodology_url,omitempty"`
}

type FeatureRange struct {
	Min float64 `json:"min"`
	Max float64 `json:"max"`
}

// SICapabilities is the sponsored_intelligence protocol capability block.
// Callers declaring this block MUST populate Endpoint.Transports and
// Capabilities — the schema requires both, and a nil/empty value will fail
// upstream validation.
type SICapabilities struct {
	Endpoint     SIEndpoint     `json:"endpoint"`
	Capabilities map[string]any `json:"capabilities"`
	BrandURL     string         `json:"brand_url,omitempty"`
}

type SIEndpoint struct {
	Transports []SITransport `json:"transports"`
	Preferred  string        `json:"preferred,omitempty"`
}

type SITransport struct {
	Type string `json:"type"`
	URL  string `json:"url"`
}

// BrandCapabilities is the brand protocol capability block.
type BrandCapabilities struct {
	Rights              *bool    `json:"rights,omitempty"`
	RightTypes          []string `json:"right_types,omitempty"`
	AvailableUses       []string `json:"available_uses,omitempty"`
	GenerationProviders []string `json:"generation_providers,omitempty"`
	Description         string   `json:"description,omitempty"`
}

// CreativeCapabilities is the creative protocol capability block.
type CreativeCapabilities struct {
	SupportsCompliance     *bool `json:"supports_compliance,omitempty"`
	HasCreativeLibrary     *bool `json:"has_creative_library,omitempty"`
	SupportsGeneration     *bool `json:"supports_generation,omitempty"`
	SupportsTransformation *bool `json:"supports_transformation,omitempty"`
}

// RequestSigningCapabilities declares RFC 9421 signing policy.
type RequestSigningCapabilities struct {
	Supported           bool     `json:"supported"`
	CoversContentDigest string   `json:"covers_content_digest,omitempty"`
	RequiredFor         []string `json:"required_for,omitempty"`
	WarnFor             []string `json:"warn_for,omitempty"`
	SupportedFor        []string `json:"supported_for,omitempty"`
}

// WebhookSigningCapabilities declares RFC 9421 webhook-signature policy —
// what this agent emits on outbound webhook deliveries. Top-level peer of
// RequestSigning. Profile is a closed enum ("adcp/webhook-signing/v1"); the
// value MUST match the tag= on the on-wire Signature-Input header.
type WebhookSigningCapabilities struct {
	Supported          bool     `json:"supported"`
	Profile            string   `json:"profile,omitempty"`
	Algorithms         []string `json:"algorithms,omitempty"`
	LegacyHMACFallback bool     `json:"legacy_hmac_fallback,omitempty"`
}

// IdentityCapabilities declares operator identity posture — key-scoping and
// compromise-response controls. All fields advisory in 3.x; receivers use
// them to reason about blast radius and revocation latency at onboarding.
type IdentityCapabilities struct {
	PerPrincipalKeyIsolation bool                            `json:"per_principal_key_isolation,omitempty"`
	KeyOrigins               *IdentityKeyOrigins             `json:"key_origins,omitempty"`
	CompromiseNotification   *IdentityCompromiseNotification `json:"compromise_notification,omitempty"`
}

// IdentityKeyOrigins maps signing-key purpose → publishing origin so
// counterparties can verify origin separation at onboarding.
type IdentityKeyOrigins struct {
	GovernanceSigning string `json:"governance_signing,omitempty"`
	RequestSigning    string `json:"request_signing,omitempty"`
	WebhookSigning    string `json:"webhook_signing,omitempty"`
	TMPSigning        string `json:"tmp_signing,omitempty"`
}

// IdentityCompromiseNotification declares whether this agent emits and/or
// subscribes to the identity.compromise_notification webhook event on key
// revocation due to known or suspected compromise.
type IdentityCompromiseNotification struct {
	Emits   bool `json:"emits,omitempty"`
	Accepts bool `json:"accepts,omitempty"`
}

// ComplianceTestingCapabilities declares supported comply_test_controller
// scenarios. scenarios is required when the block is present.
type ComplianceTestingCapabilities struct {
	Scenarios []string `json:"scenarios"`
}

// BrandReference identifies a brand by domain, optionally scoped to a specific
// brand within a house portfolio. Industries and DataSubjectContestation are
// inline overrides for callers that cannot modify the brand's canonical
// brand.json — used by governance to resolve Annex III vertical detection and
// GDPR Art 22(3) contestation contacts when brand.json is out of reach.
type BrandReference struct {
	Domain                  string                   `json:"domain"`
	BrandID                 string                   `json:"brand_id,omitempty"`
	Industries              []string                 `json:"industries,omitempty"`
	DataSubjectContestation *DataSubjectContestation `json:"data_subject_contestation,omitempty"`
}

type AccountReference struct {
	AccountID string          `json:"account_id,omitempty"`
	Brand     *BrandReference `json:"brand,omitempty"`
	Operator  string          `json:"operator,omitempty"`
	Sandbox   bool            `json:"sandbox,omitempty"`
}

// PublisherPropertySelector is the flattened union of the three variants in
// publisher-property-selector.json. SelectionType is the discriminator:
//
//	"all":     set PublisherDomain only.
//	"by_id":   set PublisherDomain + PropertyIDs.
//	"by_tag":  set PublisherDomain + PropertyTags.
type PublisherPropertySelector struct {
	PublisherDomain string   `json:"publisher_domain"`
	SelectionType   string   `json:"selection_type"`
	PropertyIDs     []string `json:"property_ids,omitempty"`
	PropertyTags    []string `json:"property_tags,omitempty"`
}

type AccountResult struct {
	AccountID    string          `json:"account_id"`
	Brand        *BrandReference `json:"brand,omitempty"`
	Operator     string          `json:"operator,omitempty"`
	Action       string          `json:"action"`
	Status       string          `json:"status"`
	AccountScope string          `json:"account_scope,omitempty"`
	Setup        *AccountSetup   `json:"setup,omitempty"`
	PaymentTerms string          `json:"payment_terms,omitempty"`
	Billing      string          `json:"billing,omitempty"`
}

type AccountSetup struct {
	URL     string `json:"url,omitempty"`
	Message string `json:"message,omitempty"`
}

type GovernanceResult struct {
	Account          *GovernanceAccount `json:"account"`
	Status           string             `json:"status"`
	GovernanceAgents []GovernanceAgent  `json:"governance_agents"`
}

type GovernanceAccount struct {
	Brand    *BrandReference `json:"brand,omitempty"`
	Operator string          `json:"operator,omitempty"`
}

type GovernanceAgent struct {
	URL        string   `json:"url"`
	Categories []string `json:"categories,omitempty"`
}

type ProductsData struct {
	Products          []Product        `json:"products"`
	RefinementApplied []map[string]any `json:"refinement_applied,omitempty"`
	Sandbox           bool             `json:"sandbox,omitempty"`
	Context           any              `json:"context,omitempty"`
}

// PricingOption is the flattened union of all variants in pricing-option.json.
// The schema is a oneOf of 9 pricing models (cpm, vcpm, cpc, cpcv, cpv, cpp,
// cpa, flat_rate, time); the Go representation carries every variant's fields
// so a single struct type can be constructed for any model. PricingModel is
// the discriminator; callers MUST only set fields that apply to their model.
//
// Fields by variant:
//
//	cpm / vcpm / cpc / cpcv / cpv / cpp:
//	  FixedPrice (fixed) OR FloorPrice + PriceGuidance (auction); MaxBid for
//	  auction models to interpret bid_price as a ceiling.
//	cpa:
//	  FixedPrice, EventSourceID, EventType (required); CustomEventName when
//	  EventType="custom"; EligibleAdjustments for adjustment filtering.
//	flat_rate:
//	  FixedPrice (required).
//	time:
//	  FixedPrice (required), Parameters for duration/unit specifics.
//	All variants: PricingOptionID, Currency, MinSpendPerPackage, PriceBreakdown.
type PricingOption struct {
	PricingOptionID     string   `json:"pricing_option_id"`
	PricingModel        string   `json:"pricing_model"`
	Currency            string   `json:"currency"`
	FixedPrice          float64  `json:"fixed_price,omitempty"`
	FloorPrice          float64  `json:"floor_price,omitempty"`
	MinSpendPerPackage  float64  `json:"min_spend_per_package,omitempty"`
	MaxBid              *bool    `json:"max_bid,omitempty"`
	PriceGuidance       any      `json:"price_guidance,omitempty"`
	PriceBreakdown      any      `json:"price_breakdown,omitempty"`
	EventSourceID       string   `json:"event_source_id,omitempty"`
	EventType           string   `json:"event_type,omitempty"`
	CustomEventName     string   `json:"custom_event_name,omitempty"`
	EligibleAdjustments []string `json:"eligible_adjustments,omitempty"`
	Parameters          any      `json:"parameters,omitempty"`
}

// OptimizationGoal is a flattened representation of the optimization-goal
// oneOf. Target remains the nested raw oneOf payload, while Extra preserves
// schema-allowed future fields so read-modify-write callers do not strip
// unknown goal metadata on replacement updates. Extra keys that collide with
// typed fields are ignored when marshaling; set the typed field instead.
type OptimizationGoal struct {
	Kind                string                             `json:"kind,omitempty"`
	Metric              string                             `json:"metric,omitempty"`
	ReachUnit           string                             `json:"reach_unit,omitempty"`
	TargetFrequency     *OptimizationGoalTargetFrequency   `json:"target_frequency,omitempty"`
	ViewDurationSeconds float64                            `json:"view_duration_seconds,omitempty"`
	Target              any                                `json:"target,omitempty"`
	Priority            int                                `json:"priority,omitempty"`
	EventSources        []OptimizationGoalEventSource      `json:"event_sources,omitempty"`
	AttributionWindow   *OptimizationGoalAttributionWindow `json:"attribution_window,omitempty"`
	Extra               map[string]any                     `json:"-"`
}

func (g OptimizationGoal) MarshalJSON() ([]byte, error) {
	out := make(map[string]any, len(g.Extra))
	for k, v := range g.Extra {
		if isOptimizationGoalTypedJSONField(k) {
			continue
		}
		out[k] = v
	}
	if g.Kind != "" {
		out["kind"] = g.Kind
	}
	if g.Metric != "" {
		out["metric"] = g.Metric
	}
	if g.ReachUnit != "" {
		out["reach_unit"] = g.ReachUnit
	}
	if g.TargetFrequency != nil {
		out["target_frequency"] = g.TargetFrequency
	}
	if g.ViewDurationSeconds != 0 {
		out["view_duration_seconds"] = g.ViewDurationSeconds
	}
	if g.Target != nil {
		out["target"] = g.Target
	}
	if g.Priority != 0 {
		out["priority"] = g.Priority
	}
	if len(g.EventSources) > 0 {
		out["event_sources"] = g.EventSources
	}
	if g.AttributionWindow != nil {
		out["attribution_window"] = g.AttributionWindow
	}
	return json.Marshal(out)
}

func isOptimizationGoalTypedJSONField(key string) bool {
	switch key {
	case "kind",
		"metric",
		"reach_unit",
		"target_frequency",
		"view_duration_seconds",
		"target",
		"priority",
		"event_sources",
		"attribution_window":
		return true
	default:
		return false
	}
}

func (g *OptimizationGoal) UnmarshalJSON(data []byte) error {
	type alias OptimizationGoal
	var typed alias
	if err := json.Unmarshal(data, &typed); err != nil {
		return err
	}

	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	for key := range raw {
		if isOptimizationGoalTypedJSONField(key) {
			delete(raw, key)
		}
	}

	*g = OptimizationGoal(typed)
	if len(raw) == 0 {
		g.Extra = nil
		return nil
	}
	g.Extra = make(map[string]any, len(raw))
	for k, v := range raw {
		var value any
		if err := json.Unmarshal(v, &value); err != nil {
			return err
		}
		g.Extra[k] = value
	}
	return nil
}

type MediaBuyListItem struct {
	MediaBuyID string    `json:"media_buy_id"`
	Status     string    `json:"status"`
	Currency   string    `json:"currency"`
	Packages   []Package `json:"packages"`
}

// MediaBuyData is one item in a get_media_buys response.
type MediaBuyData struct {
	MediaBuyID       string                 `json:"media_buy_id"`
	Account          *Account               `json:"account,omitempty"`
	Status           string                 `json:"status"`
	Currency         string                 `json:"currency"`
	TotalBudget      float64                `json:"total_budget"`
	StartTime        string                 `json:"start_time,omitempty"`
	EndTime          string                 `json:"end_time,omitempty"`
	InvoiceRecipient *BusinessEntity        `json:"invoice_recipient,omitempty"`
	ConfirmedAt      string                 `json:"confirmed_at,omitempty"`
	Cancellation     any                    `json:"cancellation,omitempty"`
	CreativeDeadline string                 `json:"creative_deadline,omitempty"`
	Revision         int                    `json:"revision,omitempty"`
	CreatedAt        string                 `json:"created_at,omitempty"`
	UpdatedAt        string                 `json:"updated_at,omitempty"`
	ValidActions     []string               `json:"valid_actions,omitempty"`
	History          []MediaBuyHistoryEntry `json:"history,omitempty"`
	Packages         []PackageStatus        `json:"packages"`
	Ext              any                    `json:"ext,omitempty"`
}

// MarshalJSON preserves an explicitly empty valid_actions array. In the protocol,
// [] means no actions are available; omission means the seller did not say.
func (d MediaBuyData) MarshalJSON() ([]byte, error) {
	type mediaBuyData MediaBuyData
	b, err := json.Marshal(mediaBuyData(d))
	if err != nil {
		return nil, err
	}
	if d.ValidActions == nil {
		return b, nil
	}
	var out map[string]any
	if err := json.Unmarshal(b, &out); err != nil {
		return nil, err
	}
	out["valid_actions"] = d.ValidActions
	return json.Marshal(out)
}

// MediaBuyHistoryEntry is a get_media_buys revision history entry.
type MediaBuyHistoryEntry struct {
	Revision  int    `json:"revision"`
	Timestamp string `json:"timestamp"`
	Actor     string `json:"actor,omitempty"`
	Action    string `json:"action"`
	Summary   string `json:"summary,omitempty"`
	PackageID string `json:"package_id,omitempty"`
	Ext       any    `json:"ext,omitempty"`
}

// PackageStatus is a package row in get_media_buys. It embeds Package for
// ergonomic construction from create/update state, but marshals only the fields
// modeled by the get_media_buys response schema.
type PackageStatus struct {
	Package
	Currency                  string                    `json:"currency,omitempty"`
	CreativeApprovals         []PackageCreativeApproval `json:"creative_approvals,omitempty"`
	FormatIDsPending          []FormatRef               `json:"format_ids_pending,omitempty"`
	SnapshotUnavailableReason string                    `json:"snapshot_unavailable_reason,omitempty"`
	Snapshot                  *PackageSnapshot          `json:"snapshot,omitempty"`
}

func (p PackageStatus) MarshalJSON() ([]byte, error) {
	out := map[string]any{"package_id": p.PackageID}
	if p.ProductID != "" {
		out["product_id"] = p.ProductID
	}
	if p.Budget != 0 {
		out["budget"] = p.Budget
	}
	if p.BidPrice != 0 {
		out["bid_price"] = p.BidPrice
	}
	if p.Impressions != 0 {
		out["impressions"] = p.Impressions
	}
	if p.Currency != "" {
		out["currency"] = p.Currency
	}
	if p.StartTime != "" {
		out["start_time"] = p.StartTime
	}
	if p.EndTime != "" {
		out["end_time"] = p.EndTime
	}
	if p.Paused != nil {
		out["paused"] = p.Paused
	}
	if p.Canceled != nil {
		out["canceled"] = p.Canceled
	}
	if p.Cancellation != nil {
		out["cancellation"] = p.Cancellation
	}
	if p.CreativeDeadline != "" {
		out["creative_deadline"] = p.CreativeDeadline
	}
	if p.TargetingOverlay != nil {
		out["targeting_overlay"] = p.TargetingOverlay
	}
	if p.Ext != nil {
		out["ext"] = p.Ext
	}
	if len(p.CreativeApprovals) > 0 {
		out["creative_approvals"] = p.CreativeApprovals
	}
	if len(p.FormatIDsPending) > 0 {
		out["format_ids_pending"] = p.FormatIDsPending
	}
	if p.SnapshotUnavailableReason != "" {
		out["snapshot_unavailable_reason"] = p.SnapshotUnavailableReason
	}
	if p.Snapshot != nil {
		out["snapshot"] = p.Snapshot
	}
	return json.Marshal(out)
}

type PackageCreativeApproval struct {
	CreativeID      string `json:"creative_id"`
	ApprovalStatus  string `json:"approval_status"`
	RejectionReason string `json:"rejection_reason,omitempty"`
}

type PackageSnapshot struct {
	AsOf             string  `json:"as_of"`
	StalenessSeconds int     `json:"staleness_seconds"`
	Impressions      float64 `json:"impressions"`
	Spend            float64 `json:"spend"`
	Currency         string  `json:"currency,omitempty"`
	Clicks           float64 `json:"clicks,omitempty"`
	PacingIndex      float64 `json:"pacing_index,omitempty"`
	DeliveryStatus   string  `json:"delivery_status,omitempty"`
	Ext              any     `json:"ext,omitempty"`
}

type DeliveryData struct {
	ReportingPeriod    ReportingPeriod    `json:"reporting_period"`
	Currency           string             `json:"currency"`
	MediaBuyDeliveries []MediaBuyDelivery `json:"media_buy_deliveries"`
	Context            any                `json:"context,omitempty"`
}

type ReportingPeriod struct {
	Start string `json:"start"`
	End   string `json:"end"`
}

// MarshalJSON preserves the schema-required spend field even when it is zero.
func (d DeliveryTotals) MarshalJSON() ([]byte, error) {
	type deliveryTotals DeliveryTotals
	b, err := json.Marshal(deliveryTotals(d))
	if err != nil {
		return nil, err
	}
	var out map[string]any
	if err := json.Unmarshal(b, &out); err != nil {
		return nil, err
	}
	out["spend"] = d.Spend
	return json.Marshal(out)
}

// Render is a rendering variant inside a CreativeFormat. Wired into generated
// CreativeFormat via schemas/generate.py's INLINE_TYPE_HINTS so the format.json
// renders[] oneOf items become []Render instead of []any.
type Render struct {
	Width  int `json:"width"`
	Height int `json:"height"`
}

type AssetSlot struct {
	ItemType           string   `json:"item_type"`
	AssetID            string   `json:"asset_id"`
	AssetType          string   `json:"asset_type"`
	Required           bool     `json:"required"`
	Description        string   `json:"description,omitempty"`
	AcceptedMediaTypes []string `json:"accepted_media_types,omitempty"`
}

type CreativeResult struct {
	CreativeID      string   `json:"creative_id"`
	Action          string   `json:"action"`
	Status          string   `json:"status,omitempty"`
	RejectionReason string   `json:"rejection_reason,omitempty"`
	Errors          []string `json:"errors,omitempty"`
}

type CreativeListItem struct {
	CreativeID string    `json:"creative_id"`
	Name       string    `json:"name"`
	FormatID   FormatRef `json:"format_id"`
	Status     string    `json:"status"`
}

type PreviewResult struct {
	ResponseType string    `json:"response_type"`
	Previews     []Preview `json:"previews"`
	ExpiresAt    string    `json:"expires_at"`
}

type Preview struct {
	PreviewID string          `json:"preview_id"`
	Input     map[string]any  `json:"input"`
	Renders   []PreviewRender `json:"renders"`
}

type PreviewRender struct {
	RenderID     string  `json:"render_id"`
	OutputFormat string  `json:"output_format"`
	PreviewURL   string  `json:"preview_url,omitempty"`
	HTML         string  `json:"html,omitempty"`
	Role         string  `json:"role"`
	Dimensions   *Render `json:"dimensions,omitempty"`
}

type BuildCreativeResult struct {
	CreativeManifest map[string]any `json:"creative_manifest"`
	Sandbox          bool           `json:"sandbox,omitempty"`
}

type SignalID struct {
	Source             string `json:"source"`
	DataProviderDomain string `json:"data_provider_domain,omitempty"`
	AgentURL           string `json:"agent_url,omitempty"`
	ID                 string `json:"id"`
}

// SignalPricing is an alias for VendorPricingOption. Upstream's
// signal-pricing-option.json is a deprecated $ref to vendor-pricing-option.json;
// the two were always the same shape on the wire. Kept here so existing callers
// that reference SignalPricing continue to compile.
type SignalPricing = VendorPricingOption

// Deployment is the flattened union of deployment.json's oneOf variants.
// Type is the discriminator:
//
//	"platform": set Platform (required), AgentURL, Account, ActivationKey.
//	"agent":    set AgentURL (required), ActivationKey.
//
// All variants: IsLive, DeployedAt, EstimatedActivationDurationMinutes.
// Do not mix variant-specific fields — schema validators accept it because
// additionalProperties=true, but the result is semantically wrong.
type Deployment struct {
	Type                               string         `json:"type"`
	Platform                           string         `json:"platform,omitempty"`
	AgentURL                           string         `json:"agent_url,omitempty"`
	Account                            string         `json:"account,omitempty"`
	IsLive                             bool           `json:"is_live"`
	ActivationKey                      *ActivationKey `json:"activation_key,omitempty"`
	DeployedAt                         string         `json:"deployed_at,omitempty"`
	EstimatedActivationDurationMinutes int            `json:"estimated_activation_duration_minutes,omitempty"`
}

type ActivationKey struct {
	Type      string `json:"type"`
	SegmentID string `json:"segment_id,omitempty"`
	Key       string `json:"key,omitempty"`
	Value     string `json:"value,omitempty"`
}

type CatalogResult struct {
	CatalogID     string `json:"catalog_id"`
	Action        string `json:"action"`
	ItemCount     int    `json:"item_count"`
	ItemsApproved int    `json:"items_approved"`
}

type EventSourceResult struct {
	EventSourceID string            `json:"event_source_id"`
	Action        string            `json:"action"`
	Setup         *EventSourceSetup `json:"setup,omitempty"`
}

// EventSourceSetup provides integration instructions for an event source.
type EventSourceSetup struct {
	Snippet     string `json:"snippet,omitempty"`
	Description string `json:"description,omitempty"`
}

type LogEventResult struct {
	EventsReceived  int `json:"events_received"`
	EventsProcessed int `json:"events_processed"`
}

// --- Collection domain ---

// CollectionList is a managed collection list with optional dynamic filters.
type CollectionList struct {
	ListID             string                 `json:"list_id"`
	Name               string                 `json:"name"`
	Description        string                 `json:"description,omitempty"`
	Principal          string                 `json:"principal,omitempty"`
	BaseCollections    []BaseCollectionSource `json:"base_collections,omitempty"`
	Filters            *CollectionListFilters `json:"filters,omitempty"`
	Brand              *BrandReference        `json:"brand,omitempty"`
	WebhookURL         string                 `json:"webhook_url,omitempty"`
	CacheDurationHours int                    `json:"cache_duration_hours,omitempty"`
	CreatedAt          string                 `json:"created_at,omitempty"`
	UpdatedAt          string                 `json:"updated_at,omitempty"`
	CollectionCount    int                    `json:"collection_count,omitempty"`
}

// BaseCollectionSource selects collections for a collection list.
// Use one of the constructor functions: ByDistributionIDs, ByPublisherCollections, ByPublisherGenres.
type BaseCollectionSource struct {
	SelectionType   string           `json:"selection_type"`
	Identifiers     []DistributionID `json:"identifiers,omitempty"`
	PublisherDomain string           `json:"publisher_domain,omitempty"`
	CollectionIDs   []string         `json:"collection_ids,omitempty"`
	Genres          []string         `json:"genres,omitempty"`
	GenreTaxonomy   string           `json:"genre_taxonomy,omitempty"`
}

// ByDistributionIDs creates a source that selects collections by platform-independent identifiers.
func ByDistributionIDs(ids []DistributionID) BaseCollectionSource {
	return BaseCollectionSource{
		SelectionType: "distribution_ids",
		Identifiers:   ids,
	}
}

// ByPublisherCollections creates a source that selects specific collections within a publisher.
func ByPublisherCollections(domain string, collectionIDs []string) BaseCollectionSource {
	return BaseCollectionSource{
		SelectionType:   "publisher_collections",
		PublisherDomain: domain,
		CollectionIDs:   collectionIDs,
	}
}

// ByPublisherGenres creates a source that selects collections from a publisher by genre.
func ByPublisherGenres(domain string, genres []string, taxonomy string) BaseCollectionSource {
	return BaseCollectionSource{
		SelectionType:   "publisher_genres",
		PublisherDomain: domain,
		Genres:          genres,
		GenreTaxonomy:   taxonomy,
	}
}

// DistributionID is a platform-independent identifier for cross-publisher matching.
type DistributionID struct {
	Type  string `json:"type"`
	Value string `json:"value"`
}

// CollectionListFilters dynamically modify a collection list when resolved.
// Include filters are allowlists; exclude filters are blocklists.
// When both are present, include is applied first, then exclude narrows further.
type CollectionListFilters struct {
	ContentRatingsExclude  []ContentRating  `json:"content_ratings_exclude,omitempty"`
	ContentRatingsInclude  []ContentRating  `json:"content_ratings_include,omitempty"`
	GenresExclude          []string         `json:"genres_exclude,omitempty"`
	GenresInclude          []string         `json:"genres_include,omitempty"`
	GenreTaxonomy          string           `json:"genre_taxonomy,omitempty"`
	Kinds                  []string         `json:"kinds,omitempty"`
	ExcludeDistributionIDs []DistributionID `json:"exclude_distribution_ids,omitempty"`
	ProductionQuality      []string         `json:"production_quality,omitempty"`
}

// ContentRating is a content advisory rating using a specified rating system.
type ContentRating struct {
	System string `json:"system"`
	Rating string `json:"rating"`
}

// ResolvedCollection is a single collection entry in a get_collection_list response.
type ResolvedCollection struct {
	CollectionRID   string           `json:"collection_rid,omitempty"`
	Name            string           `json:"name"`
	DistributionIDs []DistributionID `json:"distribution_ids,omitempty"`
	ContentRating   *ContentRating   `json:"content_rating,omitempty"`
	Genre           []string         `json:"genre,omitempty"`
	GenreTaxonomy   string           `json:"genre_taxonomy,omitempty"`
	Kind            string           `json:"kind,omitempty"`
}

// CollectionPagination has higher limits than standard pagination
// because collection lists can contain thousands of entries.
type CollectionPagination struct {
	MaxResults int    `json:"max_results,omitempty"` // 1-10000, default 1000
	Cursor     string `json:"cursor,omitempty"`
}

// --- Business terms ---

// Duration is a time duration expressed as an interval and unit.
type Duration struct {
	Interval int    `json:"interval"`
	Unit     string `json:"unit"` // "seconds", "minutes", "hours", "days", "campaign"
}

// CancellationPolicy declares cancellation terms for a product.
type CancellationPolicy struct {
	NoticePeriod    Duration        `json:"notice_period"`
	CancellationFee CancellationFee `json:"cancellation_fee"`
}

// CancellationFee describes the fee charged for insufficient cancellation notice.
type CancellationFee struct {
	Type   string  `json:"type"` // "percent_remaining", "full_commitment", "fixed_fee", "none"
	Rate   float64 `json:"rate,omitempty"`
	Amount float64 `json:"amount,omitempty"`
}

// CollectionListRef references an externally managed collection list.
type CollectionListRef struct {
	AgentURL  string `json:"agent_url"`
	ListID    string `json:"list_id"`
	AuthToken string `json:"auth_token,omitempty"`
}

// CreativeConsumption reports consumption metrics from paid creative generation.
type CreativeConsumption struct {
	Tokens          int     `json:"tokens,omitempty"`
	ImagesGenerated int     `json:"images_generated,omitempty"`
	Renders         int     `json:"renders,omitempty"`
	DurationSeconds float64 `json:"duration_seconds,omitempty"`
}

// IndustryIdentifier is an industry-standard creative identifier (Ad-ID, ISCI, etc.).
type IndustryIdentifier struct {
	Type  string `json:"type"`
	Value string `json:"value"`
}

// MeasurementTerms declares billing measurement and makegood terms.
type MeasurementTerms struct {
	BillingMeasurement *BillingMeasurement `json:"billing_measurement,omitempty"`
	MakegoodPolicy     *MakegoodPolicy     `json:"makegood_policy,omitempty"`
}

// BillingMeasurement identifies the vendor whose measurement is authoritative for invoicing.
type BillingMeasurement struct {
	Vendor             *BrandReference `json:"vendor"`
	MaxVariancePercent float64         `json:"max_variance_percent,omitempty"`
	MeasurementWindow  string          `json:"measurement_window,omitempty"`
}

// MakegoodPolicy declares available remedies when a threshold is breached.
type MakegoodPolicy struct {
	AvailableRemedies []string `json:"available_remedies"`
}

// MeasurementWindow defines a measurement maturation window for broadcast TV.
type MeasurementWindow struct {
	WindowID                 string `json:"window_id"`
	Description              string `json:"description,omitempty"`
	DurationDays             int    `json:"duration_days"`
	ExpectedAvailabilityDays int    `json:"expected_availability_days,omitempty"`
	IsGuaranteeBasis         bool   `json:"is_guarantee_basis,omitempty"`
}

// PerformanceStandard defines a rate threshold for a performance metric.
type PerformanceStandard struct {
	Metric    string          `json:"metric"`
	Threshold float64         `json:"threshold"`
	Standard  string          `json:"standard,omitempty"`
	Vendor    *BrandReference `json:"vendor"`
}

// VendorPricingOption wires the vendor-pricing-option.json schema — a
// pricing_option_id wrapper around the signal-pricing.json oneOf. Discriminated
// by Model: cpm, percent_of_media, flat_fee, per_unit, custom. Custom pricing
// requires Description + Metadata; buyers should route it through operator
// review rather than auto-selecting. PricingOptionID is the wrapper field,
// not part of the oneOf.
//
// Go cannot express JSON Schema oneOf at the type level, so required-field
// enforcement per variant is deferred to the schema validator; omitempty on
// numeric fields means legitimate zero values (e.g. CPM: 0) do not round-trip.
type VendorPricingOption struct {
	PricingOptionID string         `json:"pricing_option_id"`
	Model           string         `json:"model"`
	CPM             float64        `json:"cpm,omitempty"`
	Percent         float64        `json:"percent,omitempty"`
	MaxCPM          float64        `json:"max_cpm,omitempty"`
	Amount          float64        `json:"amount,omitempty"`
	Period          string         `json:"period,omitempty"`
	Unit            string         `json:"unit,omitempty"`
	UnitPrice       float64        `json:"unit_price,omitempty"`
	Description     string         `json:"description,omitempty"`
	Metadata        map[string]any `json:"metadata,omitempty"`
	Currency        string         `json:"currency,omitempty"`
	Ext             any            `json:"ext,omitempty"`
}
