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

	// Direct-match fields for buyer-typed value targeting that the TMP
	// request carries verbatim. Each field is independent: a non-empty
	// inclusion list requires a match; a non-empty exclusion list
	// rejects on match. Empty fields impose no constraint, so adding
	// any of these to a config that did not previously set them is
	// backward compatible.
	//
	// Geo dimensions honor the spec hierarchy resolution rule
	// (docs/media-buy/advanced-topics/targeting.mdx): exclusion at a
	// higher level (country) takes precedence over inclusion at a more
	// specific level (region, metro). The engine enforces this by
	// short-circuiting on country exclusion before evaluating region
	// or metro.
	//
	// These fields are an alternative to expressing the same intent
	// through ContextSignals cfgs: a value-equality match against
	// dimensions the publisher sends on the request needs no signal
	// indirection. ContextSignals remains the mechanism for third-party
	// audience signals where membership is provider-defined.

	// Countries restricts to requests whose Geo.country is in the list
	// (ISO 3166-1 alpha-2). Empty disables the gate.
	Countries []string `json:"countries,omitempty"`
	// CountriesExclude rejects requests whose Geo.country is in the
	// list. Takes precedence over inclusion at region and metro
	// (hierarchy resolution).
	CountriesExclude []string `json:"countries_exclude,omitempty"`

	// Regions restricts to requests whose Geo.region is in the list
	// (ISO 3166-2 subdivision codes).
	Regions []string `json:"regions,omitempty"`
	// RegionsExclude rejects requests whose Geo.region is in the list.
	// Takes precedence over inclusion at metro (hierarchy resolution).
	RegionsExclude []string `json:"regions_exclude,omitempty"`

	// Metros restricts to requests whose Geo.metro matches at least
	// one entry on system and value. Different systems in the same
	// list are independent — the request's classification system must
	// match an entry's system AND the request's value must be in that
	// entry's values.
	Metros []MetroTarget `json:"metros,omitempty"`
	// MetrosExclude rejects requests whose Geo.metro matches any entry.
	MetrosExclude []MetroTarget `json:"metros_exclude,omitempty"`

	// Languages restricts to requests whose ContextSignals.language is
	// in the list (ISO 639-1).
	Languages []string `json:"languages,omitempty"`
	// LanguagesExclude rejects requests whose language is in the list.
	LanguagesExclude []string `json:"languages_exclude,omitempty"`

	// Sentiments restricts to requests whose ContextSignals.sentiment
	// is in the list (one of "positive", "negative", "neutral", "mixed").
	Sentiments []string `json:"sentiments,omitempty"`
	// SentimentsExclude rejects requests whose sentiment is in the list.
	SentimentsExclude []string `json:"sentiments_exclude,omitempty"`

	// Keywords requires at least one overlap with
	// ContextSignals.keywords on the request.
	Keywords []string `json:"keywords,omitempty"`
	// KeywordsExclude rejects requests whose keywords overlap with
	// this list.
	KeywordsExclude []string `json:"keywords_exclude,omitempty"`

	// ContentPolicies requires at least one overlap with
	// ContextSignals.content_policies on the request.
	ContentPolicies []string `json:"content_policies,omitempty"`
	// ContentPoliciesExclude rejects requests whose content policies
	// overlap with this list.
	ContentPoliciesExclude []string `json:"content_policies_exclude,omitempty"`

	// Content-artifact identifier lists. Each list is matched against
	// ArtifactRefs entries of the corresponding Type. Inclusion requires
	// at least one ArtifactRef value to be in the list; exclusion
	// rejects on any overlap.
	EIDRs             []string `json:"eidrs,omitempty"`
	EIDRsExclude      []string `json:"eidrs_exclude,omitempty"`
	Gracenotes        []string `json:"gracenotes,omitempty"`
	GracenotesExclude []string `json:"gracenotes_exclude,omitempty"`
	ISRCs             []string `json:"isrcs,omitempty"`
	ISRCsExclude      []string `json:"isrcs_exclude,omitempty"`
	GTINs             []string `json:"gtins,omitempty"`
	GTINsExclude      []string `json:"gtins_exclude,omitempty"`
	RSSGUIDs          []string `json:"rss_guids,omitempty"`
	RSSGUIDsExclude   []string `json:"rss_guids_exclude,omitempty"`
	ISBNs             []string `json:"isbns,omitempty"`
	ISBNsExclude      []string `json:"isbns_exclude,omitempty"`

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
	Macros           map[string]string  `json:"macros,omitempty"`

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

// MetroTarget is one entry in a Metros / MetrosExclude list. System is
// the metro classification system (e.g., "nielsen_dma", "uk_itl2",
// "eurostat_nuts2") and Values are the metro codes within that system.
// A request's Geo.metro matches when its system equals this entry's
// System AND its value is in Values.
type MetroTarget struct {
	System string   `json:"system"`
	Values []string `json:"values"`
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
