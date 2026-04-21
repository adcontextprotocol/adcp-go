package valkeystore

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/adcontextprotocol/adcp-go/targeting"
	"github.com/adcontextprotocol/adcp-go/tmproto"
	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// setupIntegration creates an Engine backed by a real Redis (miniredis) Store.
func setupIntegration(t *testing.T) (*targeting.Engine, *Store, *miniredis.Miniredis, *targeting.ResolvedPackages) {
	t.Helper()
	mr, err := miniredis.Run()
	require.NoError(t, err)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	store := New(rdb)
	ctx := context.Background()

	// Seed configs via Store.Set (same as production path).
	seedJSON := func(key string, v any) {
		t.Helper()
		data, err := json.Marshal(v)
		require.NoError(t, err)
		require.NoError(t, store.Set(ctx, key, string(data), 0))
	}

	pkgAlpha := targeting.PackageIdentityConfig{
		AdvertiserID:   "adv-x",
		CampaignID:     "campaign-x",
		CreativeID:     "creative-alpha",
		FrequencyRules: []targeting.FrequencyRuleJSON{{MaxCount: 3, WindowSeconds: 86400}},
		TargetSegments: []string{"sports"},
	}
	pkgBeta := targeting.PackageIdentityConfig{
		AdvertiserID:   "adv-x",
		CampaignID:     "campaign-x",
		CreativeID:     "creative-beta",
		FrequencyRules: []targeting.FrequencyRuleJSON{{MaxCount: 5, WindowSeconds: 86400}},
	}
	campaignX := targeting.CampaignFreqConfig{
		FrequencyRules: []targeting.FrequencyRuleJSON{{MaxCount: 5, WindowSeconds: 604800}},
	}
	seedJSON("config:pkg:pkg-alpha", pkgAlpha)
	seedJSON("config:pkg:pkg-beta", pkgBeta)
	seedJSON("config:campaign:campaign-x", campaignX)

	// Seed user profile with sports segment.
	tokenHash := targeting.HashToken("user-valkey")
	profileJSON, err := json.Marshal(targeting.UserProfile{Segments: map[string]float64{"sports": 1.0}})
	require.NoError(t, err)
	require.NoError(t, store.Set(ctx, "user:profile:"+tokenHash, string(profileJSON), 0))

	engine := targeting.NewEngine(targeting.EngineConfig{
		ProviderID: "test-valkey",
		Store:      store,
		Packages: []targeting.PackageConfig{
			{PackageID: "pkg-alpha"},
			{PackageID: "pkg-beta"},
		},
	})

	resolved := &targeting.ResolvedPackages{
		SegmentIndex: map[string][]string{"sports": {"pkg-alpha"}},
		IdentityConfigs: map[string]*targeting.PackageIdentityConfig{
			"pkg-alpha": &pkgAlpha,
			"pkg-beta":  &pkgBeta,
		},
		CampaignConfigs: map[string]*targeting.CampaignFreqConfig{
			"campaign-x": &campaignX,
		},
	}

	return engine, store, mr, resolved
}

// seedExposures writes the given exposure entries into the user's binary log.
func seedExposures(t *testing.T, store *Store, userToken string, entries []targeting.ExposureEntry) {
	t.Helper()
	hash := targeting.HashToken(userToken)
	bin := targeting.EncodeBinaryExposureLog(entries)
	require.NoError(t, store.Set(context.Background(), "user:exposures:"+hash, string(bin), 0))
}

func creativeEntry(pkgID, impressionID string, ts time.Time) targeting.ExposureEntry {
	cfg := map[string]struct {
		advertiser string
		campaign   string
		creative   string
	}{
		"pkg-alpha": {"adv-x", "campaign-x", "creative-alpha"},
		"pkg-beta":  {"adv-x", "campaign-x", "creative-beta"},
	}
	c := cfg[pkgID]
	return targeting.ExposureEntry{
		ImpressionID: impressionID,
		AdvertiserID: c.advertiser,
		CampaignID:   c.campaign,
		CreativeID:   c.creative,
		Timestamp:    ts.Unix(),
	}
}

