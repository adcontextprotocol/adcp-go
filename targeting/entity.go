package targeting

import (
	"encoding/json"

	"github.com/adcontextprotocol/adcp-go/tmproto"
)

// PackageConfig defines all targeting dimensions for a package.
// Each field is optional. Empty/nil/zero means that dimension is not evaluated.
//
// A package is an inventory unit (e.g., "premium 300x250 on food sites").
// Multiple Deals can compete for the same package — each deal is a separate
// advertiser/brand offering a price for that inventory. When the package
// activates, the engine returns one Offer per Deal. The publisher mediates.
//
// If no Deals are configured, the engine returns a single Offer using the
// package-level offer fields (Brand, Price, etc.) for backwards compatibility.
type PackageConfig struct {
	PackageID  string
	MediaBuyID string

	// Context dimensions (evaluated during EvaluateContext).
	PropertyList Bitmap // Per-package property bitmap. Nil = all properties pass.
	URLBlocklist bool   // If true, check "url:blocklist:{pkg}" in Store.
	URLAllowlist bool   // If true, check "url:allowlist:{pkg}" in Store.
	TopicTargets bool   // If true, check "topics:package:{pkg}" in Store.

	// Identity dimensions are data-driven from the Store.
	// Push PackageIdentityConfig to "config:pkg:{packageID}" and
	// CampaignFreqConfig to "config:campaign:{campaignID}".

	// Offers — competing brand offers for this package. When the package
	// activates, each entry produces a separate Offer in the response.
	// The publisher mediates among them. Empty means use package-level fields.
	Offers []OfferConfig

	// Package-level offer fields — used when Offers is empty (single-brand package).
	Brand            json.RawMessage
	Price            tmproto.OfferPrice
	Summary          string
	ManifestType     string
	CreativeManifest json.RawMessage
	Macros           map[string]string

	// Output configuration.
	EmitSegments []string // Raw segment IDs to include in Signals when this package activates.
}

// PackageContextConfig is the context-side configuration for a package,
// stored in the Store as JSON at key "config:pkg:{packageID}:context".
// Used when DynamicPackages is true on the Engine.
type PackageContextConfig struct {
	PackageID    string             `json:"package_id"`
	MediaBuyID   string             `json:"media_buy_id,omitempty"`
	URLBlocklist bool               `json:"url_blocklist,omitempty"`
	URLAllowlist bool               `json:"url_allowlist,omitempty"`
	TopicTargets bool               `json:"topic_targets,omitempty"`
	PropertyRIDs []string           `json:"property_rids,omitempty"` // per-package property targeting
	EmitSegments []string           `json:"emit_segments,omitempty"`
	Offers       []OfferConfigJSON  `json:"offers,omitempty"`
	Brand        json.RawMessage    `json:"brand,omitempty"`
	Price        tmproto.OfferPrice `json:"price"`
	Summary      string             `json:"summary,omitempty"`
	ManifestType string             `json:"manifest_type,omitempty"`
	Macros       map[string]string  `json:"macros,omitempty"`
}

// OfferConfigJSON is the JSON-serializable form of OfferConfig.
type OfferConfigJSON struct {
	DealID       string             `json:"deal_id,omitempty"`
	Brand        json.RawMessage    `json:"brand,omitempty"`
	Price        tmproto.OfferPrice `json:"price"`
	Summary      string             `json:"summary,omitempty"`
	ManifestType string             `json:"manifest_type,omitempty"`
	Macros       map[string]string  `json:"macros,omitempty"`
}

// OfferConfig represents a single brand's offer competing for a package.
type OfferConfig struct {
	DealID           string // Optional reference to a commercial arrangement.
	Brand            json.RawMessage
	Price            tmproto.OfferPrice
	Summary          string
	ManifestType     string
	CreativeManifest json.RawMessage
	Macros           map[string]string
}
