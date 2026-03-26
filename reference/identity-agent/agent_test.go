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
	agent := NewIdentityAgent(rdb,
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
					{MaxCount: 2, Window: 12 * time.Hour},  // 2 per 12h
					{MaxCount: 5, Window: 7 * 24 * time.Hour}, // AND 5 per week
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

	// 5 exposures across two packages in campaign-acme (campaign cap is 5)
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

	// 3 exposures on pkg-display-001 (package cap=3, campaign cap=5)
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

	// pkg-multi-rule: 2 per 12h AND 5 per 7d
	// Expose 2 times — should hit the 12h cap
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

	// Expose 3 times (hits package cap of 3 per 24h)
	for i := 0; i < 3; i++ {
		_, _ = agent.Expose(ctx, &tmp.ExposeRequest{UserToken: "user-abc", PackageID: "pkg-display-001"})
	}

	// Should be capped now
	resp, _ := agent.IdentityMatch(ctx, &tmp.IdentityMatchRequest{
		RequestID: "id-before", UserToken: "user-abc", PackageIDs: []string{"pkg-display-001"},
	})
	if resp.Eligibility[0].Eligible {
		t.Error("should be capped (3/3 in 24h)")
	}

	// Fast-forward miniredis by 25 hours — exposures fall outside the 24h window
	mr.FastForward(25 * time.Hour)

	// The sorted set entries still exist but their timestamps are now >24h old.
	// ZCOUNT with the sliding window cutoff should return 0.
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
