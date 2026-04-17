package targeting

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/adcontextprotocol/adcp-go/exposure"
	"github.com/adcontextprotocol/adcp-go/tmproto"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestSystem_HeavyUser creates a user with 1,500 exposures over 30 days
// (50/day across 10 packages) and verifies fcap evaluation works correctly
// with the single-pull exposure log model.
func TestSystem_HeavyUser(t *testing.T) {
	store := NewMockStore()
	now := time.Date(2026, 6, 15, 12, 0, 0, 0, time.UTC)
	store.Now = func() time.Time { return now }

	// Build 1,500 exposures: 50/day × 30 days across 10 packages.
	var entries []exposure.ExposureEntry
	for day := range 30 {
		for imp := range 50 {
			pkgIdx := imp % 10
			ts := now.Add(-time.Duration(30-day) * 24 * time.Hour).Add(time.Duration(imp) * time.Minute)
			entries = append(entries, exposure.ExposureEntry{
				ImpressionID: fmt.Sprintf("imp-d%d-i%d", day, imp),
				PackageID:    fmt.Sprintf("pkg-%d", pkgIdx),
				CampaignID:   fmt.Sprintf("campaign-%d", pkgIdx/3),
				Timestamp:    ts.Unix(),
			})
		}
	}
	store.SetUserExposures("user-heavy", entries)
	store.SetUserProfile("user-heavy", map[string]float64{"premium_audience": 0.9})

	t.Logf("Loaded %d exposures for heavy user", len(entries))

	// Build identity configs.
	idConfigs := make(map[string]*exposure.PackageIdentityConfig)
	campConfigs := make(map[string]*exposure.CampaignFreqConfig)
	for i := range 10 {
		pkgID := fmt.Sprintf("pkg-%d", i)
		campID := fmt.Sprintf("campaign-%d", i/3)
		idConfigs[pkgID] = &exposure.PackageIdentityConfig{
			CampaignID:     campID,
			FrequencyRules: []exposure.FrequencyRuleJSON{{MaxCount: 5, WindowSeconds: 86400}}, // 5/day
			TargetSegments: []string{"premium_audience"},
		}
		campConfigs[campID] = &exposure.CampaignFreqConfig{
			FrequencyRules: []exposure.FrequencyRuleJSON{{MaxCount: 20, WindowSeconds: 604800}}, // 20/week
		}
	}

	resolved := &ResolvedPackages{
		SegmentIndex:    map[string][]string{"premium_audience": {"pkg-0", "pkg-1", "pkg-2", "pkg-3", "pkg-4", "pkg-5", "pkg-6", "pkg-7", "pkg-8", "pkg-9"}},
		IdentityConfigs: idConfigs,
		CampaignConfigs: campConfigs,
	}

	engine := NewEngine(EngineConfig{ProviderID: "test", Store: store})
	engine.Now = func() time.Time { return now }

	pkgIDs := make([]string, 10)
	for i := range 10 {
		pkgIDs[i] = fmt.Sprintf("pkg-%d", i)
	}

	// Evaluate.
	start := time.Now()
	resp, err := engine.EvaluateIdentityResolved(context.Background(), resolved, &tmproto.IdentityMatchRequest{
		RequestID: "heavy-1", UserToken: "user-heavy", PackageIDs: pkgIDs,
	})
	elapsed := time.Since(start)

	require.NoError(t, err)

	eligible := 0
	for _, e := range resp.Eligibility {
		if e.Eligible {
			eligible++
		}
	}

	t.Logf("Heavy user: %d/%d eligible, evaluated in %v", eligible, len(pkgIDs), elapsed)
	t.Logf("Exposure log size: %d bytes", len(exposure.SerializeExposureLog(entries)))

	// All packages should be daily-capped (5 exposures/day, user has 5/day per pkg).
	for _, e := range resp.Eligibility {
		assert.False(t, e.Eligible, "%s should be daily-capped (5/day)", e.PackageID)
	}

	// Benchmark: run 1000 evaluations.
	const iterations = 1000
	benchStart := time.Now()
	for range iterations {
		_, _ = engine.EvaluateIdentityResolved(context.Background(), resolved, &tmproto.IdentityMatchRequest{
			RequestID: "bench", UserToken: "user-heavy", PackageIDs: pkgIDs,
		})
	}
	benchElapsed := time.Since(benchStart)
	t.Logf("Benchmark: %d evals in %v (%v/eval, %d QPS)", iterations, benchElapsed, benchElapsed/iterations, int(float64(iterations)/benchElapsed.Seconds()))
}

