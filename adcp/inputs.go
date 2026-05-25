package adcp

// Request types for AdCP tools are generated from JSON schemas in types_gen.go
// (e.g., GetProductsRequest, CreateMediaBuyRequest, SyncAccountsRequest).
//
// This file contains types the generator can't produce: EmptyInput (no schema),
// PackageInput (needs typed business terms), and nested item types that are
// inline objects in the schemas rather than $ref targets.

// EmptyInput is the input type for tools that accept no parameters (e.g. get_adcp_capabilities).
type EmptyInput struct{}

// PackageInput is a single package in a create_media_buy request.
type PackageInput struct {
	ProductID       string  `json:"product_id"`
	PricingOptionID string  `json:"pricing_option_id,omitempty"`
	Budget          float64 `json:"budget"`
	BidPrice        float64 `json:"bid_price,omitempty"`
	BuyerRef        string  `json:"buyer_ref,omitempty"`

	// Business terms (buyer proposals, override product defaults)
	MeasurementTerms     *MeasurementTerms     `json:"measurement_terms,omitempty"`
	PerformanceStandards []PerformanceStandard `json:"performance_standards,omitempty"`
	TargetingOverlay     *Targeting            `json:"targeting_overlay,omitempty"`
	CreativeAssignments  []any                 `json:"creative_assignments,omitempty"`

	// Broadcast / scheduling
	AgencyEstimateNumber string `json:"agency_estimate_number,omitempty"`
	StartTime            string `json:"start_time,omitempty"`
	EndTime              string `json:"end_time,omitempty"`
}

// --- Nested item types (inline objects in schemas, no $ref) ---

// AccountInput is a single account in a sync_accounts request.
type AccountInput struct {
	Brand        *BrandReference `json:"brand,omitempty"`
	Operator     string          `json:"operator,omitempty"`
	Billing      string          `json:"billing,omitempty"`
	PaymentTerms string          `json:"payment_terms,omitempty"`
	AccountID    string          `json:"account_id,omitempty"`
	Sandbox      bool            `json:"sandbox,omitempty"`
}

// GovernanceAccountInput is a single account in a sync_governance request.
type GovernanceAccountInput struct {
	Account          *GovernanceAccount `json:"account,omitempty"`
	Brand            *BrandReference    `json:"brand,omitempty"`
	Operator         string             `json:"operator,omitempty"`
	GovernanceAgents []GovernanceAgent  `json:"governance_agents,omitempty"`
}

// CreativeInput is a single creative in a sync_creatives request.
type CreativeInput struct {
	CreativeID string         `json:"creative_id"`
	FormatID   *FormatRef     `json:"format_id,omitempty"`
	Name       string         `json:"name,omitempty"`
	Assets     map[string]any `json:"assets,omitempty"`
}

// CreativeFilters contains filters for list_creatives.
type CreativeFilters struct {
	CreativeIDs []string    `json:"creative_ids,omitempty"`
	FormatIDs   []FormatRef `json:"format_ids,omitempty"`
}

// CatalogInput is a single catalog in a sync_catalogs request.
type CatalogInput struct {
	CatalogID string           `json:"catalog_id"`
	Items     []map[string]any `json:"items,omitempty"`
}

// EventSourceInput is a single event source.
type EventSourceInput struct {
	EventSourceID string `json:"event_source_id"`
}

// DestinationInput is a single destination in an activate_signal request.
type DestinationInput struct {
	Type     string `json:"type"`
	Platform string `json:"platform,omitempty"`
	AgentURL string `json:"agent_url,omitempty"`
	Account  string `json:"account,omitempty"`
}

// SignalFilters contains optional filters for get_signals.
type SignalFilters struct {
	MaxCPM                float64  `json:"max_cpm,omitempty"`
	MinCoveragePercentage float64  `json:"min_coverage_percentage,omitempty"`
	CatalogTypes          []string `json:"catalog_types,omitempty"`
}
