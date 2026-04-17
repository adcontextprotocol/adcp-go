package exposure

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseExposureLog_Empty(t *testing.T) {
	log := ParseExposureLog("")
	assert.Empty(t, log)
}

func TestParseExposureLog_Valid(t *testing.T) {
	data := `[{"id":"imp-1","pkg":"pkg-food","cmp":"acme","ts":1718438400},{"id":"imp-2","pkg":"pkg-tech","ts":1718352000}]`
	log := ParseExposureLog(data)
	require.Len(t, log, 2)
	assert.Equal(t, "imp-1", log[0].ImpressionID)
	assert.Equal(t, "pkg-food", log[0].PackageID)
	assert.Empty(t, log[1].CampaignID)
}

func TestSerializeExposureLog_RoundTrip(t *testing.T) {
	original := ExposureLog{
		{ImpressionID: "imp-1", PackageID: "pkg-food", CampaignID: "acme", Timestamp: 1000},
		{ImpressionID: "imp-2", PackageID: "pkg-tech", Timestamp: 2000},
	}
	data := SerializeExposureLog(original)
	parsed := ParseExposureLog(data)
	require.Len(t, parsed, 2)
	assert.Equal(t, "imp-1", parsed[0].ImpressionID)
	assert.Equal(t, "imp-2", parsed[1].ImpressionID)
}

func TestMergeExposureLogs_Dedup(t *testing.T) {
	log1 := ExposureLog{
		{ImpressionID: "imp-1", PackageID: "pkg-food", Timestamp: 1000},
		{ImpressionID: "imp-2", PackageID: "pkg-tech", Timestamp: 2000},
	}
	log2 := ExposureLog{
		{ImpressionID: "imp-1", PackageID: "pkg-food", Timestamp: 1000}, // dup
		{ImpressionID: "imp-3", PackageID: "pkg-food", Timestamp: 3000},
	}

	merged := MergeExposureLogs(log1, log2)
	require.Len(t, merged, 3, "expected 3 entries (imp-1 deduped)")
	// Should be sorted newest first.
	assert.Equal(t, "imp-3", merged[0].ImpressionID, "expected imp-3 first (newest)")
}

func TestMergeExposureLogs_Disjoint(t *testing.T) {
	log1 := ExposureLog{{ImpressionID: "a", Timestamp: 100}}
	log2 := ExposureLog{{ImpressionID: "b", Timestamp: 200}}
	log3 := ExposureLog{{ImpressionID: "c", Timestamp: 300}}

	merged := MergeExposureLogs(log1, log2, log3)
	assert.Len(t, merged, 3)
}

func TestPruneExpired(t *testing.T) {
	log := ExposureLog{
		{ImpressionID: "old", Timestamp: 100},
		{ImpressionID: "new", Timestamp: 1000},
	}
	pruned := PruneExpired(log, 500) // cutoff at 500
	require.Len(t, pruned, 1)
	assert.Equal(t, "new", pruned[0].ImpressionID)
}

func TestCheckFrequencyRules_NotCapped(t *testing.T) {
	log := ExposureLog{
		{ImpressionID: "1", PackageID: "pkg-food", Timestamp: 1000},
		{ImpressionID: "2", PackageID: "pkg-food", Timestamp: 2000},
	}
	rules := []FrequencyRule{{MaxCount: 5, Window: 24 * time.Hour}}
	now := time.Unix(3000, 0)

	assert.False(t, CheckFrequencyRules(log, "pkg", "pkg-food", rules, now), "should not be capped (2/5)")
}

func TestCheckFrequencyRules_Capped(t *testing.T) {
	log := ExposureLog{
		{ImpressionID: "1", PackageID: "pkg-food", Timestamp: 1000},
		{ImpressionID: "2", PackageID: "pkg-food", Timestamp: 2000},
		{ImpressionID: "3", PackageID: "pkg-food", Timestamp: 3000},
	}
	rules := []FrequencyRule{{MaxCount: 3, Window: 24 * time.Hour}}
	now := time.Unix(4000, 0)

	assert.True(t, CheckFrequencyRules(log, "pkg", "pkg-food", rules, now), "should be capped (3/3)")
}

