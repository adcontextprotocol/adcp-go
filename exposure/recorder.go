package exposure

import (
	"context"
	"fmt"
	"time"
)

// Metrics receives instrumentation callbacks from ExposureRecorder.
// The noop default adds zero overhead.
type Metrics interface {
	ExposureRecorded(packageID string)
	StoreError(operation string, err error)
}

// Clock returns the current wall-clock time. A Clock can be shared between
// a recorder, a targeting engine, and a store in tests so a single time
// source drives all time-dependent code.
type Clock interface {
	Now() time.Time
}

// ClockFunc adapts a plain func() time.Time to the Clock interface.
type ClockFunc func() time.Time

func (f ClockFunc) Now() time.Time { return f() }

// Store is the Store surface required by RecordExposure. It combines the
// exposure-log reader/writer operations with the sorted-set append used
// for the frequency-cap bookkeeping and the config reader needed to resolve
// campaign IDs.
type Store interface {
	RecorderStore
}

// RecorderConfig holds configuration for Recorder.
type RecorderConfig struct {
	// ProviderID is the source identifier to record on impressions that do
	// not set SourceID explicitly.
	ProviderID string

	// Store persists exposure logs, frequency-cap sorted sets, intent
	// timestamps, and reads package/campaign configs.
	Store Store

	// Metrics receives per-exposure instrumentation callbacks. nil = noop.
	Metrics Metrics

	// Clock is the time source. nil = wall clock.
	Clock Clock
}

// Recorder writes exposure events and frequency-cap data to the store.
//
// The recorder reads package-identity and campaign configuration from the
// same Store that the targeting engine reads. The store schema — keys such
// as user:exposures:<hash>, freq:pkg:*, freq:campaign:*, intent:* — is the
// integration contract shared with any reader (including the targeting
// engine's EvaluateIdentity path).
type Recorder struct {
	providerID string
	store      Store
	metrics    Metrics
	clock      Clock
}

type noopMetrics struct{}

func (noopMetrics) ExposureRecorded(string)  {}
func (noopMetrics) StoreError(string, error) {}

// NewRecorder creates a Recorder.
func NewRecorder(cfg RecorderConfig) *Recorder {
	metrics := cfg.Metrics
	if metrics == nil {
		metrics = noopMetrics{}
	}
	clock := cfg.Clock
	if clock == nil {
		clock = ClockFunc(time.Now)
	}
	return &Recorder{
		providerID: cfg.ProviderID,
		store:      cfg.Store,
		metrics:    metrics,
		clock:      clock,
	}
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
func (r *Recorder) RecordExposure(ctx context.Context, req *ExposeRequest) (*ExposeResponse, error) {
	if err := ValidateExposeRequest(req); err != nil {
		return nil, fmt.Errorf("invalid expose request: %w", err)
	}
	now := r.clock.Now()
	identities := ResolveExposeIdentities(req)

	// Resolve source ID: use request field, fall back to recorder's provider ID.
	sourceID := req.SourceID
	if sourceID == "" {
		sourceID = r.providerID
	}

	// Resolve campaign ID.
	campaignID := req.CampaignID
	if campaignID == "" {
		idCfg, _ := LoadPackageIdentityConfig(ctx, r.store, req.PackageID)
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
	nowStr := fmt.Sprintf("%d", now.Unix())

	hashes := make([]string, len(identities))
	for i, uid := range identities {
		hashes[i] = HashToken(uid.UserToken)
	}

	// Write to each UID's exposure log. Capture the first UID's log for the response.
	var firstLog BinaryExposureLog
	for i, hash := range hashes {
		key := "user:exposures:" + hash

		val, _, err := r.store.Get(ctx, key)
		if err != nil {
			r.metrics.StoreError("read_exposure_log", err)
			continue
		}

		// Prune expired entries and append new one, upgrading v1→v2 if needed.
		existing := BinaryExposureLog(val)
		if len(val) > 0 {
			if err := ValidateBinaryLog(existing); err != nil {
				r.metrics.StoreError("corrupt_exposure_log", err)
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

		if err := r.store.Set(ctx, key, string(pruned), 30*24*time.Hour); err != nil {
			r.metrics.StoreError("write_exposure_log", err)
		}
		if i == 0 {
			firstLog = BinaryExposureLog(pruned)
		}

		// Package frequency sorted set.
		member := sourceID + ":" + impressionID
		pkgFreqKey := fmt.Sprintf("freq:pkg:%s:%s", req.PackageID, hash)
		if err := r.store.ZAdd(ctx, pkgFreqKey, nowMs, member); err != nil {
			r.metrics.StoreError("zadd_pkg_freq", err)
		} else {
			if err := r.store.ZRemRangeByScore(ctx, pkgFreqKey, 0, pruneBeforeMs); err != nil {
				r.metrics.StoreError("zremrangebyscore_pkg_freq", err)
			}
			_ = r.store.ZExpire(ctx, pkgFreqKey, 30*24*time.Hour)
		}

		// Campaign frequency sorted set.
		if campaignID != "" {
			campFreqKey := fmt.Sprintf("freq:campaign:%s:%s", campaignID, hash)
			if err := r.store.ZAdd(ctx, campFreqKey, nowMs, member); err != nil {
				r.metrics.StoreError("zadd_campaign_freq", err)
			} else {
				if err := r.store.ZRemRangeByScore(ctx, campFreqKey, 0, pruneBeforeMs); err != nil {
					r.metrics.StoreError("zremrangebyscore_campaign_freq", err)
				}
				_ = r.store.ZExpire(ctx, campFreqKey, 30*24*time.Hour)
			}
		}

		// Intent timestamp.
		intentKey := fmt.Sprintf("intent:%s:%s", req.PackageID, hash)
		if err := r.store.Set(ctx, intentKey, nowStr, 7*24*time.Hour); err != nil {
			r.metrics.StoreError("set_intent", err)
		}
	}

	r.metrics.ExposureRecorded(req.PackageID)

	// Compute campaign count from the first UID's log (already in memory, no re-read).
	resp := &ExposeResponse{PackageID: req.PackageID}
	if campaignID != "" && firstLog.Len() > 0 {
		campHash := HashString(campaignID)

		campCfg, _ := LoadCampaignFreqConfig(ctx, r.store, campaignID)
		if campCfg != nil && len(campCfg.FrequencyRules) > 0 {
			rules := ToFrequencyRules(campCfg.FrequencyRules)
			shortestRule := rules[0]
			for _, rule := range rules[1:] {
				if rule.Window < shortestRule.Window {
					shortestRule = rule
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
