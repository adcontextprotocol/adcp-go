package targeting

import (
	"context"
	"fmt"
	"math"
	"testing"
	"time"

	"github.com/adcontextprotocol/adcp-go/tmproto"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func setupContextEngine(t *testing.T) (*Engine, *MockStore) {
	t.Helper()
	store := NewMockStore()
	props := PropertyList{
		Global: NewMapBitmap("1", "2", "3", "4", "5"),
	}
	engine := NewEngine(EngineConfig{
		ProviderID: "test-provider",
		Store:      store,
		Properties: props,
		Packages: []PackageConfig{
			{PackageID: "pkg-1"},
			{PackageID: "pkg-2"},
		},
	})
	return engine, store
}

func setupIdentityEngine(t *testing.T) (*Engine, *MockStore, *ResolvedPackages) {
	t.Helper()
	store := NewMockStore()
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	// Seed identity config in Store (data-driven).
	store.SetPackageIdentityConfig("pkg-display-001", PackageIdentityConfig{
		CampaignID:     "campaign-acme",
		FrequencyRules: []FrequencyRuleJSON{{MaxCount: 3, WindowSeconds: 86400}},
		TargetSegments: []string{"cooking", "home"},
	})
	store.SetPackageIdentityConfig("pkg-display-002", PackageIdentityConfig{
		CampaignID:     "campaign-acme",
		FrequencyRules: []FrequencyRuleJSON{{MaxCount: 5, WindowSeconds: 43200}},
	})
	store.SetPackageIdentityConfig("pkg-multi-rule", PackageIdentityConfig{
		CampaignID: "campaign-acme",
		FrequencyRules: []FrequencyRuleJSON{
			{MaxCount: 2, WindowSeconds: 43200},
			{MaxCount: 5, WindowSeconds: 604800},
		},
	})
	// pkg-no-cap: no identity config = always eligible
	store.SetCampaignFreqConfig("campaign-acme", CampaignFreqConfig{
		FrequencyRules: []FrequencyRuleJSON{{MaxCount: 5, WindowSeconds: 604800}},
	})

	// Build resolved packages for the resolved eval path.
	resolved := &ResolvedPackages{
		SegmentIndex: map[string][]string{
			"cooking": {"pkg-display-001"},
			"home":    {"pkg-display-001"},
		},
		IdentityConfigs: map[string]*PackageIdentityConfig{
			"pkg-display-001": {CampaignID: "campaign-acme", FrequencyRules: []FrequencyRuleJSON{{MaxCount: 3, WindowSeconds: 86400}}, TargetSegments: []string{"cooking", "home"}},
			"pkg-display-002": {CampaignID: "campaign-acme", FrequencyRules: []FrequencyRuleJSON{{MaxCount: 5, WindowSeconds: 43200}}},
			"pkg-multi-rule":  {CampaignID: "campaign-acme", FrequencyRules: []FrequencyRuleJSON{{MaxCount: 2, WindowSeconds: 43200}, {MaxCount: 5, WindowSeconds: 604800}}},
			"pkg-no-cap":      {},
		},
		CampaignConfigs: map[string]*CampaignFreqConfig{
			"campaign-acme": {FrequencyRules: []FrequencyRuleJSON{{MaxCount: 5, WindowSeconds: 604800}}},
		},
	}

	engine := NewEngine(EngineConfig{
		ProviderID: "test-provider",
		Store:      store,
		Packages: []PackageConfig{
			{PackageID: "pkg-display-001"},
			{PackageID: "pkg-display-002"},
			{PackageID: "pkg-multi-rule"},
			{PackageID: "pkg-no-cap"},
		},
	})
	engine.Now = func() time.Time { return now }
	store.Now = func() time.Time { return now }
	return engine, store, resolved
}

// --- Context Tests ---

func TestContext_BitmapPreFilter_Targeted(t *testing.T) {
	engine, _ := setupContextEngine(t)
	resp, err := engine.EvaluateContext(context.Background(), &tmproto.ContextMatchRequest{
		RequestID:   "test-1",
		PropertyRID: "1",
		PackageIDs:  []string{"pkg-1"},
	})
	require.NoError(t, err)
	assert.Len(t, resp.Offers, 1)
}

func TestContext_BitmapPreFilter_NotTargeted(t *testing.T) {
	engine, _ := setupContextEngine(t)
	resp, err := engine.EvaluateContext(context.Background(), &tmproto.ContextMatchRequest{
		RequestID:   "test-2",
		PropertyRID: "999",
		PackageIDs:  []string{"pkg-1"},
	})
	require.NoError(t, err)
	assert.Empty(t, resp.Offers)
}

func TestContext_PropertySuppression(t *testing.T) {
	engine, _ := setupContextEngine(t)
	ctx := context.Background()
	_ = engine.SuppressProperty(ctx, "2", time.Hour)

	resp, err := engine.EvaluateContext(ctx, &tmproto.ContextMatchRequest{
		RequestID:   "test-3",
		PropertyRID: "2",
		PackageIDs:  []string{"pkg-1"},
	})
	require.NoError(t, err)
	assert.Empty(t, resp.Offers, "expected 0 offers for suppressed property")
}

func TestContext_PerPackageTargeting(t *testing.T) {
	store := NewMockStore()
	engine := NewEngine(EngineConfig{
		ProviderID: "test-provider",
		Store:      store,
		Properties: PropertyList{
			Global:    NewMapBitmap("1", "3"),
			ByPackage: map[string]Bitmap{"pkg-scoped": NewMapBitmap("3")},
		},
		Packages: []PackageConfig{
			{PackageID: "pkg-scoped"},
		},
	})

	ctx := context.Background()

	// Property 1 is in global but not in pkg-scoped.
	resp, err := engine.EvaluateContext(ctx, &tmproto.ContextMatchRequest{
		RequestID:   "test-4a",
		PropertyRID: "1",
		PackageIDs:  []string{"pkg-scoped"},
	})
	require.NoError(t, err)
	assert.Empty(t, resp.Offers, "expected 0 offers (property not in package bitmap)")

	// Property 3 is in both global and pkg-scoped.
	resp, err = engine.EvaluateContext(ctx, &tmproto.ContextMatchRequest{
		RequestID:   "test-4b",
		PropertyRID: "3",
		PackageIDs:  []string{"pkg-scoped"},
	})
	require.NoError(t, err)
	assert.Len(t, resp.Offers, 1)
}

func TestContext_TopicMatch(t *testing.T) {
	store := NewMockStore()
	store.SetAdd("topics:package:pkg-food", "food.cooking", "food.baking")
	store.SetAdd("topics:artifact:article:pasta", "food.cooking", "food.italian")

	engine := NewEngine(EngineConfig{
		ProviderID: "test-provider",
		Store:      store,
		Properties: PropertyList{Global: NewMapBitmap("10")},
		Packages:   []PackageConfig{{PackageID: "pkg-food", TopicTargets: true}},
	})

	resp, err := engine.EvaluateContext(context.Background(), &tmproto.ContextMatchRequest{
		RequestID:    "test-topic",
		PropertyRID:  "10",
		ArtifactRefs: []tmproto.ArtifactRef{{Type: tmproto.ArtifactRefTypeURL, Value: "article:pasta"}},
		PackageIDs:   []string{"pkg-food"},
	})
	require.NoError(t, err)
	assert.Len(t, resp.Offers, 1, "expected 1 offer (topic match)")
}

func TestContext_TopicMiss(t *testing.T) {
	store := NewMockStore()
	store.SetAdd("topics:package:pkg-food", "food.cooking")
	store.SetAdd("topics:artifact:article:cpu", "technology.hardware")

	engine := NewEngine(EngineConfig{
		ProviderID: "test-provider",
		Store:      store,
		Properties: PropertyList{Global: NewMapBitmap("10")},
		Packages:   []PackageConfig{{PackageID: "pkg-food", TopicTargets: true}},
	})

	resp, err := engine.EvaluateContext(context.Background(), &tmproto.ContextMatchRequest{
		RequestID:    "test-topic-miss",
		PropertyRID:  "10",
		ArtifactRefs: []tmproto.ArtifactRef{{Type: tmproto.ArtifactRefTypeURL, Value: "article:cpu"}},
		PackageIDs:   []string{"pkg-food"},
	})
	require.NoError(t, err)
	assert.Empty(t, resp.Offers, "expected 0 offers (topic mismatch)")
}

func TestContext_URLBlocklist(t *testing.T) {
	store := NewMockStore()
	blockedHash := HashURL("article:controversial")
	store.SetAdd("url:blocklist:pkg-family", blockedHash)

	engine := NewEngine(EngineConfig{
		ProviderID: "test-provider",
		Store:      store,
		Properties: PropertyList{Global: NewMapBitmap("20")},
		Packages:   []PackageConfig{{PackageID: "pkg-family", URLBlocklist: true}},
	})

	resp, err := engine.EvaluateContext(context.Background(), &tmproto.ContextMatchRequest{
		RequestID:    "test-block",
		PropertyRID:  "20",
		ArtifactRefs: []tmproto.ArtifactRef{{Type: tmproto.ArtifactRefTypeURL, Value: "article:controversial"}},
		PackageIDs:   []string{"pkg-family"},
	})
	require.NoError(t, err)
	assert.Empty(t, resp.Offers, "expected 0 offers (URL blocked)")
}

func TestContext_URLAllowlist(t *testing.T) {
	store := NewMockStore()
	allowedHash := HashURL("article:safe-content")
	store.SetAdd("url:allowlist:pkg-premium", allowedHash)

	engine := NewEngine(EngineConfig{
		ProviderID: "test-provider",
		Store:      store,
		Properties: PropertyList{Global: NewMapBitmap("20")},
		Packages:   []PackageConfig{{PackageID: "pkg-premium", URLAllowlist: true}},
	})

	// Allowed URL should produce an offer.
	resp, err := engine.EvaluateContext(context.Background(), &tmproto.ContextMatchRequest{
		RequestID:    "test-allow-hit",
		PropertyRID:  "20",
		ArtifactRefs: []tmproto.ArtifactRef{{Type: tmproto.ArtifactRefTypeURL, Value: "article:safe-content"}},
		PackageIDs:   []string{"pkg-premium"},
	})
	require.NoError(t, err)
	assert.Len(t, resp.Offers, 1, "expected 1 offer (URL in allowlist)")

	// Non-allowed URL should be blocked.
	resp, err = engine.EvaluateContext(context.Background(), &tmproto.ContextMatchRequest{
		RequestID:    "test-allow-miss",
		PropertyRID:  "20",
		ArtifactRefs: []tmproto.ArtifactRef{{Type: tmproto.ArtifactRefTypeURL, Value: "article:other-content"}},
		PackageIDs:   []string{"pkg-premium"},
	})
	require.NoError(t, err)
	assert.Empty(t, resp.Offers, "expected 0 offers (URL not in allowlist)")
}

func TestContext_MultiplePackages_MixedResults(t *testing.T) {
	store := NewMockStore()
	store.SetAdd("topics:package:pkg-food", "food.cooking")
	store.SetAdd("topics:package:pkg-tech", "technology.reviews")
	store.SetAdd("topics:artifact:article:pasta", "food.cooking", "food.italian")

	engine := NewEngine(EngineConfig{
		ProviderID: "test-provider",
		Store:      store,
		Properties: PropertyList{Global: NewMapBitmap("30")},
		Packages: []PackageConfig{
			{PackageID: "pkg-food", TopicTargets: true},
			{PackageID: "pkg-tech", TopicTargets: true},
		},
	})

	resp, err := engine.EvaluateContext(context.Background(), &tmproto.ContextMatchRequest{
		RequestID:    "test-multi",
		PropertyRID:  "30",
		ArtifactRefs: []tmproto.ArtifactRef{{Type: tmproto.ArtifactRefTypeURL, Value: "article:pasta"}},
		PackageIDs:   []string{"pkg-food", "pkg-tech"},
	})
	require.NoError(t, err)

	matched := map[string]bool{}
	for _, o := range resp.Offers {
		matched[o.PackageID] = true
	}
	assert.True(t, matched["pkg-food"], "pkg-food should match")
	assert.False(t, matched["pkg-tech"], "pkg-tech should not match")
}

func TestContext_EmitSegments(t *testing.T) {
	store := NewMockStore()
	engine := NewEngine(EngineConfig{
		ProviderID: "test-provider",
		Store:      store,
		Properties: PropertyList{Global: NewMapBitmap("1")},
		Packages: []PackageConfig{
			{PackageID: "pkg-1", EmitSegments: []string{"sports_lovers", "premium_audience"}},
		},
	})

	resp, err := engine.EvaluateContext(context.Background(), &tmproto.ContextMatchRequest{
		RequestID:   "test-segments",
		PropertyRID: "1",
		PackageIDs:  []string{"pkg-1"},
	})
	require.NoError(t, err)
	require.NotNil(t, resp.Signals, "expected signals with segments")
	segs, ok := resp.Signals["segments"].([]string)
	require.True(t, ok, "segments should be []string, got %v", resp.Signals["segments"])
	assert.Len(t, segs, 2)
}

func TestContext_RequestIDPreserved(t *testing.T) {
	engine, _ := setupContextEngine(t)
	resp, err := engine.EvaluateContext(context.Background(), &tmproto.ContextMatchRequest{
		RequestID:   "preserve-me",
		PropertyRID: "999",
		PackageIDs:  []string{"pkg-1"},
	})
	require.NoError(t, err)
	assert.Equal(t, "preserve-me", resp.RequestID)
}

func TestContext_UnknownPackageSkipped(t *testing.T) {
	engine, _ := setupContextEngine(t)
	resp, err := engine.EvaluateContext(context.Background(), &tmproto.ContextMatchRequest{
		RequestID:   "test-unknown-pkg",
		PropertyRID: "1",
		PackageIDs:  []string{"pkg-unknown"},
	})
	require.NoError(t, err)
	assert.Empty(t, resp.Offers, "expected 0 offers for unknown package")
}

// --- Identity Tests (using resolved path with exposure logs) ---

func TestIdentity_ExposureIncrements(t *testing.T) {
	engine, _, _ := setupIdentityEngine(t)
	ctx := context.Background()

	resp, err := engine.RecordExposure(ctx, &ExposeRequest{
		UserToken: "user-abc",
		PackageID: "pkg-display-001",
	})
	require.NoError(t, err)
	assert.Equal(t, 1, resp.CampaignCount)
	assert.Equal(t, 4, resp.CampaignRemaining)
}

func TestIdentity_CampaignFrequencyCap(t *testing.T) {
	engine, store, resolved := setupIdentityEngine(t)
	ctx := context.Background()

	store.SetUserProfile("user-abc", map[string]float64{"cooking": 0})

	// 5 exposures across two packages in campaign-acme (cap is 5/7d).
	for i := range 3 {
		_, _ = engine.RecordExposure(ctx, &ExposeRequest{UserToken: "user-abc", PackageID: "pkg-display-001", ImpressionID: fmt.Sprintf("imp-001-%d", i)})
	}
	for i := range 2 {
		_, _ = engine.RecordExposure(ctx, &ExposeRequest{UserToken: "user-abc", PackageID: "pkg-display-002", ImpressionID: fmt.Sprintf("imp-002-%d", i)})
	}

	resp, err := engine.EvaluateIdentityResolved(ctx, resolved, &tmproto.IdentityMatchRequest{
		RequestID:  "id-campaign",
		Identities: []tmproto.IdentityToken{{UserToken: "user-abc"}},
		PackageIDs: []string{"pkg-display-001", "pkg-display-002"},
	})
	require.NoError(t, err)
	for _, e := range resp.Eligibility {
		assert.False(t, e.Eligible, "%s should be campaign-capped", e.PackageID)
	}
}

func TestIdentity_PackageCappedButCampaignNot(t *testing.T) {
	engine, store, resolved := setupIdentityEngine(t)
	ctx := context.Background()

	store.SetUserProfile("user-abc", map[string]float64{"cooking": 0})

	// 3 exposures on pkg-display-001 (package cap=3, campaign cap=5).
	for i := range 3 {
		_, _ = engine.RecordExposure(ctx, &ExposeRequest{UserToken: "user-abc", PackageID: "pkg-display-001", ImpressionID: fmt.Sprintf("imp-cap-%d", i)})
	}

	resp, err := engine.EvaluateIdentityResolved(ctx, resolved, &tmproto.IdentityMatchRequest{
		RequestID:  "id-pkg-cap",
		Identities: []tmproto.IdentityToken{{UserToken: "user-abc"}},
		PackageIDs: []string{"pkg-display-001", "pkg-display-002"},
	})
	require.NoError(t, err)

	byPkg := map[string]tmproto.PackageEligibility{}
	for _, e := range resp.Eligibility {
		byPkg[e.PackageID] = e
	}
	assert.False(t, byPkg["pkg-display-001"].Eligible, "pkg-display-001 should be package-capped (3/3)")
	assert.True(t, byPkg["pkg-display-002"].Eligible, "pkg-display-002 should still be eligible")
}

func TestIdentity_MultipleFrequencyRules(t *testing.T) {
	engine, _, resolved := setupIdentityEngine(t)
	ctx := context.Background()

	// pkg-multi-rule: 2 per 12h AND 5 per 7d.
	_, _ = engine.RecordExposure(ctx, &ExposeRequest{UserToken: "user-abc", PackageID: "pkg-multi-rule", ImpressionID: "imp-multi-1"})
	_, _ = engine.RecordExposure(ctx, &ExposeRequest{UserToken: "user-abc", PackageID: "pkg-multi-rule", ImpressionID: "imp-multi-2"})

	resp, err := engine.EvaluateIdentityResolved(ctx, resolved, &tmproto.IdentityMatchRequest{
		RequestID:  "id-multi",
		Identities: []tmproto.IdentityToken{{UserToken: "user-abc"}},
		PackageIDs: []string{"pkg-multi-rule"},
	})
	require.NoError(t, err)
	assert.False(t, resp.Eligibility[0].Eligible, "should be capped by 12h rule (2/2)")
}

func TestIdentity_SlidingWindowExpiry(t *testing.T) {
	engine, store, resolved := setupIdentityEngine(t)
	ctx := context.Background()
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	store.SetUserProfile("user-abc", map[string]float64{"cooking": 0})

	// 3 exposures (hits cap).
	for i := range 3 {
		_, _ = engine.RecordExposure(ctx, &ExposeRequest{UserToken: "user-abc", PackageID: "pkg-display-001", ImpressionID: fmt.Sprintf("imp-window-%d", i)})
	}

	resp, _ := engine.EvaluateIdentityResolved(ctx, resolved, &tmproto.IdentityMatchRequest{
		RequestID: "id-before", Identities: []tmproto.IdentityToken{{UserToken: "user-abc"}}, PackageIDs: []string{"pkg-display-001"},
	})
	assert.False(t, resp.Eligibility[0].Eligible, "should be capped (3/3)")

	// Advance past 24h window.
	future := now.Add(25 * time.Hour)
	engine.Now = func() time.Time { return future }
	store.Now = func() time.Time { return future }

	resp, _ = engine.EvaluateIdentityResolved(ctx, resolved, &tmproto.IdentityMatchRequest{
		RequestID: "id-after", Identities: []tmproto.IdentityToken{{UserToken: "user-abc"}}, PackageIDs: []string{"pkg-display-001"},
	})
	assert.True(t, resp.Eligibility[0].Eligible, "should be eligible after window expires")
}

func TestIdentity_IntentScore(t *testing.T) {
	engine, store, resolved := setupIdentityEngine(t)
	ctx := context.Background()

	store.SetUserProfile("user-abc", map[string]float64{"cooking": 0})

	_, _ = engine.RecordExposure(ctx, &ExposeRequest{UserToken: "user-abc", PackageID: "pkg-display-001"})

	resp, err := engine.EvaluateIdentityResolved(ctx, resolved, &tmproto.IdentityMatchRequest{
		RequestID: "id-intent", Identities: []tmproto.IdentityToken{{UserToken: "user-abc"}}, PackageIDs: []string{"pkg-display-001"},
	})
	require.NoError(t, err)
	require.NotNil(t, resp.Eligibility[0].IntentScore)
	assert.GreaterOrEqual(t, *resp.Eligibility[0].IntentScore, 0.99, "expected high intent score after recent exposure")
}

func TestIdentity_AudienceNotInSegment(t *testing.T) {
	engine, _, resolved := setupIdentityEngine(t)
	// No user profile set -> user has no segments -> should fail audience gate.
	resp, _ := engine.EvaluateIdentityResolved(context.Background(), resolved, &tmproto.IdentityMatchRequest{
		RequestID: "id-audience", Identities: []tmproto.IdentityToken{{UserToken: "user-abc"}}, PackageIDs: []string{"pkg-display-001"},
	})
	assert.False(t, resp.Eligibility[0].Eligible, "should not be eligible (not in segment)")
}

func TestIdentity_NoCapPackage(t *testing.T) {
	engine, _, resolved := setupIdentityEngine(t)
	resp, _ := engine.EvaluateIdentityResolved(context.Background(), resolved, &tmproto.IdentityMatchRequest{
		RequestID: "id-nocap", Identities: []tmproto.IdentityToken{{UserToken: "user-abc"}}, PackageIDs: []string{"pkg-no-cap"},
	})
	assert.True(t, resp.Eligibility[0].Eligible, "pkg-no-cap should always be eligible")
}

func TestIdentity_UnknownPackage(t *testing.T) {
	engine, _, resolved := setupIdentityEngine(t)
	resp, _ := engine.EvaluateIdentityResolved(context.Background(), resolved, &tmproto.IdentityMatchRequest{
		RequestID: "id-unknown", Identities: []tmproto.IdentityToken{{UserToken: "user-abc"}}, PackageIDs: []string{"pkg-unknown"},
	})
	// Unknown package with no identity config -> eligible (no restrictions).
	assert.True(t, resp.Eligibility[0].Eligible, "unknown package with no identity config should be eligible")
}

func TestIdentity_RequestIDPreserved(t *testing.T) {
	engine, _, resolved := setupIdentityEngine(t)
	resp, _ := engine.EvaluateIdentityResolved(context.Background(), resolved, &tmproto.IdentityMatchRequest{
		RequestID: "keep-this", Identities: []tmproto.IdentityToken{{UserToken: "user-abc"}}, PackageIDs: []string{"pkg-no-cap"},
	})
	assert.Equal(t, "keep-this", resp.RequestID)
}

// --- Source Provenance Tests ---

func TestIdentity_SourceIDFallsBackToProviderID(t *testing.T) {
	engine, _, _ := setupIdentityEngine(t)
	ctx := context.Background()

	// No source_id -> engine uses providerID ("test-provider").
	resp, err := engine.RecordExposure(ctx, &ExposeRequest{
		UserToken:    "user-src",
		PackageID:    "pkg-display-001",
		ImpressionID: "imp-src-1",
	})
	require.NoError(t, err)
	assert.Equal(t, "pkg-display-001", resp.PackageID)

	// Read back the binary log and verify source hash = hash("test-provider").
	hash := HashToken("user-src")
	val, _, _ := engine.store.Get(ctx, "user:exposures:"+hash)
	blog := BinaryExposureLog(val)
	require.Equal(t, 1, blog.Len())
	assert.Equal(t, hashString("test-provider"), blog.SourceHash(0), "expected source hash of 'test-provider' when source_id not provided")
}

func TestIdentity_SourceIDStampedOnBinaryLog(t *testing.T) {
	engine, _, _ := setupIdentityEngine(t)
	ctx := context.Background()

	_, err := engine.RecordExposure(ctx, &ExposeRequest{
		SourceID:     "agent-cnn-v2",
		UserToken:    "user-source-stamp",
		PackageID:    "pkg-display-001",
		ImpressionID: "imp-src-2",
	})
	require.NoError(t, err)

	hash := HashToken("user-source-stamp")
	val, _, _ := engine.store.Get(ctx, "user:exposures:"+hash)
	blog := BinaryExposureLog(val)
	require.Equal(t, 1, blog.Len())
	assert.Equal(t, hashString("agent-cnn-v2"), blog.SourceHash(0), "expected source hash of 'agent-cnn-v2'")
}

func TestIdentity_SourceNamespacesSortedSetMembers(t *testing.T) {
	engine, _, _ := setupIdentityEngine(t)
	ctx := context.Background()

	// Two different sources submit the same impression_id.
	_, _ = engine.RecordExposure(ctx, &ExposeRequest{
		SourceID:     "agent-a",
		UserToken:    "user-ns",
		PackageID:    "pkg-display-001",
		ImpressionID: "imp-dup",
	})
	_, _ = engine.RecordExposure(ctx, &ExposeRequest{
		SourceID:     "agent-b",
		UserToken:    "user-ns",
		PackageID:    "pkg-display-001",
		ImpressionID: "imp-dup",
	})

	// ZCount should show 2 distinct members (agent-a:imp-dup and agent-b:imp-dup).
	hash := HashToken("user-ns")
	key := "freq:pkg:pkg-display-001:" + hash
	count, err := engine.store.ZCount(ctx, key, 0, math.MaxFloat64)
	require.NoError(t, err)
	assert.Equal(t, int64(2), count, "expected 2 sorted set members (namespaced by source)")
}
