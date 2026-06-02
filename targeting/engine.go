// Package targeting provides data-driven targeting engines for TMP agents.
//
// Capabilities activate based on what data is present. Push property bitmaps
// and property targeting works. Push audience segments and audience targeting
// works. No data for a dimension means that dimension is a no-op.
//
// Two engines live here: ContextEngine evaluates context-match requests
// against a ContextStore (property bitmaps, URL filters, topic sets), and
// IdentityEngine evaluates identity-match requests against an audience
// service. They are deployed as separate processes — the context agent and
// identity agent — so that user-token data never traverses the context path.
package targeting

import (
	"context"
	"encoding/json"
	"log/slog"
	"time"

	"github.com/adcontextprotocol/adcp-go/targeting/audience"
	"github.com/adcontextprotocol/adcp-go/targeting/topicstore"
	"github.com/adcontextprotocol/adcp-go/tmproto"
)

// ContextEngine evaluates context-match requests. Push data (property lists,
// package configs) and it evaluates. No data for a dimension = no-op.
type ContextEngine struct {
	providerID string
	store      ContextStore

	properties PropertyList

	packages        map[string]PackageConfig
	dynamicPackages bool

	// acceptedTaxonomies enumerates the topic taxonomies this deployment
	// trusts. A publisher's ContextSignals.Topics are unioned into the
	// engine's topic set only when (TaxonomySource, TaxonomyID) is in this
	// list; Valkey topic lookups happen once per accepted taxonomy per
	// artifact. Empty disables topic targeting: every TopicTargets package
	// fails the topic check rather than passing through vacuously, so a
	// deployment cannot accidentally match on unscoped data.
	//
	// acceptedSet is the map form of acceptedTaxonomies for O(1) lookup;
	// the slice keeps the iteration order callers configured.
	acceptedTaxonomies []topicstore.Taxonomy
	acceptedSet        map[topicstore.Taxonomy]struct{}

	metrics Metrics
}

// ContextEngineConfig holds all configuration for creating a ContextEngine.
type ContextEngineConfig struct {
	ProviderID      string
	Store           ContextStore
	Properties      PropertyList
	Packages        []PackageConfig
	DynamicPackages bool // When true, load package configs from Store at eval time.

	// AcceptedTaxonomies enumerates the topic taxonomies this deployment
	// trusts on inbound ContextSignals and consults on Valkey artifact /
	// package topic lookups. Empty disables topic targeting: every
	// TopicTargets package fails the topic check (fail-closed) — a
	// deployment that wants topic targeting MUST declare at least one
	// accepted taxonomy.
	//
	// A publisher who sends ContextSignals.Topics with an empty
	// TaxonomySource gets a Taxonomy{Source: ""} key that
	// Taxonomy.Validate rejects and that no deployment can configure as
	// accepted. Those topics are silently dropped; deployments that need
	// to honor untagged topics must agree out-of-band with publishers on
	// a default Source value to send.
	AcceptedTaxonomies []topicstore.Taxonomy

	Metrics Metrics // nil = noop
}

