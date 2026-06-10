// Package targeting provides data-driven targeting engines for TMP agents.
//
// Two engines live here: ContextEngine evaluates context-match requests
// against a ContextStorage (property bitmaps, package configs, topic
// sets, URL filters, suppression markers), and IdentityEngine (in
// identity_engine.go) evaluates identity-match requests against an
// audience service. They are deployed as separate processes — the
// context agent and identity agent — so that user-token data never
// traverses the context path.
package targeting

import (
	"context"
	"encoding/json"
	"log/slog"
	"time"

	"github.com/adcontextprotocol/adcp-go/adcp/urlcanon"
	"github.com/adcontextprotocol/adcp-go/targeting/signalstore"
	"github.com/adcontextprotocol/adcp-go/targeting/topicstore"
	"github.com/adcontextprotocol/adcp-go/tmproto"
)

// GeoCountryKey is the ContextMatchRequest.Geo map key carrying the
// ISO 3166-1 alpha-2 country code. Hoisted to a constant so a future
// move to a typed Geo struct is a single grep target.
const GeoCountryKey = "country"

// ContextEngine evaluates context-match requests. The storage backend
// supplies every piece of data the engine consults at request time;
// caching, persistence, and refresh policy live in the storage impl, not
// here.
type ContextEngine struct {
	providerID string
	properties PropertyList
	storage    ContextStorage
	now        func() time.Time

	// acceptedTaxonomies enumerates the topic taxonomies this deployment
	// trusts. A publisher's ContextSignals.Topics are unioned into the
	// engine's topic set only when (TaxonomySource, TaxonomyID) is in
	// this list. Empty disables topic targeting: every TopicTargets
	// package fails the topic check rather than passing through
	// vacuously, so a deployment cannot accidentally match on unscoped
	// data.
	//
	// acceptedSet is the map form of acceptedTaxonomies for O(1) lookup;
	// the slice keeps the iteration order callers configured.
	acceptedTaxonomies []topicstore.Taxonomy
	acceptedSet        map[topicstore.Taxonomy]struct{}

	metrics Metrics
}

// ContextEngineConfig holds all configuration for creating a ContextEngine.
type ContextEngineConfig struct {
	// ProviderID is the publisher-assigned identifier for this provider
	// registration. Used in logs, metrics, and suppression keys (see
	// tmproto.ProviderRegistration.ProviderID). Stable for the engine's
	// lifetime; rotate by restarting with a new value.
	ProviderID string

	// Properties is the registry-derived global (and optionally
	// per-package) property bitmap. Requests whose property_rid is not
	// in Properties.Global short-circuit before any storage lookup.
	Properties PropertyList

	// Storage is the read surface for media-buy resolution, package
	// configs, topic data, URL lists, and suppression. Required.
	Storage ContextStorage

	// AcceptedTaxonomies enumerates the topic taxonomies this deployment
	// trusts on inbound ContextSignals and consults on storage artifact
	// / package topic lookups. Empty disables topic targeting: every
	// TopicTargets package fails the topic check (fail-closed) — a
	// deployment that wants topic targeting MUST declare at least one
	// accepted taxonomy.
	//
	// A publisher who sends ContextSignals.Topics with an empty
	// TaxonomySource gets a Taxonomy{Source: ""} key that
	// Taxonomy.Validate rejects and that no deployment can configure as
	// accepted. Those topics are silently dropped.
	AcceptedTaxonomies []topicstore.Taxonomy

	// Now is the clock the engine uses for media-buy date filtering.
	// Defaults to time.Now; overridable for tests.
	Now func() time.Time

	Metrics Metrics // nil = noop
}

