package adcp

// Input types for AdCP tool handlers. Use with adcp.AddTool for typed input parsing.
// Fields use `omitempty` so storyboard requests with missing optional fields are accepted.

// EmptyInput is the input type for tools that accept no parameters (e.g. get_adcp_capabilities).
type EmptyInput struct{}

// SyncAccountsInput is the input for sync_accounts.
type SyncAccountsInput struct {
	Accounts []AccountInput `json:"accounts"`
}

// AccountInput is a single account in a sync_accounts request.
type AccountInput struct {
	Brand        *BrandReference `json:"brand,omitempty"`
	Operator     string          `json:"operator,omitempty"`
	Billing      string          `json:"billing,omitempty"`
	PaymentTerms string          `json:"payment_terms,omitempty"`
	AccountID    string          `json:"account_id,omitempty"`
	Sandbox      bool            `json:"sandbox,omitempty"`
}

// SyncGovernanceInput is the input for sync_governance.
type SyncGovernanceInput struct {
	Accounts []GovernanceAccountInput `json:"accounts"`
}

// GovernanceAccountInput is a single account in a sync_governance request.
type GovernanceAccountInput struct {
	Account          *GovernanceAccount `json:"account,omitempty"`
	Brand            *BrandReference    `json:"brand,omitempty"`
	Operator         string             `json:"operator,omitempty"`
	GovernanceAgents []GovernanceAgent  `json:"governance_agents,omitempty"`
}

// GetProductsInput is the input for get_products.
type GetProductsInput struct {
	Brief   any               `json:"brief,omitempty"`
	Account *AccountReference `json:"account,omitempty"`
}

// CreateMediaBuyInput is the input for create_media_buy.
type CreateMediaBuyInput struct {
	Account  *AccountReference  `json:"account,omitempty"`
	Packages []PackageInput     `json:"packages"`
	Currency string             `json:"currency,omitempty"`
}

// PackageInput is a single package in a create_media_buy request.
type PackageInput struct {
	ProductID       string  `json:"product_id"`
	PricingOptionID string  `json:"pricing_option_id,omitempty"`
	Budget          float64 `json:"budget"`
	BidPrice        float64 `json:"bid_price,omitempty"`
}

// GetMediaBuysInput is the input for get_media_buys.
type GetMediaBuysInput struct {
	Account    *AccountReference `json:"account,omitempty"`
	MediaBuyID string            `json:"media_buy_id,omitempty"`
}

// ListCreativeFormatsInput is the input for list_creative_formats.
type ListCreativeFormatsInput struct {
	Account *AccountReference `json:"account,omitempty"`
}

// SyncCreativesInput is the input for sync_creatives.
type SyncCreativesInput struct {
	Account   *AccountReference `json:"account,omitempty"`
	Creatives []CreativeInput   `json:"creatives"`
}

// CreativeInput is a single creative in a sync_creatives request.
type CreativeInput struct {
	CreativeID string           `json:"creative_id"`
	FormatID   *CreativeFormatID `json:"format_id,omitempty"`
	Name       string           `json:"name,omitempty"`
	Assets     map[string]any   `json:"assets,omitempty"`
}

// GetMediaBuyDeliveryInput is the input for get_media_buy_delivery.
type GetMediaBuyDeliveryInput struct {
	Account    *AccountReference `json:"account,omitempty"`
	MediaBuyID string            `json:"media_buy_id,omitempty"`
	MediaBuyIDs []string         `json:"media_buy_ids,omitempty"`
}

// ListCreativesInput is the input for list_creatives.
type ListCreativesInput struct {
	Filters *CreativeFilters `json:"filters,omitempty"`
}

// CreativeFilters contains filters for list_creatives.
type CreativeFilters struct {
	FormatIDs []CreativeFormatID `json:"format_ids,omitempty"`
}

// PreviewCreativeInput is the input for preview_creative.
type PreviewCreativeInput struct {
	CreativeID string `json:"creative_id"`
}

// BuildCreativeInput is the input for build_creative.
type BuildCreativeInput struct {
	CreativeID string `json:"creative_id"`
}

// SyncCatalogsInput is the input for sync_catalogs.
type SyncCatalogsInput struct {
	Account  *AccountReference `json:"account,omitempty"`
	Catalogs []CatalogInput    `json:"catalogs"`
}

// CatalogInput is a single catalog in a sync_catalogs request.
type CatalogInput struct {
	CatalogID string           `json:"catalog_id"`
	Items     []map[string]any `json:"items,omitempty"`
}

// SyncEventSourcesInput is the input for sync_event_sources.
type SyncEventSourcesInput struct {
	EventSources []EventSourceInput `json:"event_sources"`
}

// EventSourceInput is a single event source.
type EventSourceInput struct {
	EventSourceID string `json:"event_source_id"`
}

// LogEventInput is the input for log_event.
type LogEventInput struct {
	Events []map[string]any `json:"events"`
}

// PerformanceFeedbackInput is the input for provide_performance_feedback.
type PerformanceFeedbackInput struct {
	MediaBuyID string         `json:"media_buy_id,omitempty"`
	Metrics    map[string]any `json:"metrics,omitempty"`
}

// GetSignalsInput is the input for get_signals.
type GetSignalsInput struct {
	SignalSpec string     `json:"signal_spec,omitempty"`
	SignalIDs  []SignalID `json:"signal_ids,omitempty"`
	Filters    *SignalFilters `json:"filters,omitempty"`
	MaxResults int        `json:"max_results,omitempty"`
}

// SignalFilters contains optional filters for get_signals.
type SignalFilters struct {
	MaxCPM                float64  `json:"max_cpm,omitempty"`
	MinCoveragePercentage float64  `json:"min_coverage_percentage,omitempty"`
	CatalogTypes          []string `json:"catalog_types,omitempty"`
}

// ActivateSignalInput is the input for activate_signal.
type ActivateSignalInput struct {
	SignalAgentSegmentID string              `json:"signal_agent_segment_id"`
	PricingOptionID      string              `json:"pricing_option_id,omitempty"`
	Destinations         []DestinationInput  `json:"destinations"`
}

// DestinationInput is a single destination in an activate_signal request.
type DestinationInput struct {
	Type     string `json:"type"`
	Platform string `json:"platform,omitempty"`
	AgentURL string `json:"agent_url,omitempty"`
	Account  string `json:"account,omitempty"`
}
