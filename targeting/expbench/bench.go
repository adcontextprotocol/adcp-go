package expbench

import (
	"context"
	"fmt"
	"math/rand/v2"
	"sort"
	"time"
)

// LoadProfile describes a synthetic user's exposure log shape.
type LoadProfile struct {
	Name              string
	NumImpressions    int           // total impressions over Window
	Window            time.Duration // spread evenly over this duration ending at "now"
	KeysPerImpression int           // how many fcap_keys each impression carries
}

// GenerateLog produces a deterministic synthetic exposure log for the
// profile, ending at endTimestamp (unix seconds). Impressions are spread
// uniformly across the window. Each impression's fcap_keys are drawn from
// a pool large enough to make the keys diverse but small enough to make
// them re-occur (so frequency caps actually fire in the bench).
func GenerateLog(profile LoadProfile, endTimestamp int64, seed uint64) []Impression {
	r := rand.New(rand.NewPCG(seed, seed^0xdeadbeef))
	imps := make([]Impression, profile.NumImpressions)
	windowSec := int64(profile.Window.Seconds())
	startTS := endTimestamp - windowSec

	// Pool of distinct fcap_keys spanning ~3 dimensions.
	const numCampaigns = 50
	const numAdvertisers = 20
	const numCreatives = 200
	const numLineItems = 100

	for i := range imps {
		// Spread timestamps roughly uniformly over the window with a small
		// jitter, then sort.
		ts := startTS + int64(float64(windowSec)*float64(i)/float64(profile.NumImpressions))
		ts += int64(r.IntN(60)) // up to 1 minute jitter

		keys := make([]string, 0, profile.KeysPerImpression)
		// Always include a campaign and advertiser; fill the rest with creatives/line-items.
		keys = append(keys, fmt.Sprintf("campaign:%d", r.IntN(numCampaigns)))
		if profile.KeysPerImpression >= 2 {
			keys = append(keys, fmt.Sprintf("advertiser:%d", r.IntN(numAdvertisers)))
		}
		if profile.KeysPerImpression >= 3 {
			keys = append(keys, fmt.Sprintf("creative:%d", r.IntN(numCreatives)))
		}
		if profile.KeysPerImpression >= 4 {
			keys = append(keys, fmt.Sprintf("lineitem:%d", r.IntN(numLineItems)))
		}
		for j := 4; j < profile.KeysPerImpression; j++ {
			keys = append(keys, fmt.Sprintf("dim%d:%d", j, r.IntN(50)))
		}

		imps[i] = Impression{
			ImpressionID: fmt.Sprintf("imp-%d-%d", seed, i),
			Timestamp:    ts,
			FcapKeys:     keys,
		}
	}
	sort.Slice(imps, func(i, j int) bool { return imps[i].Timestamp < imps[j].Timestamp })
	return imps
}

// Stats is per-operation latency in microseconds.
type Stats struct {
	N      int
	P50us  float64
	P95us  float64
	P99us  float64
	MeanUs float64
}

// Result is the bench output for one (variant, profile, rule-window) cell.
type Result struct {
	Variant    string
	Profile    string
	RuleWindow string

	WriteSteady   Stats // write into a full 30-day log
	ReadSingle    Stats // single fcap_key eligibility
	ReadBatch100  Stats // 100 fcap_keys at once
	ReadBatch1000 Stats // 1000 fcap_keys at once

	CleanupUs    float64 // single cleanup call, drops one day's worth
	CleanupBytes int64   // memory after cleanup (decreased)

	MemoryBytes int64 // valkey MEMORY USAGE after seeding
}

// computeStats returns p50/p95/p99/mean over a slice of latencies in
// microseconds.
func computeStats(latenciesUs []float64) Stats {
	if len(latenciesUs) == 0 {
		return Stats{}
	}
	sorted := make([]float64, len(latenciesUs))
	copy(sorted, latenciesUs)
	sort.Float64s(sorted)
	pct := func(p float64) float64 {
		idx := int(float64(len(sorted)-1) * p)
		return sorted[idx]
	}
	var sum float64
	for _, v := range latenciesUs {
		sum += v
	}
	return Stats{
		N:      len(latenciesUs),
		P50us:  pct(0.50),
		P95us:  pct(0.95),
		P99us:  pct(0.99),
		MeanUs: sum / float64(len(latenciesUs)),
	}
}

// FormatResultsTable renders results as a markdown table.
func FormatResultsTable(results []Result) string {
	out := "| variant | profile | rule | write p50 | write p95 | read1 p50 | read100 p50 | read1k p50 | cleanup | memory |\n"
	out += "|---|---|---|---:|---:|---:|---:|---:|---:|---:|\n"
	for _, r := range results {
		out += fmt.Sprintf("| %s | %s | %s | %.0f µs | %.0f µs | %.0f µs | %.0f µs | %.0f µs | %.0f µs | %s |\n",
			r.Variant, r.Profile, r.RuleWindow,
			r.WriteSteady.P50us, r.WriteSteady.P95us,
			r.ReadSingle.P50us,
			r.ReadBatch100.P50us, r.ReadBatch1000.P50us,
			r.CleanupUs,
			fmtBytes(r.MemoryBytes),
		)
	}
	return out
}

func fmtBytes(b int64) string {
	switch {
	case b >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(b)/(1<<20))
	case b >= 1<<10:
		return fmt.Sprintf("%.1f KB", float64(b)/(1<<10))
	default:
		return fmt.Sprintf("%d B", b)
	}
}