// NewContextEngine creates a context-match engine. The caller's
// cfg.AcceptedTaxonomies slice is copied so post-construction mutation
// cannot reach into engine state.
func NewContextEngine(cfg ContextEngineConfig) *ContextEngine {
	metrics := cfg.Metrics
	if metrics == nil {
		metrics = noopMetrics{}
	}
	acceptedSlice := make([]topicstore.Taxonomy, len(cfg.AcceptedTaxonomies))
	copy(acceptedSlice, cfg.AcceptedTaxonomies)
	acceptedSet := make(map[topicstore.Taxonomy]struct{}, len(acceptedSlice))
	for _, t := range acceptedSlice {
		acceptedSet[t] = struct{}{}
	}
	now := cfg.Now
	if now == nil {
		now = time.Now
	}
	return &ContextEngine{
		providerID:         cfg.ProviderID,
		properties:         cfg.Properties,
		storage:            cfg.Storage,
		now:                now,
		acceptedTaxonomies: acceptedSlice,
		acceptedSet:        acceptedSet,
		metrics:            metrics,
	}
}

// acceptsTaxonomy reports whether tax is configured as accepted.
func (e *ContextEngine) acceptsTaxonomy(tax topicstore.Taxonomy) bool {
	_, ok := e.acceptedSet[tax]
	return ok
}

// publisherTopicsByTaxonomy returns ContextSignals.Topics grouped by the
// declared taxonomy when that taxonomy is in the accepted set. Returns
// nil when no usable publisher topics are present.
//
// The TMP wire today carries exactly one (TaxonomySource, TaxonomyID)
// pair per ContextSignals, so the returned map has at most one entry.
// The map shape is forward-compat for a future spec extension that lets
// a publisher disclose topics under multiple taxonomies in a single
// request; downstream callers handle multi-entry input without further
// changes.
func (e *ContextEngine) publisherTopicsByTaxonomy(req *tmproto.ContextMatchRequest) map[topicstore.Taxonomy][]string {
	cs := req.ContextSignals
	if cs == nil || len(cs.Topics) == 0 {
		return nil
	}
	tax := topicstore.Taxonomy{Source: cs.TaxonomySource, ID: cs.TaxonomyID}
	if !e.acceptsTaxonomy(tax) {
		return nil
	}
	return map[topicstore.Taxonomy][]string{tax: cs.Topics}
}

// ContextResult holds the output of context evaluation.
type ContextResult struct {
	RequestID string
	Offers    []tmproto.Offer
	Signals   map[string]any
}

