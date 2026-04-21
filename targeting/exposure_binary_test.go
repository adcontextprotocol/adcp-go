package targeting

import (
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBinary_EncodeAndQuery(t *testing.T) {
	log := ExposureLog{
		{ImpressionID: "imp-1", CreativeID: "creative-food", CampaignID: "acme", Timestamp: 1000},
		{ImpressionID: "imp-2", CreativeID: "creative-tech", CampaignID: "acme", Timestamp: 2000},
		{ImpressionID: "imp-3", CreativeID: "creative-food", CampaignID: "acme", Timestamp: 3000},
	}

	bin := EncodeBinaryExposureLog(log)
	require.Equal(t, 3, bin.Len())
	assert.Equal(t, int64(1000), bin.Timestamp(0))

	foodHash := hashString("creative-food")
	latest := LatestExposureBinary(bin, FilterCreative, foodHash)
	assert.Equal(t, int64(3000), latest)

	rules := []FrequencyRule{{MaxCount: 2, Window: 24 * time.Hour}}
	capped := CheckFrequencyRulesBinary(bin, FilterCreative, foodHash, rules, 4000)
	assert.True(t, capped, "expected capped (2 food-creative exposures, cap 2)")
}

func TestBinary_MergeDedup(t *testing.T) {
	log1 := ExposureLog{
		{ImpressionID: "imp-1", CreativeID: "creative-food", Timestamp: 1000},
		{ImpressionID: "imp-2", CreativeID: "creative-tech", Timestamp: 2000},
	}
	log2 := ExposureLog{
		{ImpressionID: "imp-1", CreativeID: "creative-food", Timestamp: 1000}, // dup
		{ImpressionID: "imp-3", CreativeID: "creative-food", Timestamp: 3000},
	}

	bin1 := EncodeBinaryExposureLog(log1)
	bin2 := EncodeBinaryExposureLog(log2)
	merged := MergeBinaryLogs(bin1, bin2)

	assert.Equal(t, 3, merged.Len(), "expected 3 after dedup")
}

func TestBinary_VersionHeader(t *testing.T) {
	log := ExposureLog{
		{ImpressionID: "imp-1", CreativeID: "creative-food", CampaignID: "acme", Timestamp: 1000},
	}
	bin := EncodeBinaryExposureLog(log)

	assert.Equal(t, uint16(binaryVersion3), bin.Version())
	assert.Equal(t, uint16(binaryEntrySize), bin.EntrySize())
	assert.Len(t, []byte(bin), binaryHeaderSize+binaryEntrySize)
	assert.NoError(t, ValidateBinaryLog(bin))
}

func TestBinary_ValidateRejectsInvalid(t *testing.T) {
	tests := []struct {
		name string
		data BinaryExposureLog
		err  error
	}{
		{"too short", BinaryExposureLog{0x01}, ErrBinaryTooShort},
		{"unknown version", BinaryExposureLog{0x02, 0x00, 0x28, 0x00}, ErrBinaryUnknownVersion},
		{"zero entry size", BinaryExposureLog{0x03, 0x00, 0x00, 0x00}, ErrBinaryCorrupt},
		{"wrong entry size", BinaryExposureLog{0x03, 0x00, 0x10, 0x00}, ErrBinaryCorrupt},
		{"unaligned payload", append(
			BinaryExposureLog{0x03, 0x00, 0x28, 0x00}, // valid v3 header
			make([]byte, 15)...,                        // 15 bytes, not multiple of 40
		), ErrBinaryCorrupt},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateBinaryLog(tt.data)
			assert.Equal(t, tt.err, err)
		})
	}
}

func TestBinary_EmptyLog(t *testing.T) {
	bin := EncodeBinaryExposureLog(nil)
	assert.Equal(t, 0, bin.Len())
	assert.NoError(t, ValidateBinaryLog(bin))
}

func TestBinary_MergedLogIsVersioned(t *testing.T) {
	log1 := EncodeBinaryExposureLog(ExposureLog{
		{ImpressionID: "imp-1", CreativeID: "creative-a", Timestamp: 1000},
	})
	log2 := EncodeBinaryExposureLog(ExposureLog{
		{ImpressionID: "imp-2", CreativeID: "creative-b", Timestamp: 2000},
	})
	merged := MergeBinaryLogs(log1, log2)
	assert.NoError(t, ValidateBinaryLog(merged))
	assert.Equal(t, uint16(binaryVersion3), merged.Version())
	assert.Equal(t, 2, merged.Len())
}

