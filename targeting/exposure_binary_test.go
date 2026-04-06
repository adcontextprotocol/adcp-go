package targeting

import (
	"encoding/binary"
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

func TestBinary_VersionHeader(t *testing.T) {
	log := ExposureLog{
		{ImpressionID: "imp-1", PackageID: "pkg-food", CampaignID: "acme", Timestamp: 1000},
	}
	bin := EncodeBinaryExposureLog(log)

	if bin.Version() != 2 {
		t.Errorf("expected version 2, got %d", bin.Version())
	}
	if bin.EntrySize() != 40 {
		t.Errorf("expected entry size 40, got %d", bin.EntrySize())
	}
	// 4-byte header + 1*40 = 44 bytes total.
	if len(bin) != 44 {
		t.Errorf("expected 44 bytes, got %d", len(bin))
	}
	if err := ValidateBinaryLog(bin); err != nil {
		t.Errorf("valid log failed validation: %v", err)
	}
}

func TestBinary_ValidateRejectsInvalid(t *testing.T) {
	tests := []struct {
		name string
		data BinaryExposureLog
		err  error
	}{
		{"too short", BinaryExposureLog{0x01}, ErrBinaryTooShort},
		{"unknown version", BinaryExposureLog{0x03, 0x00, 0x28, 0x00}, ErrBinaryUnknownVersion},
		{"zero entry size", BinaryExposureLog{0x02, 0x00, 0x00, 0x00}, ErrBinaryCorrupt},
		{"wrong entry size v2", BinaryExposureLog{0x02, 0x00, 0x10, 0x00}, ErrBinaryCorrupt},
		{"wrong entry size v1", BinaryExposureLog{0x01, 0x00, 0x10, 0x00}, ErrBinaryCorrupt},
		{"unaligned payload v2", append(
			BinaryExposureLog{0x02, 0x00, 0x28, 0x00}, // valid v2 header
			make([]byte, 15)...,                         // 15 bytes, not multiple of 40
		), ErrBinaryCorrupt},
		{"unaligned payload v1", append(
			BinaryExposureLog{0x01, 0x00, 0x20, 0x00}, // valid v1 header
			make([]byte, 15)...,                         // 15 bytes, not multiple of 32
		), ErrBinaryCorrupt},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateBinaryLog(tt.data)
			if err != tt.err {
				t.Errorf("expected %v, got %v", tt.err, err)
			}
		})
	}
}

func TestBinary_EmptyLog(t *testing.T) {
	bin := EncodeBinaryExposureLog(nil)
	if bin.Len() != 0 {
		t.Errorf("expected 0 entries, got %d", bin.Len())
	}
	if err := ValidateBinaryLog(bin); err != nil {
		t.Errorf("empty log failed validation: %v", err)
	}
}

func TestBinary_MergedLogIsVersioned(t *testing.T) {
	log1 := EncodeBinaryExposureLog(ExposureLog{
		{ImpressionID: "imp-1", PackageID: "pkg-a", Timestamp: 1000},
	})
	log2 := EncodeBinaryExposureLog(ExposureLog{
		{ImpressionID: "imp-2", PackageID: "pkg-b", Timestamp: 2000},
	})
	merged := MergeBinaryLogs(log1, log2)
	if err := ValidateBinaryLog(merged); err != nil {
		t.Errorf("merged log failed validation: %v", err)
	}
	if merged.Version() != 2 {
		t.Errorf("expected version 2 on merged log, got %d", merged.Version())
	}
	if merged.Len() != 2 {
		t.Errorf("expected 2 entries, got %d", merged.Len())
	}
}

func TestBinary_Truncate(t *testing.T) {
	var entries ExposureLog
	for i := range 100 {
		entries = append(entries, ExposureEntry{
			ImpressionID: fmt.Sprintf("imp-%d", i),
			PackageID:    "pkg-a",
			Timestamp:    int64(i),
		})
	}
	bin := EncodeBinaryExposureLog(entries)

	truncated := TruncateBinaryLog(bin, 10)
	if err := ValidateBinaryLog(truncated); err != nil {
		t.Fatalf("truncated log failed validation: %v", err)
	}
	if truncated.Len() != 10 {
		t.Errorf("expected 10 entries, got %d", truncated.Len())
	}
	// Should keep the last 10 (timestamps 90-99).
	if truncated.Timestamp(0) != 90 {
		t.Errorf("expected first kept timestamp 90, got %d", truncated.Timestamp(0))
	}
	if truncated.Timestamp(9) != 99 {
		t.Errorf("expected last kept timestamp 99, got %d", truncated.Timestamp(9))
	}

	// No-op when under limit.
	same := TruncateBinaryLog(bin, 200)
	if same.Len() != 100 {
		t.Errorf("expected no truncation, got %d entries", same.Len())
	}
}

func TestBinary_MergeEmptyReturnsValidLog(t *testing.T) {
	merged := MergeBinaryLogs()
	if err := ValidateBinaryLog(merged); err != nil {
		t.Errorf("merge of nothing should produce valid empty log: %v", err)
	}
	if merged.Len() != 0 {
		t.Errorf("expected 0 entries, got %d", merged.Len())
	}
}