// Evaluate runs the context-match pipeline:
//
//  1. Global property bitmap pre-filter.
//  2. Property / geo suppression checks via the storage.
//  3. Resolve the active package set from Storage.ActivePackages for
//     this seller / property / country / placement at e.now(). This
//     happens on EVERY request — not only when req.PackageIDs is
//     omitted — because the request's PackageIDs list is a publisher-
//     supplied restriction, not a substitute for the provider's
//     authoritative active inventory. A stale PackageContextConfig for
//     an expired media buy must not produce offers just because a
//     publisher names it.
//  4. Resolve the candidate set: when req.PackageIDs is present, the
//     intersection of the active set and req.PackageIDs (by analogy
//     to IdentityMatchRequest.PackageIDs, which the AdCP spec documents
//     as "intersection of registered active set and package_ids" —
//     ContextMatchRequest.PackageIDs is not currently spelled out in
//     the spec, but the same provider-controls-the-active-set
//     principle applies); when omitted, the full active set.
//  5. Per package: load context config, check per-package property
//     bitmap, check URL block/allow lists, check topic match (publisher
//     topics short-circuited first, then per-artifact / per-taxonomy
//     storage lookups).
//
// Storage errors on any per-package dimension are recorded via
// StoreError and fail-closed for that package — the package's
// dimension check returns false and the package is skipped. The
// request continues evaluating the rest of its packages. A suppression
// error at the top of the pipeline is logged and the request
// continues — the alternative would be denying every request during a
// partial storage outage, which is the wrong fail mode for a kill
// switch you can't otherwise un-stick.
func (e *ContextEngine) Evaluate(ctx context.Context, req *tmproto.ContextMatchRequest) (*ContextResult, error) {
	if err := ctx.Err(); err != nil {
		// The handler's per-request deadline already fired before the
		// engine ran anything. Surface the timeout so the handler can
		// distinguish it from "evaluated, no offers" (200 with empty
		// offers, which is also a legal TMP response).
		return nil, err
	}
	evalStart := time.Now()
	rid := req.PropertyRID

	if !e.properties.ContainsGlobal(rid) {
		return &ContextResult{RequestID: req.RequestID}, nil
	}

	suppressionStart := time.Now()
	suppressed, err := e.storage.IsPropertySuppressed(ctx, e.providerID, rid)
	if err != nil {
		e.metrics.StoreError(ctx, StageSuppression, err)
	} else if suppressed {
		e.metrics.Latency(ctx, StageSuppression, time.Since(suppressionStart))
		return &ContextResult{RequestID: req.RequestID}, nil
	}
	country, _ := req.Geo[GeoCountryKey].(string)
	if country != "" {
		geoSuppressed, err := e.storage.IsGeoSuppressed(ctx, e.providerID, country)
		if err != nil {
			e.metrics.StoreError(ctx, StageSuppression, err)
		} else if geoSuppressed {
			e.metrics.Latency(ctx, StageSuppression, time.Since(suppressionStart))
			return &ContextResult{RequestID: req.RequestID}, nil
		}
	}
	e.metrics.Latency(ctx, StageSuppression, time.Since(suppressionStart))

	// seller_agent_url selects the seller's active package set. The AdCP
	// spec compares URL identifiers under URL-identifier canonicalization,
	// not byte-equality, so a request carrying
	// "https://Seller.Example.com:443/agent" must resolve to the same set
	// as one registered under "https://seller.example.com/agent".
	// Canonicalize once here, the single read-path chokepoint.
	//
	// An empty seller_agent_url carries no URL to canonicalize and is
	// passed through unchanged. A non-empty but malformed value cannot name
	// any registered seller, so it fails safe to an empty active set (empty
	// offers) rather than 5xx or a raw-string lookup that would silently
	// miss every registration.
	canonicalSeller := req.SellerAgentURL
	if canonicalSeller != "" {
		canonicalSeller, err = urlcanon.Canonicalize(canonicalSeller)
		if err != nil {
			slog.Default().Warn("seller_agent_url canonicalization failed; treating as no active packages",
				"seller_agent_url", req.SellerAgentURL, "error", err)
			return &ContextResult{RequestID: req.RequestID}, nil
		}
	}

	activePkgIDs, err := e.storage.ActivePackages(ctx, canonicalSeller, req.PropertyID, country, req.PlacementID, e.now())
	if err != nil {
		// A context timeout here means the request budget is gone;
		// surface it so the handler returns 504. Any other storage
		// error fails-closed for the request (no active-set → can't
		// honor the intersection contract; serving on a
		// possibly-expired buy is worse than empty offers).
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, ctxErr
		}
		e.metrics.StoreError(ctx, "active_packages", err)
		return &ContextResult{RequestID: req.RequestID}, nil
	}
	candidatePkgIDs := candidateSet(activePkgIDs, req.PackageIDs)
	if len(candidatePkgIDs) == 0 {
		return &ContextResult{RequestID: req.RequestID}, nil
	}

	artifactKeys := extractArtifactKeys(req)
	pubTopicsByTax := e.publisherTopicsByTaxonomy(req)
	// haveTopicSource is the "did the publisher try to disclose any
	// topics?" gate. We look at req.ContextSignals.Topics directly
	// rather than pubTopicsByTax so a publisher whose declared
	// taxonomy is unaccepted still triggers the gate — otherwise the
	// engine would pass vacuously instead of fail-closed on a
	// mis-declared taxonomy.
	cs := req.ContextSignals
	haveTopicSource := len(artifactKeys.TopicRefs) > 0 || (cs != nil && len(cs.Topics) > 0)

	// Preload candidate configs so signal targeting can plan a single
	// MGet across every candidate package's profile before the per-
	// package loop starts. The pkgconfig batcher (when the storage
	// implements it) collapses N round-trips to one; engines wired
	// against the in-memory test storage fall back to per-package
	// fetches.
	configs := e.preloadContextConfigs(ctx, candidatePkgIDs)

	// Signal targeting is skipped end-to-end (no extract, no plan,
	// no MGet, no per-package check) when zero candidates carry a
	// non-empty profile. Most requests on most deployments fall in
	// this branch, so the hot path pays nothing for an unused
	// feature.
	var (
		signalData    signalstore.LookupData
		signalFetched map[string][]string
		signalErr     error
		signalLogger  *slog.Logger
	)
	if hasAnySignalProfile(configs) {
		signalData = e.extractSignalLookupData(req, country)
		signalFetched, signalErr = e.fetchSignalsForCandidates(ctx, configs, signalData)
		signalLogger = slog.Default()
	}

	var offers []tmproto.Offer
	var segments []string

	for i, pkgID := range candidatePkgIDs {
		// Bail at the top of every iteration so a deadline that fires
		// mid-eval surfaces as 504 instead of a 200/empty-offers
		// response that hides timeouts from buyer telemetry. Storage
		// calls inside the loop body do their own ctx-error mapping
		// in the handler; this check covers the gap between iterations.
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		cfg := configs[i]
		if cfg == nil {
			continue
		}

		if !e.matchesPropertyBitmap(cfg, rid, pkgID) {
			e.metrics.ContextEvaluated(ctx, StagePropertyBitmap, false)
			continue
		}

		if !e.signalsPass(ctx, cfg, pkgID, signalData, signalFetched, signalErr, signalLogger) {
			continue
		}

		if cfg.URLBlocklist || cfg.URLAllowlist {
			blocked, err := e.checkURLFilter(ctx, artifactKeys.URLHashes, pkgID, cfg)
			if err != nil {
				// Fail-closed: URL block/allow lists are brand-safety
				// filters. A transient Valkey error must skip the
				// package, not let it activate without the filter.
				e.metrics.StoreError(ctx, StageURLFilter, err)
				continue
			}
			if blocked {
				e.metrics.ContextEvaluated(ctx, StageURLFilter, false)
				continue
			}
		}

		if cfg.TopicTargets {
			if len(e.acceptedTaxonomies) == 0 {
				e.metrics.ContextEvaluated(ctx, StageTopicNoTaxonomy, false)
				continue
			}
			if haveTopicSource {
				matched, err := e.checkTopicMatch(ctx, pkgID, artifactKeys.TopicRefs, pubTopicsByTax)
				if err != nil {
					// Fail-closed: a Valkey error here means we cannot
					// prove the package's targeted topics match the
					// artifact, so it must not activate.
					e.metrics.StoreError(ctx, StageTopicMatch, err)
					continue
				}
				if !matched {
					e.metrics.ContextEvaluated(ctx, StageTopicMatch, false)
					continue
				}
			}
			// No topic source on the request — the publisher disclosed
			// no topics at all. The package passes the topic check
			// vacuously, matching the pre-storage engine's shape.
		}

		e.metrics.ContextEvaluated(ctx, "", true)
		offers = append(offers, buildOffersFromContextConfig(pkgID, cfg)...)
		segments = append(segments, cfg.EmitSegments...)
	}

	e.metrics.Latency(ctx, "context_eval", time.Since(evalStart))

	result := &ContextResult{
		RequestID: req.RequestID,
		Offers:    offers,
	}
	if len(segments) > 0 {
		result.Signals = map[string]any{"segments": segments}
	}
	return result, nil
}

