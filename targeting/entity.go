package targeting

import (
	"encoding/json"

	"github.com/adcontextprotocol/adcp-go/targeting/signalstore"
	"github.com/adcontextprotocol/adcp-go/tmproto"
)

// PackageContextConfig is the context-side configuration for a package.
// The context engine reads it through ContextStorage; the writer side
// owns persistence (see targeting/pkgconfigstore).
//
// The snake_case JSON tags describe the writer's canonical serialization
// when stored in Valkey at config:pkg:{package_id}:context. They are
// intentionally distinct from the camelCase wire shape used by
// targeting/identityconfig/scope3, which is a separate external
// contract.
type PackageContextConfig struct {
	PackageID        string             `json:"package_id"`
	MediaBuyID       string             `json:"media_buy_id,omitempty"`
	URLBlocklist     bool               `json:"url_blocklist,omitempty"`
	URLAllowlist     bool               `json:"url_allowlist,omitempty"`
	TopicTargets     bool               `json:"topic_targets,omitempty"`
	PropertyRIDs     []string           `json:"property_rids,omitempty"`
	EmitSegments     []string           `json:"emit_segments,omitempty"`

	// ContextSignals gates the package on context-attribute signal
	// targeting. Nil or empty profile passes vacuously. Restricted to
	// context attributes — see signalstore.AllowedKeyTypes;
	// identity-keyed cfgs are rejected by signalstore.Profile.Validate.
	ContextSignals   *signalstore.Profile `json:"context_signals,omitempty"`

	Offers           []OfferConfigJSON  `json:"offers,omitempty"`
	Brand            json.RawMessage    `json:"brand,omitempty"`
	Price            tmproto.OfferPrice `json:"price"`
	Summary          string             `json:"summary,omitempty"`
	ManifestType     string             `json:"manifest_type,omitempty"`
	CreativeManifest json.RawMessage    `json:"creative_manifest,omitempty"`
	Macros           map[string]string  `json:"macros,omitempty"`
}

// OfferConfigJSON is one competing brand offer for a package. When a
// package activates with multiple Offers configured, each entry
// produces a separate tmproto.Offer in the response and the publisher
// mediates.
type OfferConfigJSON struct {
	DealID       string             `json:"deal_id,omitempty"`
	Brand        json.RawMessage    `json:"brand,omitempty"`
	Price        tmproto.OfferPrice `json:"price"`
	Summary      string             `json:"summary,omitempty"`
	ManifestType string             `json:"manifest_type,omitempty"`
	Macros       map[string]string  `json:"macros,omitempty"`
}
