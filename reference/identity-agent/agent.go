package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"math"
	"time"

	"github.com/adcontextprotocol/adcp-go/tmp"
	"github.com/redis/go-redis/v9"
)

// FrequencyRule defines a sliding window frequency cap.
// Multiple rules can be combined: "2 per 12h AND 5 per 7d".
type FrequencyRule struct {
	MaxCount int           // Maximum impressions in the window
	Window   time.Duration // Sliding window duration
}

// PackageConfig defines frequency cap and targeting for a package.
type PackageConfig struct {
	PackageID      string
	CampaignID     string          // Groups packages for cross-pub frequency capping
	FrequencyRules []FrequencyRule // Package-level caps (all must pass)
	TargetSegments []string        // User must be in ANY of these segments
}

// CampaignConfig defines campaign-level frequency caps.
type CampaignConfig struct {
	CampaignID     string
	FrequencyRules []FrequencyRule // Campaign-level caps (all must pass)
}

// IdentityAgent evaluates user eligibility using Valkey/Redis.
type IdentityAgent struct {
	rdb       *redis.Client
	packages  map[string]PackageConfig
	campaigns map[string]CampaignConfig
}

// NewIdentityAgent creates an agent with the given Redis client and configs.
func NewIdentityAgent(rdb *redis.Client, packages []PackageConfig, campaigns []CampaignConfig) *IdentityAgent {
	pkgMap := make(map[string]PackageConfig, len(packages))
	for _, p := range packages {
		pkgMap[p.PackageID] = p
	}
	campMap := make(map[string]CampaignConfig, len(campaigns))
	for _, c := range campaigns {
		campMap[c.CampaignID] = c
	}
	return &IdentityAgent{rdb: rdb, packages: pkgMap, campaigns: campMap}
}

// IdentityMatch evaluates a user against all requested packages.
func (a *IdentityAgent) IdentityMatch(ctx context.Context, req *tmp.IdentityMatchRequest) (*tmp.IdentityMatchResponse, error) {
	tokenHash := hashToken(req.UserToken)

	var eligibility []tmp.PackageEligibility
	for _, pkgID := range req.PackageIDs {
		pkg, known := a.packages[pkgID]
		if !known {
			eligibility = append(eligibility, tmp.PackageEligibility{
				PackageID: pkgID,
				Eligible:  false,
			})
			continue
		}

		eligible := true

		// Check campaign-level frequency caps (broader scope)
		if pkg.CampaignID != "" {
			if camp, ok := a.campaigns[pkg.CampaignID]; ok && len(camp.FrequencyRules) > 0 {
				key := fmt.Sprintf("freq:campaign:%s:%s", camp.CampaignID, tokenHash)
				capped, err := a.checkFrequencyRules(ctx, key, camp.FrequencyRules)
				if err != nil {
					return nil, fmt.Errorf("campaign freq cap for %s: %w", pkgID, err)
				}
				if capped {
					eligible = false
				}
			}
		}

		// Check package-level frequency caps
		if eligible && len(pkg.FrequencyRules) > 0 {
			key := fmt.Sprintf("freq:pkg:%s:%s", pkg.PackageID, tokenHash)
			capped, err := a.checkFrequencyRules(ctx, key, pkg.FrequencyRules)
			if err != nil {
				return nil, fmt.Errorf("package freq cap for %s: %w", pkgID, err)
			}
			if capped {
				eligible = false
			}
		}

		// Check audience segments
		if eligible && len(pkg.TargetSegments) > 0 {
			matched, err := a.checkAudienceMatch(ctx, tokenHash, pkg.TargetSegments)
			if err != nil {
				return nil, fmt.Errorf("audience check for %s: %w", pkgID, err)
			}
			if !matched {
				eligible = false
			}
		}

		// Compute intent score
		intent, err := a.computeIntentScore(ctx, tokenHash, pkgID)
		if err != nil {
			return nil, fmt.Errorf("intent score for %s: %w", pkgID, err)
		}

		e := tmp.PackageEligibility{
			PackageID: pkgID,
			Eligible:  eligible,
		}
		if intent > 0 {
			e.IntentScore = &intent
		}
		eligibility = append(eligibility, e)
	}

	return &tmp.IdentityMatchResponse{
		RequestID:   req.RequestID,
		Eligibility: eligibility,
	}, nil
}