// matchesPropertyBitmap enforces the per-package property bitmap (when
// PackageContextConfig.PropertyRIDs is non-empty) and the engine's
// PropertyList.ByPackage override (when configured). The global bitmap
// is checked earlier at the top of Evaluate.
func (e *ContextEngine) matchesPropertyBitmap(cfg *PackageContextConfig, rid, pkgID string) bool {
	if len(cfg.PropertyRIDs) > 0 {
		var pkgBitmap Bitmap = NewMapBitmap(cfg.PropertyRIDs...)
		if !pkgBitmap.Contains(rid) {
			return false
		}
	}
	return e.properties.ContainsPackage(pkgID, rid)
}

// checkURLFilter applies the package's blocklist and allowlist rules
// to every artifact in the request. Returns true when at least one
// artifact is blocked or, with an allowlist configured, when no
// artifact is allowed. The hash slice mirrors `artifacts` 1:1: for
// `url` refs the engine hashed the URL via tmproto.HashURL; for
// `url_hash` refs the wire value is used directly, since the publisher
// already produced the spec-canonical Blake3+base64 form.
func (e *ContextEngine) checkURLFilter(ctx context.Context, artifactHashes []string, pkgID string, cfg *PackageContextConfig) (bool, error) {
	if cfg.URLBlocklist {
		for _, hash := range artifactHashes {
			blocked, err := e.storage.URLBlocked(ctx, pkgID, hash)
			if err != nil {
				return false, err
			}
			if blocked {
				return true, nil
			}
		}
	}
	if cfg.URLAllowlist {
		if len(artifactHashes) == 0 {
			return true, nil
		}
		anyAllowed := false
		for _, hash := range artifactHashes {
			allowed, err := e.storage.URLAllowed(ctx, pkgID, hash)
			if err != nil {
				return false, err
			}
			if allowed {
				anyAllowed = true
				break
			}
		}
		if !anyAllowed {
			return true, nil
		}
	}
	return false, nil
}

