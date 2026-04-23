// Package targeting provides a data-driven targeting engine for TMP agents.
//
// Capabilities activate based on what data is present. Push property bitmaps
// and property targeting works. Push audience membership and audience targeting
// works. No data for a dimension means that dimension is a no-op.
//
// The engine evaluates both context match and identity match requests,
// replacing the need for separate agent implementations.
package targeting

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
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
		topics, err := e.store.SetMembers(ctx, keyPrefixTopicsArtifact+artifact)
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

	// 1. Build MGet keys: exposures for each UID.
	expKeys := make([]string, len(identities))
	for i, uid := range identities {
		expKeys[i] = keyPrefixUserExposures + HashToken(uid.UserToken)
	}

	// 2. Single MGet for exposures — 1 round-trip.
	expValues, err := e.store.MGet(ctx, expKeys...)
	if err != nil {
		e.metrics.StoreError("load_user_data", err)
		expValues = make([]string, len(expKeys))
	}

	// 3. Parse exposure logs (binary, zero-copy).
	firstLogs := make([]BinaryExposureLog, 0, len(identities))
	for i := range identities {
		exposureData := expValues[i]
		binLog := BinaryExposureLog(exposureData)
		if len(exposureData) > 0 {
			if err := ValidateBinaryLog(binLog); err != nil {
				e.metrics.StoreError("corrupt_exposure_log", err)
				binLog = nil
			}
		}
		firstLogs = append(firstLogs, binLog)
	}

	// 4. Pre-hash user tokens for per-package audience lookups.
	userHashes := make([]string, len(identities))
	for i, uid := range identities {
		userHashes[i] = HashToken(uid.UserToken)
	}

	// 5. Batch-load audience membership for all audience-gated packages — 1 round-trip.
	// audienceHit[pkgID] = true if at least one identity is in the package's audience.
	audienceHit := make(map[string]bool)
	var audiencePkgIDs []string
	var audienceKeys []string
	for _, pkgID := range req.PackageIDs {
		idCfg := resolved.IdentityConfigs[pkgID]
		if idCfg != nil && idCfg.Audience {
			audiencePkgIDs = append(audiencePkgIDs, pkgID)
			audienceKeys = append(audienceKeys, keyPrefixPackageAudience+HashToken(pkgID))
		}
	}
	if len(audienceKeys) > 0 {
		batchVals, err := e.store.HMGetBatch(ctx, audienceKeys, userHashes)
		if err != nil {
			e.metrics.StoreError(StageAudience, err)
			// On error treat all audience packages as ineligible.
			for _, pkgID := range audiencePkgIDs {
				audienceHit[pkgID] = false
			}
		} else {
			for i, pkgID := range audiencePkgIDs {
				for _, v := range batchVals[i] {
					if v != "" {
						audienceHit[pkgID] = true
						break
					}
				}
			}
		}
	}

	// 6. Evaluate each requested package using binary lazy dedup.
	var eligibility []tmproto.PackageEligibility
	for _, pkgID := range req.PackageIDs {
		idCfg := resolved.IdentityConfigs[pkgID]
		eligible := true

		// Audience gating: result from the batch lookup above.
		if idCfg != nil && idCfg.Audience {
			if !audienceHit[pkgID] {
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

	e.metrics.Latency("identity_eval", time.Since(evalStart))

	return &IdentityResult{
		RequestID:   req.RequestID,
		Eligibility: eligibility,
	}, nil
}

// SetPackageUser adds a user to a package's audience with the given intent score.
func (e *Engine) SetPackageUser(ctx context.Context, packageID, userToken string, intent float64) error {
	pkgKey := keyPrefixPackageAudience + HashToken(packageID)
	return e.store.HSet(ctx, pkgKey, HashToken(userToken), strconv.FormatFloat(intent, 'f', -1, 64))
}

// AddPackageUsers adds multiple users to a package's audience in a single batch.
// The users map is keyed by user token.
func (e *Engine) AddPackageUsers(ctx context.Context, packageID string, users map[string]float64) error {
	pkgKey := keyPrefixPackageAudience + HashToken(packageID)
	fields := make(map[string]string, len(users))
	for userToken, intent := range users {
		fields[HashToken(userToken)] = strconv.FormatFloat(intent, 'f', -1, 64)
	}
	return e.store.HMSet(ctx, pkgKey, fields)
}

// RemovePackageUsers removes specific users from a package's audience.
func (e *Engine) RemovePackageUsers(ctx context.Context, packageID string, userTokens []string) error {
	pkgKey := keyPrefixPackageAudience + HashToken(packageID)
	fields := make([]string, len(userTokens))
	for i, token := range userTokens {
		fields[i] = HashToken(token)
	}
	return e.store.HDel(ctx, pkgKey, fields...)
}

// DeletePackageUsers removes a package's entire audience.
func (e *Engine) DeletePackageUsers(ctx context.Context, packageID string) error {
	return e.store.Del(ctx, keyPrefixPackageAudience+HashToken(packageID))
}

// MSetPackageUsers adds users to multiple packages in one call.
// The packages map is keyed by package ID; each value maps user token to intent score.
func (e *Engine) MSetPackageUsers(ctx context.Context, packages map[string]map[string]float64) error {
	for packageID, users := range packages {
		pkgKey := keyPrefixPackageAudience + HashToken(packageID)
		fields := make(map[string]string, len(users))
		for userToken, intent := range users {
			fields[HashToken(userToken)] = strconv.FormatFloat(intent, 'f', -1, 64)
		}
		if err := e.store.HMSet(ctx, pkgKey, fields); err != nil {
			return err
		}
	}
	return nil
}

// MDeletePackageUsers removes the entire audience for multiple packages in one call.
func (e *Engine) MDeletePackageUsers(ctx context.Context, packageIDs []string) error {
	keys := make([]string, len(packageIDs))
	for i, packageID := range packageIDs {
		keys[i] = keyPrefixPackageAudience + HashToken(packageID)
	}
	return e.store.MDel(ctx, keys...)
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
func (e *Engine) RecordExposure(ctx context.Context, req *ExposeRequest) (*ExposeResponse, error) {
	if err := ValidateExposeRequest(req); err != nil {
		return nil, fmt.Errorf("invalid expose request: %w", err)
	}
	now := e.now()
	identities := resolveExposeIdentities(req)

	// Resolve source ID: use request field, fall back to engine's provider ID.
	sourceID := req.SourceID
	if sourceID == "" {
		sourceID = e.providerID
	}

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
		SourceID:     sourceID,
		Timestamp:    now.Unix(),
	}

	cutoff := now.Add(-30 * 24 * time.Hour).Unix()
	nowMs := float64(now.UnixMilli())
	pruneBeforeMs := float64(now.Add(-30 * 24 * time.Hour).UnixMilli())

	// Pre-compute token hashes (SHA-256, avoid re-hashing per loop).
	hashes := make([]string, len(identities))
	for i, uid := range identities {
		hashes[i] = HashToken(uid.UserToken)
	}

	// Write to each UID's exposure log. Capture the first UID's log for the response.
	var firstLog BinaryExposureLog
	for i, hash := range hashes {
		key := keyPrefixUserExposures + hash

		val, _, err := e.store.Get(ctx, key)
		if err != nil {
			e.metrics.StoreError("read_exposure_log", err)
			continue
		}

		// Prune expired entries and append new one, upgrading v1→v2 if needed.
		existing := BinaryExposureLog(val)
		if len(val) > 0 {
			if err := ValidateBinaryLog(existing); err != nil {
				e.metrics.StoreError("corrupt_exposure_log", err)
				existing = nil
			}
		}
		newEntry := EncodeBinaryExposureLog(ExposureLog{entry})

		pruned := newBinaryLog(existing.Len() + 1)
		es := int(existing.EntrySize())
		for j := range existing.Len() {
			if existing.Timestamp(j) >= cutoff {
				off := existing.entryOffset(j)
				if es == binaryEntrySize {
					pruned = append(pruned, existing[off:off+binaryEntrySize]...)
				} else {
					// Upgrade v1 entry: copy 32 bytes + 8 zero bytes for source hash.
					pruned = append(pruned, existing[off:off+binaryEntrySize1]...)
					pruned = append(pruned, 0, 0, 0, 0, 0, 0, 0, 0)
				}
			}
		}
		// Append the new v2 entry's payload (skip its header).
		pruned = append(pruned, newEntry[binaryHeaderSize:]...)
		pruned = TruncateBinaryLog(pruned, maxExposureEntries)

		if err := e.store.Set(ctx, key, string(pruned), 30*24*time.Hour); err != nil {
			e.metrics.StoreError("write_exposure_log", err)
		}
		if i == 0 {
			firstLog = BinaryExposureLog(pruned)
		}

		// Package frequency sorted set.
		member := sourceID + ":" + impressionID
		pkgFreqKey := fmt.Sprintf("freq:pkg:%s:%s", req.PackageID, hash)
		if err := e.store.ZAdd(ctx, pkgFreqKey, nowMs, member); err != nil {
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
			if err := e.store.ZAdd(ctx, campFreqKey, nowMs, member); err != nil {
				e.metrics.StoreError("zadd_campaign_freq", err)
			} else {
				if err := e.store.ZRemRangeByScore(ctx, campFreqKey, 0, pruneBeforeMs); err != nil {
					e.metrics.StoreError("zremrangebyscore_campaign_freq", err)
				}
				_ = e.store.ZExpire(ctx, campFreqKey, 30*24*time.Hour)
			}
		}
	}

	e.metrics.ExposureRecorded(req.PackageID)

	// Compute campaign count from the first UID's log (already in memory, no re-read).
	resp := &ExposeResponse{PackageID: req.PackageID}
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
			remaining := max(shortestRule.MaxCount-count, 0)
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
			blocked, err := e.store.SetIsMember(ctx, keyPrefixURLBlocklist+pkgID, urlHash)
			if err != nil {
				return false, err
			}
			if blocked {
				return true, nil
			}
		}

		if cfg.URLAllowlist {
			allowKey := keyPrefixURLAllowlist + pkgID
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
		intersection, err := e.store.SetIntersect(ctx, keyPrefixTopicsPackage+pkgID, keyPrefixTopicsArtifact+artifact)
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
