// Package fixture is the single source of truth for every identifier the
// containerized end-to-end stack shares between processes: the registry
// stub, the identity-config stub, the Valkey seeder, the router config
// generator, and the verifier.
//
// Nothing here is read from the environment. A value that two containers
// must agree on lives in this file and only in this file, so a drifting
// seed and a drifting assertion are impossible by construction.
package fixture

import (
	"fmt"

	"github.com/adcontextprotocol/adcp-go/targeting/signalstore"
	"github.com/adcontextprotocol/adcp-go/targeting/topicstore"
	"github.com/adcontextprotocol/adcp-go/tmproto"
)

// --- Service addresses inside the compose network ----------------------------
//
// The provider endpoints double as the value each agent must publish in
// TMP_OWN_ENDPOINT_URL: the router signs each fan-out over the provider's
// registered base URL, and the agent rebuilds the same signing input from
// its own configured endpoint. A single character of drift between the two
// fails every signature, so both sides read these constants.

const (
	// ContextAgentEndpoint is the context provider's registered base URL.
	// The router appends /context when dispatching.
	ContextAgentEndpoint = "http://context-agent:8081"
	// IdentityAgentEndpoint is the identity provider's registered base URL.
	// The router appends /identity when dispatching.
	IdentityAgentEndpoint = "http://identity-agent:8080"

	// RouterAddr is the router's protocol listener inside the network.
	RouterAddr = ":8080"
	// RouterAdminAddr moves /metrics and /providers off the protocol
	// listener, mirroring how the agents split ADMIN_PORT.
	RouterAdminAddr = ":9090"
	// RouterBaseURL is how in-network clients reach the protocol listener.
	RouterBaseURL = "http://router:8080"
	// RouterAdminBaseURL is how in-network clients reach the admin listener.
	RouterAdminBaseURL = "http://router:9090"

	// RegistryStubBaseURL is the AdCP registry feed stub. The registry
	// client appends /api/registry/feed.
	RegistryStubBaseURL = "http://registrystub:9101"
	// RegistryStubPort is the port the stub binds.
	RegistryStubPort = 9101
	// ConfigStubPort is the port the identity-config stub binds.
	ConfigStubPort = 9102
	// ConfigStubBaseURL is the identity-config stub's origin. The identity
	// agent's CONFIG_SOURCE_URL is this plus /v1/identity-configs, spelled out
	// in the compose file because an external image cannot import this package.
	ConfigStubBaseURL = "http://configstub:9102"

	// ValkeyAddr is the single Valkey the whole stack shares. The three
	// read paths are separated by logical DB rather than by container: the
	// stack is about wiring, not about store isolation.
	ValkeyAddr = "valkey:6379"
	// ContextValkeyDB backs the context-agent's media-buy, package-config,
	// topic, signal and suppression reads.
	ContextValkeyDB = 0
	// AudienceValkeyDB backs the identity-agent's audience membership reads.
	AudienceValkeyDB = 1
	// FCapValkeyDB backs the identity-agent's frequency-cap marker reads.
	FCapValkeyDB = 2
)

// --- Shared secrets ----------------------------------------------------------
//
// Bearer tokens for the two stubs. Fixed values: the stack is a hermetic
// test network with no path to the internet, and a generated token would
// have to be passed through four containers to no benefit.

const (
	// RegistryFeedToken authenticates the router's and the context-agent's
	// registry feed polls.
	RegistryFeedToken = "e2e-registry-token" //nolint:gosec // fixed stub value, see above
	// ConfigSourceToken authenticates the identity-agent's config polls.
	ConfigSourceToken = "e2e-config-token" //nolint:gosec // fixed stub value, see above
)

// --- Provider registration ---------------------------------------------------

