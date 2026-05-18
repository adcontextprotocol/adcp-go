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
	"time"

	"github.com/adcontextprotocol/adcp-go/targeting/audience"
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

	metrics Metrics
}

// ContextEngineConfig holds all configuration for creating a ContextEngine.
type ContextEngineConfig struct {
	ProviderID      string
	Store           ContextStore
	Properties      PropertyList
	Packages        []PackageConfig
	DynamicPackages bool    // When true, load package configs from Store at eval time.
	Metrics         Metrics // nil = noop
}

// NewContextEngine creates a context-match engine.
func NewContextEngine(cfg ContextEngineConfig) *ContextEngine {
	pkgMap := make(map[string]PackageConfig, len(cfg.Packages))
	for _, p := range cfg.Packages {
		pkgMap[p.PackageID] = p
	}
	metrics := cfg.Metrics
	if metrics == nil {
		metrics = noopMetrics{}
	}
	return &ContextEngine{
		providerID:      cfg.ProviderID,
		store:           cfg.Store,
		properties:      cfg.Properties,
		packages:        pkgMap,
		dynamicPackages: cfg.DynamicPackages,
		metrics:         metrics,
	}
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
			matched, err := e.checkTopicMatch(ctx, artifactRefs, pkgID)
			if err != nil {
				e.metrics.StoreError(ctx, StageTopicMatch, err)
			} else if !matched {
				e.metrics.ContextEvaluated(ctx, StageTopicMatch, false)
				continue
			}
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

	var artifactTopics []string
	for _, artifact := range artifactRefs {
		topics, err := e.store.SetMembers(ctx, "topics:artifact:"+artifact)
		if err != nil {
			e.metrics.StoreError(ctx, StageTopicMatch, err)
		} else {
			artifactTopics = append(artifactTopics, topics...)
		}
	}

	var artifactHashes []string
	for _, artifact := range artifactRefs {
		artifactHashes = append(artifactHashes, HashURL(artifact))
	}

	topicCandidates := resolved.TopicCandidates(artifactTopics)

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

		if cfg.TopicTargets && len(artifactRefs) > 0 {
			if len(topicCandidates) == 0 {
				e.metrics.ContextEvaluated(ctx, StageTopicMatch, false)
				continue
			}
			if _, ok := topicCandidates[pkgID]; !ok {
				e.metrics.ContextEvaluated(ctx, StageTopicMatch, false)
				continue
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

// checkTopicMatch checks if artifacts have topic overlap with a package.
func (e *ContextEngine) checkTopicMatch(ctx context.Context, artifacts []string, pkgID string) (bool, error) {
	if len(artifacts) == 0 {
		return true, nil
	}
	for _, artifact := range artifacts {
		intersection, err := e.store.SetIntersect(ctx, "topics:package:"+pkgID, "topics:artifact:"+artifact)
		if err != nil {
			return false, err
		}
		if len(intersection) > 0 {
			return true, nil
		}
	}
	return false, nil
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
