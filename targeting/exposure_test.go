package targeting

import (
	"testing"
	"time"
)

func TestParseExposureLog_Empty(t *testing.T) {
	log := ParseExposureLog("")
	if len(log) != 0 {
		t.Errorf("expected empty, got %d entries", len(log))
	}
}

func TestParseExposureLog_Valid(t *testing.T) {
	data := `[{"id":"imp-1","pkg":"pkg-food","cmp":"acme","ts":1718438400},{"id":"imp-2","pkg":"pkg-tech","ts":1718352000}]`
	log := ParseExposureLog(data)
	if len(log) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(log))
	}
	if log[0].ImpressionID != "imp-1" || log[0].PackageID != "pkg-food" {
		t.Errorf("unexpected first entry: %+v", log[0])
	}
	if log[1].CampaignID != "" {
		t.Errorf("expected empty campaign on second entry, got %q", log[1].CampaignID)
	}
}

func TestSerializeExposureLog_RoundTrip(t *testing.T) {
	original := ExposureLog{
		{ImpressionID: "imp-1", PackageID: "pkg-food", CampaignID: "acme", Timestamp: 1000},
		{ImpressionID: "imp-2", PackageID: "pkg-tech", Timestamp: 2000},
	}
	data := SerializeExposureLog(original)
	parsed := ParseExposureLog(data)
	if len(parsed) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(parsed))
	}
	if parsed[0].ImpressionID != "imp-1" || parsed[1].ImpressionID != "imp-2" {
		t.Errorf("round-trip mismatch: %+v", parsed)
	}
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
	if len(merged) != 3 {
		t.Fatalf("expected 3 entries (imp-1 deduped), got %d", len(merged))
	}
	// Should be sorted newest first.
	if merged[0].ImpressionID != "imp-3" {
		t.Errorf("expected imp-3 first (newest), got %s", merged[0].ImpressionID)
	}
}

func TestMergeExposureLogs_Disjoint(t *testing.T) {
	log1 := ExposureLog{{ImpressionID: "a", Timestamp: 100}}
	log2 := ExposureLog{{ImpressionID: "b", Timestamp: 200}}
	log3 := ExposureLog{{ImpressionID: "c", Timestamp: 300}}

	merged := MergeExposureLogs(log1, log2, log3)
	if len(merged) != 3 {
		t.Fatalf("expected 3, got %d", len(merged))
	}
}

func TestPruneExpired(t *testing.T) {
	log := ExposureLog{
		{ImpressionID: "old", Timestamp: 100},
		{ImpressionID: "new", Timestamp: 1000},
	}
	pruned := PruneExpired(log, 500) // cutoff at 500
	if len(pruned) != 1 {
		t.Fatalf("expected 1 entry after pruning, got %d", len(pruned))
	}
	if pruned[0].ImpressionID != "new" {
		t.Errorf("expected 'new' entry, got %s", pruned[0].ImpressionID)
	}
}

func TestCheckFrequencyRules_NotCapped(t *testing.T) {
	log := ExposureLog{
		{ImpressionID: "1", PackageID: "pkg-food", Timestamp: 1000},
		{ImpressionID: "2", PackageID: "pkg-food", Timestamp: 2000},
	}
	rules := []FrequencyRule{{MaxCount: 5, Window: 24 * time.Hour}}
	now := time.Unix(3000, 0)

	if CheckFrequencyRules(log, "pkg", "pkg-food", rules, now) {
		t.Error("should not be capped (2/5)")
	}
}

func TestCheckFrequencyRules_Capped(t *testing.T) {
	log := ExposureLog{
		{ImpressionID: "1", PackageID: "pkg-food", Timestamp: 1000},
		{ImpressionID: "2", PackageID: "pkg-food", Timestamp: 2000},
		{ImpressionID: "3", PackageID: "pkg-food", Timestamp: 3000},
	}
	rules := []FrequencyRule{{MaxCount: 3, Window: 24 * time.Hour}}
	now := time.Unix(4000, 0)

	if !CheckFrequencyRules(log, "pkg", "pkg-food", rules, now) {
		t.Error("should be capped (3/3)")
	}
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

	if !CheckFrequencyRules(log, "pkg", "pkg-food", rules, now) {
		t.Error("should be capped by 1h rule (2/2)")
	}
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

	if !CheckFrequencyRules(log, "cmp", "acme", rules, now) {
		t.Error("should be campaign-capped (5/5)")
	}
}

