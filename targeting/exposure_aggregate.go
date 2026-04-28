package targeting

// PreaggregatedExposures partitions a user's exposure log entries by filter
// hash for fast per-package eligibility checks. Trades a one-time build pass
// (O(total log entries)) for O(matches-per-filter) per-package lookups,
// instead of the O(packages × total log entries) cost of scanning all logs
// for every package check.
//
// Use when the eligibility evaluator will check many packages against the
// same logs. For small-package requests, the naive CheckFrequencyRulesMultiLog
// is faster because the build overhead isn't amortized — at 10 packages ×
// 10K-entry log × 3 identities the build cost more than triples per-request
// CPU. ShouldPreaggregate gates the choice based on the empirical crossover.
type PreaggregatedExposures struct {
	// Per-key bucket of entries that match that hash. Each entry carries the
	// timestamp and impression hash so per-rule window filtering and
	// per-impression dedup can run on the much smaller bucket.
	byCampaign map[uint64][]aggEntry
	byPackage  map[uint64][]aggEntry
	// Per-package latest timestamp; precomputed once during the build pass
	// so intent-score lookups don't have to re-scan the log per package.
	latestByPackage map[uint64]int64
}

type aggEntry struct {
	impHash   uint64
	timestamp int64
}

// BuildPreaggregatedExposures partitions the entries across all the user's
// identity logs into per-(campaign|package) buckets and precomputes
// per-package latest timestamps. Cost: O(L × I) where L is total entries
// per identity and I is number of identities.
func BuildPreaggregatedExposures(logs []BinaryExposureLog) *PreaggregatedExposures {
	// Pre-size maps to reduce growth allocations; assume a typical user has
	// O(distinct_campaigns) << total_entries.
	totalEntries := 0
	for _, log := range logs {
		totalEntries += log.Len()
	}
	cap := totalEntries / 4
	if cap < 8 {
		cap = 8
	}
	pa := &PreaggregatedExposures{
		byCampaign:      make(map[uint64][]aggEntry, cap),
		byPackage:       make(map[uint64][]aggEntry, cap),
		latestByPackage: make(map[uint64]int64, cap),
	}

	for _, log := range logs {
		n := log.Len()
		for i := 0; i < n; i++ {
			ts := log.Timestamp(i)
			imp := log.ImpressionHash(i)
			campH := log.CampaignHash(i)
			pkgH := log.PackageHash(i)
			entry := aggEntry{impHash: imp, timestamp: ts}
			if campH != 0 {
				pa.byCampaign[campH] = append(pa.byCampaign[campH], entry)
			}
			pa.byPackage[pkgH] = append(pa.byPackage[pkgH], entry)
			if cur, ok := pa.latestByPackage[pkgH]; !ok || ts > cur {
				pa.latestByPackage[pkgH] = ts
			}
		}
	}
	return pa
}

// LatestExposureAggregated returns the latest timestamp recorded for the
// given package hash. Equivalent to LatestExposureMultiLog but reads from
// the precomputed index, O(1) per call.
func LatestExposureAggregated(agg *PreaggregatedExposures, pkgHash uint64) int64 {
	return agg.latestByPackage[pkgHash]
}

// CheckFrequencyRulesAggregated checks frequency rules against a
// pre-aggregated bucket. Equivalent to CheckFrequencyRulesMultiLog but
// avoids re-scanning the full log per check.
func CheckFrequencyRulesAggregated(agg *PreaggregatedExposures, filterHash uint64, isCampaign bool, rules []FrequencyRule, nowUnix int64) bool {
	var bucket []aggEntry
	if isCampaign {
		bucket = agg.byCampaign[filterHash]
	} else {
		bucket = agg.byPackage[filterHash]
	}
	if len(bucket) == 0 {
		return false
	}
	for _, rule := range rules {
		cutoff := nowUnix - int64(rule.Window.Seconds())
		seen := make(map[uint64]struct{})
		count := 0
		for i := range bucket {
			if bucket[i].timestamp < cutoff {
				continue
			}
			if _, dup := seen[bucket[i].impHash]; dup {
				continue
			}
			seen[bucket[i].impHash] = struct{}{}
			count++
			if count >= rule.MaxCount {
				return true
			}
		}
	}
	return false
}

// PreaggregatePackagesThreshold is the per-request candidate-package count
// above which the eligibility evaluator should build a preaggregated view of
// the user's exposure log before per-package checks.
//
// Empirically tuned (TestPreaggregate_Crossover): below ~50 packages, the
// map-build allocation overhead per log entry dominates and naive scanning
// is faster regardless of log size — at 10 packages × 10K log × 3 identities,
// preagg is ~3× slower (1.25ms vs 408µs). Above ~50 packages, preagg
// amortizes — at 1000 packages × 1000-entry log × 3 identities the speedup
// is ~26×; at 1000 × 10K × 3 identities, ~40×.
const PreaggregatePackagesThreshold = 50

// ShouldPreaggregate returns whether the eligibility evaluator should build
// a preaggregated view before per-package checks. Cheap to evaluate; safe to
// call on every request.
func ShouldPreaggregate(numPackages int) bool {
	return numPackages > PreaggregatePackagesThreshold
}