const (
	// ContextProviderID is the provider_id the context-agent runs under.
	// Also stamped into its suppression keys, so the seeder writes
	// suppressions under the same value.
	ContextProviderID = "e2e_context"
	// IdentityProviderID is the provider_id the identity-agent runs under.
	IdentityProviderID = "e2e_identity"

	// RouterSigningKID is the key id the router signs fan-outs with and
	// publishes on /registry/snapshot.
	RouterSigningKID = "e2e-router-1"

	// UIDType is the only identity type this stack exercises. MAID is a
	// format-only decoder — a 32-char lowercase-hex token round-trips
	// through the agent's canonicalizer unchanged, so the audience and
	// fcap keys the seeder writes are exactly the ones the agent reads.
	UIDType = tmproto.UIDTypeMAID

	// Country is the ISO 3166-1 alpha-2 code the identity provider serves
	// and the verifier stamps on identity requests, exercising the
	// router's country-based provider filter.
	Country = "US"
)

// --- Seller ------------------------------------------------------------------

// SellerAgentURL scopes every package in the stack. The context-agent
// resolves active media buys by seller, and the identity-agent keys its
// config snapshot on (seller_agent_url, package_id).
const SellerAgentURL = "https://seller.e2e.local/agent"

// --- Properties --------------------------------------------------------------

// Property is one record the registry stub publishes on its feed. The
// router hydrates its /registry/snapshot from these, and the context-agent
// hydrates the global property bitmap that gates every request from the
// same feed.
type Property struct {
	PropertyID   string
	PropertyRID  string
	PropertyType tmproto.PropertyType
	Domain       string
	Placements   []string
}

// Properties are the property records the feed publishes, in feed order.
var Properties = []Property{
	{
		PropertyID:   "e2e-news",
		PropertyRID:  "019700ff-0e2e-7000-8000-000000000001",
		PropertyType: tmproto.PropertyTypeWebsite,
		Domain:       "news.e2e.local",
		Placements:   NewsPlacements,
	},
	{
		PropertyID:   "e2e-video",
		PropertyRID:  "019700ff-0e2e-7000-8000-000000000002",
		PropertyType: tmproto.PropertyTypeCTVApp,
		Domain:       "video.e2e.local",
		Placements:   []string{PlacementPreroll},
	},
	{
		PropertyID:   "e2e-shuttered",
		PropertyRID:  "019700ff-0e2e-7000-8000-000000000003",
		PropertyType: tmproto.PropertyTypeWebsite,
		Domain:       "shuttered.e2e.local",
		Placements:   []string{PlacementMatchedArtifact},
	},
}

// Named indexes into Properties, so callers never hard-code a RID.
const (
	newsPropertyIdx      = 0
	videoPropertyIdx     = 1
	shutteredPropertyIdx = 2
)

// NewsProperty is a website property with no suppression, used for the
// artifact-driven context scenarios.
func NewsProperty() Property { return Properties[newsPropertyIdx] }

// VideoProperty is a CTV property used to prove per-package property
// scoping (PackageContextConfig.PropertyRIDs).
func VideoProperty() Property { return Properties[videoPropertyIdx] }

// ShutteredProperty is registered in the feed but carries a suppression
// marker, so the context-agent's kill switch drops every offer for it.
func ShutteredProperty() Property { return Properties[shutteredPropertyIdx] }

// UnregisteredPropertyRID is a well-formed property_rid that the feed never
// publishes. Requests carrying it short-circuit on the agent's property
// bitmap, which is the fail-closed behavior the stack asserts.
const UnregisteredPropertyRID = "019700ff-0e2e-7000-8000-0000000000ff"

