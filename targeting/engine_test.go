package targeting

import (
	"context"
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

	store.SetPackageIdentityConfig("pkg-display-001", PackageIdentityConfig{
		TargetSegments: []string{"cooking", "home"},
	})
	store.SetPackageIdentityConfig("pkg-display-002", PackageIdentityConfig{})

	resolved := &ResolvedPackages{
		SegmentIndex: map[string][]string{
			"cooking": {"pkg-display-001"},
			"home":    {"pkg-display-001"},
		},
		IdentityConfigs: map[string]*PackageIdentityConfig{
			"pkg-display-001": {TargetSegments: []string{"cooking", "home"}},
			"pkg-display-002": {},
			"pkg-no-segments": {},
		},
	}

	engine := NewEngine(EngineConfig{
		ProviderID: "test-provider",
		Store:      store,
		Packages: []PackageConfig{
			{PackageID: "pkg-display-001"},
			{PackageID: "pkg-display-002"},
			{PackageID: "pkg-no-segments"},
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

	resp, err := engine.EvaluateContext(ctx, &tmproto.ContextMatchRequest{
		RequestID:   "test-4a",
		PropertyRID: "1",
		PackageIDs:  []string{"pkg-scoped"},
	})
	require.NoError(t, err)
	assert.Empty(t, resp.Offers, "expected 0 offers (property not in package bitmap)")

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

	resp, err := engine.EvaluateContext(context.Background(), &tmproto.ContextMatchRequest{
		RequestID:    "test-allow-hit",
		PropertyRID:  "20",
		ArtifactRefs: []tmproto.ArtifactRef{{Type: tmproto.ArtifactRefTypeURL, Value: "article:safe-content"}},
		PackageIDs:   []string{"pkg-premium"},
	})
	require.NoError(t, err)
	assert.Len(t, resp.Offers, 1, "expected 1 offer (URL in allowlist)")

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

// --- Identity Tests (segment gating only; fcap is handled by fcap.Service) ---

func TestIdentity_AudienceMatch(t *testing.T) {
	engine, store, resolved := setupIdentityEngine(t)
	store.SetUserProfile("user-abc", map[string]float64{"cooking": 0})

	resp, err := engine.EvaluateIdentityResolved(context.Background(), resolved, &tmproto.IdentityMatchRequest{
		RequestID:  "id-audience-hit",
		Identities: []tmproto.IdentityToken{{UserToken: "user-abc"}},
		PackageIDs: []string{"pkg-display-001"},
	})
	require.NoError(t, err)
	assert.True(t, resp.Eligibility[0].Eligible)
}

func TestIdentity_AudienceNotInSegment(t *testing.T) {
	engine, _, resolved := setupIdentityEngine(t)
	resp, _ := engine.EvaluateIdentityResolved(context.Background(), resolved, &tmproto.IdentityMatchRequest{
		RequestID:  "id-audience",
		Identities: []tmproto.IdentityToken{{UserToken: "user-abc"}},
		PackageIDs: []string{"pkg-display-001"},
	})
	assert.False(t, resp.Eligibility[0].Eligible, "should not be eligible (not in segment)")
}

func TestIdentity_NoSegmentTargeting(t *testing.T) {
	engine, _, resolved := setupIdentityEngine(t)
	resp, _ := engine.EvaluateIdentityResolved(context.Background(), resolved, &tmproto.IdentityMatchRequest{
		RequestID:  "id-no-seg",
		Identities: []tmproto.IdentityToken{{UserToken: "user-abc"}},
		PackageIDs: []string{"pkg-no-segments"},
	})
	assert.True(t, resp.Eligibility[0].Eligible, "package with no segment targeting should always be eligible")
}

func TestIdentity_UnknownPackage(t *testing.T) {
	engine, _, resolved := setupIdentityEngine(t)
	resp, _ := engine.EvaluateIdentityResolved(context.Background(), resolved, &tmproto.IdentityMatchRequest{
		RequestID:  "id-unknown",
		Identities: []tmproto.IdentityToken{{UserToken: "user-abc"}},
		PackageIDs: []string{"pkg-unknown"},
	})
	assert.True(t, resp.Eligibility[0].Eligible, "unknown package with no identity config should be eligible")
}

func TestIdentity_RequestIDPreserved(t *testing.T) {
	engine, _, resolved := setupIdentityEngine(t)
	resp, _ := engine.EvaluateIdentityResolved(context.Background(), resolved, &tmproto.IdentityMatchRequest{
		RequestID:  "keep-this",
		Identities: []tmproto.IdentityToken{{UserToken: "user-abc"}},
		PackageIDs: []string{"pkg-no-segments"},
	})
	assert.Equal(t, "keep-this", resp.RequestID)
}