func TestValkeyIntegration_CreativeFrequencyCap(t *testing.T) {
	engine, store, mr, resolved := setupIntegration(t)
	defer mr.Close()
	ctx := context.Background()
	now := time.Now()

	// 3 exposures hit the pkg-alpha creative cap (3/24h).
	var entries []targeting.ExposureEntry
	for i := range 3 {
		entries = append(entries, creativeEntry("pkg-alpha", fmt.Sprintf("imp-valkey-%d", i), now))
	}
	seedExposures(t, store, "user-valkey", entries)

	resp, err := engine.EvaluateIdentityResolved(ctx, resolved, &tmproto.IdentityMatchRequest{
		RequestID: "valkey-pkg-cap", Identities: []tmproto.IdentityToken{{UserToken: "user-valkey"}},
		PackageIDs: []string{"pkg-alpha"},
	})
	require.NoError(t, err)
	assert.False(t, resp.Eligibility[0].Eligible, "pkg-alpha should be creative-capped (3/3)")
}

func TestValkeyIntegration_CampaignFrequencyCap(t *testing.T) {
	engine, store, mr, resolved := setupIntegration(t)
	defer mr.Close()
	ctx := context.Background()
	now := time.Now()

	// 3 on pkg-alpha + 2 on pkg-beta = 5 total (campaign cap = 5/7d).
	var entries []targeting.ExposureEntry
	for i := range 3 {
		entries = append(entries, creativeEntry("pkg-alpha", fmt.Sprintf("imp-v-camp-a-%d", i), now))
	}
	for i := range 2 {
		entries = append(entries, creativeEntry("pkg-beta", fmt.Sprintf("imp-v-camp-b-%d", i), now))
	}
	seedExposures(t, store, "user-valkey", entries)

	resp, err := engine.EvaluateIdentityResolved(ctx, resolved, &tmproto.IdentityMatchRequest{
		RequestID: "valkey-camp-cap", Identities: []tmproto.IdentityToken{{UserToken: "user-valkey"}},
		PackageIDs: []string{"pkg-alpha", "pkg-beta"},
	})
	require.NoError(t, err)
	for _, e := range resp.Eligibility {
		assert.False(t, e.Eligible, "%s should be campaign-capped", e.PackageID)
	}
}

func TestValkeyIntegration_SlidingWindowExpiry(t *testing.T) {
	engine, store, mr, resolved := setupIntegration(t)
	defer mr.Close()
	ctx := context.Background()
	now := time.Now()

	// 3 exposures hits creative cap.
	var entries []targeting.ExposureEntry
	for i := range 3 {
		entries = append(entries, creativeEntry("pkg-alpha", fmt.Sprintf("imp-v-window-%d", i), now))
	}
	seedExposures(t, store, "user-valkey", entries)

	resp, _ := engine.EvaluateIdentityResolved(ctx, resolved, &tmproto.IdentityMatchRequest{
		RequestID: "v-before", Identities: []tmproto.IdentityToken{{UserToken: "user-valkey"}},
		PackageIDs: []string{"pkg-alpha"},
	})
	assert.False(t, resp.Eligibility[0].Eligible, "should be capped")

	// Advance engine time past the 24h window.
	engine.Now = func() time.Time { return now.Add(25 * time.Hour) }

	resp, _ = engine.EvaluateIdentityResolved(ctx, resolved, &tmproto.IdentityMatchRequest{
		RequestID: "v-after", Identities: []tmproto.IdentityToken{{UserToken: "user-valkey"}},
		PackageIDs: []string{"pkg-alpha"},
	})
	assert.True(t, resp.Eligibility[0].Eligible, "should be eligible after window expires")
}

func TestValkeyIntegration_IntentScore(t *testing.T) {
	engine, store, mr, resolved := setupIntegration(t)
	defer mr.Close()
	ctx := context.Background()
	now := time.Now()

	seedExposures(t, store, "user-valkey", []targeting.ExposureEntry{
		creativeEntry("pkg-alpha", "imp-v-intent", now),
	})

	resp, err := engine.EvaluateIdentityResolved(ctx, resolved, &tmproto.IdentityMatchRequest{
		RequestID: "v-intent", Identities: []tmproto.IdentityToken{{UserToken: "user-valkey"}},
		PackageIDs: []string{"pkg-alpha"},
	})
	require.NoError(t, err)
	require.NotNil(t, resp.Eligibility[0].IntentScore, "expected intent score to be set")
	assert.GreaterOrEqual(t, *resp.Eligibility[0].IntentScore, 0.99, "expected high intent score after recent exposure")
}
