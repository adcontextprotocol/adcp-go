package targeting

import (
	"encoding/json"
	"slices"

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
	PackageID    string   `json:"package_id"`
	MediaBuyID   string   `json:"media_buy_id,omitempty"`
	TopicTargets bool     `json:"topic_targets,omitempty"`
	PropertyRIDs []string `json:"property_rids,omitempty"`
	EmitSegments []string `json:"emit_segments,omitempty"`

	// ContextSignals gates the package on context-attribute signal
	// targeting. Nil or empty profile passes vacuously. Restricted to
	// context attributes — see signalstore.AllowedKeyTypes;
	// identity-keyed cfgs are rejected by signalstore.Profile.Validate.
	//
	// Topic cfgs key on a namespaced value, NOT the bare topic id: the
	// engine prefixes each topic with its taxonomy as
	// "{taxonomy_source}:{taxonomy_id}:{topic_id}" (the
	// topicstore.Taxonomy.String() form) so the same topic id under
	// different taxonomies stays distinct. The spec carries the
	// taxonomy once on the ContextSignals envelope rather than per
	// topic, so anyone authoring these cfgs out-of-band MUST apply the
	// same prefix to a topic cfg's stored signal value.
	ContextSignals *signalstore.Profile `json:"context_signals,omitempty"`

	Offers           []OfferConfigJSON  `json:"offers,omitempty"`
	Brand            json.RawMessage    `json:"brand,omitempty"`
	Price            tmproto.OfferPrice `json:"price"`
	Summary          string             `json:"summary,omitempty"`
	ManifestType     string             `json:"manifest_type,omitempty"`
	CreativeManifest json.RawMessage    `json:"creative_manifest,omitempty"`
	// CreativeData carries package-local free-form rendering enhancements
	// (sponsor labels, promo codes). Not ad-server macro substitution and
	// not attribution tracking — per AdCP 3.2, tracker URLs belong in
	// CreativeManifest assets and per-user exposure tracking uses TMPX.
	// Operator configs previously used the "macros" JSON key for this
	// field; the wire tag is now "creative_data".
	CreativeData     map[string]string  `json:"creative_data,omitempty"`

	// propertyRIDBitmap is a materialized O(1) view of PropertyRIDs so
	// membership checks on the hot path avoid rebuilding a map on every
	// request. Populated by MaterializePropertyBitmap; ContainsPropertyRID
	// falls back to a linear slice scan when the bitmap is nil.
	//
	// Safety invariant: PropertyRIDs contents MUST be treated as
	// immutable after MaterializePropertyBitmap runs. The bitmap is
	// derived from the slice at build time and never re-derived, so an
	// in-place element write, a resize, or any other mutation of the
	// backing array silently desynchronizes the bitmap from PropertyRIDs
	// and produces a stale gate. A caller that must mutate PropertyRIDs
	// on an already-materialized config MUST call
	// MaterializePropertyBitmap again on the mutated copy.
	//
	// The pkgconfigstore cache's `out := *cfg` clone preserves the
	// invariant by REALLOCATING PropertyRIDs with identical contents
	// (see clonePackageContextConfig) rather than reusing the backing
	// array; the bitmap pointer memcopies to the clone and stays
	// consistent with the freshly allocated slice because contents
	// match. If that clone is ever changed to reuse the slice in place,
	// this gate breaks silently — re-materialize at the mutation site.
	//
	// Under the immutability invariant the bitmap is safe to share
	// across clones and across goroutines: MapBitmap is written only
	// inside NewMapBitmap (before the pointer is published), and every
	// later access is a read-only map lookup.
	propertyRIDBitmap Bitmap
}

// MaterializePropertyBitmap builds the O(1) membership index over
// PropertyRIDs so subsequent ContainsPropertyRID calls do not rebuild a
// set on every check. Storage decoders SHOULD call this once after
// unmarshaling; callers that construct configs directly (e.g. tests)
// can call it explicitly or omit it — ContainsPropertyRID has a
// slice-scan fallback. Idempotent; the last call wins.
func (c *PackageContextConfig) MaterializePropertyBitmap() {
	if len(c.PropertyRIDs) == 0 {
		c.propertyRIDBitmap = nil
		return
	}
	c.propertyRIDBitmap = NewMapBitmap(c.PropertyRIDs...)
}

// ContainsPropertyRID reports whether rid passes the config's
// PropertyRIDs gate. An empty PropertyRIDs list is "no gate" and
// returns true. Uses the materialized bitmap when present; falls back
// to a linear slice scan otherwise so directly-constructed configs
// keep working without an explicit MaterializePropertyBitmap call.
func (c *PackageContextConfig) ContainsPropertyRID(rid string) bool {
	if len(c.PropertyRIDs) == 0 {
		return true
	}
	if c.propertyRIDBitmap != nil {
		return c.propertyRIDBitmap.Contains(rid)
	}
	return slices.Contains(c.PropertyRIDs, rid)
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
	// See PackageContextConfig.CreativeData for semantics; same field
	// under the AdCP 3.2 rename from macros.
	CreativeData map[string]string  `json:"creative_data,omitempty"`
}

// UnmarshalJSON accepts both the AdCP 3.2 canonical "creative_data"
// key and the pre-3.2 legacy "macros" key so persisted operator
// configs written under the old key keep loading after the rename.
// If both keys are present, "creative_data" wins. Rewriting a loaded
// config back to storage canonicalizes it to "creative_data".
func (o *OfferConfigJSON) UnmarshalJSON(data []byte) error {
	type raw OfferConfigJSON
	aux := &struct {
		LegacyMacros map[string]string `json:"macros,omitempty"`
		*raw
	}{raw: (*raw)(o)}
	if err := json.Unmarshal(data, aux); err != nil {
		return err
	}
	if o.CreativeData == nil && aux.LegacyMacros != nil {
		o.CreativeData = aux.LegacyMacros
	}
	return nil
}

// UnmarshalJSON accepts both the AdCP 3.2 canonical "creative_data"
// key and the pre-3.2 legacy "macros" key so persisted operator
// configs written under the old key keep loading after the rename.
// If both keys are present, "creative_data" wins. Rewriting a loaded
// config back to storage canonicalizes it to "creative_data".
func (c *PackageContextConfig) UnmarshalJSON(data []byte) error {
	type raw PackageContextConfig
	aux := &struct {
		LegacyMacros map[string]string `json:"macros,omitempty"`
		*raw
	}{raw: (*raw)(c)}
	if err := json.Unmarshal(data, aux); err != nil {
		return err
	}
	if c.CreativeData == nil && aux.LegacyMacros != nil {
		c.CreativeData = aux.LegacyMacros
	}
	return nil
}