func TestBinary_Truncate(t *testing.T) {
	var entries ExposureLog
	for i := range 100 {
		entries = append(entries, ExposureEntry{
			ImpressionID: fmt.Sprintf("imp-%d", i),
			CreativeID:   "creative-a",
			Timestamp:    int64(i),
		})
	}
	bin := EncodeBinaryExposureLog(entries)

	truncated := TruncateBinaryLog(bin, 10)
	require.NoError(t, ValidateBinaryLog(truncated))
	assert.Equal(t, 10, truncated.Len())
	// Should keep the last 10 (timestamps 90-99).
	assert.Equal(t, int64(90), truncated.Timestamp(0))
	assert.Equal(t, int64(99), truncated.Timestamp(9))

	// No-op when under limit.
	same := TruncateBinaryLog(bin, 200)
	assert.Equal(t, 100, same.Len())
}

func TestBinary_MergeEmptyReturnsValidLog(t *testing.T) {
	merged := MergeBinaryLogs()
	assert.NoError(t, ValidateBinaryLog(merged), "merge of nothing should produce valid empty log")
	assert.Equal(t, 0, merged.Len())
}

func TestBinary_DimensionHashes(t *testing.T) {
	log := ExposureLog{
		{ImpressionID: "imp-1", AdvertiserID: "adv-acme", CampaignID: "camp-1", CreativeID: "creative-food", Timestamp: 1000},
	}
	bin := EncodeBinaryExposureLog(log)

	assert.Equal(t, hashString("adv-acme"), bin.AdvertiserHash(0))
	assert.Equal(t, hashString("camp-1"), bin.CampaignHash(0))
	assert.Equal(t, hashString("creative-food"), bin.CreativeHash(0))
	assert.Equal(t, hashString("imp-1"), bin.ImpressionHash(0))
}

func TestScale_JSONvsBinary(t *testing.T) {
	// Build 1,500 exposures.
	var entries ExposureLog
	now := time.Now()
	for i := range 1500 {
		entries = append(entries, ExposureEntry{
			ImpressionID: fmt.Sprintf("imp-%d", i),
			CreativeID:   fmt.Sprintf("creative-%d", i%10),
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
	creativeHash := hashString("creative-0")
	campHash := hashString("campaign-0")
	rules := []FrequencyRule{{MaxCount: 50, Window: 24 * time.Hour}}
	nowUnix := now.Unix()

	start := time.Now()
	for range iterations {
		log := ParseExposureLog(jsonData)
		CheckFrequencyRules(log, FilterCreative, "creative-0", rules, now)
		LatestExposureTime(log, "creative-0")
	}
	jsonTime := time.Since(start)

	start = time.Now()
	for range iterations {
		CheckFrequencyRulesBinary(binData, FilterCreative, creativeHash, rules, nowUnix)
		LatestExposureBinary(binData, FilterCreative, creativeHash)
	}
	binaryTime := time.Since(start)

	jsonData2 := SerializeExposureLog(entries[:750])
	jsonData3 := SerializeExposureLog(entries[750:])
	start = time.Now()
	for range iterations {
		log1 := ParseExposureLog(jsonData2)
		log2 := ParseExposureLog(jsonData3)
		merged := MergeExposureLogs(log1, log2)
		CheckFrequencyRules(merged, FilterCreative, "creative-0", rules, now)
	}
	jsonMergeTime := time.Since(start)

	binData2 := EncodeBinaryExposureLog(entries[:750])
	binData3 := EncodeBinaryExposureLog(entries[750:])
	start = time.Now()
	for range iterations {
		merged := MergeBinaryLogs(binData2, binData3)
		CheckFrequencyRulesBinary(merged, FilterCreative, creativeHash, rules, nowUnix)
	}
	binaryMergeTime := time.Since(start)

	start = time.Now()
	for range iterations {
		CheckFrequencyRulesMultiLog([]BinaryExposureLog{binData2, binData3}, FilterCreative, creativeHash, rules, nowUnix)
		LatestExposureMultiLog([]BinaryExposureLog{binData2, binData3}, FilterCreative, creativeHash)
	}
	binaryLazyTime := time.Since(start)

	start = time.Now()
	for range iterations {
		CheckFrequencyRulesBinary(binData, FilterCampaign, campHash, rules, nowUnix)
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