// checkTopicMatch returns true when at least one accepted taxonomy
// yields a topic overlap between (publisher topics ∪ artifact topics)
// and the package's stored topic set. Callers verify upstream that
// there is at least one topic source (artifact refs or accepted
// ContextSignals topics) before calling.
//
// Publisher topics are checked first across every taxonomy carrying
// them; short-circuits on the first match. Falls back to per-artifact /
// per-accepted-taxonomy SetMembers via the storage.
func (e *ContextEngine) checkTopicMatch(ctx context.Context, pkgID string, artifacts []string, pubTopicsByTax map[topicstore.Taxonomy][]string) (bool, error) {
	for tax, pubTopics := range pubTopicsByTax {
		pkgTopics, err := e.storage.PackageTopics(ctx, tax, pkgID)
		if err != nil {
			return false, err
		}
		if len(pkgTopics) == 0 {
			continue
		}
		if hasOverlap(pkgTopics, pubTopics) {
			return true, nil
		}
	}

	for _, artifact := range artifacts {
		for _, tax := range e.acceptedTaxonomies {
			artTopics, err := e.storage.ArtifactTopics(ctx, tax, artifact)
			if err != nil {
				return false, err
			}
			if len(artTopics) == 0 {
				continue
			}
			pkgTopics, err := e.storage.PackageTopics(ctx, tax, pkgID)
			if err != nil {
				return false, err
			}
			if len(pkgTopics) == 0 {
				continue
			}
			if hasOverlap(pkgTopics, artTopics) {
				return true, nil
			}
		}
	}
	return false, nil
}

// candidateSet returns the engine's candidate package id list for a
// request. When the publisher's PackageIDs is non-empty, the result is
// the intersection of activeIDs and requested, preserving the order
// of `requested` and silently dropping ids that the provider does not
// have registered as active (per the TMP spec on
// IdentityMatchRequest.PackageIDs — same principle applies to
// context-match). When `requested` is empty the result is `activeIDs`
// unchanged.
func candidateSet(activeIDs, requested []string) []string {
	if len(requested) == 0 {
		return activeIDs
	}
	active := make(map[string]struct{}, len(activeIDs))
	for _, id := range activeIDs {
		active[id] = struct{}{}
	}
	out := make([]string, 0, len(requested))
	for _, id := range requested {
		if _, ok := active[id]; ok {
			out = append(out, id)
		}
	}
	return out
}

