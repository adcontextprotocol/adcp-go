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
		CampaignID:     "campaign-x",
		FrequencyRules: []targeting.FrequencyRuleJSON{{MaxCount: 3, WindowSeconds: 86400}},
		Audience:       true,
	}
	pkgBeta := targeting.PackageIdentityConfig{
		CampaignID:     "campaign-x",
		FrequencyRules: []targeting.FrequencyRuleJSON{{MaxCount: 5, WindowSeconds: 86400}},
	}
	campaignX := targeting.CampaignFreqConfig{
		FrequencyRules: []targeting.FrequencyRuleJSON{{MaxCount: 5, WindowSeconds: 604800}},
	}
	seedJSON("config:pkg:pkg-alpha", pkgAlpha)
	seedJSON("config:pkg:pkg-beta", pkgBeta)
	seedJSON("config:campaign:campaign-x", campaignX)

	engine := targeting.NewEngine(targeting.EngineConfig{
		ProviderID: "test-valkey",
		Store:      store,
		Packages: []targeting.PackageConfig{
			{PackageID: "pkg-alpha"},
			{PackageID: "pkg-beta"},
		},
	})

	// Seed user into pkg-alpha's audience.
	require.NoError(t, engine.AddPackageUsers(ctx, "pkg-alpha", map[string]float64{"user-valkey": 1.0}))

	resolved := &targeting.ResolvedPackages{
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

func TestValkeyIntegration_PackageFrequencyCap(t *testing.T) {
	engine, _, mr, resolved := setupIntegration(t)
	defer mr.Close()
	ctx := context.Background()

	// Record 3 exposures (package cap = 3/24h).
	for i := range 3 {
		_, err := engine.RecordExposure(ctx, &targeting.ExposeRequest{
			UserToken: "user-valkey", PackageID: "pkg-alpha",
			ImpressionID: fmt.Sprintf("imp-valkey-%d", i),
		})
		require.NoError(t, err)
	}

	resp, err := engine.EvaluateIdentityResolved(ctx, resolved, &tmproto.IdentityMatchRequest{
		RequestID: "valkey-pkg-cap", Identities: []tmproto.IdentityToken{{UserToken: "user-valkey"}},
		PackageIDs: []string{"pkg-alpha"},
	})
	require.NoError(t, err)
	assert.False(t, resp.Eligibility[0].Eligible, "pkg-alpha should be package-capped (3/3)")
}

func TestValkeyIntegration_CampaignFrequencyCap(t *testing.T) {
	engine, _, mr, resolved := setupIntegration(t)
	defer mr.Close()
	ctx := context.Background()

	// 3 on pkg-alpha + 2 on pkg-beta = 5 total (campaign cap = 5/7d).
	for i := range 3 {
		_, _ = engine.RecordExposure(ctx, &targeting.ExposeRequest{
			UserToken: "user-valkey", PackageID: "pkg-alpha",
			ImpressionID: fmt.Sprintf("imp-v-camp-a-%d", i),
		})
	}
	for i := range 2 {
		_, _ = engine.RecordExposure(ctx, &targeting.ExposeRequest{
			UserToken: "user-valkey", PackageID: "pkg-beta",
			ImpressionID: fmt.Sprintf("imp-v-camp-b-%d", i),
		})
	}

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
	engine, _, mr, resolved := setupIntegration(t)
	defer mr.Close()
	ctx := context.Background()

	// 3 exposures hits package cap.
	for i := range 3 {
		_, _ = engine.RecordExposure(ctx, &targeting.ExposeRequest{
			UserToken: "user-valkey", PackageID: "pkg-alpha",
			ImpressionID: fmt.Sprintf("imp-v-window-%d", i),
		})
	}

	resp, _ := engine.EvaluateIdentityResolved(ctx, resolved, &tmproto.IdentityMatchRequest{
		RequestID: "v-before", Identities: []tmproto.IdentityToken{{UserToken: "user-valkey"}},
		PackageIDs: []string{"pkg-alpha"},
	})
	assert.False(t, resp.Eligibility[0].Eligible, "should be capped")

	// Fast-forward miniredis past the 24h window.
	mr.FastForward(25 * time.Hour)
	// Also advance engine time so the cutoff is correct.
	engine.Now = func() time.Time { return time.Now().Add(25 * time.Hour) }

	resp, _ = engine.EvaluateIdentityResolved(ctx, resolved, &tmproto.IdentityMatchRequest{
		RequestID: "v-after", Identities: []tmproto.IdentityToken{{UserToken: "user-valkey"}},
		PackageIDs: []string{"pkg-alpha"},
	})
	assert.True(t, resp.Eligibility[0].Eligible, "should be eligible after window expires")
}

func TestValkeyIntegration_IntentScore(t *testing.T) {
	engine, _, mr, resolved := setupIntegration(t)
	defer mr.Close()
	ctx := context.Background()

	_, _ = engine.RecordExposure(ctx, &targeting.ExposeRequest{
		UserToken: "user-valkey", PackageID: "pkg-alpha",
	})

	resp, err := engine.EvaluateIdentityResolved(ctx, resolved, &tmproto.IdentityMatchRequest{
		RequestID: "v-intent", Identities: []tmproto.IdentityToken{{UserToken: "user-valkey"}},
		PackageIDs: []string{"pkg-alpha"},
	})
	require.NoError(t, err)
	require.NotNil(t, resp.Eligibility[0].IntentScore, "expected intent score to be set")
	assert.GreaterOrEqual(t, *resp.Eligibility[0].IntentScore, 0.99, "expected high intent score after recent exposure")
}

func TestValkeyIntegration_ExposureResponse(t *testing.T) {
	engine, _, mr, _ := setupIntegration(t)
	defer mr.Close()
	ctx := context.Background()

	resp, err := engine.RecordExposure(ctx, &targeting.ExposeRequest{
		UserToken: "user-valkey", PackageID: "pkg-alpha",
	})
	require.NoError(t, err)
	assert.Equal(t, "pkg-alpha", resp.PackageID)
	assert.Equal(t, 1, resp.CampaignCount)
	assert.Equal(t, 4, resp.CampaignRemaining)
}
