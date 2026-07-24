// Package corpus holds constants shared between the seeder and loadgen
// so both tools generate/expect the same synthetic identifiers without
// coordination via env vars or filesystem.
//
// property_rid values are UUIDv7-shaped strings. The context-agent's
// PROPERTY_RIDS env var accepts them verbatim and the request-time
// property bitmap short-circuits any request whose property_rid isn't
// in the accepted set — so seeder + loadgen + compose all agree on the
// same list defined here.
package corpus

import (
	"fmt"
	"os"
	"strings"

	"github.com/adcontextprotocol/adcp-go/targeting/topicstore"
)

// SellerAgentURL is the seller_agent_url the seeder registers media
// buys under and the loadgen puts on every ContextMatchRequest so the
// agent's active-set lookup resolves the seeded buys. Kept identical to
// bench/identity-perf's seller so container logs read consistently across
// harnesses.
const SellerAgentURL = "https://seller.perf.local/agent"

// defaultPropertyRIDs is the fallback list used only when the
// PROPERTY_RIDS env var is unset (dev / test invocations). Under
// docker compose, bench/context-perf/perf.env is wired into every
// service via `env_file:` so the seeder, loadgen, and context-agent
// agree on exactly one list with no code-side duplication.
var defaultPropertyRIDs = []string{
	"019700ff-0001-7000-8000-000000000001",
	"019700ff-0001-7000-8000-000000000002",
	"019700ff-0001-7000-8000-000000000003",
	"019700ff-0001-7000-8000-000000000004",
	"019700ff-0001-7000-8000-000000000005",
}

// PropertyRIDs returns the property_rid list this process should draw
// from. Reads PROPERTY_RIDS (comma-separated) from the environment,
// falls back to defaultPropertyRIDs when unset. Whitespace around each
// entry is trimmed; empty entries are dropped. Panics if the env is
// set to a value that parses to zero non-empty entries — a truncated
// or malformed override would silently disable the property bitmap
// and let every request short-circuit.
func PropertyRIDs() []string {
	raw := os.Getenv("PROPERTY_RIDS")
	if raw == "" {
		out := make([]string, len(defaultPropertyRIDs))
		copy(out, defaultPropertyRIDs)
		return out
	}
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	if len(out) == 0 {
		panic(fmt.Sprintf("corpus: PROPERTY_RIDS=%q parses to zero non-empty entries", raw))
	}
	return out
}

// PropertyID is the human-readable property slug the loadgen puts on
// requests. The agent uses property_rid as the primary key; the
// property_id is optional and used for logging only.
const PropertyID = "perf-property"

// PropertyType is what the loadgen stamps on requests.
const PropertyType = "website"

// PlacementIDs is the pool of placement_ids the loadgen draws from.
// Seeded MediaBuyPackages leave PlacementIDs empty (== all placements),
// so any placement_id from this pool matches every active package.
var PlacementIDs = []string{
	"slot-header",
	"slot-mid",
	"slot-footer",
	"slot-sidebar",
}

// Country is the country the loadgen puts on geo (when it puts one at
// all). MediaBuys are seeded with empty Countries (== all), so this is
// only exercised by the geo-suppression code path.
const Country = "US"

// ProviderID is the provider_id the agent runs as and the seeder
// writes suppressions under.
const ProviderID = "perf-provider"

// MediaBuyID returns the deterministic media-buy id for the i-th
// synthetic buy. Kept as a helper so seeder + loadgen agree.
func MediaBuyID(i int) string { return fmt.Sprintf("mb-perf-%05d", i) }

// PackageID returns the deterministic package id for the i-th
// synthetic package.
func PackageID(i int) string { return fmt.Sprintf("pkg-perf-%05d", i) }

// TopicID returns the taxonomy-scoped topic id for the i-th topic in
// the seeded pool.
func TopicID(i int) string { return fmt.Sprintf("topic-%05d", i) }

// ArtifactURL returns the deterministic URL used as an artifact_ref
// value AND as the topicstore ArtifactKey ref segment for the i-th
// artifact. Because the ref segment on the topic key matches the URL
// value the loadgen emits verbatim, no url_hash step is needed on
// either side to keep them in lockstep.
func ArtifactURL(i int) string {
	return fmt.Sprintf("https://perf.local/article-%05d", i)
}

// SignalOwnerID is the signal_owner_id the seeded signal keys and the
// per-package Cfg entries use. Matches signalstore's expectation of a
// stable free-form string.
const SignalOwnerID = "1"

// SignalID is the signal_id value the seeder writes into each signal
// key's CSV payload and the per-package Cfg.SignalID gates on.
const SignalID = "sig-perf-1"

// AcceptedTaxonomy is the taxonomy the context-agent accepts and the
// seeder writes topic/signal data under. IAB Content Taxonomy 3.0.
var AcceptedTaxonomy = topicstore.Taxonomy{Source: "iab", ID: 7}
