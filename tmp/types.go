// Package tmp defines the Trusted Match Protocol message types.
//
// TMP separates ad decisioning into two independent operations:
// Context Match (what content is this?) and Identity Match (is this user eligible?).
// The two operations never share data. The publisher joins them locally.
package tmp

// ContextMatchRequest is sent by the publisher to evaluate available packages
// against content context. MUST NOT contain user identity.
type ContextMatchRequest struct {
	ProtocolVersion string             `json:"protocol_version,omitempty"`
	RequestID       string             `json:"request_id"`
	PropertyID      string             `json:"property_id"`
	PropertyRID     uint64             `json:"property_rid,omitempty"` // Registry integer ID, enriched by router
	PropertyType    PropertyType       `json:"property_type"`
	PlacementID     string             `json:"placement_id"`
	Artifacts       []string           `json:"artifacts,omitempty"`
	Geo             *Geo               `json:"geo,omitempty"`       // Geographic context, publisher controls granularity
	URLHash         uint64             `json:"url_hash,omitempty"`  // FNV-1a of canonicalized URL, enriched by router
	AvailablePkgs   []AvailablePackage `json:"available_packages"`
	Signature       string             `json:"signature,omitempty"` // Ed25519 signature from publisher/router
}

// Geo describes the geographic context of the impression.
// Publisher controls granularity — country for volume filtering,
// finer for valuation. Describes where the ad serves, not where the user is.
type Geo struct {
	Country string `json:"country,omitempty"` // ISO 3166-1 alpha-2
	Region  string `json:"region,omitempty"`  // ISO 3166-2 subdivision
	Metro   *Metro `json:"metro,omitempty"`   // Metro area per AdCP metro-system enum
}

// Metro identifies a metro area using an AdCP-standard classification system.
type Metro struct {
	System string `json:"system"` // "nielsen_dma", "uk_itl2", "eurostat_nuts2", "custom"
	Value  string `json:"value"`  // Code within the system (e.g., "501" for NY DMA)
}

// ContextMatchResponse contains offers for matched packages and optional
// response-level targeting signals for ad server pass-through.
type ContextMatchResponse struct {
	RequestID string   `json:"request_id"`
	Offers    []Offer  `json:"offers"`
	Signals   *Signals `json:"signals,omitempty"`
}

// IdentityMatchRequest is sent by the publisher to evaluate user eligibility.
// MUST NOT contain page context. package_ids MUST include ALL active packages
// for the buyer, not just those on the current page.
type IdentityMatchRequest struct {
	ProtocolVersion string  `json:"protocol_version,omitempty"`
	RequestID       string  `json:"request_id"`
	UserToken       string  `json:"user_token"`
	UIDType         UIDType `json:"uid_type,omitempty"`
	PackageIDs      []string `json:"package_ids"`
}

// IdentityMatchResponse contains per-package eligibility determinations.
type IdentityMatchResponse struct {
	RequestID   string              `json:"request_id"`
	Eligibility []PackageEligibility `json:"eligibility"`
}

// Offer is a buyer's response for a single package. For simple GAM activation,
// only PackageID is needed. For richer integrations, the buyer can include
// brand, price, summary, and creative manifest.
type Offer struct {
	PackageID        string            `json:"package_id"`
	Brand            *BrandRef         `json:"brand,omitempty"`
	Price            *OfferPrice       `json:"price,omitempty"`
	Summary          string            `json:"summary,omitempty"`
	CreativeManifest any               `json:"creative_manifest,omitempty"`
	Macros           map[string]string `json:"macros,omitempty"`
}

// OfferPrice represents a variable price for a TMP offer.
type OfferPrice struct {
	Amount   float64    `json:"amount"`
	Currency string     `json:"currency,omitempty"`
	Model    PriceModel `json:"model"`
}

// AvailablePackage is a lightweight projection of a media buy package
// with the fields needed for real-time evaluation.
type AvailablePackage struct {
	PackageID  string    `json:"package_id"`
	MediaBuyID string    `json:"media_buy_id"`
	FormatIDs  []string  `json:"format_ids,omitempty"`
	Catalogs   []Catalog `json:"catalogs,omitempty"`
}

// PackageEligibility is an identity-based eligibility determination.
// The buyer computes eligibility from frequency caps, audience membership,
// etc. and returns an opaque boolean. The publisher does not learn why.
type PackageEligibility struct {
	PackageID   string   `json:"package_id"`
	Eligible    bool     `json:"eligible"`
	IntentScore *float64 `json:"intent_score,omitempty"`
}

