package targeting

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"fmt"
	"testing"
	"time"

	"github.com/adcontextprotocol/adcp-go/tmproto"
)

// mockRegistry implements PropertyRegistry for tests.
type mockRegistry struct {
	keys map[uint64]ed25519.PublicKey
}

func (r *mockRegistry) GetPublicKey(rid uint64) ed25519.PublicKey {
	return r.keys[rid]
}

func setupContextEngine(t *testing.T) (*Engine, *MockStore) {
	t.Helper()
	store := NewMockStore()
	props := PropertyList{
		Global: NewMapBitmap(1, 2, 3, 4, 5),
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
		RequestID:     "test-1",
		PropertyRID:   1,
		AvailablePkgs: []tmproto.AvailablePackage{{PackageID: "pkg-1"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.Offers) != 1 {
		t.Errorf("expected 1 offer, got %d", len(resp.Offers))
	}
}

func TestContext_BitmapPreFilter_NotTargeted(t *testing.T) {
	engine, _ := setupContextEngine(t)
	resp, err := engine.EvaluateContext(context.Background(), &tmproto.ContextMatchRequest{
		RequestID:     "test-2",
		PropertyRID:   999,
		AvailablePkgs: []tmproto.AvailablePackage{{PackageID: "pkg-1"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.Offers) != 0 {
		t.Errorf("expected 0 offers, got %d", len(resp.Offers))
	}
}

func TestContext_PropertySuppression(t *testing.T) {
	engine, _ := setupContextEngine(t)
	ctx := context.Background()
	_ = engine.SuppressProperty(ctx, 2, time.Hour)

	resp, err := engine.EvaluateContext(ctx, &tmproto.ContextMatchRequest{
		RequestID:     "test-3",
		PropertyRID:   2,
		AvailablePkgs: []tmproto.AvailablePackage{{PackageID: "pkg-1"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.Offers) != 0 {
		t.Errorf("expected 0 offers for suppressed property, got %d", len(resp.Offers))
	}
}

func TestContext_PerPackageTargeting(t *testing.T) {
	store := NewMockStore()
	engine := NewEngine(EngineConfig{
		ProviderID: "test-provider",
		Store:      store,
		Properties: PropertyList{
			Global:    NewMapBitmap(1, 3),
			ByPackage: map[string]Bitmap{"pkg-scoped": NewMapBitmap(3)},
		},
		Packages: []PackageConfig{
			{PackageID: "pkg-scoped"},
		},
	})

	ctx := context.Background()

	// Property 1 is in global but not in pkg-scoped.
	resp, err := engine.EvaluateContext(ctx, &tmproto.ContextMatchRequest{
		RequestID:     "test-4a",
		PropertyRID:   1,
		AvailablePkgs: []tmproto.AvailablePackage{{PackageID: "pkg-scoped"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.Offers) != 0 {
		t.Errorf("expected 0 offers (property not in package bitmap), got %d", len(resp.Offers))
	}

	// Property 3 is in both global and pkg-scoped.
	resp, err = engine.EvaluateContext(ctx, &tmproto.ContextMatchRequest{
		RequestID:     "test-4b",
		PropertyRID:   3,
		AvailablePkgs: []tmproto.AvailablePackage{{PackageID: "pkg-scoped"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.Offers) != 1 {
		t.Errorf("expected 1 offer, got %d", len(resp.Offers))
	}
}

func TestContext_TopicMatch(t *testing.T) {
	store := NewMockStore()
	store.SetAdd("topics:package:pkg-food", "food.cooking", "food.baking")
	store.SetAdd("topics:artifact:article:pasta", "food.cooking", "food.italian")

	engine := NewEngine(EngineConfig{
		ProviderID: "test-provider",
		Store:      store,
		Properties: PropertyList{Global: NewMapBitmap(10)},
		Packages:   []PackageConfig{{PackageID: "pkg-food", TopicTargets: true}},
	})

	resp, err := engine.EvaluateContext(context.Background(), &tmproto.ContextMatchRequest{
		RequestID:     "test-topic",
		PropertyRID:   10,
		Artifacts:     []string{"article:pasta"},
		AvailablePkgs: []tmproto.AvailablePackage{{PackageID: "pkg-food"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.Offers) != 1 {
		t.Errorf("expected 1 offer (topic match), got %d", len(resp.Offers))
	}
}

func TestContext_TopicMiss(t *testing.T) {
	store := NewMockStore()
	store.SetAdd("topics:package:pkg-food", "food.cooking")
	store.SetAdd("topics:artifact:article:cpu", "technology.hardware")

	engine := NewEngine(EngineConfig{
		ProviderID: "test-provider",
		Store:      store,
		Properties: PropertyList{Global: NewMapBitmap(10)},
		Packages:   []PackageConfig{{PackageID: "pkg-food", TopicTargets: true}},
	})

	resp, err := engine.EvaluateContext(context.Background(), &tmproto.ContextMatchRequest{
		RequestID:     "test-topic-miss",
		PropertyRID:   10,
		Artifacts:     []string{"article:cpu"},
		AvailablePkgs: []tmproto.AvailablePackage{{PackageID: "pkg-food"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.Offers) != 0 {
		t.Errorf("expected 0 offers (topic mismatch), got %d", len(resp.Offers))
	}
}

func TestContext_URLBlocklist(t *testing.T) {
	store := NewMockStore()
	blockedHash := HashURL("article:controversial")
	store.SetAdd("url:blocklist:pkg-family", blockedHash)

	engine := NewEngine(EngineConfig{
		ProviderID: "test-provider",
		Store:      store,
		Properties: PropertyList{Global: NewMapBitmap(20)},
		Packages:   []PackageConfig{{PackageID: "pkg-family", URLBlocklist: true}},
	})

	resp, err := engine.EvaluateContext(context.Background(), &tmproto.ContextMatchRequest{
		RequestID:     "test-block",
		PropertyRID:   20,
		Artifacts:     []string{"article:controversial"},
		AvailablePkgs: []tmproto.AvailablePackage{{PackageID: "pkg-family"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.Offers) != 0 {
		t.Errorf("expected 0 offers (URL blocked), got %d", len(resp.Offers))
	}
}

func TestContext_URLAllowlist(t *testing.T) {
	store := NewMockStore()
	allowedHash := HashURL("article:safe-content")
	store.SetAdd("url:allowlist:pkg-premium", allowedHash)

	engine := NewEngine(EngineConfig{
		ProviderID: "test-provider",
		Store:      store,
		Properties: PropertyList{Global: NewMapBitmap(20)},
		Packages:   []PackageConfig{{PackageID: "pkg-premium", URLAllowlist: true}},
	})

	// Allowed URL should produce an offer.
	resp, err := engine.EvaluateContext(context.Background(), &tmproto.ContextMatchRequest{
		RequestID:     "test-allow-hit",
		PropertyRID:   20,
		Artifacts:     []string{"article:safe-content"},
		AvailablePkgs: []tmproto.AvailablePackage{{PackageID: "pkg-premium"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.Offers) != 1 {
		t.Errorf("expected 1 offer (URL in allowlist), got %d", len(resp.Offers))
	}

	// Non-allowed URL should be blocked.
	resp, err = engine.EvaluateContext(context.Background(), &tmproto.ContextMatchRequest{
		RequestID:     "test-allow-miss",
		PropertyRID:   20,
		Artifacts:     []string{"article:other-content"},
		AvailablePkgs: []tmproto.AvailablePackage{{PackageID: "pkg-premium"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.Offers) != 0 {
		t.Errorf("expected 0 offers (URL not in allowlist), got %d", len(resp.Offers))
	}
}

func TestContext_MultiplePackages_MixedResults(t *testing.T) {
	store := NewMockStore()
	store.SetAdd("topics:package:pkg-food", "food.cooking")
	store.SetAdd("topics:package:pkg-tech", "technology.reviews")
	store.SetAdd("topics:artifact:article:pasta", "food.cooking", "food.italian")

	engine := NewEngine(EngineConfig{
		ProviderID: "test-provider",
		Store:      store,
		Properties: PropertyList{Global: NewMapBitmap(30)},
		Packages: []PackageConfig{
			{PackageID: "pkg-food", TopicTargets: true},
			{PackageID: "pkg-tech", TopicTargets: true},
		},
	})

	resp, err := engine.EvaluateContext(context.Background(), &tmproto.ContextMatchRequest{
		RequestID:   "test-multi",
		PropertyRID: 30,
		Artifacts:   []string{"article:pasta"},
		AvailablePkgs: []tmproto.AvailablePackage{
			{PackageID: "pkg-food"},
			{PackageID: "pkg-tech"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	matched := map[string]bool{}
	for _, o := range resp.Offers {
		matched[o.PackageID] = true
	}
	if !matched["pkg-food"] {
		t.Error("pkg-food should match")
	}
	if matched["pkg-tech"] {
		t.Error("pkg-tech should not match")
	}
}

func TestContext_EmitSegments(t *testing.T) {
	store := NewMockStore()
	engine := NewEngine(EngineConfig{
		ProviderID: "test-provider",
		Store:      store,
		Properties: PropertyList{Global: NewMapBitmap(1)},
		Packages: []PackageConfig{
			{PackageID: "pkg-1", EmitSegments: []string{"sports_lovers", "premium_audience"}},
		},
	})

	resp, err := engine.EvaluateContext(context.Background(), &tmproto.ContextMatchRequest{
		RequestID:     "test-segments",
		PropertyRID:   1,
		AvailablePkgs: []tmproto.AvailablePackage{{PackageID: "pkg-1"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Signals == nil {
		t.Fatal("expected signals with segments")
	}
	if len(resp.Signals.Segments) != 2 {
		t.Errorf("expected 2 segments, got %d", len(resp.Signals.Segments))
	}
}

func TestContext_RequestIDPreserved(t *testing.T) {
	engine, _ := setupContextEngine(t)
	resp, err := engine.EvaluateContext(context.Background(), &tmproto.ContextMatchRequest{
		RequestID:     "preserve-me",
		PropertyRID:   999,
		AvailablePkgs: []tmproto.AvailablePackage{{PackageID: "pkg-1"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.RequestID != "preserve-me" {
		t.Errorf("expected request_id 'preserve-me', got %q", resp.RequestID)
	}
}

func TestContext_UnknownPackageSkipped(t *testing.T) {
	engine, _ := setupContextEngine(t)
	resp, err := engine.EvaluateContext(context.Background(), &tmproto.ContextMatchRequest{
		RequestID:     "test-unknown-pkg",
		PropertyRID:   1,
		AvailablePkgs: []tmproto.AvailablePackage{{PackageID: "pkg-unknown"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.Offers) != 0 {
		t.Errorf("expected 0 offers for unknown package, got %d", len(resp.Offers))
	}
}

func TestContext_SignatureVerification(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	store := NewMockStore()
	reg := &mockRegistry{keys: map[uint64]ed25519.PublicKey{100: pub}}

	engine := NewEngine(EngineConfig{
		ProviderID:    "test-provider",
		Store:         store,
		Registry:      reg,
		Properties:    PropertyList{Global: NewMapBitmap(100)},
		Packages:      []PackageConfig{{PackageID: "pkg-1"}},
		SigSampleRate: 100, // verify all
	})

	req := &tmproto.ContextMatchRequest{
		RequestID:    "signed-1",
		PropertyRID:  100,
		PropertyType: tmproto.PropertyTypeWebsite,
		PlacementID:  "sidebar",
		AvailablePkgs: []tmproto.AvailablePackage{
			{PackageID: "pkg-1"},
		},
	}

	// Valid signature should work.
	req.Signature = tmproto.SignRequest(req, priv)
	resp, err := engine.EvaluateContext(context.Background(), req)
	if err != nil {
		t.Fatalf("valid signature rejected: %v", err)
	}
	if len(resp.Offers) != 1 {
		t.Errorf("expected 1 offer with valid sig, got %d", len(resp.Offers))
	}

	// Tampered signature should fail.
	req.Signature = "AAAA" + req.Signature[4:]
	_, err = engine.EvaluateContext(context.Background(), req)
	if err == nil {
		t.Error("tampered signature should be rejected")
	}
}

// --- Identity Tests (using resolved path with exposure logs) ---

func TestIdentity_ExposureIncrements(t *testing.T) {
	engine, _, _ := setupIdentityEngine(t)
	ctx := context.Background()

	resp, err := engine.RecordExposure(ctx, &tmproto.ExposeRequest{
		UserToken: "user-abc",
		PackageID: "pkg-display-001",
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.CampaignCount != 1 {
		t.Errorf("expected campaign count 1, got %d", resp.CampaignCount)
	}
	if resp.CampaignRemaining != 4 {
		t.Errorf("expected 4 remaining, got %d", resp.CampaignRemaining)
	}
}

func TestIdentity_CampaignFrequencyCap(t *testing.T) {
	engine, store, resolved := setupIdentityEngine(t)
	ctx := context.Background()

	store.SetUserProfile("user-abc", map[string]float64{"cooking": 0})

	// 5 exposures across two packages in campaign-acme (cap is 5/7d).
	for i := range 3 {
		_, _ = engine.RecordExposure(ctx, &tmproto.ExposeRequest{UserToken: "user-abc", PackageID: "pkg-display-001", ImpressionID: fmt.Sprintf("imp-001-%d", i)})
	}
	for i := range 2 {
		_, _ = engine.RecordExposure(ctx, &tmproto.ExposeRequest{UserToken: "user-abc", PackageID: "pkg-display-002", ImpressionID: fmt.Sprintf("imp-002-%d", i)})
	}

	resp, err := engine.EvaluateIdentityResolved(ctx, resolved, &tmproto.IdentityMatchRequest{
		RequestID:  "id-campaign",
		UserToken:  "user-abc",
		PackageIDs: []string{"pkg-display-001", "pkg-display-002"},
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range resp.Eligibility {
		if e.Eligible {
			t.Errorf("%s should be campaign-capped", e.PackageID)
		}
	}
}

func TestIdentity_PackageCappedButCampaignNot(t *testing.T) {
	engine, store, resolved := setupIdentityEngine(t)
	ctx := context.Background()

	store.SetUserProfile("user-abc", map[string]float64{"cooking": 0})

	// 3 exposures on pkg-display-001 (package cap=3, campaign cap=5).
	for i := range 3 {
		_, _ = engine.RecordExposure(ctx, &tmproto.ExposeRequest{UserToken: "user-abc", PackageID: "pkg-display-001", ImpressionID: fmt.Sprintf("imp-cap-%d", i)})
	}

	resp, err := engine.EvaluateIdentityResolved(ctx, resolved, &tmproto.IdentityMatchRequest{
		RequestID:  "id-pkg-cap",
		UserToken:  "user-abc",
		PackageIDs: []string{"pkg-display-001", "pkg-display-002"},
	})
	if err != nil {
		t.Fatal(err)
	}

	byPkg := map[string]tmproto.PackageEligibility{}
	for _, e := range resp.Eligibility {
		byPkg[e.PackageID] = e
	}
	if byPkg["pkg-display-001"].Eligible {
		t.Error("pkg-display-001 should be package-capped (3/3)")
	}
	if !byPkg["pkg-display-002"].Eligible {
		t.Error("pkg-display-002 should still be eligible")
	}
}

func TestIdentity_MultipleFrequencyRules(t *testing.T) {
	engine, _, resolved := setupIdentityEngine(t)
	ctx := context.Background()

	// pkg-multi-rule: 2 per 12h AND 5 per 7d.
	_, _ = engine.RecordExposure(ctx, &tmproto.ExposeRequest{UserToken: "user-abc", PackageID: "pkg-multi-rule", ImpressionID: "imp-multi-1"})
	_, _ = engine.RecordExposure(ctx, &tmproto.ExposeRequest{UserToken: "user-abc", PackageID: "pkg-multi-rule", ImpressionID: "imp-multi-2"})

	resp, err := engine.EvaluateIdentityResolved(ctx, resolved, &tmproto.IdentityMatchRequest{
		RequestID:  "id-multi",
		UserToken:  "user-abc",
		PackageIDs: []string{"pkg-multi-rule"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Eligibility[0].Eligible {
		t.Error("should be capped by 12h rule (2/2)")
	}
}

func TestIdentity_SlidingWindowExpiry(t *testing.T) {
	engine, store, resolved := setupIdentityEngine(t)
	ctx := context.Background()
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	store.SetUserProfile("user-abc", map[string]float64{"cooking": 0})

	// 3 exposures (hits cap).
	for i := range 3 {
		_, _ = engine.RecordExposure(ctx, &tmproto.ExposeRequest{UserToken: "user-abc", PackageID: "pkg-display-001", ImpressionID: fmt.Sprintf("imp-window-%d", i)})
	}

	resp, _ := engine.EvaluateIdentityResolved(ctx, resolved, &tmproto.IdentityMatchRequest{
		RequestID: "id-before", UserToken: "user-abc", PackageIDs: []string{"pkg-display-001"},
	})
	if resp.Eligibility[0].Eligible {
		t.Error("should be capped (3/3)")
	}

	// Advance past 24h window.
	future := now.Add(25 * time.Hour)
	engine.Now = func() time.Time { return future }
	store.Now = func() time.Time { return future }

	resp, _ = engine.EvaluateIdentityResolved(ctx, resolved, &tmproto.IdentityMatchRequest{
		RequestID: "id-after", UserToken: "user-abc", PackageIDs: []string{"pkg-display-001"},
	})
	if !resp.Eligibility[0].Eligible {
		t.Error("should be eligible after window expires")
	}
}

func TestIdentity_IntentScore(t *testing.T) {
	engine, store, resolved := setupIdentityEngine(t)
	ctx := context.Background()

	store.SetUserProfile("user-abc", map[string]float64{"cooking": 0})

	_, _ = engine.RecordExposure(ctx, &tmproto.ExposeRequest{UserToken: "user-abc", PackageID: "pkg-display-001"})

	resp, err := engine.EvaluateIdentityResolved(ctx, resolved, &tmproto.IdentityMatchRequest{
		RequestID: "id-intent", UserToken: "user-abc", PackageIDs: []string{"pkg-display-001"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Eligibility[0].IntentScore == nil || *resp.Eligibility[0].IntentScore < 0.99 {
		t.Error("expected high intent score after recent exposure")
	}
}

func TestIdentity_AudienceNotInSegment(t *testing.T) {
	engine, _, resolved := setupIdentityEngine(t)
	// No user profile set → user has no segments → should fail audience gate.
	resp, _ := engine.EvaluateIdentityResolved(context.Background(), resolved, &tmproto.IdentityMatchRequest{
		RequestID: "id-audience", UserToken: "user-abc", PackageIDs: []string{"pkg-display-001"},
	})
	if resp.Eligibility[0].Eligible {
		t.Error("should not be eligible (not in segment)")
	}
}

func TestIdentity_NoCapPackage(t *testing.T) {
	engine, _, resolved := setupIdentityEngine(t)
	resp, _ := engine.EvaluateIdentityResolved(context.Background(), resolved, &tmproto.IdentityMatchRequest{
		RequestID: "id-nocap", UserToken: "user-abc", PackageIDs: []string{"pkg-no-cap"},
	})
	if !resp.Eligibility[0].Eligible {
		t.Error("pkg-no-cap should always be eligible")
	}
}

func TestIdentity_UnknownPackage(t *testing.T) {
	engine, _, resolved := setupIdentityEngine(t)
	resp, _ := engine.EvaluateIdentityResolved(context.Background(), resolved, &tmproto.IdentityMatchRequest{
		RequestID: "id-unknown", UserToken: "user-abc", PackageIDs: []string{"pkg-unknown"},
	})
	// Unknown package with no identity config → eligible (no restrictions).
	if !resp.Eligibility[0].Eligible {
		t.Error("unknown package with no identity config should be eligible")
	}
}

func TestIdentity_RequestIDPreserved(t *testing.T) {
	engine, _, resolved := setupIdentityEngine(t)
	resp, _ := engine.EvaluateIdentityResolved(context.Background(), resolved, &tmproto.IdentityMatchRequest{
		RequestID: "keep-this", UserToken: "user-abc", PackageIDs: []string{"pkg-no-cap"},
	})
	if resp.RequestID != "keep-this" {
		t.Errorf("expected request_id 'keep-this', got %q", resp.RequestID)
	}
}

// --- Non-Resolved Identity Tests (sorted-set frequency capping) ---

func TestIdentityNonResolved_PackageFrequencyCap(t *testing.T) {
	engine, store, _ := setupIdentityEngine(t)
	ctx := context.Background()

	store.SetAdd("audience:cooking", HashToken("user-abc"))

	// Record 3 exposures (package cap = 3/24h).
	for i := range 3 {
		_, err := engine.RecordExposure(ctx, &tmproto.ExposeRequest{
			UserToken: "user-abc", PackageID: "pkg-display-001",
			ImpressionID: fmt.Sprintf("imp-nr-%d", i),
		})
		if err != nil {
			t.Fatal(err)
		}
	}

	resp, err := engine.EvaluateIdentity(ctx, &tmproto.IdentityMatchRequest{
		RequestID: "nr-pkg-cap", UserToken: "user-abc",
		PackageIDs: []string{"pkg-display-001"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Eligibility[0].Eligible {
		t.Error("pkg-display-001 should be package-capped via sorted set (3/3)")
	}
}

func TestIdentityNonResolved_CampaignFrequencyCap(t *testing.T) {
	engine, store, _ := setupIdentityEngine(t)
	ctx := context.Background()

	store.SetAdd("audience:cooking", HashToken("user-abc"))

	// 5 exposures across two packages in campaign-acme (cap = 5/7d).
	for i := range 3 {
		_, _ = engine.RecordExposure(ctx, &tmproto.ExposeRequest{
			UserToken: "user-abc", PackageID: "pkg-display-001",
			ImpressionID: fmt.Sprintf("imp-nr-camp-1-%d", i),
		})
	}
	for i := range 2 {
		_, _ = engine.RecordExposure(ctx, &tmproto.ExposeRequest{
			UserToken: "user-abc", PackageID: "pkg-display-002",
			ImpressionID: fmt.Sprintf("imp-nr-camp-2-%d", i),
		})
	}

	resp, err := engine.EvaluateIdentity(ctx, &tmproto.IdentityMatchRequest{
		RequestID: "nr-camp-cap", UserToken: "user-abc",
		PackageIDs: []string{"pkg-display-001", "pkg-display-002"},
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range resp.Eligibility {
		if e.Eligible {
			t.Errorf("%s should be campaign-capped via sorted set", e.PackageID)
		}
	}
}

func TestIdentityNonResolved_SlidingWindowExpiry(t *testing.T) {
	engine, store, _ := setupIdentityEngine(t)
	ctx := context.Background()
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	store.SetAdd("audience:cooking", HashToken("user-abc"))

	// 3 exposures (hits package cap of 3/24h).
	for i := range 3 {
		_, _ = engine.RecordExposure(ctx, &tmproto.ExposeRequest{
			UserToken: "user-abc", PackageID: "pkg-display-001",
			ImpressionID: fmt.Sprintf("imp-nr-window-%d", i),
		})
	}

	resp, _ := engine.EvaluateIdentity(ctx, &tmproto.IdentityMatchRequest{
		RequestID: "nr-before", UserToken: "user-abc",
		PackageIDs: []string{"pkg-display-001"},
	})
	if resp.Eligibility[0].Eligible {
		t.Error("should be capped (3/3)")
	}

	// Advance past 24h window.
	future := now.Add(25 * time.Hour)
	engine.Now = func() time.Time { return future }
	store.Now = func() time.Time { return future }

	resp, _ = engine.EvaluateIdentity(ctx, &tmproto.IdentityMatchRequest{
		RequestID: "nr-after", UserToken: "user-abc",
		PackageIDs: []string{"pkg-display-001"},
	})
	if !resp.Eligibility[0].Eligible {
		t.Error("should be eligible after window expires")
	}
}

func TestIdentityNonResolved_IntentScore(t *testing.T) {
	engine, store, _ := setupIdentityEngine(t)
	ctx := context.Background()

	store.SetAdd("audience:cooking", HashToken("user-abc"))

	_, _ = engine.RecordExposure(ctx, &tmproto.ExposeRequest{
		UserToken: "user-abc", PackageID: "pkg-display-001",
	})

	resp, err := engine.EvaluateIdentity(ctx, &tmproto.IdentityMatchRequest{
		RequestID: "nr-intent", UserToken: "user-abc",
		PackageIDs: []string{"pkg-display-001"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Eligibility[0].IntentScore == nil || *resp.Eligibility[0].IntentScore < 0.99 {
		t.Error("expected high intent score after recent exposure")
	}
}

func TestIdentityNonResolved_PackageCappedButCampaignNot(t *testing.T) {
	engine, store, _ := setupIdentityEngine(t)
	ctx := context.Background()

	store.SetAdd("audience:cooking", HashToken("user-abc"))

	// 3 exposures on pkg-display-001 (package cap=3, campaign cap=5).
	for i := range 3 {
		_, _ = engine.RecordExposure(ctx, &tmproto.ExposeRequest{
			UserToken: "user-abc", PackageID: "pkg-display-001",
			ImpressionID: fmt.Sprintf("imp-nr-mixed-%d", i),
		})
	}

	resp, err := engine.EvaluateIdentity(ctx, &tmproto.IdentityMatchRequest{
		RequestID: "nr-mixed", UserToken: "user-abc",
		PackageIDs: []string{"pkg-display-001", "pkg-display-002"},
	})
	if err != nil {
		t.Fatal(err)
	}

	byPkg := map[string]tmproto.PackageEligibility{}
	for _, e := range resp.Eligibility {
		byPkg[e.PackageID] = e
	}
	if byPkg["pkg-display-001"].Eligible {
		t.Error("pkg-display-001 should be package-capped (3/3)")
	}
	if !byPkg["pkg-display-002"].Eligible {
		t.Error("pkg-display-002 should still be eligible")
	}
}