// NewContextEngine creates a context-match engine. The caller's
// cfg.AcceptedTaxonomies slice is copied so post-construction mutation
// cannot reach into engine state.
func NewContextEngine(cfg ContextEngineConfig) *ContextEngine {
	pkgMap := make(map[string]PackageConfig, len(cfg.Packages))
	for _, p := range cfg.Packages {
		pkgMap[p.PackageID] = p
	}
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
	return &ContextEngine{
		providerID:         cfg.ProviderID,
		store:              cfg.Store,
		properties:         cfg.Properties,
		packages:           pkgMap,
		dynamicPackages:    cfg.DynamicPackages,
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

// publisherTopics returns the namespaced topic strings derived from
// req.ContextSignals when its declared taxonomy is in the engine's
// accepted set. Returns nil when ContextSignals is absent, has no topics,
// or declares a taxonomy the engine does not trust.
func (e *ContextEngine) publisherTopics(req *tmproto.ContextMatchRequest) []string {
	cs := req.ContextSignals
	if cs == nil || len(cs.Topics) == 0 {
		return nil
	}
	tax := topicstore.Taxonomy{Source: cs.TaxonomySource, ID: cs.TaxonomyID}
	if !e.acceptsTaxonomy(tax) {
		return nil
	}
	return topicstore.NamespaceTopics(tax, cs.Topics)
}

// publisherTopicsByTaxonomy returns ContextSignals.Topics grouped by the
// declared taxonomy when that taxonomy is in the accepted set. Returns
// nil when no usable publisher topics are present. The non-resolved
// engine path uses this to perform an in-memory join against each
// candidate package's stored topic set per accepted taxonomy.
//
// The TMP wire today carries exactly one (TaxonomySource, TaxonomyID)
// pair per ContextSignals, so the returned map has at most one entry.
// The map shape is forward-compat for a future spec extension that
// would let a publisher disclose topics under multiple taxonomies in a
// single request; downstream callers handle multi-entry input without
// further changes.
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

// IdentityEngine evaluates identity-match requests. It reads pre-resolved
// package identity configs and consults an audience.Service for segment
// membership. It does not touch the targeting ContextStore — identity data
// flows through the audience service alone.
type IdentityEngine struct {
	audience *audience.Service
	metrics  Metrics
}

// IdentityEngineConfig holds all configuration for creating an IdentityEngine.
type IdentityEngineConfig struct {
	Audience *audience.Service // nil = identity evaluation is segment-blind
	Metrics  Metrics           // nil = noop
}

// NewIdentityEngine creates an identity-match engine.
func NewIdentityEngine(cfg IdentityEngineConfig) *IdentityEngine {
	metrics := cfg.Metrics
	if metrics == nil {
		metrics = noopMetrics{}
	}
	return &IdentityEngine{
		audience: cfg.Audience,
		metrics:  metrics,
	}
}

// ContextResult holds the output of context evaluation.
type ContextResult struct {
	RequestID string
	Offers    []tmproto.Offer
	Signals   map[string]any
}

// IdentityResult holds the output of identity evaluation.
type IdentityResult struct {
	RequestID   string
	Eligibility []tmproto.PackageEligibility
}

// EvaluateContext evaluates available packages against content context.
//
// Pipeline:
//  1. Global property bitmap check
//  2. Suppression check (property + geo)
//  3. Per-package: property bitmap → URL filter → topic match → offers + segments
func (e *ContextEngine) EvaluateContext(ctx context.Context, req *tmproto.ContextMatchRequest) (*ContextResult, error) {
	evalStart := time.Now()
	rid := req.PropertyRID

	if !e.properties.ContainsGlobal(rid) {
		return &ContextResult{RequestID: req.RequestID}, nil
	}

	suppressionStart := time.Now()
	suppressed, err := e.isPropertySuppressed(ctx, rid)
	if err != nil {
		e.metrics.StoreError(ctx, StageSuppression, err)
	} else if suppressed {
		e.metrics.Latency(ctx, StageSuppression, time.Since(suppressionStart))
		return &ContextResult{RequestID: req.RequestID}, nil
	}
	if country, _ := req.Geo["country"].(string); country != "" {
		geoSuppressed, err := e.isGeoSuppressed(ctx, country)
		if err != nil {
			e.metrics.StoreError(ctx, StageSuppression, err)
		} else if geoSuppressed {
			e.metrics.Latency(ctx, StageSuppression, time.Since(suppressionStart))
			return &ContextResult{RequestID: req.RequestID}, nil
		}
	}
	e.metrics.Latency(ctx, StageSuppression, time.Since(suppressionStart))

	artifactRefs := extractArtifactRefURLs(req)
	pubTopicsByTax := e.publisherTopicsByTaxonomy(req)
	cs := req.ContextSignals
	haveTopicSource := len(artifactRefs) > 0 || (cs != nil && len(cs.Topics) > 0)

	var dynCtxConfigs map[string]*PackageContextConfig
	if e.dynamicPackages {
		var err error
		dynCtxConfigs, err = batchLoadPackageContextConfigs(ctx, e.store, req.PackageIDs)
		if err != nil {
			e.metrics.StoreError(ctx, "load_context_configs", err)
			dynCtxConfigs = make(map[string]*PackageContextConfig)
		}
	}

	var offers []tmproto.Offer
	var segments []string

	for _, pkgID := range req.PackageIDs {
		var urlBlocklist, urlAllowlist, topicTargets bool
		var emitSegments []string
		var pkgOffers []tmproto.Offer
		var propertyBitmap Bitmap

		if e.dynamicPackages {
			dCfg := dynCtxConfigs[pkgID]
			if dCfg == nil {
				continue
			}
			urlBlocklist = dCfg.URLBlocklist
			urlAllowlist = dCfg.URLAllowlist
			topicTargets = dCfg.TopicTargets
			emitSegments = dCfg.EmitSegments
			if len(dCfg.PropertyRIDs) > 0 {
				propertyBitmap = NewMapBitmap(dCfg.PropertyRIDs...)
			}
			pkgOffers = buildOffersFromDynamic(pkgID, dCfg)
		} else {
			cfg, known := e.packages[pkgID]
			if !known {
				continue
			}
			urlBlocklist = cfg.URLBlocklist
			urlAllowlist = cfg.URLAllowlist
			topicTargets = cfg.TopicTargets
			emitSegments = cfg.EmitSegments
			propertyBitmap = cfg.PropertyList
			pkgOffers = buildOffers(cfg)
		}

		if propertyBitmap != nil && !propertyBitmap.Contains(rid) {
			e.metrics.ContextEvaluated(ctx, StagePropertyBitmap, false)
			continue
		}
		if !e.properties.ContainsPackage(pkgID, rid) {
			e.metrics.ContextEvaluated(ctx, StagePropertyBitmap, false)
			continue
		}

		if urlBlocklist || urlAllowlist {
			blocked, err := e.checkURLFilter(ctx, artifactRefs, pkgID, PackageConfig{URLBlocklist: urlBlocklist, URLAllowlist: urlAllowlist})
			if err != nil {
				e.metrics.StoreError(ctx, StageURLFilter, err)
			} else if blocked {
				e.metrics.ContextEvaluated(ctx, StageURLFilter, false)
				continue
			}
		}

		if topicTargets {
			if len(e.acceptedTaxonomies) == 0 {
				e.metrics.ContextEvaluated(ctx, StageTopicNoTaxonomy, false)
				continue
			}
			if haveTopicSource {
				matched, err := e.checkTopicMatch(ctx, artifactRefs, pubTopicsByTax, pkgID)
				if err != nil {
					e.metrics.StoreError(ctx, StageTopicMatch, err)
				} else if !matched {
					e.metrics.ContextEvaluated(ctx, StageTopicMatch, false)
					continue
				}
			}
			// haveTopicSource == false: the publisher disclosed no
			// topics at all (no artifact_refs, no ContextSignals.Topics)
			// — the package passes the topic check vacuously, matching
			// the resolved path.
		}

		e.metrics.ContextEvaluated(ctx, "", true)
		offers = append(offers, pkgOffers...)
		segments = append(segments, emitSegments...)
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

// EvaluateContextResolved evaluates context using pre-built indexes.
// Minimal Store calls: only suppression checks and artifact→topic resolution.
// All targeting lookups (property, topic, URL) are in-memory.
//
// Topic resolution is taxonomy-scoped: the engine namespaces every topic id
// by its (TaxonomySource, TaxonomyID) before joining against TopicIndex, so
// the id "632" under IAB Content Taxonomy 3.0 never cross-matches with the
// same string under a different taxonomy. ContextSignals.Topics is unioned
// into the artifact-topic set when its declared taxonomy is in the
// engine's accepted set. When publisher-provided topics already activate
// every topic-targeted package in the request, the per-artifact Valkey
// SMEMBERS lookups are skipped entirely.
//
// Errors from per-(artifact, taxonomy) SMEMBERS lookups are recorded via
// StoreError + structured log and the loop continues to the next
// taxonomy; the affected taxonomy effectively under-matches for the
// request while the rest still evaluate. There is no overall "stop on
// first error" — a partial Valkey outage degrades match coverage rather
// than failing the whole request.
func (e *ContextEngine) EvaluateContextResolved(ctx context.Context, resolved *ResolvedPackages, req *tmproto.ContextMatchRequest) (*ContextResult, error) {
	evalStart := time.Now()
	rid := req.PropertyRID

	if !e.properties.ContainsGlobal(rid) {
		return &ContextResult{RequestID: req.RequestID}, nil
	}

	suppressed, err := e.isPropertySuppressed(ctx, rid)
	if err != nil {
		e.metrics.StoreError(ctx, StageSuppression, err)
	} else if suppressed {
		return &ContextResult{RequestID: req.RequestID}, nil
	}

	propertyCandidates := resolved.ContextCandidates(rid)

	artifactRefs := extractArtifactRefURLs(req)

	// Seed artifact topics from publisher-provided ContextSignals when the
	// declared taxonomy is accepted. These count as topics for every
	// artifact in the request (the publisher classifies the placement, not
	// a per-ref view) and are also the only topic input for the
	// no-artifact / ephemeral-content case.
	pubTopics := e.publisherTopics(req)
	artifactTopics := make([]string, 0, len(pubTopics))
	artifactTopics = append(artifactTopics, pubTopics...)

	// Short-circuit: skip the Valkey SMEMBERS round-trips entirely when
	// publisher topics already produce a candidate hit for every
	// TopicTargets package in the request. The fallback path runs when at
	// least one such package still lacks a candidate.
	topicCandidates := resolved.TopicCandidates(artifactTopics)
	needArtifactLookup := len(artifactRefs) > 0 && !publisherCoversTopicTargets(req.PackageIDs, resolved, topicCandidates)
	if needArtifactLookup {
		// Fail-closed per (artifact, taxonomy): a Valkey hiccup for one
		// taxonomy under-matches that taxonomy but does not poison the
		// rest of the eval. StageTopicMatch error metric + structured
		// log so on-call can correlate under-match incidents to
		// upstream errors.
		for _, artifact := range artifactRefs {
			for _, tax := range e.acceptedTaxonomies {
				topics, err := e.store.SetMembers(ctx, topicstore.ArtifactKey(tax, artifact))
				if err != nil {
					e.metrics.StoreError(ctx, StageTopicMatch, err)
					slog.Warn("topicstore: artifact-topic lookup failed; under-match for this taxonomy",
						"request_id", req.RequestID,
						"artifact", artifact,
						"taxonomy", tax.String(),
						"error", err)
					continue
				}
				for _, t := range topics {
					artifactTopics = append(artifactTopics, topicstore.NamespaceTopic(tax, t))
				}
			}
		}
		topicCandidates = resolved.TopicCandidates(artifactTopics)
	}

	var artifactHashes []string
	for _, artifact := range artifactRefs {
		artifactHashes = append(artifactHashes, HashURL(artifact))
	}

	var offers []tmproto.Offer
	var segments []string

	for _, pkgID := range req.PackageIDs {
		cfg := resolved.ContextConfigs[pkgID]
		if cfg == nil {
			continue
		}

		if propertyCandidates != nil {
			if _, ok := propertyCandidates[pkgID]; !ok {
				e.metrics.ContextEvaluated(ctx, StagePropertyBitmap, false)
				continue
			}
		}

		if cfg.TopicTargets {
			// Topic-source matrix the gate below enforces:
			//
			//   AcceptedTax | artifact_refs | cs.Topics | outcome
			//   ------------+---------------+-----------+----------
			//   empty       | (any)         | (any)     | fail-closed
			//   non-empty   | none          | none      | vacuous pass
			//   non-empty   | none          | rogue-tax | fail (only
			//                                            source was
			//                                            rejected)
			//   non-empty   | none          | accepted  | match required
			//   non-empty   | present       | (any)     | match required
			//                                            (rogue cs.Topics
			//                                            silently ignored;
			//                                            artifact lookups
			//                                            still run)
			//
			// The fail-on-rogue-only-source case is what prevents a
			// publisher from earning free activations by mis-declaring
			// their taxonomy on an otherwise empty request — they
			// supplied a topic source the engine couldn't use, so the
			// package's required-match contract stays in force.
			if len(e.acceptedTaxonomies) == 0 {
				e.metrics.ContextEvaluated(ctx, StageTopicNoTaxonomy, false)
				continue
			}
			cs := req.ContextSignals
			haveTopicSource := len(artifactRefs) > 0 || (cs != nil && len(cs.Topics) > 0)
			if haveTopicSource {
				if _, ok := topicCandidates[pkgID]; !ok {
					e.metrics.ContextEvaluated(ctx, StageTopicMatch, false)
					continue
				}
			}
		}

		blocked := false
		for _, hash := range artifactHashes {
			if resolved.IsURLBlocked(pkgID, hash) {
				blocked = true
				break
			}
		}
		if blocked {
			e.metrics.ContextEvaluated(ctx, StageURLFilter, false)
			continue
		}

		if _, hasAllowlist := resolved.URLAllowlists[pkgID]; hasAllowlist {
			allowed := false
			for _, hash := range artifactHashes {
				if resolved.IsURLAllowed(pkgID, hash) {
					allowed = true
					break
				}
			}
			if !allowed {
				e.metrics.ContextEvaluated(ctx, StageURLFilter, false)
				continue
			}
		}

		e.metrics.ContextEvaluated(ctx, "", true)
		offers = append(offers, buildOffersFromDynamic(pkgID, cfg)...)
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

// EvaluateIdentityResolved evaluates identity eligibility using segment gating only.
// Packages that have no IdentityConfig in resolved, or whose config has an
// empty TargetSegments rule (no clauses), are reported eligible: segment
// matching is opt-in, not a default deny. When the engine has no
// audience.Service configured, every package with a non-empty rule is
// rejected (no segment data is reachable to evaluate the clauses).
//
// Audience lookups are scoped to the segments any requested package actually
// references across AllOf/AnyOf/NoneOf, so users in unrelated audiences don't
// pay for fields the engine wouldn't read.
//
// Frequency capping is handled by the separate fcap.Service; the caller
// composes engine output with fcap lookups when fcap gating is required.
func (e *IdentityEngine) EvaluateIdentityResolved(ctx context.Context, resolved *ResolvedPackages, req *tmproto.IdentityMatchRequest) (*IdentityResult, error) {
	evalStart := time.Now()
	identities := resolveIdentities(req)

	userSegments := e.resolveUserSegments(ctx, identities, collectTargetSegments(resolved, req.PackageIDs))

	var eligibility []tmproto.PackageEligibility
	for _, pkgID := range req.PackageIDs {
		idCfg := resolved.IdentityConfigs[pkgID]
		eligible := true

		if idCfg != nil && !idCfg.TargetSegments.IsEmpty() {
			if e.audience == nil || !idCfg.TargetSegments.Matches(userSegments) {
				eligible = false
				e.metrics.IdentityEvaluated(ctx, StageAudience, false)
			}
		}

		eligibility = append(eligibility, tmproto.PackageEligibility{PackageID: pkgID, Eligible: eligible})
	}

	e.metrics.Latency(ctx, "identity_eval", time.Since(evalStart))

	return &IdentityResult{
		RequestID:   req.RequestID,
		Eligibility: eligibility,
	}, nil
}

// resolveUserSegments batch-queries audience membership for the identities
// against the supplied segment set, returning the set of segments the user
// belongs to. Returns nil when there is no audience service, no identities,
// or no target segments to evaluate.
func (e *IdentityEngine) resolveUserSegments(ctx context.Context, identities []UserIdentity, targetSegments []string) map[string]struct{} {
	if e.audience == nil || len(identities) == 0 || len(targetSegments) == 0 {
		return nil
	}
	lookups := make([]audience.MembershipLookup, 0, len(identities)*len(targetSegments))
	for _, uid := range identities {
		for _, seg := range targetSegments {
			lookups = append(lookups, audience.MembershipLookup{
				UserToken:  uid.UserToken,
				AudienceID: seg,
			})
		}
	}
	results, err := e.audience.IsMemberBatch(ctx, lookups)
	if err != nil {
		e.metrics.StoreError(ctx, "load_user_audiences", err)
		results = make([]bool, len(lookups))
	}
	matched := make(map[string]struct{})
	for i, l := range lookups {
		if results[i] {
			matched[l.AudienceID] = struct{}{}
		}
	}
	return matched
}

// collectTargetSegments returns the deduplicated union of every segment ID
// referenced (across AllOf/AnyOf/NoneOf) by any package's TargetSegments rule
// in pkgIDs. Returns nil when no requested package has segment targeting.
func collectTargetSegments(resolved *ResolvedPackages, pkgIDs []string) []string {
	seen := make(map[string]struct{})
	for _, pkgID := range pkgIDs {
		cfg := resolved.IdentityConfigs[pkgID]
		if cfg == nil {
			continue
		}
		for _, seg := range cfg.TargetSegments.Segments() {
			seen[seg] = struct{}{}
		}
	}
	if len(seen) == 0 {
		return nil
	}
	out := make([]string, 0, len(seen))
	for seg := range seen {
		out = append(out, seg)
	}
	return out
}

// checkURLFilter checks artifacts against URL blocklists and allowlists.
func (e *ContextEngine) checkURLFilter(ctx context.Context, artifacts []string, pkgID string, cfg PackageConfig) (bool, error) {
	for _, artifact := range artifacts {
		urlHash := HashURL(artifact)

		if cfg.URLBlocklist {
			blocked, err := e.store.SetIsMember(ctx, "url:blocklist:"+pkgID, urlHash)
			if err != nil {
				return false, err
			}
			if blocked {
				return true, nil
			}
		}

		if cfg.URLAllowlist {
			allowKey := "url:allowlist:" + pkgID
			exists, err := e.store.Exists(ctx, allowKey)
			if err != nil {
				return false, err
			}
			if exists {
				allowed, err := e.store.SetIsMember(ctx, allowKey, urlHash)
				if err != nil {
					return false, err
				}
				if !allowed {
					return true, nil
				}
			}
		}
	}
	return false, nil
}

// checkTopicMatch returns true when at least one accepted taxonomy yields
// a topic overlap between (publisher topics ∪ artifact topics) and the
// package's stored topic set. Callers must verify upstream that there is
// at least one topic source (artifact refs or accepted ContextSignals
// topics) before calling — when both sources are empty, the caller should
// treat the topic check as vacuously passing rather than invoking this
// helper, which would otherwise return false.
//
// pubTopicsByTax may be nil when the request had no usable publisher
// topics; in that case only the artifact branch runs.
func (e *ContextEngine) checkTopicMatch(ctx context.Context, artifacts []string, pubTopicsByTax map[topicstore.Taxonomy][]string, pkgID string) (bool, error) {
	if len(e.acceptedTaxonomies) == 0 {
		return false, nil
	}

	// Publisher-topic branch: one SMEMBERS per (taxonomy carrying
	// publisher topics) and an in-memory join. Short-circuits as soon as
	// any taxonomy produces overlap.
	for tax, pubTopics := range pubTopicsByTax {
		pkgTopics, err := e.store.SetMembers(ctx, topicstore.PackageKey(tax, pkgID))
		if err != nil {
			return false, err
		}
		if len(pkgTopics) == 0 {
			continue
		}
		pkgSet := make(map[string]struct{}, len(pkgTopics))
		for _, t := range pkgTopics {
			pkgSet[t] = struct{}{}
		}
		for _, t := range pubTopics {
			if _, ok := pkgSet[t]; ok {
				return true, nil
			}
		}
	}

	// Artifact-topic branch: one SINTER per (artifact, taxonomy).
	for _, artifact := range artifacts {
		for _, tax := range e.acceptedTaxonomies {
			intersection, err := e.store.SetIntersect(ctx,
				topicstore.PackageKey(tax, pkgID),
				topicstore.ArtifactKey(tax, artifact))
			if err != nil {
				return false, err
			}
			if len(intersection) > 0 {
				return true, nil
			}
		}
	}
	return false, nil
}

// publisherCoversTopicTargets reports whether topicCandidates (built from
// publisher-provided topics alone) already places every TopicTargets
// package in pkgIDs into the candidate set. When true, the engine can
// skip per-artifact Valkey SMEMBERS lookups: union semantics guarantee
// that adding artifact topics can only grow the candidate set, never
// remove a package that publisher topics already satisfied.
func publisherCoversTopicTargets(pkgIDs []string, resolved *ResolvedPackages, candidates map[string]struct{}) bool {
	for _, pkgID := range pkgIDs {
		cfg := resolved.ContextConfigs[pkgID]
		if cfg == nil || !cfg.TopicTargets {
			continue
		}
		if _, ok := candidates[pkgID]; !ok {
			return false
		}
	}
	return true
}

// buildOffers returns one or more Offers for an activated package.
func buildOffers(cfg PackageConfig) []tmproto.Offer {
	if len(cfg.Offers) > 0 {
		offers := make([]tmproto.Offer, len(cfg.Offers))
		for i, o := range cfg.Offers {
			offers[i] = tmproto.Offer{
				PackageID:        cfg.PackageID,
				Brand:            o.Brand,
				Price:            o.Price,
				Summary:          o.Summary,
				CreativeManifest: rawMessagePtr(o.CreativeManifest),
				Macros:           o.Macros,
			}
		}
		return offers
	}
	return []tmproto.Offer{{
		PackageID:        cfg.PackageID,
		Brand:            cfg.Brand,
		Price:            cfg.Price,
		Summary:          cfg.Summary,
		CreativeManifest: rawMessagePtr(cfg.CreativeManifest),
		Macros:           cfg.Macros,
	}}
}

// rawMessagePtr returns a pointer to the given json.RawMessage, or nil if empty.
func rawMessagePtr(m json.RawMessage) *json.RawMessage {
	if len(m) == 0 {
		return nil
	}
	return &m
}

// buildOffersFromDynamic builds offers from a dynamic PackageContextConfig.
func buildOffersFromDynamic(pkgID string, cfg *PackageContextConfig) []tmproto.Offer {
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
		PackageID: pkgID,
		Brand:     cfg.Brand,
		Price:     cfg.Price,
		Summary:   cfg.Summary,
		Macros:    cfg.Macros,
	}}
}

// extractArtifactRefURLs returns the URL-typed ArtifactRefs for URL/topic checks.
func extractArtifactRefURLs(req *tmproto.ContextMatchRequest) []string {
	var urls []string
	for _, ref := range req.ArtifactRefs {
		if ref.Type == tmproto.ArtifactRefTypeURL {
			urls = append(urls, ref.Value)
		}
	}
	return urls
}
