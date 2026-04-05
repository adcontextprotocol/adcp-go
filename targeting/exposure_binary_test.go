package targeting

import (
	"fmt"
	"testing"
	"time"
)

func TestBinary_EncodeAndQuery(t *testing.T) {
	log := ExposureLog{
		{ImpressionID: "imp-1", PackageID: "pkg-food", CampaignID: "acme", Timestamp: 1000},
		{ImpressionID: "imp-2", PackageID: "pkg-tech", CampaignID: "acme", Timestamp: 2000},
		{ImpressionID: "imp-3", PackageID: "pkg-food", CampaignID: "acme", Timestamp: 3000},
	}

	bin := EncodeBinaryExposureLog(log)
	if bin.Len() != 3 {
		t.Fatalf("expected 3 entries, got %d", bin.Len())
	}
	if bin.Timestamp(0) != 1000 {
		t.Errorf("expected ts 1000, got %d", bin.Timestamp(0))
	}

	foodHash := hashString("pkg-food")
	latest := LatestExposureBinary(bin, foodHash)
	if latest != 3000 {
		t.Errorf("expected latest 3000 for pkg-food, got %d", latest)
	}

	rules := []FrequencyRule{{MaxCount: 2, Window: 24 * time.Hour}}
	capped := CheckFrequencyRulesBinary(bin, foodHash, false, rules, 4000)
	if !capped {
		t.Error("expected capped (2 food exposures, cap 2)")
	}
}

func TestBinary_MergeDedup(t *testing.T) {
	log1 := ExposureLog{
		{ImpressionID: "imp-1", PackageID: "pkg-food", Timestamp: 1000},
		{ImpressionID: "imp-2", PackageID: "pkg-tech", Timestamp: 2000},
	}
	log2 := ExposureLog{
		{ImpressionID: "imp-1", PackageID: "pkg-food", Timestamp: 1000}, // dup
		{ImpressionID: "imp-3", PackageID: "pkg-food", Timestamp: 3000},
	}

	bin1 := EncodeBinaryExposureLog(log1)
	bin2 := EncodeBinaryExposureLog(log2)
	merged := MergeBinaryLogs(bin1, bin2)

	if merged.Len() != 3 {
		t.Fatalf("expected 3 after dedup, got %d", merged.Len())
	}
}

func TestScale_JSONvsBinary(t *testing.T) {
	// Build 1,500 exposures.
	var entries ExposureLog
	now := time.Now()
	for i := range 1500 {
		entries = append(entries, ExposureEntry{
			ImpressionID: fmt.Sprintf("imp-%d", i),
			PackageID:    fmt.Sprintf("pkg-%d", i%10),
			CampaignID:   fmt.Sprintf("campaign-%d", i%4),
			Timestamp:    now.Add(-time.Duration(i) * time.Minute).Unix(),
		})
	}

	jsonData := SerializeExposureLog(entries)
	binData := EncodeBinaryExposureLog(entries)

	t.Logf("")
	t.Logf("=== JSON vs Binary Exposure Log (1,500 entries) ===")
	t.Logf("")
	t.Logf("  JSON size:   %d bytes (%.1f KB)", len(jsonData), float64(len(jsonData))/1024)
	t.Logf("  Binary size: %d bytes (%.1f KB)", len(binData), float64(len(binData))/1024)
	t.Logf("  Reduction:   %.0f%%", (1-float64(len(binData))/float64(len(jsonData)))*100)
	t.Logf("")

	const iterations = 5000
	pkgHash := hashString("pkg-0")
	campHash := hashString("campaign-0")
	rules := []FrequencyRule{{MaxCount: 50, Window: 24 * time.Hour}}
	nowUnix := now.Unix()

	// JSON parse benchmark.
	start := time.Now()
	for range iterations {
		log := ParseExposureLog(jsonData)
		CheckFrequencyRules(log, "pkg", "pkg-0", rules, now)
		LatestExposureTime(log, "pkg-0")
	}
	jsonTime := time.Since(start)

	// Binary benchmark (no parse, direct query).
	start = time.Now()
	for range iterations {
		// Binary is already in memory — no parse step.
		CheckFrequencyRulesBinary(binData, pkgHash, false, rules, nowUnix)
		LatestExposureBinary(binData, pkgHash)
	}
	binaryTime := time.Since(start)

	// JSON parse + merge (2 UIDs).
	jsonData2 := SerializeExposureLog(entries[:750])
	jsonData3 := SerializeExposureLog(entries[750:])
	start = time.Now()
	for range iterations {
		log1 := ParseExposureLog(jsonData2)
		log2 := ParseExposureLog(jsonData3)
		merged := MergeExposureLogs(log1, log2)
		CheckFrequencyRules(merged, "pkg", "pkg-0", rules, now)
	}
	jsonMergeTime := time.Since(start)

	// Binary merge (2 UIDs).
	binData2 := EncodeBinaryExposureLog(entries[:750])
	binData3 := EncodeBinaryExposureLog(entries[750:])
	start = time.Now()
	for range iterations {
		merged := MergeBinaryLogs(binData2, binData3)
		CheckFrequencyRulesBinary(merged, pkgHash, false, rules, nowUnix)
	}
	binaryMergeTime := time.Since(start)

	// Binary lazy dedup (no upfront merge, dedup during scan).
	start = time.Now()
	for range iterations {
		CheckFrequencyRulesMultiLog([]BinaryExposureLog{binData2, binData3}, pkgHash, false, rules, nowUnix)
		LatestExposureMultiLog([]BinaryExposureLog{binData2, binData3}, pkgHash)
	}
	binaryLazyTime := time.Since(start)

	// Campaign check.
	start = time.Now()
	for range iterations {
		CheckFrequencyRulesBinary(binData, campHash, true, rules, nowUnix)
	}
	binaryCampTime := time.Since(start)

	t.Logf("  Single UID (parse + fcap + intent):")
	t.Logf("    JSON:   %v/eval", jsonTime/iterations)
	t.Logf("    Binary: %v/eval", binaryTime/iterations)
	t.Logf("    Speedup: %.1fx", float64(jsonTime)/float64(binaryTime))
	t.Logf("")
	t.Logf("  Two UIDs (parse + merge + fcap):")
	t.Logf("    JSON:          %v/eval", jsonMergeTime/iterations)
	t.Logf("    Binary merge:  %v/eval", binaryMergeTime/iterations)
	t.Logf("    Binary lazy:   %v/eval", binaryLazyTime/iterations)
	t.Logf("    JSON→merge:    %.1fx speedup", float64(jsonMergeTime)/float64(binaryMergeTime))
	t.Logf("    JSON→lazy:     %.1fx speedup", float64(jsonMergeTime)/float64(binaryLazyTime))
	t.Logf("    Merge→lazy:    %.1fx speedup", float64(binaryMergeTime)/float64(binaryLazyTime))
	t.Logf("")
	t.Logf("  Campaign fcap (binary, 1500 entries):")
	t.Logf("    %v/eval", binaryCampTime/iterations)
	t.Logf("")
}