// Placements the verifier draws from. Seeded media buys leave
// MediaBuyPackage.PlacementIDs empty, so a placement never gates
// eligibility — its only job here is to separate the router's Context Match
// cache entries.
//
// The router keys cached provider responses on
// {property_rid, placement_id, provider_id, seller_agent_url, country}.
// artifact_refs are NOT part of that key, so two requests that differ only
// by artifact would share one cache entry. Every context scenario therefore
// gets a placement of its own, which guarantees each one is a real fan-out
// and not a hit on a neighbouring scenario's entry.
//
// These are the base names. The verifier suffixes each one with a per-run
// nonce so a second run against an already-warm router is also a real
// fan-out; the registry stub publishes the base names as the property's
// placement catalog.
const (
	// PlacementMatchedArtifact carries the artifact whose topics and signal
	// are seeded.
	PlacementMatchedArtifact = "slot-header"
	// PlacementUnmatchedArtifact carries the artifact with neither.
	PlacementUnmatchedArtifact = "slot-mid"
	// PlacementNoArtifact carries no artifact_refs at all.
	PlacementNoArtifact = "slot-footer"
	// PlacementSlugOnly carries property_id without property_rid, so the
	// router has to resolve the RID from its registry.
	PlacementSlugOnly = "slot-sidebar"
	// PlacementPreroll is the CTV property's only placement.
	PlacementPreroll = "slot-preroll"
	// PlacementWarmup is the base name readiness polling draws from. A poll
	// that lands while the agent's property bitmap is still hydrating caches an
	// empty-offer response, so the verifier suffixes this with the attempt
	// number as well as the run nonce: a stale entry then blocks neither an
	// assertion nor the next readiness attempt.
	PlacementWarmup = "slot-warmup"
	// PlacementCache is touched only by the cache scenario, so its first
	// request is guaranteed to be a miss.
	PlacementCache = "slot-cache"
)

// NewsPlacements is the placement catalog the registry stub publishes for
// the news property.
var NewsPlacements = []string{
	PlacementMatchedArtifact,
	PlacementUnmatchedArtifact,
	PlacementNoArtifact,
	PlacementSlugOnly,
	PlacementWarmup,
	PlacementCache,
}

// --- Context packages --------------------------------------------------------

const (
	// PackageContextOpen carries no context gate and offers on every
	// request whose property is registered and unsuppressed.
	PackageContextOpen = "pkg-ctx-open"
	// PackageContextTopic gates on topic overlap between the package's
	// topic set and the request's artifact topics. Fails closed when the
	// request carries no artifact_refs.
	PackageContextTopic = "pkg-ctx-topic"
	// PackageContextSignal gates on a context signal keyed by the
	// artifact's URL hash.
	PackageContextSignal = "pkg-ctx-signal"
	// PackageContextVideoOnly is scoped to the CTV property's RID via
	// PackageContextConfig.PropertyRIDs, so it never offers on the news
	// property.
	PackageContextVideoOnly = "pkg-ctx-video-only"
)

// ContextPackages lists every context package the seeder writes, in the
// order it writes them.
var ContextPackages = []string{
	PackageContextOpen,
	PackageContextTopic,
	PackageContextSignal,
	PackageContextVideoOnly,
}

// MediaBuyID returns the deterministic media-buy id carrying a package.
// One buy per package keeps the seed readable; the agent resolves the
// active set per seller, not per buy.
func MediaBuyID(packageID string) string { return "mb-" + packageID }

// OfferSummary is the summary the seeder stamps on a package's context
// config and the verifier reads back off the offer. Asserting on it proves
// the offer came from the seeded config rather than from a default.
func OfferSummary(packageID string) string {
	return fmt.Sprintf("e2e offer for %s", packageID)
}

// --- Artifacts, topics and signals ------------------------------------------

// AcceptedTaxonomy is the taxonomy the context-agent accepts
// (ACCEPTED_TAXONOMIES) and the seeder writes topic data under. Topics
// under any other taxonomy are dropped on read.
var AcceptedTaxonomy = topicstore.Taxonomy{Source: "iab", ID: 7}

const (
	// TopicNews is attached to both PackageContextTopic and the matching
	// artifact, so their intersection is non-empty.
	TopicNews = "topic-news"
	// TopicFinance is attached only to the non-matching artifact, so that
	// artifact's intersection with the package is empty.
	TopicFinance = "topic-finance"
)

