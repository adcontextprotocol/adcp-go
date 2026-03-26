package main

import (
	"context"
	"testing"
	"time"

	"github.com/adcontextprotocol/adcp-go/tmp"
	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

func setupTest(t *testing.T) (*IdentityAgent, *miniredis.Miniredis) {
	t.Helper()
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatal(err)
	}

	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	store := NewRedisStore(rdb)
	agent := NewIdentityAgent(store,
		[]PackageConfig{
			{
				PackageID:  "pkg-display-001",
				CampaignID: "campaign-acme",
				FrequencyRules: []FrequencyRule{
					{MaxCount: 3, Window: 24 * time.Hour},
				},
				TargetSegments: []string{"cooking", "home"},
			},
			{
				PackageID:  "pkg-display-002",
				CampaignID: "campaign-acme",
				FrequencyRules: []FrequencyRule{
					{MaxCount: 5, Window: 12 * time.Hour},
				},
			},
			{
				PackageID:  "pkg-multi-rule",
				CampaignID: "campaign-acme",
				FrequencyRules: []FrequencyRule{
					{MaxCount: 2, Window: 12 * time.Hour},
					{MaxCount: 5, Window: 7 * 24 * time.Hour},
				},
			},
			{
				PackageID: "pkg-no-cap",
			},
		},
		[]CampaignConfig{
			{
				CampaignID: "campaign-acme",
				FrequencyRules: []FrequencyRule{
					{MaxCount: 5, Window: 7 * 24 * time.Hour},
				},
			},
		},
	)

	return agent, mr
}