func TestCheckFrequencyRules_MultiRule(t *testing.T) {
	// 2 per 1 hour AND 5 per day.
	// User has 2 exposures in last hour — should be capped by first rule.
	now := time.Unix(10000, 0)
	log := ExposureLog{
		{ImpressionID: "1", PackageID: "pkg-food", Timestamp: 9500},
		{ImpressionID: "2", PackageID: "pkg-food", Timestamp: 9800},
	}
	rules := []FrequencyRule{
		{MaxCount: 2, Window: time.Hour},
		{MaxCount: 5, Window: 24 * time.Hour},
	}

	assert.True(t, CheckFrequencyRules(log, "pkg", "pkg-food", rules, now), "should be capped by 1h rule (2/2)")
}

func TestCheckFrequencyRules_CampaignLevel(t *testing.T) {
	// 5 per campaign across multiple packages.
	log := ExposureLog{
		{ImpressionID: "1", PackageID: "pkg-a", CampaignID: "acme", Timestamp: 1000},
		{ImpressionID: "2", PackageID: "pkg-b", CampaignID: "acme", Timestamp: 2000},
		{ImpressionID: "3", PackageID: "pkg-a", CampaignID: "acme", Timestamp: 3000},
		{ImpressionID: "4", PackageID: "pkg-c", CampaignID: "acme", Timestamp: 4000},
		{ImpressionID: "5", PackageID: "pkg-a", CampaignID: "acme", Timestamp: 5000},
	}
	rules := []FrequencyRule{{MaxCount: 5, Window: 7 * 24 * time.Hour}}
	now := time.Unix(6000, 0)

	assert.True(t, CheckFrequencyRules(log, "cmp", "acme", rules, now), "should be campaign-capped (5/5)")
}

func TestCheckFrequencyRules_WindowExpiry(t *testing.T) {
	// Old exposures outside the window should not count.
	now := time.Unix(100000, 0)
	log := ExposureLog{
		{ImpressionID: "1", PackageID: "pkg-food", Timestamp: 1000},  // very old
		{ImpressionID: "2", PackageID: "pkg-food", Timestamp: 99000}, // recent
	}
	rules := []FrequencyRule{{MaxCount: 2, Window: 24 * time.Hour}} // 86400s window

	assert.False(t, CheckFrequencyRules(log, "pkg", "pkg-food", rules, now), "should not be capped (only 1 in window)")
}

func TestLatestExposureTime(t *testing.T) {
	log := ExposureLog{
		{ImpressionID: "1", PackageID: "pkg-food", Timestamp: 1000},
		{ImpressionID: "2", PackageID: "pkg-tech", Timestamp: 2000},
		{ImpressionID: "3", PackageID: "pkg-food", Timestamp: 3000},
	}
	assert.Equal(t, int64(3000), LatestExposureTime(log, "pkg-food"))
	assert.Equal(t, int64(0), LatestExposureTime(log, "pkg-missing"))
}

func TestComputeIntentScore(t *testing.T) {
	now := time.Unix(1000000, 0)

	tests := []struct {
		name     string
		lastTS   int64
		minScore float64
		maxScore float64
		exact    *float64
	}{
		{"just now", now.Unix(), 0.99, 1.01, nil},
		{"3.5 days ago", now.Add(-84 * time.Hour).Unix(), 0.49, 0.51, nil},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			score := ComputeIntentScore(tt.lastTS, now)
			assert.GreaterOrEqual(t, score, tt.minScore)
			assert.LessOrEqual(t, score, tt.maxScore)
		})
	}

	// Exact zero cases.
	t.Run("7 days ago", func(t *testing.T) {
		score := ComputeIntentScore(now.Add(-168*time.Hour).Unix(), now)
		assert.Equal(t, float64(0), score)
	})
	t.Run("no exposure", func(t *testing.T) {
		score := ComputeIntentScore(0, now)
		assert.Equal(t, float64(0), score)
	})
}

func TestMergeUserProfiles(t *testing.T) {
	p1 := &UserProfile{Segments: map[string]float64{"cooking_fans": 0.8, "sports": 0.3}}
	p2 := &UserProfile{Segments: map[string]float64{"cooking_fans": 0.5, "tech": 0.9}}

	merged := MergeUserProfiles(p1, p2)
	assert.Equal(t, 0.8, merged.Segments["cooking_fans"], "expected higher value")
	assert.Equal(t, 0.3, merged.Segments["sports"])
	assert.Equal(t, 0.9, merged.Segments["tech"])
	assert.Len(t, merged.Segments, 3)
}

func TestMergeUserProfiles_WithNil(t *testing.T) {
	p1 := &UserProfile{Segments: map[string]float64{"cooking": 0.5}}
	merged := MergeUserProfiles(nil, p1, nil)
	assert.Len(t, merged.Segments, 1)
}
