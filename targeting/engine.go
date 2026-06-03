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
	"time"

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
	providerID     string
	sellerAgentURL string
	properties     PropertyList
	storage        ContextStorage
	now            func() time.Time

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

	// SellerAgentURL is the canonicalized seller_agent_url this
	// deployment represents. Required: the engine passes it to
	// Storage.ActivePackages on every request so the active-set
	// resolution is scoped to this deployment's inventory.
	SellerAgentURL string

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
		sellerAgentURL:     cfg.SellerAgentURL,
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
//     intersection of the active set and req.PackageIDs (per the TMP
//     spec's "intersection of registered active set and package_ids"
//     rule); when omitted, the full active set.
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

	activePkgIDs, err := e.storage.ActivePackages(ctx, e.sellerAgentURL, req.PropertyID, country, req.PlacementID, e.now())
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

	artifactRefs := extractArtifactRefURLs(req)
	pubTopicsByTax := e.publisherTopicsByTaxonomy(req)
	// haveTopicSource is the "did the publisher try to disclose any
	// topics?" gate. We look at req.ContextSignals.Topics directly
	// rather than pubTopicsByTax so a publisher whose declared
	// taxonomy is unaccepted still triggers the gate — otherwise the
	// engine would pass vacuously instead of fail-closed on a
	// mis-declared taxonomy.
	cs := req.ContextSignals
	haveTopicSource := len(artifactRefs) > 0 || (cs != nil && len(cs.Topics) > 0)

	var offers []tmproto.Offer
	var segments []string

	for _, pkgID := range candidatePkgIDs {
		cfg, ok, err := e.storage.ContextConfig(ctx, pkgID)
		if err != nil {
			e.metrics.StoreError(ctx, "context_config", err)
			continue
		}
		if !ok || cfg == nil {
			continue
		}

		if !e.matchesPropertyBitmap(cfg, rid, pkgID) {
			e.metrics.ContextEvaluated(ctx, StagePropertyBitmap, false)
			continue
		}

		if cfg.URLBlocklist || cfg.URLAllowlist {
			blocked, err := e.checkURLFilter(ctx, artifactRefs, pkgID, cfg)
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
				matched, err := e.checkTopicMatch(ctx, pkgID, artifactRefs, pubTopicsByTax)
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

// checkURLFilter applies the package's blocklist and allowlist rules to
// every artifact in the request. Returns true when at least one
// artifact is blocked or, with an allowlist configured, when no
// artifact is allowed.
func (e *ContextEngine) checkURLFilter(ctx context.Context, artifacts []string, pkgID string, cfg *PackageContextConfig) (bool, error) {
	if cfg.URLBlocklist {
		for _, artifact := range artifacts {
			hash := HashURL(artifact)
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
		if len(artifacts) == 0 {
			return true, nil
		}
		anyAllowed := false
		for _, artifact := range artifacts {
			hash := HashURL(artifact)
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

// extractArtifactRefURLs returns the URL-typed ArtifactRefs for URL and
// topic checks.
func extractArtifactRefURLs(req *tmproto.ContextMatchRequest) []string {
	var urls []string
	for _, ref := range req.ArtifactRefs {
		if ref.Type == tmproto.ArtifactRefTypeURL {
			urls = append(urls, ref.Value)
		}
	}
	return urls
}