// hasOverlap reports whether the two slices share any element. Both
// slices are treated as small (typical: ≤ 20 entries per package, ≤ 50
// publisher-supplied topics), so the O(n*m) scan with early-exit is
// faster than building an auxiliary set.
func hasOverlap(a, b []string) bool {
	for _, x := range a {
		for _, y := range b {
			if x == y {
				return true
			}
		}
	}
	return false
}

// buildOffersFromContextConfig builds offers from a PackageContextConfig.
// Returns one Offer per OfferConfigJSON entry when present, otherwise a
// single Offer constructed from the package-level fields.
func buildOffersFromContextConfig(pkgID string, cfg *PackageContextConfig) []tmproto.Offer {
	if len(cfg.Offers) > 0 {
		offers := make([]tmproto.Offer, len(cfg.Offers))
		for i, o := range cfg.Offers {
			offers[i] = tmproto.Offer{
				PackageID: pkgID,
				Brand:     o.Brand,
				Price:     o.Price,
				Summary:   o.Summary,
				Macros:    o.Macros,
			}
		}
		return offers
	}
	return []tmproto.Offer{{
		PackageID:        pkgID,
		Brand:            cfg.Brand,
		Price:            cfg.Price,
		Summary:          cfg.Summary,
		CreativeManifest: rawMessagePtr(cfg.CreativeManifest),
		Macros:           cfg.Macros,
	}}
}

// rawMessagePtr returns a pointer to the given json.RawMessage, or nil
// if empty.
func rawMessagePtr(m json.RawMessage) *json.RawMessage {
	if len(m) == 0 {
		return nil
	}
	return &m
}

// artifactKeys is the per-request projection of the inbound
// `artifact_refs` list onto the two engine downstream uses:
//
//   - URLHashes: spec-canonical Blake3+base64 hashes for URL block /
//     allow list SISMEMBER lookups. `url` refs are hashed via
//     tmproto.HashURL; `url_hash` refs pass through unchanged (the
//     publisher's wire value IS the storage key).
//   - TopicRefs: the verbatim ref values for ArtifactTopics(taxonomy,
//     ref) lookups against topicstore. Topic-store keys are whatever
//     the writer pipeline used at write time; this engine path uses
//     whichever form the publisher sent. Producer + consumer must
//     have agreed on the form.
//
// The two slices are independent: URLHashes is the URL-filter input;
// TopicRefs is the topic-match input. Ordering within each slice
// mirrors `req.ArtifactRefs` so iteration is deterministic.
type artifactKeys struct {
	URLHashes []string
	TopicRefs []string
}

// extractArtifactKeys projects the request's `artifact_refs` onto the
// two engine downstream uses. See artifactKeys.
func extractArtifactKeys(req *tmproto.ContextMatchRequest) artifactKeys {
	var ak artifactKeys
	for _, ref := range req.ArtifactRefs {
		switch ref.Type {
		case tmproto.ArtifactRefTypeURL:
			ak.URLHashes = append(ak.URLHashes, tmproto.HashURL(ref.Value))
			ak.TopicRefs = append(ak.TopicRefs, ref.Value)
		case tmproto.ArtifactRefTypeURLHash:
			// Wire value is already the spec-canonical Blake3+base64
			// hash, ready for SISMEMBER. Topic-store keys are
			// publisher-controlled, so the same value also flows
			// through for ArtifactTopics — the writer pipeline that
			// populated topic data for this artifact must have keyed
			// on the same form.
			ak.URLHashes = append(ak.URLHashes, ref.Value)
			ak.TopicRefs = append(ak.TopicRefs, ref.Value)
		}
	}
	return ak
}