func TestExpose_IncrementsCounters(t *testing.T) {
	agent, mr := setupTest(t)
	defer mr.Close()
	ctx := context.Background()

	resp, err := agent.Expose(ctx, &tmp.ExposeRequest{
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

func TestExpose_CampaignFrequencyCap(t *testing.T) {
	agent, mr := setupTest(t)
	defer mr.Close()
	ctx := context.Background()

	_ = agent.LoadAudienceSegment(ctx, "cooking", []string{"user-abc"})

	for i := 0; i < 3; i++ {
		_, _ = agent.Expose(ctx, &tmp.ExposeRequest{UserToken: "user-abc", PackageID: "pkg-display-001"})
	}
	for i := 0; i < 2; i++ {
		_, _ = agent.Expose(ctx, &tmp.ExposeRequest{UserToken: "user-abc", PackageID: "pkg-display-002"})
	}

	resp, err := agent.IdentityMatch(ctx, &tmp.IdentityMatchRequest{
		RequestID:  "id-test-campaign",
		UserToken:  "user-abc",
		PackageIDs: []string{"pkg-display-001", "pkg-display-002"},
	})
	if err != nil {
		t.Fatal(err)
	}

	for _, e := range resp.Eligibility {
		if e.Eligible {
			t.Errorf("%s should be campaign-capped (5/5 across campaign)", e.PackageID)
		}
	}
}

func TestExpose_PackageCappedButCampaignNot(t *testing.T) {
	agent, mr := setupTest(t)
	defer mr.Close()
	ctx := context.Background()

	_ = agent.LoadAudienceSegment(ctx, "cooking", []string{"user-abc"})

	for i := 0; i < 3; i++ {
		_, _ = agent.Expose(ctx, &tmp.ExposeRequest{UserToken: "user-abc", PackageID: "pkg-display-001"})
	}

	resp, err := agent.IdentityMatch(ctx, &tmp.IdentityMatchRequest{
		RequestID:  "id-test-pkg-cap",
		UserToken:  "user-abc",
		PackageIDs: []string{"pkg-display-001", "pkg-display-002"},
	})
	if err != nil {
		t.Fatal(err)
	}

	byPkg := map[string]tmp.PackageEligibility{}
	for _, e := range resp.Eligibility {
		byPkg[e.PackageID] = e
	}

	if byPkg["pkg-display-001"].Eligible {
		t.Error("pkg-display-001 should be package-capped (3/3)")
	}
	if !byPkg["pkg-display-002"].Eligible {
		t.Error("pkg-display-002 should still be eligible (0/5 pkg, 3/5 campaign)")
	}
}

func TestMultipleFrequencyRules(t *testing.T) {
	agent, mr := setupTest(t)
	defer mr.Close()
	ctx := context.Background()

	_, _ = agent.Expose(ctx, &tmp.ExposeRequest{UserToken: "user-abc", PackageID: "pkg-multi-rule"})
	_, _ = agent.Expose(ctx, &tmp.ExposeRequest{UserToken: "user-abc", PackageID: "pkg-multi-rule"})

	resp, err := agent.IdentityMatch(ctx, &tmp.IdentityMatchRequest{
		RequestID:  "id-test-multi",
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

func TestSlidingWindow_OldExposuresExpire(t *testing.T) {
	agent, mr := setupTest(t)
	defer mr.Close()
	ctx := context.Background()

	_ = agent.LoadAudienceSegment(ctx, "cooking", []string{"user-abc"})

	for i := 0; i < 3; i++ {
		_, _ = agent.Expose(ctx, &tmp.ExposeRequest{UserToken: "user-abc", PackageID: "pkg-display-001"})
	}

	resp, _ := agent.IdentityMatch(ctx, &tmp.IdentityMatchRequest{
		RequestID: "id-before", UserToken: "user-abc", PackageIDs: []string{"pkg-display-001"},
	})
	if resp.Eligibility[0].Eligible {
		t.Error("should be capped (3/3 in 24h)")
	}

	mr.FastForward(25 * time.Hour)

	resp, _ = agent.IdentityMatch(ctx, &tmp.IdentityMatchRequest{
		RequestID: "id-after", UserToken: "user-abc", PackageIDs: []string{"pkg-display-001"},
	})
	if !resp.Eligibility[0].Eligible {
		t.Error("should be eligible again (exposures outside 24h sliding window)")
	}
}

func TestExpose_IntentScoreUpdated(t *testing.T) {
	agent, mr := setupTest(t)
	defer mr.Close()
	ctx := context.Background()

	_ = agent.LoadAudienceSegment(ctx, "cooking", []string{"user-abc"})
	_, _ = agent.Expose(ctx, &tmp.ExposeRequest{UserToken: "user-abc", PackageID: "pkg-display-001"})

	resp, err := agent.IdentityMatch(ctx, &tmp.IdentityMatchRequest{
		RequestID: "id-intent", UserToken: "user-abc", PackageIDs: []string{"pkg-display-001"},
	})
	if err != nil {
		t.Fatal(err)
	}

	if resp.Eligibility[0].IntentScore == nil || *resp.Eligibility[0].IntentScore < 0.99 {
		t.Error("expected high intent score after recent exposure")
	}
}

func TestAudienceMatch_NotInSegment(t *testing.T) {
	agent, mr := setupTest(t)
	defer mr.Close()
	ctx := context.Background()

	resp, _ := agent.IdentityMatch(ctx, &tmp.IdentityMatchRequest{
		RequestID: "id-audience", UserToken: "user-abc", PackageIDs: []string{"pkg-display-001"},
	})

	if resp.Eligibility[0].Eligible {
		t.Error("should NOT be eligible (not in segment)")
	}
}

func TestNoCapPackage(t *testing.T) {
	agent, mr := setupTest(t)
	defer mr.Close()
	ctx := context.Background()

	resp, _ := agent.IdentityMatch(ctx, &tmp.IdentityMatchRequest{
		RequestID: "id-nocap", UserToken: "user-abc", PackageIDs: []string{"pkg-no-cap"},
	})

	if !resp.Eligibility[0].Eligible {
		t.Error("pkg-no-cap should always be eligible")
	}
}

func TestUnknownPackage(t *testing.T) {
	agent, mr := setupTest(t)
	defer mr.Close()
	ctx := context.Background()

	resp, _ := agent.IdentityMatch(ctx, &tmp.IdentityMatchRequest{
		RequestID: "id-unknown", UserToken: "user-abc", PackageIDs: []string{"pkg-unknown"},
	})

	if resp.Eligibility[0].Eligible {
		t.Error("unknown package should not be eligible")
	}
}

// --- In-Memory Store Tests ---

func TestInMemoryStore_FullFlow(t *testing.T) {
	store := NewInMemoryStore()
	agent := NewIdentityAgent(store,
		[]PackageConfig{
			{PackageID: "pkg-1", CampaignID: "camp-1", FrequencyRules: []FrequencyRule{{MaxCount: 2, Window: time.Hour}}},
		},
		[]CampaignConfig{
			{CampaignID: "camp-1", FrequencyRules: []FrequencyRule{{MaxCount: 3, Window: 24 * time.Hour}}},
		},
	)
	ctx := context.Background()

	// Two exposures should work
	_, err := agent.Expose(ctx, &tmp.ExposeRequest{UserToken: "user-1", PackageID: "pkg-1"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = agent.Expose(ctx, &tmp.ExposeRequest{UserToken: "user-1", PackageID: "pkg-1"})
	if err != nil {
		t.Fatal(err)
	}

	// Should now be capped
	resp, err := agent.IdentityMatch(ctx, &tmp.IdentityMatchRequest{
		RequestID: "test", UserToken: "user-1", PackageIDs: []string{"pkg-1"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Eligibility[0].Eligible {
		t.Error("should be capped after 2 exposures (in-memory store)")
	}
}

func TestInMemoryStore_AudienceSegments(t *testing.T) {
	store := NewInMemoryStore()
	agent := NewIdentityAgent(store,
		[]PackageConfig{
			{PackageID: "pkg-1", TargetSegments: []string{"vip"}},
		},
		nil,
	)
	ctx := context.Background()

	// Not in segment
	resp, _ := agent.IdentityMatch(ctx, &tmp.IdentityMatchRequest{
		RequestID: "t1", UserToken: "user-1", PackageIDs: []string{"pkg-1"},
	})
	if resp.Eligibility[0].Eligible {
		t.Error("should not be eligible (not in segment)")
	}

	// Load segment
	_ = agent.LoadAudienceSegment(ctx, "vip", []string{"user-1"})

	resp, _ = agent.IdentityMatch(ctx, &tmp.IdentityMatchRequest{
		RequestID: "t2", UserToken: "user-1", PackageIDs: []string{"pkg-1"},
	})
	if !resp.Eligibility[0].Eligible {
		t.Error("should be eligible after segment load")
	}
}