// Expose records that a user was shown an ad for a package.
// Adds a timestamped entry to sorted sets for both package and campaign frequency.
// Uses sorted sets for sliding window frequency capping.
func (a *IdentityAgent) Expose(ctx context.Context, req *tmp.ExposeRequest) (*tmp.ExposeResponse, error) {
	tokenHash := hashToken(req.UserToken)
	pkg, ok := a.packages[req.PackageID]
	if !ok {
		return nil, fmt.Errorf("unknown package: %s", req.PackageID)
	}

	now := time.Now()
	ts := float64(now.UnixMilli())
	member := fmt.Sprintf("%d:%s", now.UnixNano(), req.PackageID) // Unique per exposure

	pipe := a.rdb.Pipeline()

	// Add to package-level sorted set
	pkgKey := fmt.Sprintf("freq:pkg:%s:%s", req.PackageID, tokenHash)
	pipe.ZAdd(ctx, pkgKey, redis.Z{Score: ts, Member: member})
	// Set TTL to longest window + buffer to auto-cleanup
	if len(pkg.FrequencyRules) > 0 {
		maxWindow := maxRuleWindow(pkg.FrequencyRules)
		pipe.Expire(ctx, pkgKey, maxWindow+time.Hour)
	}

	// Add to campaign-level sorted set
	campaignID := req.CampaignID
	if campaignID == "" {
		campaignID = pkg.CampaignID
	}

	var campKey string
	if campaignID != "" {
		campKey = fmt.Sprintf("freq:campaign:%s:%s", campaignID, tokenHash)
		pipe.ZAdd(ctx, campKey, redis.Z{Score: ts, Member: member})
		if camp, ok := a.campaigns[campaignID]; ok && len(camp.FrequencyRules) > 0 {
			maxWindow := maxRuleWindow(camp.FrequencyRules)
			pipe.Expire(ctx, campKey, maxWindow+time.Hour)
		}
	}

	// Record interaction for intent scoring
	intentKey := fmt.Sprintf("intent:%s:%s", req.PackageID, tokenHash)
	pipe.Set(ctx, intentKey, now.Unix(), 7*24*time.Hour)

	_, err := pipe.Exec(ctx)
	if err != nil {
		return nil, err
	}

	resp := &tmp.ExposeResponse{PackageID: req.PackageID}

	// Return campaign count for the shortest window
	if campKey != "" {
		if camp, ok := a.campaigns[campaignID]; ok && len(camp.FrequencyRules) > 0 {
			shortestRule := camp.FrequencyRules[0]
			cutoff := float64(now.Add(-shortestRule.Window).UnixMilli())
			count, _ := a.rdb.ZCount(ctx, campKey, fmt.Sprintf("%f", cutoff), "+inf").Result()
			resp.CampaignCount = int(count)
			resp.CampaignRemaining = shortestRule.MaxCount - int(count)
			if resp.CampaignRemaining < 0 {
				resp.CampaignRemaining = 0
			}
		}
	}

	return resp, nil
}

// checkFrequencyRules checks all frequency rules against a sorted set.
// Returns true (capped) if ANY rule is exceeded.
// Each rule is a sliding window: count entries within [now-window, now].
func (a *IdentityAgent) checkFrequencyRules(ctx context.Context, key string, rules []FrequencyRule) (bool, error) {
	now := time.Now()
	for _, rule := range rules {
		cutoff := float64(now.Add(-rule.Window).UnixMilli())
		count, err := a.rdb.ZCount(ctx, key, fmt.Sprintf("%f", cutoff), "+inf").Result()
		if err != nil && err != redis.Nil {
			return false, err
		}
		if int(count) >= rule.MaxCount {
			return true, nil
		}
	}
	return false, nil
}

func maxRuleWindow(rules []FrequencyRule) time.Duration {
	var max time.Duration
	for _, r := range rules {
		if r.Window > max {
			max = r.Window
		}
	}
	return max
}

func (a *IdentityAgent) checkAudienceMatch(ctx context.Context, tokenHash string, segments []string) (bool, error) {
	for _, seg := range segments {
		key := fmt.Sprintf("audience:%s", seg)
		member, err := a.rdb.SIsMember(ctx, key, tokenHash).Result()
		if err != nil {
			return false, err
		}
		if member {
			return true, nil
		}
	}
	return false, nil
}

func (a *IdentityAgent) computeIntentScore(ctx context.Context, tokenHash, packageID string) (float64, error) {
	key := fmt.Sprintf("intent:%s:%s", packageID, tokenHash)
	ts, err := a.rdb.Get(ctx, key).Int64()
	if err == redis.Nil {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	hoursSince := time.Since(time.Unix(ts, 0)).Hours()
	score := 1.0 - (hoursSince / 168.0)
	return math.Max(0, score), nil
}

// --- Data Sync Helpers ---

// LoadAudienceSegment bulk-loads user tokens into an audience segment set.
func (a *IdentityAgent) LoadAudienceSegment(ctx context.Context, segmentID string, userTokens []string) error {
	key := fmt.Sprintf("audience:%s", segmentID)
	members := make([]interface{}, len(userTokens))
	for i, tok := range userTokens {
		members[i] = hashToken(tok)
	}
	return a.rdb.SAdd(ctx, key, members...).Err()
}

func hashToken(token string) string {
	h := sha256.Sum256([]byte(token))
	return hex.EncodeToString(h[:16])
}