// RunBench runs the full benchmark suite for one (variant, profile,
// rule-window) cell. The rule's window controls the server-side range
// filter on ZSET reads — a 24h rule lets ZSETs skip 96% of a 30-day
// log; a 30d rule pulls everything.
func RunBench(ctx context.Context, v Variant, profile LoadProfile, rules []FrequencyRule, ruleLabel, userID string, now int64, iterations, batchPoolSize int) (Result, error) {
	res := Result{Variant: v.Name(), Profile: profile.Name, RuleWindow: ruleLabel}

	// Reset to a clean slate.
	if err := v.Reset(ctx, userID); err != nil {
		return res, fmt.Errorf("reset: %w", err)
	}
	imps := GenerateLog(profile, now, 0xC0FFEE)
	if err := v.Seed(ctx, userID, imps); err != nil {
		return res, fmt.Errorf("seed: %w", err)
	}
	mem, err := v.MemoryUsage(ctx, userID)
	if err != nil {
		return res, fmt.Errorf("memory usage: %w", err)
	}
	res.MemoryBytes = mem

	// Pre-build a pool of distinct fcap_keys to drive read benchmarks. Reuse
	// keys that actually appear in the seeded log so reads find matches.
	keyPool := make([]string, 0, batchPoolSize)
	keySeen := make(map[string]struct{}, batchPoolSize)
	for _, imp := range imps {
		for _, k := range imp.FcapKeys {
			if _, seen := keySeen[k]; seen {
				continue
			}
			keySeen[k] = struct{}{}
			keyPool = append(keyPool, k)
			if len(keyPool) >= batchPoolSize {
				break
			}
		}
		if len(keyPool) >= batchPoolSize {
			break
		}
	}

	// Steady-state write: append to the already-full log.
	writeLatencies := make([]float64, 0, iterations)
	for i := 0; i < iterations; i++ {
		imp := Impression{
			ImpressionID: fmt.Sprintf("write-%d", i),
			Timestamp:    now + int64(i),
			FcapKeys:     []string{"campaign:1", "advertiser:2", "creative:3", "lineitem:4"},
		}
		start := time.Now()
		if err := v.Write(ctx, userID, imp); err != nil {
			return res, fmt.Errorf("write: %w", err)
		}
		writeLatencies = append(writeLatencies, float64(time.Since(start).Microseconds()))
	}
	res.WriteSteady = computeStats(writeLatencies)

	// Single-rule read.
	singleLatencies := make([]float64, 0, iterations)
	for i := 0; i < iterations; i++ {
		k := keyPool[i%len(keyPool)]
		kh := HashKey(k)
		start := time.Now()
		_, _, err := v.ReadAndCheck(ctx, userID, kh, rules, now)
		if err != nil {
			return res, fmt.Errorf("read single: %w", err)
		}
		singleLatencies = append(singleLatencies, float64(time.Since(start).Microseconds()))
	}
	res.ReadSingle = computeStats(singleLatencies)

	// Batch read: 100 fcap_keys.
	batch100Latencies := make([]float64, 0, iterations)
	keys100 := make([]uint64, 0, 100)
	for i := 0; i < 100 && i < len(keyPool); i++ {
		keys100 = append(keys100, HashKey(keyPool[i]))
	}
	for i := 0; i < iterations; i++ {
		start := time.Now()
		_, err := v.ReadBatchCheck(ctx, userID, keys100, rules, now)
		if err != nil {
			return res, fmt.Errorf("read batch 100: %w", err)
		}
		batch100Latencies = append(batch100Latencies, float64(time.Since(start).Microseconds()))
	}
	res.ReadBatch100 = computeStats(batch100Latencies)

	// Batch read: 1000 fcap_keys (synthesizing keys beyond the pool with
	// fresh hashes that won't match any entry — that mirrors the real
	// case where many candidate packages aren't ones the user has seen).
	batch1000Latencies := make([]float64, 0, iterations)
	keys1000 := make([]uint64, 0, 1000)
	for i := 0; i < 1000; i++ {
		if i < len(keyPool) {
			keys1000 = append(keys1000, HashKey(keyPool[i]))
		} else {
			keys1000 = append(keys1000, HashKey(fmt.Sprintf("nomatch:%d", i)))
		}
	}
	for i := 0; i < iterations/2; i++ { // half iterations: each call is heavier
		start := time.Now()
		_, err := v.ReadBatchCheck(ctx, userID, keys1000, rules, now)
		if err != nil {
			return res, fmt.Errorf("read batch 1000: %w", err)
		}
		batch1000Latencies = append(batch1000Latencies, float64(time.Since(start).Microseconds()))
	}
	res.ReadBatch1000 = computeStats(batch1000Latencies)

	// Cleanup: drop entries older than (now - 29 days). Simulates the
	// hourly cleanup pass that drops entries falling out of the 30-day
	// window. Re-seed first so per-write writes haven't already
	// rebalanced the log.
	if err := v.Reset(ctx, userID); err != nil {
		return res, fmt.Errorf("reset before cleanup: %w", err)
	}
	if err := v.Seed(ctx, userID, imps); err != nil {
		return res, fmt.Errorf("re-seed for cleanup: %w", err)
	}
	cleanupStart := time.Now()
	if err := v.Cleanup(ctx, userID, now, 29*24*time.Hour); err != nil {
		return res, fmt.Errorf("cleanup: %w", err)
	}
	res.CleanupUs = float64(time.Since(cleanupStart).Microseconds())
	memAfter, err := v.MemoryUsage(ctx, userID)
	if err != nil {
		return res, fmt.Errorf("memory after cleanup: %w", err)
	}
	res.CleanupBytes = memAfter

	return res, nil
}
