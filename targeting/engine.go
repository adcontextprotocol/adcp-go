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
	"fmt"
	"math"
	"strconv"
	"sync/atomic"
	"time"

	"github.com/adcontextprotocol/adcp-go/tmproto"
)

const signatureFailureTTL = 5 * time.Minute

// Engine is the data-driven targeting engine. Push data (property lists,
// entity configs) and it evaluates. No data for a dimension = no-op.
type Engine struct {
	providerID string
	store      Store
	registry   PropertyRegistry

	properties PropertyList

	packages        map[string]PackageConfig
	dynamicPackages bool

	metrics Metrics

	sigSampleRate     uint32
	requireSignatures bool
	sigCounter        atomic.Uint64

	// Now returns the current time. Defaults to time.Now.
	// Override in tests to control time.
	Now func() time.Time
}

// EngineConfig holds all configuration for creating an Engine.
type EngineConfig struct {
	ProviderID        string
	Store             Store
	Registry          PropertyRegistry // nil = no signature verification
	Properties        PropertyList
	Packages        []PackageConfig
	DynamicPackages bool    // When true, load package configs from Store at eval time.
	Metrics         Metrics // nil = noop
	SigSampleRate     uint32  // 0-100. 0 disables verification.
	RequireSignatures bool
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
		providerID:        cfg.ProviderID,
		store:             cfg.Store,
		registry:          cfg.Registry,
		properties:        cfg.Properties,
		packages:          pkgMap,
		dynamicPackages:   cfg.DynamicPackages,
		metrics:           metrics,
		sigSampleRate:     cfg.SigSampleRate,
		requireSignatures: cfg.RequireSignatures,
		Now:               time.Now,
	}
}

// ContextResult holds the output of context evaluation.
type ContextResult struct {
	RequestID string
	Offers    []tmproto.Offer
	Signals   *tmproto.Signals
}

// EvaluateContext evaluates available packages against content context.
//
// Pipeline:
//  1. Global property bitmap check
//  2. Suppression check (property + geo)
//  3. Signature verification (sampling)
//  4. Per-package: property bitmap → URL filter → topic match → offers + segments
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
	if req.Geo != nil {
		geoSuppressed, err := e.isGeoSuppressed(ctx, req.Geo.Country)
		if err != nil {
			e.metrics.StoreError(StageSuppression, err)
		} else if geoSuppressed {
			e.metrics.Latency(StageSuppression, time.Since(suppressionStart))
			return &ContextResult{RequestID: req.RequestID}, nil
		}
	}
	e.metrics.Latency(StageSuppression, time.Since(suppressionStart))

	// 3. Signature verification (not Store-dependent, errors are hard failures).
	sigStart := time.Now()
	if err := e.verifySignature(ctx, req); err != nil {
		return nil, fmt.Errorf("signature verification for property %d: %w", rid, err)
	}
	e.metrics.Latency(StageSignature, time.Since(sigStart))

	// 4. Per-package evaluation.
	// In dynamic mode, batch-load all context configs from Store (1 MGet).
	var dynCtxConfigs map[string]*PackageContextConfig
	if e.dynamicPackages {
		pkgIDs := make([]string, len(req.AvailablePkgs))
		for i, p := range req.AvailablePkgs {
			pkgIDs[i] = p.PackageID
		}
		var err error
		dynCtxConfigs, err = batchLoadPackageContextConfigs(ctx, e.store, pkgIDs)
		if err != nil {
			e.metrics.StoreError("load_context_configs", err)
			dynCtxConfigs = make(map[string]*PackageContextConfig)
		}
	}

	var offers []tmproto.Offer
	var segments []string

	for _, pkg := range req.AvailablePkgs {
		// Resolve config: dynamic or static.
		var urlBlocklist, urlAllowlist, topicTargets bool
		var emitSegments []string
		var pkgOffers []tmproto.Offer
		var propertyBitmap Bitmap

		if e.dynamicPackages {
			dCfg := dynCtxConfigs[pkg.PackageID]
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
			pkgOffers = buildOffersFromDynamic(pkg.PackageID, dCfg)
		} else {
			cfg, known := e.packages[pkg.PackageID]
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
			e.metrics.ContextEvaluated(pkg.PackageID, StagePropertyBitmap, false)
			continue
		}
		if !e.properties.ContainsPackage(pkg.PackageID, rid) {
			e.metrics.ContextEvaluated(pkg.PackageID, StagePropertyBitmap, false)
			continue
		}

		// URL filter (graceful: skip on Store error).
		if urlBlocklist || urlAllowlist {
			blocked, err := e.checkURLFilter(ctx, req.Artifacts, pkg.PackageID, PackageConfig{URLBlocklist: urlBlocklist, URLAllowlist: urlAllowlist})
			if err != nil {
				e.metrics.StoreError(StageURLFilter, err)
			} else if blocked {
				e.metrics.ContextEvaluated(pkg.PackageID, StageURLFilter, false)
				continue
			}
		}

		// Topic match (graceful: skip on Store error).
		if topicTargets {
			matched, err := e.checkTopicMatch(ctx, req.Artifacts, pkg.PackageID)
			if err != nil {
				e.metrics.StoreError(StageTopicMatch, err)
			} else if !matched {
				e.metrics.ContextEvaluated(pkg.PackageID, StageTopicMatch, false)
				continue
			}
		}

		e.metrics.ContextEvaluated(pkg.PackageID, "", true)
		offers = append(offers, pkgOffers...)
		segments = append(segments, emitSegments...)
	}

	e.metrics.Latency("context_eval", time.Since(evalStart))

	result := &ContextResult{
		RequestID: req.RequestID,
		Offers:    offers,
	}
	if len(segments) > 0 {
		result.Signals = &tmproto.Signals{Segments: segments}
	}
	return result, nil
}

