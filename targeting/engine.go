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

	// Now returns the current time. Defaults to time.Now.
	// Override in tests to control time.
	Now func() time.Time
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
		Now:             time.Now,
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

	// 1. Global property bitmap (in-memory, no degradation needed).
	if !e.properties.ContainsGlobal(rid) {
		return &ContextResult{RequestID: req.RequestID}, nil
	}

	// 2. Suppression (graceful: skip on Store error).
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

	// Extract artifact refs as string keys for URL/topic checks.
	artifactRefs := extractArtifactRefURLs(req)

	// 4. Per-package evaluation.
	// In dynamic mode, batch-load all context configs from Store (1 MGet).
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
		// Resolve config: dynamic or static.
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

		// Per-package property bitmap.
		if propertyBitmap != nil && !propertyBitmap.Contains(rid) {
			e.metrics.ContextEvaluated(pkgID, StagePropertyBitmap, false)
			continue
		}
		if !e.properties.ContainsPackage(pkgID, rid) {
			e.metrics.ContextEvaluated(pkgID, StagePropertyBitmap, false)
			continue
		}

		// URL filter (graceful: skip on Store error).
		if urlBlocklist || urlAllowlist {
			blocked, err := e.checkURLFilter(ctx, artifactRefs, pkgID, PackageConfig{URLBlocklist: urlBlocklist, URLAllowlist: urlAllowlist})
			if err != nil {
				e.metrics.StoreError(StageURLFilter, err)
			} else if blocked {
				e.metrics.ContextEvaluated(pkgID, StageURLFilter, false)
				continue
			}
		}

		// Topic match (graceful: skip on Store error).
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

	// 1. Global property bitmap (in-memory).
	if !e.properties.ContainsGlobal(rid) {
		return &ContextResult{RequestID: req.RequestID}, nil
	}

	// 2. Suppression (graceful: skip on Store error).
	suppressed, err := e.isPropertySuppressed(ctx, rid)
	if err != nil {
		e.metrics.StoreError(StageSuppression, err)
	} else if suppressed {
		return &ContextResult{RequestID: req.RequestID}, nil
	}

	// 3. PropertyIndex: which packages target this property?
	propertyCandidates := resolved.ContextCandidates(rid)

	// Extract artifact refs as string keys for URL/topic checks.
	artifactRefs := extractArtifactRefURLs(req)

	// 5. Resolve artifact topics from Store (per-request, can't cache).
	var artifactTopics []string
	for _, artifact := range artifactRefs {
		topics, err := e.store.SetMembers(ctx, "topics:artifact:"+artifact)
		if err != nil {
			e.metrics.StoreError(StageTopicMatch, err)
		} else {
			artifactTopics = append(artifactTopics, topics...)
		}
	}

	// 6. Compute URL hashes for artifacts.
	var artifactHashes []string
	for _, artifact := range artifactRefs {
		artifactHashes = append(artifactHashes, HashURL(artifact))
	}

	// 7. Pre-compute topic candidates (once, not per-package).
	topicCandidates := resolved.TopicCandidates(artifactTopics)

	var offers []tmproto.Offer
	var segments []string

	for _, pkgID := range req.PackageIDs {
		cfg := resolved.ContextConfigs[pkgID]
		if cfg == nil {
			continue
		}

		// PropertyIndex check (in-memory).
		if propertyCandidates != nil {
			if _, ok := propertyCandidates[pkgID]; !ok {
				e.metrics.ContextEvaluated(pkgID, StagePropertyBitmap, false)
				continue
			}
		}

		// TopicIndex check (pre-computed above, in-memory lookup).
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

		// URL blocklist check (in-memory).
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

		// URL allowlist check (in-memory).
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

// EvaluateIdentityResolved evaluates identity using pre-built indexes,
// binary exposure logs, and lazy dedup. 1 MGet round-trip for all UIDs,
// then all computation is local with zero allocations per package.
func (e *Engine) EvaluateIdentityResolved(ctx context.Context, resolved *ResolvedPackages, req *tmproto.IdentityMatchRequest) (*IdentityResult, error) {
	evalStart := time.Now()
	now := e.now()
	nowUnix := now.Unix()
	identities := resolveIdentities(req)

	// 1. Build MGet keys: profile + exposures for each UID.
	keys := make([]string, 0, len(identities)*2)
	for _, uid := range identities {
		hash := HashToken(uid.UserToken)
		keys = append(keys, "user:profile:"+hash)
		keys = append(keys, "user:exposures:"+hash)
	}

	// 2. Single MGet — 1 round-trip.
	values, err := e.store.MGet(ctx, keys...)
	if err != nil {
		e.metrics.StoreError("load_user_data", err)
		values = make([]string, len(keys))
	}

	// 3. Parse profiles (JSON, small) and exposure logs (binary, zero-copy).
	profiles := make([]*UserProfile, 0, len(identities))
	firstLogs := make([]BinaryExposureLog, 0, len(identities))
	for i := range identities {
		profileData := values[i*2]
		exposureData := values[i*2+1]
		profiles = append(profiles, ParseUserProfile(profileData))
		binLog := BinaryExposureLog(exposureData)
		if len(exposureData) > 0 {
			if err := ValidateBinaryLog(binLog); err != nil {
				e.metrics.StoreError("corrupt_exposure_log", err)
				binLog = nil
			}
		}
		firstLogs = append(firstLogs, binLog)
	}
	mergedProfile := MergeUserProfiles(profiles...)

	// 4. Extract segment names from merged profile.
	var userSegments []string
	for seg := range mergedProfile.Segments {
		userSegments = append(userSegments, seg)
	}

	// 5. SegmentIndex: which packages is this user eligible for?
	segmentEligible := resolved.SegmentCandidates(userSegments)

	// 6. Evaluate each requested package using binary lazy dedup.
	var eligibility []tmproto.PackageEligibility
	for _, pkgID := range req.PackageIDs {
		idCfg := resolved.IdentityConfigs[pkgID]
		eligible := true

		// Segment gating.
		if idCfg != nil && len(idCfg.TargetSegments) > 0 {
			if _, ok := segmentEligible[pkgID]; !ok {
				eligible = false
				e.metrics.IdentityEvaluated(pkgID, StageAudience, false)
			}
		}

		// Campaign frequency cap (binary lazy dedup across all UID logs).
		if eligible && idCfg != nil && idCfg.CampaignID != "" {
			campCfg := resolved.CampaignConfigs[idCfg.CampaignID]
			if campCfg != nil && len(campCfg.FrequencyRules) > 0 {
				rules := toFrequencyRules(campCfg.FrequencyRules)
				campHash := hashString(idCfg.CampaignID)
				if CheckFrequencyRulesMultiLog(firstLogs, FilterCampaign, campHash, rules, nowUnix) {
					eligible = false
					e.metrics.IdentityEvaluated(pkgID, StageCampaignFreq, false)
				}
			}
		}

		// Creative-level frequency cap (binary lazy dedup across all UID logs).
		// PackageIdentityConfig.FrequencyRules are applied against the package's
		// creative. Without a CreativeID on the config, the package cap is skipped.
		var creativeHash uint64
		if idCfg != nil && idCfg.CreativeID != "" {
			creativeHash = hashString(idCfg.CreativeID)
		}
		if eligible && idCfg != nil && idCfg.CreativeID != "" && len(idCfg.FrequencyRules) > 0 {
			rules := toFrequencyRules(idCfg.FrequencyRules)
			if CheckFrequencyRulesMultiLog(firstLogs, FilterCreative, creativeHash, rules, nowUnix) {
				eligible = false
				e.metrics.IdentityEvaluated(pkgID, StagePackageFreq, false)
			}
		}

		// Intent score (binary, scan across all UID logs) keyed by creative.
		var intent float64
		if idCfg != nil && idCfg.CreativeID != "" {
			latestTS := LatestExposureMultiLog(firstLogs, FilterCreative, creativeHash)
			if latestTS > 0 {
				intent = ComputeIntentScore(latestTS, now)
			}
		}

		pe := tmproto.PackageEligibility{PackageID: pkgID, Eligible: eligible}
		if intent > 0 {
			pe.IntentScore = &intent
		}
		eligibility = append(eligibility, pe)
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
	// No artifacts = pass through.
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

func (e *Engine) now() time.Time {
	if e.Now != nil {
		return e.Now()
	}
	return time.Now()
}

// buildOffers returns one or more Offers for an activated package.
// If Offers are configured, each entry produces a separate Offer.
// Otherwise, a single Offer is built from the package-level fields.
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
// Non-URL refs (eidr, gtin, isrc, etc.) are ignored — they aren't resolvable as
// web URLs.
func extractArtifactRefURLs(req *tmproto.ContextMatchRequest) []string {
	var urls []string
	for _, ref := range req.ArtifactRefs {
		if ref.Type == tmproto.ArtifactRefTypeURL {
			urls = append(urls, ref.Value)
		}
	}
	return urls
}