func TestCheckFrequencyRules_WindowExpiry(t *testing.T) {
	// Old exposures outside the window should not count.
	now := time.Unix(100000, 0)
	log := ExposureLog{
		{ImpressionID: "1", PackageID: "pkg-food", Timestamp: 1000},  // very old
		{ImpressionID: "2", PackageID: "pkg-food", Timestamp: 99000}, // recent
	}
	rules := []FrequencyRule{{MaxCount: 2, Window: 24 * time.Hour}} // 86400s window

	if CheckFrequencyRules(log, "pkg", "pkg-food", rules, now) {
		t.Error("should not be capped (only 1 in window)")
	}
}

func TestLatestExposureTime(t *testing.T) {
	log := ExposureLog{
		{ImpressionID: "1", PackageID: "pkg-food", Timestamp: 1000},
		{ImpressionID: "2", PackageID: "pkg-tech", Timestamp: 2000},
		{ImpressionID: "3", PackageID: "pkg-food", Timestamp: 3000},
	}
	if ts := LatestExposureTime(log, "pkg-food"); ts != 3000 {
		t.Errorf("expected 3000, got %d", ts)
	}
	if ts := LatestExposureTime(log, "pkg-missing"); ts != 0 {
		t.Errorf("expected 0 for missing package, got %d", ts)
	}
}

func TestComputeIntentScore(t *testing.T) {
	now := time.Unix(1000000, 0)

	// Just now.
	score := ComputeIntentScore(now.Unix(), now)
	if score < 0.99 {
		t.Errorf("expected ~1.0 for just now, got %.2f", score)
	}

	// 3.5 days ago.
	score = ComputeIntentScore(now.Add(-84*time.Hour).Unix(), now)
	if score < 0.49 || score > 0.51 {
		t.Errorf("expected ~0.5 for 3.5 days, got %.2f", score)
	}

	// 7 days ago.
	score = ComputeIntentScore(now.Add(-168*time.Hour).Unix(), now)
	if score != 0 {
		t.Errorf("expected 0 for 7 days, got %.2f", score)
	}

	// No exposure.
	score = ComputeIntentScore(0, now)
	if score != 0 {
		t.Errorf("expected 0 for no exposure, got %.2f", score)
	}
}

func TestMergeUserProfiles(t *testing.T) {
	p1 := &UserProfile{Segments: map[string]float64{"cooking_fans": 0.8, "sports": 0.3}}
	p2 := &UserProfile{Segments: map[string]float64{"cooking_fans": 0.5, "tech": 0.9}}

	merged := MergeUserProfiles(p1, p2)
	if merged.Segments["cooking_fans"] != 0.8 {
		t.Errorf("expected 0.8 (higher), got %.1f", merged.Segments["cooking_fans"])
	}
	if merged.Segments["sports"] != 0.3 {
		t.Errorf("expected 0.3, got %.1f", merged.Segments["sports"])
	}
	if merged.Segments["tech"] != 0.9 {
		t.Errorf("expected 0.9, got %.1f", merged.Segments["tech"])
	}
	if len(merged.Segments) != 3 {
		t.Errorf("expected 3 segments, got %d", len(merged.Segments))
	}
}

func TestMergeUserProfiles_WithNil(t *testing.T) {
	p1 := &UserProfile{Segments: map[string]float64{"cooking": 0.5}}
	merged := MergeUserProfiles(nil, p1, nil)
	if len(merged.Segments) != 1 {
		t.Errorf("expected 1 segment, got %d", len(merged.Segments))
	}
}
