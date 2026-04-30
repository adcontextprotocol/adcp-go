// Package targeting provides a data-driven targeting engine for TMP agents.
//
// Capabilities activate based on what data is present. Push property bitmaps
// and property targeting works. Push audience segments and audience targeting
// works. No data for a dimension means that dimension is a no-op.
//
// The engine evaluates both context match and identity match requests,
// replacing the need for separate agent implementations.
package targeting

import (
	"context"
	"encoding/json"
	"time"

	"github.com/adcontextprotocol/adcp-go/tmproto"
)

// Engine is the data-driven targeting engine. Push data (property lists,
// entity configs) and it evaluates. No data for a dimension = no-op.
type Engine struct {
	providerID string
	store      Store

	properties PropertyList

	packages        map[string]PackageConfig
	dynamicPackages bool

	metrics Metrics
}

// EngineConfig holds all configuration for creating an Engine.
type EngineConfig struct {
	ProviderID      string
	Store           Store
	Properties      PropertyList
	Packages        []PackageConfig
	DynamicPackages bool    // When true, load package configs from Store at eval time.
	Metrics         Metrics // nil = noop
}

// NewEngine creates a targeting engine.
func NewEngine(cfg EngineConfig) *Engine {
	pkgMap := make(map[string]PackageConfig, len(cfg.Packages))
	for _, p := range cfg.Packages {
		pkgMap[p.PackageID] = p
	}
	metrics := cfg.Metrics
	if metrics == nil {
		metrics = noopMetrics{}
	}
	return &Engine{
		providerID:      cfg.ProviderID,
		store:           cfg.Store,
		properties:      cfg.Properties,
		packages:        pkgMap,
		dynamicPackages: cfg.DynamicPackages,
		metrics:         metrics,
	}
}

// ContextResult holds the output of context evaluation.
type ContextResult struct {
	RequestID string
	Offers    []tmproto.Offer
	Signals   map[string]any
}

// EvaluateContext evaluates available packages against content context.
//
// Pipeline:
//  1. Global property bitmap check
//  2. Suppression check (property + geo)
//  3. Per-package: property bitmap → URL filter → topic match → offers + segments
func (e *Engine) EvaluateContext(ctx context.Context, req *tmproto.ContextMatchRequest) (*ContextResult, error) {
	evalStart := time.Now()
	rid := req.PropertyRID

	if !e.properties.ContainsGlobal(rid) {
		return &ContextResult{RequestID: req.RequestID}, nil
	}

	suppressionStart := time.Now()
	suppressed, err := e.isPropertySuppressed(ctx, rid)
	if err != nil {
		e.metrics.StoreError(StageSuppression, err)
	} else if suppressed {
		e.metrics.Latency(StageSuppression, time.Since(suppressionStart))
		return &ContextResult{RequestID: req.RequestID}, nil
	}
	if country, _ := req.Geo["country"].(string); country != "" {
		geoSuppressed, err := e.isGeoSuppressed(ctx, country)
		if err != nil {
			e.metrics.StoreError(StageSuppression, err)
		} else if geoSuppressed {
			e.metrics.Latency(StageSuppression, time.Since(suppressionStart))
			return &ContextResult{RequestID: req.RequestID}, nil
		}
	}
	e.metrics.Latency(StageSuppression, time.Since(suppressionStart))

	artifactRefs := extractArtifactRefURLs(req)

	var dynCtxConfigs map[string]*PackageContextConfig
	if e.dynamicPackages {
		var err error
		dynCtxConfigs, err = batchLoadPackageContextConfigs(ctx, e.store, req.PackageIDs)
		if err != nil {
			e.metrics.StoreError("load_context_configs", err)
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
			e.metrics.ContextEvaluated(pkgID, StagePropertyBitmap, false)
			continue
		}
		if !e.properties.ContainsPackage(pkgID, rid) {
			e.metrics.ContextEvaluated(pkgID, StagePropertyBitmap, false)
			continue
		}

		if urlBlocklist || urlAllowlist {
			blocked, err := e.checkURLFilter(ctx, artifactRefs, pkgID, PackageConfig{URLBlocklist: urlBlocklist, URLAllowlist: urlAllowlist})
			if err != nil {
				e.metrics.StoreError(StageURLFilter, err)
			} else if blocked {
				e.metrics.ContextEvaluated(pkgID, StageURLFilter, false)
				continue
			}
		}

		if topicTargets {
			matched, err := e.checkTopicMatch(ctx, artifactRefs, pkgID)
			if err != nil {
				e.metrics.StoreError(StageTopicMatch, err)
			} else if !matched {
				e.metrics.ContextEvaluated(pkgID, StageTopicMatch, false)
				continue
			}
		}

		e.metrics.ContextEvaluated(pkgID, "", true)
		offers = append(offers, pkgOffers...)
		segments = append(segments, emitSegments...)
	}

	e.metrics.Latency("context_eval", time.Since(evalStart))

	result := &ContextResult{
		RequestID: req.RequestID,
		Offers:    offers,
	}
	if len(segments) > 0 {
		result.Signals = map[string]any{"segments": segments}
	}
	return result, nil
}

// IdentityResult holds the output of identity evaluation.
type IdentityResult struct {
	RequestID   string
	Eligibility []tmproto.PackageEligibility
}