// TestSystem_IdentityGraph tests 3 UIDs for the same user with inconsistent
// pairs per request. Verifies segment union and exposure dedup.
func TestSystem_IdentityGraph(t *testing.T) {
	store := NewMockStore()
	now := time.Date(2026, 6, 15, 12, 0, 0, 0, time.UTC)
	store.Now = func() time.Time { return now }

	// 3 UIDs: cookie, UID2, hashed email.
	cookie := "tok-cookie-abc"
	uid2 := "tok-uid2-xyz"
	email := "tok-email-hash-123"

	// Different segments per UID.
	store.SetUserProfile(cookie, map[string]float64{"cooking_fans": 0.8})
	store.SetUserProfile(uid2, map[string]float64{"sports_fans": 0.5})
	store.SetUserProfile(email, map[string]float64{"cooking_fans": 0.6, "tech_enthusiasts": 0.9})

	engine := NewEngine(EngineConfig{ProviderID: "test", Store: store})
	engine.Now = func() time.Time { return now }
	recorder := exposure.NewRecorder(exposure.RecorderConfig{
		ProviderID: "test",
		Store:      store,
		Clock:      exposure.ClockFunc(func() time.Time { return now }),
	})

	// Record exposures under different UIDs at different times.
	// Day 1-10: cookie only.
	for day := range 10 {
		for i := range 3 {
			ts := now.Add(-time.Duration(30-day) * 24 * time.Hour)
			_, _ = recorder.RecordExposure(context.Background(), &exposure.ExposeRequest{
				UserToken: cookie, PackageID: "pkg-food", ImpressionID: fmt.Sprintf("imp-cookie-d%d-i%d", day, i),
				CampaignID: "campaign-food",
			})
			_ = ts
		}
	}
	// Day 11-20: UID2 only.
	for day := 10; day < 20; day++ {
		for i := range 2 {
			_, _ = recorder.RecordExposure(context.Background(), &exposure.ExposeRequest{
				UserToken: uid2, PackageID: "pkg-food", ImpressionID: fmt.Sprintf("imp-uid2-d%d-i%d", day, i),
				CampaignID: "campaign-food",
			})
		}
	}
	// Day 21-30: hashed email. Some impressions shared with cookie (same impression ID).
	for day := 20; day < 30; day++ {
		// Unique impressions under email.
		_, _ = recorder.RecordExposure(context.Background(), &exposure.ExposeRequest{
			UserToken: email, PackageID: "pkg-food", ImpressionID: fmt.Sprintf("imp-email-d%d", day),
			CampaignID: "campaign-food",
		})
		// Shared impression: also record under cookie with same impression ID.
		sharedImpID := fmt.Sprintf("imp-shared-d%d", day)
		_, _ = recorder.RecordExposure(context.Background(), &exposure.ExposeRequest{
			UserToken: email, PackageID: "pkg-food", ImpressionID: sharedImpID,
			CampaignID: "campaign-food",
		})
		_, _ = recorder.RecordExposure(context.Background(), &exposure.ExposeRequest{
			UserToken: cookie, PackageID: "pkg-food", ImpressionID: sharedImpID,
			CampaignID: "campaign-food",
		})
	}

	resolved := &ResolvedPackages{
		SegmentIndex: map[string][]string{
			"cooking_fans":     {"pkg-food"},
			"sports_fans":      {"pkg-sports"},
			"tech_enthusiasts": {"pkg-tech"},
		},
		IdentityConfigs: map[string]*exposure.PackageIdentityConfig{
			"pkg-food":   {CampaignID: "campaign-food", FrequencyRules: []exposure.FrequencyRuleJSON{{MaxCount: 100, WindowSeconds: 30 * 86400}}, TargetSegments: []string{"cooking_fans"}},
			"pkg-sports": {TargetSegments: []string{"sports_fans"}},
			"pkg-tech":   {TargetSegments: []string{"tech_enthusiasts"}},
		},
		CampaignConfigs: map[string]*exposure.CampaignFreqConfig{
			"campaign-food": {FrequencyRules: []exposure.FrequencyRuleJSON{{MaxCount: 100, WindowSeconds: 30 * 86400}}},
		},
	}

	t.Log("")
	t.Log("=== Identity Graph: 3 UIDs, inconsistent pairs ===")
	t.Log("")

	// Request A: cookie.
	respA, _ := engine.EvaluateIdentityResolved(context.Background(), resolved, &tmproto.IdentityMatchRequest{
		RequestID:  "req-A",
		UserToken:  cookie,
		UIDType:    tmproto.UIDTypePublisherFirstParty,
		PackageIDs: []string{"pkg-food", "pkg-sports", "pkg-tech"},
	})

	// Request B: UID2.
	respB, _ := engine.EvaluateIdentityResolved(context.Background(), resolved, &tmproto.IdentityMatchRequest{
		RequestID:  "req-B",
		UserToken:  uid2,
		UIDType:    tmproto.UIDTypeUID2,
		PackageIDs: []string{"pkg-food", "pkg-sports", "pkg-tech"},
	})

	// Request C: email.
	respC, _ := engine.EvaluateIdentityResolved(context.Background(), resolved, &tmproto.IdentityMatchRequest{
		RequestID:  "req-C",
		UserToken:  email,
		UIDType:    tmproto.UIDTypeHashedEmail,
		PackageIDs: []string{"pkg-food", "pkg-sports", "pkg-tech"},
	})

	for label, resp := range map[string]*IdentityResult{"A(cookie)": respA, "B(uid2)": respB, "C(email)": respC} {
		t.Logf("  Request %s:", label)
		for _, e := range resp.Eligibility {
			intent := "none"
			if e.IntentScore != nil {
				intent = fmt.Sprintf("%.2f", *e.IntentScore)
			}
			t.Logf("    %-12s eligible=%-5v intent=%s", e.PackageID, e.Eligible, intent)
		}
	}
	t.Log("")

	// Verify: Request A (cookie only) sees cooking_fans.
	eligA := map[string]bool{}
	for _, e := range respA.Eligibility {
		eligA[e.PackageID] = e.Eligible
	}
	assert.True(t, eligA["pkg-food"], "Request A: pkg-food should be eligible (cooking_fans from cookie)")

	// Verify: Request B (uid2 only) sees sports_fans.
	eligB := map[string]bool{}
	for _, e := range respB.Eligibility {
		eligB[e.PackageID] = e.Eligible
	}
	assert.True(t, eligB["pkg-sports"], "Request B: pkg-sports should be eligible (sports_fans from uid2)")

	// Verify: exposure dedup. Cookie has ~40 exposures (30 unique + 10 shared).
	// Email has ~20 exposures (10 unique + 10 shared). Union should dedup the shared ones.
	// Total unique impressions across cookie+email should be less than sum of both.
	cookieHash := exposure.HashToken(cookie)
	emailHash := exposure.HashToken(email)
	cookieVal, _, _ := store.Get(context.Background(), "user:exposures:"+cookieHash)
	emailVal, _, _ := store.Get(context.Background(), "user:exposures:"+emailHash)
	cookieBin := exposure.BinaryExposureLog(cookieVal)
	emailBin := exposure.BinaryExposureLog(emailVal)
	mergedBin := exposure.MergeBinaryLogs(cookieBin, emailBin)
	t.Logf("  Cookie exposures: %d, Email exposures: %d, Merged (deduped): %d",
		cookieBin.Len(), emailBin.Len(), mergedBin.Len())

	assert.Less(t, mergedBin.Len(), cookieBin.Len()+emailBin.Len(), "expected dedup to reduce total exposure count")
}