// IdentityResult holds the output of identity evaluation.
type IdentityResult struct {
	RequestID   string
	Eligibility []tmproto.PackageEligibility
}

// EvaluateIdentity evaluates user eligibility against requested packages.
//
// Pipeline per package:
//  1. Campaign frequency cap
//  2. Package frequency cap
//  3. Audience segment match
//  4. Intent score
func (e *Engine) EvaluateIdentity(ctx context.Context, req *tmproto.IdentityMatchRequest) (*IdentityResult, error) {
	evalStart := time.Now()
	tokenHash := HashToken(req.UserToken)
	now := e.now()

	// Batch-load all identity configs (1 MGet) and campaign configs (1 MGet).
	idConfigs, err := batchLoadPackageIdentityConfigs(ctx, e.store, req.PackageIDs)
	if err != nil {
		e.metrics.StoreError("load_identity_configs", err)
		idConfigs = make(map[string]*PackageIdentityConfig)
	}

	// Collect unique campaign IDs for batch campaign config load.
	campaignIDSet := make(map[string]struct{})
	for _, cfg := range idConfigs {
		if cfg != nil && cfg.CampaignID != "" {
			campaignIDSet[cfg.CampaignID] = struct{}{}
		}
	}
	campaignIDs := make([]string, 0, len(campaignIDSet))
	for id := range campaignIDSet {
		campaignIDs = append(campaignIDs, id)
	}
	campConfigs, err := batchLoadCampaignFreqConfigs(ctx, e.store, campaignIDs)
	if err != nil {
		e.metrics.StoreError("load_campaign_configs", err)
		campConfigs = make(map[string]*CampaignFreqConfig)
	}

	var eligibility []tmproto.PackageEligibility
	for _, pkgID := range req.PackageIDs {
		// In dynamic mode, any package with identity config is "known."
		// In static mode, the package must be in e.packages.
		if !e.dynamicPackages {
			if _, known := e.packages[pkgID]; !known {
				eligibility = append(eligibility, tmproto.PackageEligibility{
					PackageID: pkgID,
					Eligible:  false,
				})
				continue
			}
		}

		eligible := true
		idCfg := idConfigs[pkgID]

		// Campaign frequency cap (graceful: skip on Store error).
		if eligible && idCfg != nil && idCfg.CampaignID != "" {
			campCfg := campConfigs[idCfg.CampaignID]
			if campCfg != nil && len(campCfg.FrequencyRules) > 0 {
				key := fmt.Sprintf("freq:campaign:%s:%s", idCfg.CampaignID, tokenHash)
				rules := toFrequencyRules(campCfg.FrequencyRules)
				capped, err := e.checkFrequencyRules(ctx, key, rules, now)
				if err != nil {
					e.metrics.StoreError(StageCampaignFreq, err)
				} else if capped {
					eligible = false
					e.metrics.IdentityEvaluated(pkgID, StageCampaignFreq, false)
				}
			}
		}

		// Package frequency cap (graceful: skip on Store error).
		if eligible && idCfg != nil && len(idCfg.FrequencyRules) > 0 {
			key := fmt.Sprintf("freq:pkg:%s:%s", pkgID, tokenHash)
			rules := toFrequencyRules(idCfg.FrequencyRules)
			capped, err := e.checkFrequencyRules(ctx, key, rules, now)
			if err != nil {
				e.metrics.StoreError(StagePackageFreq, err)
			} else if capped {
				eligible = false
				e.metrics.IdentityEvaluated(pkgID, StagePackageFreq, false)
			}
		}

		// Audience segments (graceful: skip on Store error).
		if eligible && idCfg != nil && len(idCfg.TargetSegments) > 0 {
			matched, err := e.checkAudienceMatch(ctx, tokenHash, idCfg.TargetSegments)
			if err != nil {
				e.metrics.StoreError(StageAudience, err)
			} else if !matched {
				eligible = false
				e.metrics.IdentityEvaluated(pkgID, StageAudience, false)
			}
		}

		// Intent score (graceful: skip on Store error, return 0).
		var intent float64
		if score, err := e.computeIntentScore(ctx, tokenHash, pkgID, now); err != nil {
			e.metrics.StoreError("intent", err)
		} else {
			intent = score
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

// EvaluateContextResolved evaluates context using pre-built indexes.
// Minimal Store calls: only suppression checks and artifact→topic resolution.
// All targeting lookups (property, topic, URL) are in-memory.
func (e *Engine) EvaluateContextResolved(ctx context.Context, resolved *ResolvedPackages, req *tmproto.ContextMatchRequest) (*ContextResult, error) {
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

	// 3. Signature verification.
	if err := e.verifySignature(ctx, req); err != nil {
		return nil, fmt.Errorf("signature verification for property %d: %w", rid, err)
	}

	// 4. PropertyIndex: which packages target this property?
	propertyCandidates := resolved.ContextCandidates(rid)

	// 5. Resolve artifact topics from Store (per-request, can't cache).
	var artifactTopics []string
	for _, artifact := range req.Artifacts {
		topics, err := e.store.SetMembers(ctx, "topics:artifact:"+artifact)
		if err != nil {
			e.metrics.StoreError(StageTopicMatch, err)
		} else {
			artifactTopics = append(artifactTopics, topics...)
		}
	}

	// 6. Compute URL hashes for artifacts.
	var artifactHashes []string
	for _, artifact := range req.Artifacts {
		artifactHashes = append(artifactHashes, HashURL(artifact))
	}

	// 7. Pre-compute topic candidates (once, not per-package).
	topicCandidates := resolved.TopicCandidates(artifactTopics)

	var offers []tmproto.Offer
	var segments []string

	for _, pkg := range req.AvailablePkgs {
		pkgID := pkg.PackageID
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
		if cfg.TopicTargets && len(req.Artifacts) > 0 {
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

	result := &ContextResult{
		RequestID: req.RequestID,
		Offers:    offers,
	}
	if len(segments) > 0 {
		result.Signals = &tmproto.Signals{Segments: segments}
	}
	return result, nil
}

// EvaluateIdentityResolved evaluates identity using pre-built indexes,
// binary exposure logs, and lazy dedup. 1 MGet round-trip for all UIDs,
// then all computation is local with zero allocations per package.
func (e *Engine) EvaluateIdentityResolved(ctx context.Context, resolved *ResolvedPackages, req *tmproto.IdentityMatchRequest) (*IdentityResult, error) {
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
				if CheckFrequencyRulesMultiLog(firstLogs, campHash, true, rules, nowUnix) {
					eligible = false
					e.metrics.IdentityEvaluated(pkgID, StageCampaignFreq, false)
				}
			}
		}

		// Package frequency cap (binary lazy dedup across all UID logs).
		pkgHash := hashString(pkgID)
		if eligible && idCfg != nil && len(idCfg.FrequencyRules) > 0 {
			rules := toFrequencyRules(idCfg.FrequencyRules)
			if CheckFrequencyRulesMultiLog(firstLogs, pkgHash, false, rules, nowUnix) {
				eligible = false
				e.metrics.IdentityEvaluated(pkgID, StagePackageFreq, false)
			}
		}

		// Intent score (binary, scan across all UID logs).
		var intent float64
		latestTS := LatestExposureMultiLog(firstLogs, pkgHash)
		if latestTS > 0 {
			intent = ComputeIntentScore(latestTS, now)
		}

		pe := tmproto.PackageEligibility{PackageID: pkgID, Eligible: eligible}
		if intent > 0 {
			pe.IntentScore = &intent
		}
		eligibility = append(eligibility, pe)
	}

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

// RecordExposure records an impression to the exposure log for all UIDs.
// Each UID's exposure log is read, the new entry is appended,
// old entries are pruned, and the log is written back.
//
// NOTE: The read-modify-write is not atomic. Concurrent RecordExposure calls
// for the same user may lose an exposure. This is acceptable for frequency
// capping (slightly under-counting is benign). For strict counting, use
// Valkey Lua scripting for atomic append.
// TODO: Add Store.Append method for atomic binary append.
func (e *Engine) RecordExposure(ctx context.Context, req *tmproto.ExposeRequest) (*tmproto.ExposeResponse, error) {
	now := e.now()
	identities := resolveExposeIdentities(req)

	// Resolve campaign ID.
	campaignID := req.CampaignID
	if campaignID == "" {
		idCfg, _ := loadPackageIdentityConfig(ctx, e.store, req.PackageID)
		if idCfg != nil {
			campaignID = idCfg.CampaignID
		}
	}

	// Generate impression ID if not provided.
	impressionID := req.ImpressionID
	if impressionID == "" {
		impressionID = fmt.Sprintf("%d:%s", now.UnixNano(), req.PackageID)
	}

	entry := ExposureEntry{
		ImpressionID: impressionID,
		PackageID:    req.PackageID,
		CampaignID:   campaignID,
		Timestamp:    now.Unix(),
	}

	cutoff := now.Add(-30 * 24 * time.Hour).Unix()
	nowMs := float64(now.UnixMilli())
	pruneBeforeMs := float64(now.Add(-30 * 24 * time.Hour).UnixMilli())
	nowStr := fmt.Sprintf("%d", now.Unix())

	// Pre-compute token hashes (SHA-256, avoid re-hashing per loop).
	hashes := make([]string, len(identities))
	for i, uid := range identities {
		hashes[i] = HashToken(uid.UserToken)
	}

	// Write to each UID's exposure log. Capture the first UID's log for the response.
	var firstLog BinaryExposureLog
	for i, hash := range hashes {
		key := "user:exposures:" + hash

		val, _, err := e.store.Get(ctx, key)
		if err != nil {
			e.metrics.StoreError("read_exposure_log", err)
			continue
		}

		// Prune expired entries and append new one, preserving versioned header.
		existing := BinaryExposureLog(val)
		if len(val) > 0 {
			if err := ValidateBinaryLog(existing); err != nil {
				e.metrics.StoreError("corrupt_exposure_log", err)
				existing = nil
			}
		}
		newEntry := EncodeBinaryExposureLog(ExposureLog{entry})

		pruned := newBinaryLog(existing.Len() + 1)
		for j := range existing.Len() {
			if existing.Timestamp(j) >= cutoff {
				off := existing.entryOffset(j)
				pruned = append(pruned, existing[off:off+binaryEntrySize]...)
			}
		}
		// Append the new entry's payload (skip its header).
		pruned = append(pruned, newEntry[binaryHeaderSize:]...)
		pruned = TruncateBinaryLog(pruned, maxExposureEntries)

		if err := e.store.Set(ctx, key, string(pruned), 30*24*time.Hour); err != nil {
			e.metrics.StoreError("write_exposure_log", err)
		}
		if i == 0 {
			firstLog = BinaryExposureLog(pruned)
		}

		// Package frequency sorted set.
		pkgFreqKey := fmt.Sprintf("freq:pkg:%s:%s", req.PackageID, hash)
		if err := e.store.ZAdd(ctx, pkgFreqKey, nowMs, impressionID); err != nil {
			e.metrics.StoreError("zadd_pkg_freq", err)
		} else {
			if err := e.store.ZRemRangeByScore(ctx, pkgFreqKey, 0, pruneBeforeMs); err != nil {
				e.metrics.StoreError("zremrangebyscore_pkg_freq", err)
			}
			_ = e.store.ZExpire(ctx, pkgFreqKey, 30*24*time.Hour)
		}

		// Campaign frequency sorted set.
		if campaignID != "" {
			campFreqKey := fmt.Sprintf("freq:campaign:%s:%s", campaignID, hash)
			if err := e.store.ZAdd(ctx, campFreqKey, nowMs, impressionID); err != nil {
				e.metrics.StoreError("zadd_campaign_freq", err)
			} else {
				if err := e.store.ZRemRangeByScore(ctx, campFreqKey, 0, pruneBeforeMs); err != nil {
					e.metrics.StoreError("zremrangebyscore_campaign_freq", err)
				}
				_ = e.store.ZExpire(ctx, campFreqKey, 30*24*time.Hour)
			}
		}

		// Intent timestamp.
		intentKey := fmt.Sprintf("intent:%s:%s", req.PackageID, hash)
		if err := e.store.Set(ctx, intentKey, nowStr, 7*24*time.Hour); err != nil {
			e.metrics.StoreError("set_intent", err)
		}
	}

	e.metrics.ExposureRecorded(req.PackageID)

	// Compute campaign count from the first UID's log (already in memory, no re-read).
	resp := &tmproto.ExposeResponse{PackageID: req.PackageID}
	if campaignID != "" && firstLog.Len() > 0 {
		campHash := hashString(campaignID)

		campCfg, _ := loadCampaignFreqConfig(ctx, e.store, campaignID)
		if campCfg != nil && len(campCfg.FrequencyRules) > 0 {
			rules := toFrequencyRules(campCfg.FrequencyRules)
			shortestRule := rules[0]
			for _, r := range rules[1:] {
				if r.Window < shortestRule.Window {
					shortestRule = r
				}
			}
			windowStart := now.Add(-shortestRule.Window).Unix()
			count := 0
			for i := range firstLog.Len() {
				if firstLog.CampaignHash(i) == campHash && firstLog.Timestamp(i) >= windowStart {
					count++
				}
			}
			resp.CampaignCount = count
			remaining := shortestRule.MaxCount - count
			if remaining < 0 {
				remaining = 0
			}
			resp.CampaignRemaining = remaining
		}
	}

	return resp, nil
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

// checkFrequencyRules checks all rules against a sorted set.
// Returns true (capped) if ANY rule is exceeded.
func (e *Engine) checkFrequencyRules(ctx context.Context, key string, rules []FrequencyRule, now time.Time) (bool, error) {
	for _, rule := range rules {
		cutoff := float64(now.Add(-rule.Window).UnixMilli())
		count, err := e.store.ZCount(ctx, key, cutoff, math.MaxFloat64)
		if err != nil {
			return false, err
		}
		if int(count) >= rule.MaxCount {
			return true, nil
		}
	}
	return false, nil
}

// checkAudienceMatch checks if a user is in any of the target segments.
func (e *Engine) checkAudienceMatch(ctx context.Context, tokenHash string, segments []string) (bool, error) {
	for _, seg := range segments {
		key := fmt.Sprintf("audience:%s", seg)
		member, err := e.store.SetIsMember(ctx, key, tokenHash)
		if err != nil {
			return false, err
		}
		if member {
			return true, nil
		}
	}
	return false, nil
}

// computeIntentScore calculates a recency-based intent score.
// Decays linearly from 1.0 to 0.0 over 7 days.
func (e *Engine) computeIntentScore(ctx context.Context, tokenHash, packageID string, now time.Time) (float64, error) {
	key := fmt.Sprintf("intent:%s:%s", packageID, tokenHash)
	val, ok, err := e.store.Get(ctx, key)
	if err != nil {
		return 0, err
	}
	if !ok {
		return 0, nil
	}
	ts, err := strconv.ParseInt(val, 10, 64)
	if err != nil {
		return 0, nil //nolint:nilerr // unparseable timestamp = no intent score
	}
	hoursSince := now.Sub(time.Unix(ts, 0)).Hours()
	score := 1.0 - (hoursSince / 168.0)
	return math.Max(0, score), nil
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
				DealID:           o.DealID,
				Brand:            o.Brand,
				Price:            o.Price,
				Summary:          o.Summary,
				ManifestType:     o.ManifestType,
				CreativeManifest: o.CreativeManifest,
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
		ManifestType:     cfg.ManifestType,
		CreativeManifest: cfg.CreativeManifest,
		Macros:           cfg.Macros,
	}}
}

// buildOffersFromDynamic builds offers from a dynamic PackageContextConfig.
func buildOffersFromDynamic(pkgID string, cfg *PackageContextConfig) []tmproto.Offer {
	if len(cfg.Offers) > 0 {
		offers := make([]tmproto.Offer, len(cfg.Offers))
		for i, o := range cfg.Offers {
			offers[i] = tmproto.Offer{
				PackageID:    pkgID,
				DealID:       o.DealID,
				Brand:        o.Brand,
				Price:        o.Price,
				Summary:      o.Summary,
				ManifestType: o.ManifestType,
				Macros:       o.Macros,
			}
		}
		return offers
	}
	return []tmproto.Offer{{
		PackageID:    pkgID,
		Brand:        cfg.Brand,
		Price:        cfg.Price,
		Summary:      cfg.Summary,
		ManifestType: cfg.ManifestType,
		Macros:       cfg.Macros,
	}}
}