// EvaluateContextResolved evaluates context using pre-built indexes.
// Minimal Store calls: only suppression checks and artifact→topic resolution.
// All targeting lookups (property, topic, URL) are in-memory.
func (e *Engine) EvaluateContextResolved(ctx context.Context, resolved *ResolvedPackages, req *tmproto.ContextMatchRequest) (*ContextResult, error) {
	evalStart := time.Now()
	rid := req.PropertyRID

	if !e.properties.ContainsGlobal(rid) {
		return &ContextResult{RequestID: req.RequestID}, nil
	}

	suppressed, err := e.isPropertySuppressed(ctx, rid)
	if err != nil {
		e.metrics.StoreError(StageSuppression, err)
	} else if suppressed {
		return &ContextResult{RequestID: req.RequestID}, nil
	}

	propertyCandidates := resolved.ContextCandidates(rid)

	artifactRefs := extractArtifactRefURLs(req)

	var artifactTopics []string
	for _, artifact := range artifactRefs {
		topics, err := e.store.SetMembers(ctx, "topics:artifact:"+artifact)
		if err != nil {
			e.metrics.StoreError(StageTopicMatch, err)
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
				e.metrics.ContextEvaluated(pkgID, StagePropertyBitmap, false)
				continue
			}
		}

		if cfg.TopicTargets && len(artifactRefs) > 0 {
			if len(topicCandidates) == 0 {
				e.metrics.ContextEvaluated(pkgID, StageTopicMatch, false)
				continue
			}
			if _, ok := topicCandidates[pkgID]; !ok {
				e.metrics.ContextEvaluated(pkgID, StageTopicMatch, false)
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
			e.metrics.ContextEvaluated(pkgID, StageURLFilter, false)
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
				e.metrics.ContextEvaluated(pkgID, StageURLFilter, false)
				continue
			}
		}

		e.metrics.ContextEvaluated(pkgID, "", true)
		offers = append(offers, buildOffersFromDynamic(pkgID, cfg)...)
		segments = append(segments, cfg.EmitSegments...)
	}

	e.metrics.Latency("context_eval", time.Since(evalStart))

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
// Packages that have no IdentityConfig in resolved, or whose config has no
// TargetSegments, are reported eligible: segment matching is opt-in, not a
// default deny.
//
// Frequency capping is handled by the separate fcap.Service; the caller composes
// engine output with fcap lookups when fcap gating is required.
func (e *Engine) EvaluateIdentityResolved(ctx context.Context, resolved *ResolvedPackages, req *tmproto.IdentityMatchRequest) (*IdentityResult, error) {
	evalStart := time.Now()
	identities := resolveIdentities(req)

	keys := make([]string, 0, len(identities))
	for _, uid := range identities {
		keys = append(keys, "user:profile:"+HashToken(uid.UserToken))
	}

	values, err := e.store.MGet(ctx, keys...)
	if err != nil {
		e.metrics.StoreError("load_user_data", err)
		values = make([]string, len(keys))
	}

	profiles := make([]*UserProfile, 0, len(identities))
	for i := range identities {
		profiles = append(profiles, ParseUserProfile(values[i]))
	}
	mergedProfile := MergeUserProfiles(profiles...)

	var userSegments []string
	for seg := range mergedProfile.Segments {
		userSegments = append(userSegments, seg)
	}

	segmentEligible := resolved.SegmentCandidates(userSegments)

	var eligibility []tmproto.PackageEligibility
	for _, pkgID := range req.PackageIDs {
		idCfg := resolved.IdentityConfigs[pkgID]
		eligible := true

		if idCfg != nil && len(idCfg.TargetSegments) > 0 {
			if _, ok := segmentEligible[pkgID]; !ok {
				eligible = false
				e.metrics.IdentityEvaluated(pkgID, StageAudience, false)
			}
		}

		eligibility = append(eligibility, tmproto.PackageEligibility{PackageID: pkgID, Eligible: eligible})
	}

	e.metrics.Latency("identity_eval", time.Since(evalStart))

	return &IdentityResult{
		RequestID:   req.RequestID,
		Eligibility: eligibility,
	}, nil
}

// SetUserProfile writes a user's segment memberships to the profile key.
func (e *Engine) SetUserProfile(ctx context.Context, userToken string, segments map[string]float64) error {
	hash := HashToken(userToken)
	profile := UserProfile{Segments: segments}
	data, err := json.Marshal(profile)
	if err != nil {
		return err
	}
	return e.store.Set(ctx, "user:profile:"+hash, string(data), 0)
}

// SetUserProfiles writes segment memberships for multiple users in a single batch.
// The profiles map is keyed by user token.
func (e *Engine) SetUserProfiles(ctx context.Context, profiles map[string]map[string]float64) error {
	kvs := make(map[string]string, len(profiles))
	for userToken, segments := range profiles {
		hash := HashToken(userToken)
		profile := UserProfile{Segments: segments}
		data, err := json.Marshal(profile)
		if err != nil {
			return err
		}
		kvs["user:profile:"+hash] = string(data)
	}
	return e.store.MSet(ctx, kvs, 0)
}

// checkURLFilter checks artifacts against URL blocklists and allowlists.
func (e *Engine) checkURLFilter(ctx context.Context, artifacts []string, pkgID string, cfg PackageConfig) (bool, error) {
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
func (e *Engine) checkTopicMatch(ctx context.Context, artifacts []string, pkgID string) (bool, error) {
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