func TestBinary_SourceHash(t *testing.T) {
	log := ExposureLog{
		{ImpressionID: "imp-1", PackageID: "pkg-food", CampaignID: "acme", SourceID: "agent-cnn", Timestamp: 1000},
		{ImpressionID: "imp-2", PackageID: "pkg-tech", CampaignID: "acme", SourceID: "agent-nyt", Timestamp: 2000},
	}
	bin := EncodeBinaryExposureLog(log)

	cnnHash := hashString("agent-cnn")
	nytHash := hashString("agent-nyt")

	if bin.SourceHash(0) != cnnHash {
		t.Errorf("entry 0: expected source hash %d, got %d", cnnHash, bin.SourceHash(0))
	}
	if bin.SourceHash(1) != nytHash {
		t.Errorf("entry 1: expected source hash %d, got %d", nytHash, bin.SourceHash(1))
	}
}

func TestBinary_SourceHashOfEmptyString(t *testing.T) {
	log := ExposureLog{
		{ImpressionID: "imp-1", PackageID: "pkg-food", Timestamp: 1000},
	}
	bin := EncodeBinaryExposureLog(log)
	emptyHash := hashString("")
	if bin.SourceHash(0) != emptyHash {
		t.Errorf("expected hash of empty string, got %d", bin.SourceHash(0))
	}
}

func TestBinary_V1ReadCompatibility(t *testing.T) {
	// Construct a v1 binary log manually (32-byte entries).
	v1 := make([]byte, binaryHeaderSize+2*binaryEntrySize1)
	binary.LittleEndian.PutUint16(v1[0:], binaryVersion1)
	binary.LittleEndian.PutUint16(v1[2:], binaryEntrySize1)

	// Entry 0: ts=1000, imp=hash("imp-1"), pkg=hash("pkg-a"), camp=hash("c1")
	off := binaryHeaderSize
	binary.LittleEndian.PutUint64(v1[off:], 1000)
	binary.LittleEndian.PutUint64(v1[off+8:], hashString("imp-1"))
	binary.LittleEndian.PutUint64(v1[off+16:], hashString("pkg-a"))
	binary.LittleEndian.PutUint64(v1[off+24:], hashString("c1"))

	// Entry 1: ts=2000
	off = binaryHeaderSize + binaryEntrySize1
	binary.LittleEndian.PutUint64(v1[off:], 2000)
	binary.LittleEndian.PutUint64(v1[off+8:], hashString("imp-2"))
	binary.LittleEndian.PutUint64(v1[off+16:], hashString("pkg-b"))
	binary.LittleEndian.PutUint64(v1[off+24:], hashString("c1"))

	blog := BinaryExposureLog(v1)

	// Validation passes.
	if err := ValidateBinaryLog(blog); err != nil {
		t.Fatalf("v1 log should validate: %v", err)
	}
	if blog.Len() != 2 {
		t.Fatalf("expected 2 entries, got %d", blog.Len())
	}
	if blog.Timestamp(0) != 1000 {
		t.Errorf("expected ts 1000, got %d", blog.Timestamp(0))
	}
	// V1 source hash returns 0.
	if blog.SourceHash(0) != 0 {
		t.Errorf("v1 source hash should be 0, got %d", blog.SourceHash(0))
	}

	// Frequency check works on v1 logs.
	rules := []FrequencyRule{{MaxCount: 1, Window: 24 * time.Hour}}
	capped := CheckFrequencyRulesBinary(blog, hashString("pkg-a"), false, rules, 3000)
	if !capped {
		t.Error("expected capped for pkg-a (1 exposure, cap 1)")
	}
}

func TestBinary_V1UpgradeOnMerge(t *testing.T) {
	// Build a v1 log.
	v1 := make([]byte, binaryHeaderSize+1*binaryEntrySize1)
	binary.LittleEndian.PutUint16(v1[0:], binaryVersion1)
	binary.LittleEndian.PutUint16(v1[2:], binaryEntrySize1)
	off := binaryHeaderSize
	binary.LittleEndian.PutUint64(v1[off:], 1000)
	binary.LittleEndian.PutUint64(v1[off+8:], hashString("imp-v1"))
	binary.LittleEndian.PutUint64(v1[off+16:], hashString("pkg-a"))
	binary.LittleEndian.PutUint64(v1[off+24:], hashString("c1"))

	// Build a v2 log.
	v2 := EncodeBinaryExposureLog(ExposureLog{
		{ImpressionID: "imp-v2", PackageID: "pkg-a", CampaignID: "c1", SourceID: "agent-x", Timestamp: 2000},
	})

	// Merge upgrades v1 entries to v2 format.
	merged := MergeBinaryLogs(BinaryExposureLog(v1), v2)
	if merged.Version() != 2 {
		t.Errorf("expected v2 after merge, got %d", merged.Version())
	}
	if merged.Len() != 2 {
		t.Fatalf("expected 2 entries, got %d", merged.Len())
	}
	if err := ValidateBinaryLog(merged); err != nil {
		t.Fatalf("merged log should validate: %v", err)
	}

	// V1 entry gets source hash 0, v2 entry keeps its source hash.
	if merged.SourceHash(0) != 0 {
		t.Errorf("upgraded v1 entry should have source hash 0, got %d", merged.SourceHash(0))
	}
	if merged.SourceHash(1) != hashString("agent-x") {
		t.Errorf("v2 entry should keep source hash")
	}
}

