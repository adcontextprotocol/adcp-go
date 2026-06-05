package targeting

import (
	"context"
	"time"

	"github.com/adcontextprotocol/adcp-go/targeting/topicstore"
)

// ContextStorage is the data surface the context engine consults at
// request time. Implementations decide how data is fetched and cached;
// the engine treats every call as "ask the storage, trust the answer."
//
// Two implementations ship with this repo:
//
//   - targeting/contextstorage.InMemory — a snapshot-style impl backed by
//     plain maps; used by engine tests, the reference agent, and any
//     embedder that wants to build a ContextStorage by hand.
//   - targeting/contextagent's bundle — the production impl. Layers a
//     per-domain Service (mediabuystore, pkgconfigstore, urlliststore,
//     suppressionstore, topicstore) optionally wrapped in an LRU cache
//     decorator. The engine never sees those packages directly.
//
// All methods are called from request hot paths and MUST respect the
// passed context. Returning a non-nil error is a "this lookup failed"
// signal; the engine fails-closed on the affected dimension for the
// current request (under-match) and continues, mirroring how the
// pre-storage engine handled per-key Valkey errors.
type ContextStorage interface {
	// ActivePackages returns the package IDs the deployment offers for
	// the given (sellerAgentURL, propertyID, country, placementID)
	// tuple at `now` — i.e., every package whose media buy is active
	// (date / geo / property) and that itself is willing to serve the
	// placement.
	//
	// The engine consults this on every request, not only when
	// req.PackageIDs is omitted. When req.PackageIDs IS present the
	// engine intersects this set with the inbound list, per the TMP
	// spec's "intersection of registered active set and package_ids"
	// rule (see IdentityMatchRequest.PackageIDs docstring in
	// tmproto/types_gen.go — the principle applies to context-match
	// too). Skipping this step would let a stale PackageContextConfig
	// for an expired media buy still produce offers if a publisher
	// names it explicitly.
	ActivePackages(ctx context.Context, sellerAgentURL, propertyID, country, placementID string, now time.Time) ([]string, error)

	// ContextConfig returns the package's context-side configuration.
	// `ok == false` (with err == nil) means no config is stored for that
	// package — the engine skips it.
	//
	// Storage implementations MAY also implement an optional
	// `ContextConfigs(ctx, []string) ([]*PackageContextConfig, error)`
	// method that returns every requested package's config in a
	// single round-trip; the engine feature-detects that method to
	// collapse per-package fetches into one MGet when planning
	// signal-targeting lookups. Implementations that do not provide
	// it fall back to repeated ContextConfig calls.
	ContextConfig(ctx context.Context, packageID string) (cfg *PackageContextConfig, ok bool, err error)

	// ArtifactTopics returns the raw topic ids stored for `ref` under
	// `tax`. The engine namespaces them via topicstore.NamespaceTopic
	// before joining; storage returns the un-namespaced ids it has on
	// hand. nil (with err == nil) means no topics are stored.
	ArtifactTopics(ctx context.Context, tax topicstore.Taxonomy, ref string) ([]string, error)

	// PackageTopics returns the raw topic ids `packageID` targets under
	// `tax`. Same shape as ArtifactTopics.
	PackageTopics(ctx context.Context, tax topicstore.Taxonomy, packageID string) ([]string, error)

	// URLBlocked reports whether `urlHash` is in `packageID`'s blocklist.
	// false (with err == nil) covers both "no blocklist configured" and
	// "blocklist exists but hash absent"; the caller distinguishes via
	// PackageContextConfig.URLBlocklist.
	URLBlocked(ctx context.Context, packageID, urlHash string) (bool, error)

	// URLAllowed reports whether `urlHash` is in `packageID`'s allowlist.
	// Returns false when the hash is absent OR when no allowlist is
	// configured; the caller distinguishes via
	// PackageContextConfig.URLAllowlist.
	URLAllowed(ctx context.Context, packageID, urlHash string) (bool, error)

	// IsPropertySuppressed reports whether `propertyRID` is currently
	// suppressed for `providerID`. Suppressions are deployment-scoped
	// kill switches; a true return short-circuits the entire request.
	IsPropertySuppressed(ctx context.Context, providerID, propertyRID string) (bool, error)

	// IsGeoSuppressed reports whether `country` is currently suppressed
	// for `providerID`. Same kill-switch semantics as
	// IsPropertySuppressed.
	IsGeoSuppressed(ctx context.Context, providerID, country string) (bool, error)

	// SignalMGet fetches the raw CSV-encoded signal-id strings stored
	// at each requested signal:* key. The returned slice is aligned 1:1
	// with the input; empty string at index i means "missing key".
	// Used by the engine to evaluate context-attribute signal targeting
	// in a single round-trip across every candidate package per request.
	SignalMGet(ctx context.Context, keys ...string) ([]string, error)
}
