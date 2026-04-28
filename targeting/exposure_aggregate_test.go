package targeting

import (
	"fmt"
	"testing"
	"time"
)

// TestPreaggregate_Crossover measures naive vs preaggregated frequency-cap
// evaluation across the (packages × log_entries × identities) matrix to
// determine where the heuristic threshold should sit.
func TestPreaggregate_Crossover(t *testing.T) {
	pkgCounts := []int{10, 100, 1000}
	logSizes := []int{0, 100, 1000, 10000}
	identityCounts := []int{1, 3}

	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC).Unix()
	rules := []FrequencyRule{{MaxCount: 1_000_000, Window: 24 * time.Hour}}

	t.Log("")
	t.Log("=== Naive scan vs preaggregated lookup, frequency-cap eligibility ===")
	t.Log("")
	t.Logf("  %-9s %-9s %-3s %-12s %-12s %-12s",
		"packages", "log_size", "ids", "naive (ns)", "preagg (ns)", "ratio")
	t.Logf("  %-9s %-9s %-3s %-12s %-12s %-12s",
		"--------", "--------", "---", "----------", "-----------", "-----")

	for _, numPkgs := range pkgCounts {
		for _, logSize := range logSizes {
			for _, numIds := range identityCounts {
				// Build per-identity binary logs of `logSize` entries, distributed
				// across numPkgs packages with ~10 distinct campaigns.
				logs := make([]BinaryExposureLog, numIds)
				for i := 0; i < numIds; i++ {
					entries := make(ExposureLog, 0, logSize)
					for j := 0; j < logSize; j++ {
						pkgIdx := j % numPkgs
						entries = append(entries, ExposureEntry{
							ImpressionID: fmt.Sprintf("imp-%d-%d", i, j),
							PackageID:    fmt.Sprintf("pkg-%d", pkgIdx),
							CampaignID:   fmt.Sprintf("camp-%d", pkgIdx%10),
							SourceID:     "bench",
							Timestamp:    now - int64(j*60),
						})
					}
					logs[i] = EncodeBinaryExposureLog(entries)
				}

				pkgHashes := make([]uint64, numPkgs)
				for i := 0; i < numPkgs; i++ {
					pkgHashes[i] = hashString(fmt.Sprintf("pkg-%d", i))
				}

				const iters = 200

				// Naive timing: re-scan all logs per package check.
				start := time.Now()
				for it := 0; it < iters; it++ {
					for _, h := range pkgHashes {
						_ = CheckFrequencyRulesMultiLog(logs, h, false, rules, now)
					}
				}
				naive := time.Since(start) / time.Duration(iters)

				// Preaggregated timing: build once per request, then cheap lookups.
				start = time.Now()
				for it := 0; it < iters; it++ {
					agg := BuildPreaggregatedExposures(logs)
					for _, h := range pkgHashes {
						_ = CheckFrequencyRulesAggregated(agg, h, false, rules, now)
					}
				}
				preagg := time.Since(start) / time.Duration(iters)

				ratio := float64(naive) / float64(preagg)

				t.Logf("  %-9d %-9d %-3d %-12d %-12d %-12.2fx",
					numPkgs, logSize, numIds,
					naive.Nanoseconds(), preagg.Nanoseconds(), ratio)
			}
		}
	}
	t.Log("")
}