func TestBinary_V1TruncateUpgrades(t *testing.T) {
	// Build a v1 log with 20 entries, truncate to 5 — should upgrade to v2.
	v1 := make([]byte, binaryHeaderSize+20*binaryEntrySize1)
	binary.LittleEndian.PutUint16(v1[0:], binaryVersion1)
	binary.LittleEndian.PutUint16(v1[2:], binaryEntrySize1)
	for i := range 20 {
		off := binaryHeaderSize + i*binaryEntrySize1
		binary.LittleEndian.PutUint64(v1[off:], uint64(1000+i))
		binary.LittleEndian.PutUint64(v1[off+8:], hashString(fmt.Sprintf("imp-%d", i)))
		binary.LittleEndian.PutUint64(v1[off+16:], hashString("pkg-a"))
		binary.LittleEndian.PutUint64(v1[off+24:], hashString("c1"))
	}

	truncated := TruncateBinaryLog(BinaryExposureLog(v1), 5)
	if err := ValidateBinaryLog(truncated); err != nil {
		t.Fatalf("truncated log failed validation: %v", err)
	}
	if truncated.Version() != 2 {
		t.Errorf("expected v2 after truncation, got %d", truncated.Version())
	}
	if truncated.Len() != 5 {
		t.Fatalf("expected 5 entries, got %d", truncated.Len())
	}
	// Should keep last 5 entries (timestamps 1015-1019).
	if truncated.Timestamp(0) != 1015 {
		t.Errorf("expected first kept timestamp 1015, got %d", truncated.Timestamp(0))
	}
	// Upgraded entries should have source hash 0.
	if truncated.SourceHash(0) != 0 {
		t.Errorf("upgraded v1 entry should have source hash 0, got %d", truncated.SourceHash(0))
	}
}

func TestBinary_V1UnderLimitUpgrades(t *testing.T) {
	// Build a v1 log with 2 entries, under limit — should upgrade to v2 without truncation.
	v1 := make([]byte, binaryHeaderSize+2*binaryEntrySize1)
	binary.LittleEndian.PutUint16(v1[0:], binaryVersion1)
	binary.LittleEndian.PutUint16(v1[2:], binaryEntrySize1)
	for i := range 2 {
		off := binaryHeaderSize + i*binaryEntrySize1
		binary.LittleEndian.PutUint64(v1[off:], uint64(1000+i))
		binary.LittleEndian.PutUint64(v1[off+8:], hashString(fmt.Sprintf("imp-%d", i)))
		binary.LittleEndian.PutUint64(v1[off+16:], hashString("pkg-a"))
		binary.LittleEndian.PutUint64(v1[off+24:], hashString("c1"))
	}

	upgraded := TruncateBinaryLog(BinaryExposureLog(v1), 100)
	if upgraded.Version() != 2 {
		t.Errorf("expected v2 after upgrade, got %d", upgraded.Version())
	}
	if upgraded.Len() != 2 {
		t.Errorf("expected 2 entries preserved, got %d", upgraded.Len())
	}
}

func TestBinary_MultiLogWorksWithMixedVersions(t *testing.T) {
	// V1 log with 1 entry for pkg-a.
	v1 := make([]byte, binaryHeaderSize+1*binaryEntrySize1)
	binary.LittleEndian.PutUint16(v1[0:], binaryVersion1)
	binary.LittleEndian.PutUint16(v1[2:], binaryEntrySize1)
	off := binaryHeaderSize
	binary.LittleEndian.PutUint64(v1[off:], 1000)
	binary.LittleEndian.PutUint64(v1[off+8:], hashString("imp-v1"))
	binary.LittleEndian.PutUint64(v1[off+16:], hashString("pkg-a"))
	binary.LittleEndian.PutUint64(v1[off+24:], hashString("c1"))

	// V2 log with 1 entry for pkg-a (different impression).
	v2 := EncodeBinaryExposureLog(ExposureLog{
		{ImpressionID: "imp-v2", PackageID: "pkg-a", CampaignID: "c1", SourceID: "agent-x", Timestamp: 2000},
	})

	logs := []BinaryExposureLog{BinaryExposureLog(v1), v2}
	rules := []FrequencyRule{{MaxCount: 2, Window: 24 * time.Hour}}

	// 2 entries for pkg-a, cap=2 → capped.
	capped := CheckFrequencyRulesMultiLog(logs, hashString("pkg-a"), false, rules, 3000)
	if !capped {
		t.Error("expected capped with mixed v1+v2 logs")
	}

	// latest across mixed versions.
	latest := LatestExposureMultiLog(logs, hashString("pkg-a"))
	if latest != 2000 {
		t.Errorf("expected latest 2000, got %d", latest)
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

	const iterations = 1000
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