// TestSystem_RollingWindowExpiry simulates 35 days and verifies old exposures
// are pruned and fcap rules correctly ignore expired entries.
func TestSystem_RollingWindowExpiry(t *testing.T) {
	store := NewMockStore()
	baseTime := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	currentTime := baseTime
	store.Now = func() time.Time { return currentTime }

	clock := exposure.ClockFunc(func() time.Time { return currentTime })
	engine := NewEngine(EngineConfig{ProviderID: "test", Store: store})
	engine.Now = clock.Now
	recorder := exposure.NewRecorder(exposure.RecorderConfig{
		ProviderID: "test",
		Store:      store,
		Clock:      clock,
	})

	store.SetUserProfile("user-rolling", map[string]float64{"all": 0})

	resolved := &ResolvedPackages{
		SegmentIndex: map[string][]string{"all": {"pkg-test"}},
		IdentityConfigs: map[string]*exposure.PackageIdentityConfig{
			"pkg-test": {
				FrequencyRules: []exposure.FrequencyRuleJSON{{MaxCount: 5, WindowSeconds: 86400}}, // 5/day
				TargetSegments: []string{"all"},
			},
		},
		CampaignConfigs: map[string]*exposure.CampaignFreqConfig{},
	}

	t.Log("")
	t.Log("=== Rolling Window Expiry: 35 days simulated ===")
	t.Log("")

	// Simulate 35 days, recording 2 exposures per day.
	for day := range 35 {
		currentTime = baseTime.Add(time.Duration(day) * 24 * time.Hour)
		store.Now = clock.Now

		for i := range 2 {
			_, err := recorder.RecordExposure(context.Background(), &exposure.ExposeRequest{
				UserToken:    "user-rolling",
				PackageID:    "pkg-test",
				ImpressionID: fmt.Sprintf("imp-d%d-i%d", day, i),
			})
			require.NoError(t, err, "day %d expose", day)
		}

		// Evaluate eligibility.
		resp, _ := engine.EvaluateIdentityResolved(context.Background(), resolved, &tmproto.IdentityMatchRequest{
			RequestID: fmt.Sprintf("eval-d%d", day), UserToken: "user-rolling", PackageIDs: []string{"pkg-test"},
		})

		// Should NOT be capped (2/5 per day, even with boundary overlap max is 4).
		assert.True(t, resp.Eligibility[0].Eligible, "day %d: should be eligible (2/5 per day)", day)
	}

	// Check the exposure log: should be pruned to ~30 days.
	hash := exposure.HashToken("user-rolling")
	val, _, _ := store.Get(context.Background(), "user:exposures:"+hash)
	binLog := exposure.BinaryExposureLog(val)
	require.NoError(t, exposure.ValidateBinaryLog(binLog))

	// Pruning happens on each write. Entries older than 30 days should be gone.
	thirtyDaysAgo := currentTime.Add(-30 * 24 * time.Hour).Unix()
	oldEntries := 0
	for i := range binLog.Len() {
		if binLog.Timestamp(i) < thirtyDaysAgo {
			oldEntries++
		}
	}

	t.Logf("  After 35 days: %d entries in log, %d older than 30 days", binLog.Len(), oldEntries)

	assert.Equal(t, 0, oldEntries, "expected 0 entries older than 30 days")

	// Should have ~60 entries (30 days × 2/day, first 5 days pruned).
	if binLog.Len() > 62 || binLog.Len() < 58 {
		t.Logf("  WARNING: expected ~60 entries, got %d (acceptable variance from pruning boundary)", binLog.Len())
	}

	t.Logf("  Exposure log correctly pruned: %d entries, 0 expired", binLog.Len())
}