// ExposeRequest notifies the identity provider that a user was exposed to a package.
// Sent by the publisher AFTER rendering the ad. This closes the frequency cap loop.
// campaign_id enables cross-publisher, cross-media-buy frequency management.
type ExposeRequest struct {
	UserToken  string `json:"user_token"`
	UIDType    UIDType `json:"uid_type,omitempty"`
	PackageID  string `json:"package_id"`
	CampaignID string `json:"campaign_id,omitempty"`
	Timestamp  int64  `json:"timestamp,omitempty"`
}

// ExposeResponse acknowledges an exposure notification.
type ExposeResponse struct {
	PackageID         string `json:"package_id"`
	CampaignCount     int    `json:"campaign_count,omitempty"`
	CampaignRemaining int    `json:"campaign_remaining,omitempty"`
}

// ErrorResponse is returned when a provider or router cannot process a request.
type ErrorResponse struct {
	RequestID string    `json:"request_id"`
	Code      ErrorCode `json:"code"`
	Message   string    `json:"message,omitempty"`
}

// Signals contains response-level targeting signals for ad server pass-through.
type Signals struct {
	Segments    []string       `json:"segments,omitempty"`
	TargetingKVs []KeyValuePair `json:"targeting_kvs,omitempty"`
}

// KeyValuePair is a targeting key-value pair for ad server integration.
type KeyValuePair struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

// BrandRef identifies a brand on an offer.
type BrandRef struct {
	Name             string `json:"name"`
	AdvertiserDomain string `json:"advertiser_domain,omitempty"`
}

// Catalog is a lightweight reference to a buyer's product catalog
// with selectors scoping which items are in play.
type Catalog struct {
	CatalogID string   `json:"catalog_id"`
	Type      string   `json:"type,omitempty"`
	GTINs     []string `json:"gtins,omitempty"`
	IDs       []string `json:"ids,omitempty"`
	Tags      []string `json:"tags,omitempty"`
	Category  string   `json:"category,omitempty"`
	Query     string   `json:"query,omitempty"`
}

// PropertyType identifies the type of publisher property.
type PropertyType string

const (
	PropertyTypeWebsite        PropertyType = "website"
	PropertyTypeMobileApp      PropertyType = "mobile_app"
	PropertyTypeCTVApp         PropertyType = "ctv_app"
	PropertyTypeDesktopApp     PropertyType = "desktop_app"
	PropertyTypeDOOH           PropertyType = "dooh"
	PropertyTypePodcast        PropertyType = "podcast"
	PropertyTypeRadio          PropertyType = "radio"
	PropertyTypeStreamingAudio PropertyType = "streaming_audio"
	PropertyTypeAIAssistant    PropertyType = "ai_assistant"
)

// UIDType identifies the type of user identifier token.
type UIDType string

const (
	UIDTypeUID2               UIDType = "uid2"
	UIDTypeRampID             UIDType = "rampid"
	UIDTypeID5                UIDType = "id5"
	UIDTypeEUID               UIDType = "euid"
	UIDTypePairID             UIDType = "pairid"
	UIDTypeMAID               UIDType = "maid"
	UIDTypeHashedEmail        UIDType = "hashed_email"
	UIDTypePublisherFirstParty UIDType = "publisher_first_party"
	UIDTypeOther              UIDType = "other"
)

// PriceModel identifies the pricing model for an offer.
type PriceModel string

const (
	PriceModelCPM  PriceModel = "cpm"
	PriceModelCPC  PriceModel = "cpc"
	PriceModelCPCV PriceModel = "cpcv"
	PriceModelCPA  PriceModel = "cpa"
	PriceModelFlat PriceModel = "flat"
)

// ResponseType declares what a publisher can accept back from context match.
type ResponseType string

const (
	ResponseTypeActivation   ResponseType = "activation"
	ResponseTypeCatalogItems ResponseType = "catalog_items"
	ResponseTypeCreative     ResponseType = "creative"
	ResponseTypeDeal         ResponseType = "deal"
)

// ErrorCode is a machine-readable error code.
type ErrorCode string

const (
	ErrorCodeInvalidRequest      ErrorCode = "invalid_request"
	ErrorCodeUnknownPackage      ErrorCode = "unknown_package"
	ErrorCodeRateLimited         ErrorCode = "rate_limited"
	ErrorCodeTimeout             ErrorCode = "timeout"
	ErrorCodeInternalError       ErrorCode = "internal_error"
	ErrorCodeProviderUnavailable ErrorCode = "provider_unavailable"
)