// Artifact URLs the verifier puts on requests as `artifact_refs[].value`
// with type `url` — the shape a publisher actually sends. The engine treats
// that type differently on its two artifact-driven paths, and the seeder has
// to key each store the way the path that reads it does:
//
//	topics   ref value used verbatim, so the topic index is keyed on the URL
//	signals  ref value canonicalized and hashed through tmproto.HashURL,
//	         because raw URLs collide with the signal key's delimiters
//
// Sending type `url_hash` with a raw URL would skip the hashing entirely and
// leave canonicalization untested, so the fixture deliberately does not.
const (
	// ArtifactMatched carries TopicNews and a seeded context signal, so it
	// activates both the topic-gated and signal-gated packages.
	ArtifactMatched = "https://news.e2e.local/article-market-open"
	// ArtifactUnmatched carries neither, so it activates neither.
	ArtifactUnmatched = "https://news.e2e.local/article-quarterly-results"
)

// SignalValue returns the value the signal keyspace is keyed on for an
// artifact URL: the canonicalized-then-hashed form the engine derives from a
// `url` artifact ref. The seeder writes under this and the engine looks up
// under this, so a regression in URL canonicalization or hashing breaks the
// signal scenario instead of passing on a shape neither side normalizes.
func SignalValue(artifactURL string) string { return tmproto.HashURL(artifactURL) }

const (
	// SignalOwnerID scopes the seeded signal keyspace.
	SignalOwnerID = "1"
	// SignalID is the value the seeded signal key carries and the
	// signal-gated package's config matches on.
	SignalID = "sig-e2e-1"
)

// SignalKeyTypes is the key-type list both the seeded signal keys and the
// package config use. url_hash is the axis a `url` artifact ref projects onto.
var SignalKeyTypes = []signalstore.KeyType{signalstore.KeyTypeURLHash}

// --- Identity packages and users --------------------------------------------

const (
	// PackageIdentityOpen carries no audience rule, so every user is
	// eligible unless a frequency-cap marker excludes them.
	PackageIdentityOpen = "pkg-id-open"
	// PackageIdentityAudience carries an anyOf audience rule, so only a
	// user with that segment membership is eligible.
	PackageIdentityAudience = "pkg-id-audience"
	// PackageIdentityCapped carries no audience rule but has a
	// frequency-cap marker seeded for UserSegmented, so that user is
	// excluded and UserAnonymous is not.
	PackageIdentityCapped = "pkg-id-capped"
)

// IdentityPackages lists every package the identity-config stub publishes,
// in the order the verifier sends them.
var IdentityPackages = []string{
	PackageIdentityOpen,
	PackageIdentityAudience,
	PackageIdentityCapped,
}

// AudienceSegment is the only segment in the stack. UserSegmented belongs
// to it; UserAnonymous does not.
const AudienceSegment = "aud-e2e-sports"

// AudienceScore is the membership score the seeder writes. Any finite
// value makes the user a member; the engine gates on presence.
const AudienceScore = 1.0

// UserToken returns a MAID-shaped token: 32 lowercase hex characters,
// which the agent's MAID decoder turns into 16 bytes and re-encodes to the
// same string. That round-trip is what lets the seeder compute the audience
// and fcap keys the agent will read.
func UserToken(n int) string { return fmt.Sprintf("%032x", 0x0E2E0000+n) }

// UserSegmented belongs to AudienceSegment and carries a frequency-cap
// marker on PackageIdentityCapped.
func UserSegmented() string { return UserToken(1) }

// UserAnonymous belongs to no segment and carries no frequency-cap markers.
func UserAnonymous() string { return UserToken(2) }

// --- seeded state lifetimes --------------------------------------------------
//
// Both are long enough that a slow stack cannot expire them mid-run, and short
// enough that a leftover Valkey volume does not keep serving them for days.
// The suppression store rejects a non-positive TTL, so this is not optional.

// FCapTTLHours bounds the seeded frequency-cap markers.
const FCapTTLHours = 24

// SuppressionTTLHours bounds the seeded property suppression marker.
const SuppressionTTLHours = 24
