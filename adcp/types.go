// Package adcp provides helpers for building AdCP MCP servers in Go.
package adcp

type CapabilitiesData struct {
	ADCP               *ADCPVersion `json:"adcp,omitempty"`
	SupportedProtocols []string     `json:"supported_protocols"`
}

type ADCPVersion struct {
	MajorVersions []int `json:"major_versions"`
}

type BrandReference struct {
	Domain string `json:"domain"`
}

type AccountReference struct {
	AccountID string          `json:"account_id,omitempty"`
	Brand     *BrandReference `json:"brand,omitempty"`
	Operator  string          `json:"operator,omitempty"`
	Sandbox   bool            `json:"sandbox,omitempty"`
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
	Products []Product `json:"products"`
	Sandbox  bool      `json:"sandbox,omitempty"`
}

type Product struct {
	ProductID           string          `json:"product_id"`
	Name                string          `json:"name"`
	Description         string          `json:"description,omitempty"`
	Channel             string          `json:"channel,omitempty"`
	DeliveryType        string          `json:"delivery_type"`
	PricingOptions      []PricingOption `json:"pricing_options"`
	Exclusivity         string          `json:"exclusivity,omitempty"`
	Targeting           *Targeting      `json:"targeting,omitempty"`
	CreativeSpecs       []CreativeSpec  `json:"creative_specs,omitempty"`
	PublisherProperties []string        `json:"publisher_properties"`
	FormatIDs           []FormatRef     `json:"format_ids"`
}

// FormatRef is a reference to a creative format.
type FormatRef struct {
	AgentURL string `json:"agent_url"`
	ID       string `json:"id"`
}

type PricingOption struct {
	PricingOptionID string  `json:"pricing_option_id"`
	PricingModel    string  `json:"pricing_model"`
	FixedPrice      float64 `json:"fixed_price,omitempty"`
	FloorPrice      float64 `json:"floor_price,omitempty"`
	Currency        string  `json:"currency"`
	MinSpendPerPkg  float64 `json:"min_spend_per_package,omitempty"`
}

type Targeting struct {
	Geo      []GeoTarget `json:"geo,omitempty"`
	Channels []string    `json:"channels,omitempty"`
}

type GeoTarget struct {
	Country string `json:"country,omitempty"`
	Region  string `json:"region,omitempty"`
}

type CreativeSpec struct {
	FormatID string `json:"format_id,omitempty"`
	Width    int    `json:"width,omitempty"`
	Height   int    `json:"height,omitempty"`
}

type MediaBuyData struct {
	MediaBuyID string    `json:"media_buy_id"`
	Status     string    `json:"status,omitempty"`
	Currency   string    `json:"currency,omitempty"`
	Packages   []Package `json:"packages"`
}

type Package struct {
	PackageID       string  `json:"package_id"`
	ProductID       string  `json:"product_id"`
	PricingOptionID string  `json:"pricing_option_id"`
	Budget          float64 `json:"budget"`
	Status          string  `json:"status,omitempty"`
}

type MediaBuyListItem struct {
	MediaBuyID string    `json:"media_buy_id"`
	Status     string    `json:"status"`
	Currency   string    `json:"currency"`
	Packages   []Package `json:"packages"`
}

type DeliveryData struct {
	ReportingPeriod    ReportingPeriod    `json:"reporting_period"`
	MediaBuyDeliveries []MediaBuyDelivery `json:"media_buy_deliveries"`
}

type ReportingPeriod struct {
	Start string `json:"start"`
	End   string `json:"end"`
}

type MediaBuyDelivery struct {
	MediaBuyID string            `json:"media_buy_id"`
	Status     string            `json:"status"`
	Totals     DeliveryTotals    `json:"totals"`
	ByPackage  []PackageDelivery `json:"by_package"`
}

type DeliveryTotals struct {
	Impressions int     `json:"impressions"`
	Clicks      int     `json:"clicks,omitempty"`
	Spend       float64 `json:"spend"`
	Conversions int     `json:"conversions,omitempty"`
}

type PackageDelivery struct {
	PackageID string         `json:"package_id"`
	Totals    DeliveryTotals `json:"totals"`
}

type CreativeFormatID struct {
	AgentURL string `json:"agent_url"`
	ID       string `json:"id"`
}

type CreativeFormat struct {
	FormatID    CreativeFormatID `json:"format_id"`
	Name        string           `json:"name"`
	Description string           `json:"description,omitempty"`
	Renders     []Render         `json:"renders,omitempty"`
	Assets      []AssetSlot      `json:"assets,omitempty"`
}

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
	CreativeID string           `json:"creative_id"`
	Name       string           `json:"name"`
	FormatID   CreativeFormatID `json:"format_id"`
	Status     string           `json:"status"`
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

type Signal struct {
	SignalAgentSegmentID string          `json:"signal_agent_segment_id"`
	Name                 string          `json:"name"`
	Description          string          `json:"description"`
	SignalType           string          `json:"signal_type"`
	DataProvider         string          `json:"data_provider"`
	CoveragePercentage   float64         `json:"coverage_percentage"`
	Deployments          []Deployment    `json:"deployments"`
	PricingOptions       []SignalPricing `json:"pricing_options"`
	SignalID             SignalID        `json:"signal_id"`
	ValueType            string          `json:"value_type,omitempty"`
}

type SignalID struct {
	Source             string `json:"source"`
	DataProviderDomain string `json:"data_provider_domain,omitempty"`
	AgentURL           string `json:"agent_url,omitempty"`
	ID                 string `json:"id"`
}

type SignalPricing struct {
	PricingOptionID string  `json:"pricing_option_id"`
	Model           string  `json:"model"`
	CPM             float64 `json:"cpm,omitempty"`
	Percent         float64 `json:"percent,omitempty"`
	Amount          float64 `json:"amount,omitempty"`
	Period          string  `json:"period,omitempty"`
	Currency        string  `json:"currency"`
}

type Deployment struct {
	Type          string         `json:"type"`
	Platform      string         `json:"platform,omitempty"`
	AgentURL      string         `json:"agent_url,omitempty"`
	Account       string         `json:"account,omitempty"`
	IsLive        bool           `json:"is_live"`
	ActivationKey *ActivationKey `json:"activation_key,omitempty"`
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
	EventSourceID string `json:"event_source_id"`
	Action        string `json:"action"`
}

type LogEventResult struct {
	EventsReceived  int `json:"events_received"`
	EventsProcessed int `json:"events_processed"`
}
