package expbench

import (
	"context"
	"fmt"
	"os"
	"runtime"
	"sort"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
)

// TestBenchSplit measures the read path with three timers per call:
// total = fetch + process. Fetch is "wait on valkey + deserialize wire
// protocol into Go types"; process is "in-memory eligibility scan with
// no I/O." Goal: see where the cost actually lives, not just the
// end-to-end number.
//
//	VALKEY_ADDR=localhost:6380 go test -run TestBenchSplit -v -timeout 5m ./targeting/expbench/
func TestBenchSplit(t *testing.T) {
	addr := os.Getenv("VALKEY_ADDR")
	if addr == "" {
		addr = "localhost:6380"
	}
	rdb := redis.NewClient(&redis.Options{Addr: addr})
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	if err := rdb.Ping(ctx).Err(); err != nil {
		t.Skipf("valkey unreachable at %s: %v", addr, err)
	}

	variants := []SplitVariant{
		NewBinaryStore(rdb),
		NewZSetArrayStore(rdb),
		NewZSetPerKeyStore(rdb),
		NewZSetPerKeyedStore(rdb),
	}
	profiles := []LoadProfile{
		{Name: "median 3K", NumImpressions: 3_000, Window: 30 * 24 * time.Hour, KeysPerImpression: 4},
		{Name: "heavy 30K", NumImpressions: 30_000, Window: 30 * 24 * time.Hour, KeysPerImpression: 4},
	}

	now := time.Now().Unix()
	const iterations = 200
	ruleSets := []struct {
		name  string
		rules []FrequencyRule
	}{
		{"24h", []FrequencyRule{{MaxCount: 100, Window: 24 * time.Hour}}},
		{"30d", []FrequencyRule{{MaxCount: 100, Window: 30 * 24 * time.Hour}}},
	}

	type cell struct {
		variant       string
		profile       string
		ruleWindow    string
		batch         int
		fetchUs       float64
		processUs     float64
		totalUs       float64
		fetchBytes    int
		fetchMembers  int
		processAllocs uint64
	}
	var rows []cell

	for _, v := range variants {
		for _, p := range profiles {
			userID := "split-" + v.Name() + "-" + p.Name
			_ = v.Reset(ctx, userID)
			imps := GenerateLog(p, now, 0xC0FFEE)
			if err := v.Seed(ctx, userID, imps); err != nil {
				t.Fatalf("seed %s/%s: %v", v.Name(), p.Name, err)
			}

			// Build the same fcap_key pool as the other bench.
			seenKeys := make(map[string]struct{})
			pool := make([]string, 0, 1000)
			for _, imp := range imps {
				for _, k := range imp.FcapKeys {
					if _, seen := seenKeys[k]; seen {
						continue
					}
					seenKeys[k] = struct{}{}
					pool = append(pool, k)
					if len(pool) >= 1000 {
						break
					}
				}
				if len(pool) >= 1000 {
					break
				}
			}

			for _, rs := range ruleSets {
				fetchWindow := rs.rules[0].Window
				for _, batchSize := range []int{1, 100, 1000} {
					keys := make([]uint64, 0, batchSize)
					for i := 0; i < batchSize; i++ {
						if i < len(pool) {
							keys = append(keys, HashKey(pool[i]))
						} else {
							keys = append(keys, HashKey(fmt.Sprintf("nomatch:%d", i)))
						}
					}

					iters := iterations
					if batchSize == 1000 {
						iters = iterations / 2
					}

					fetchLatencies := make([]float64, 0, iters)
					processLatencies := make([]float64, 0, iters)
					totalLatencies := make([]float64, 0, iters)
					var fetchBytes, fetchMembers int
					var totalProcessAllocs uint64
					for i := 0; i < iters; i++ {
						tStart := time.Now()
						raw, err := v.Fetch(ctx, userID, fetchWindow, now)
						fetchEnd := time.Now()
						if err != nil {
							t.Fatalf("fetch %s/%s: %v", v.Name(), p.Name, err)
						}
						if i == 0 {
							fetchBytes, fetchMembers = SizeOfFetch(raw)
						}

						var ms1, ms2 runtime.MemStats
						runtime.ReadMemStats(&ms1)
						_ = v.Process(raw, keys, rs.rules, now)
						processEnd := time.Now()
						runtime.ReadMemStats(&ms2)
						totalProcessAllocs += ms2.TotalAlloc - ms1.TotalAlloc

						fetchLatencies = append(fetchLatencies, float64(fetchEnd.Sub(tStart).Microseconds()))
						processLatencies = append(processLatencies, float64(processEnd.Sub(fetchEnd).Microseconds()))
						totalLatencies = append(totalLatencies, float64(processEnd.Sub(tStart).Microseconds()))
					}
					rows = append(rows, cell{
						variant:       v.Name(),
						profile:       p.Name,
						ruleWindow:    rs.name,
						batch:         batchSize,
						fetchUs:       median(fetchLatencies),
						processUs:     median(processLatencies),
						totalUs:       median(totalLatencies),
						fetchBytes:    fetchBytes,
						fetchMembers:  fetchMembers,
						processAllocs: totalProcessAllocs / uint64(iters),
					})
				}
			}
			_ = v.Reset(ctx, userID)
		}
	}

	out := "\n| variant | profile | rule | batch | fetch p50 | process p50 | total p50 | fetch bytes | members | proc allocs |\n"
	out += "|---|---|---|---:|---:|---:|---:|---:|---:|---:|\n"
	for _, r := range rows {
		out += fmt.Sprintf("| %s | %s | %s | %d | %.0f µs | %.0f µs | %.0f µs | %s | %d | %s |\n",
			r.variant, r.profile, r.ruleWindow, r.batch,
			r.fetchUs, r.processUs, r.totalUs,
			fmtBytes(int64(r.fetchBytes)), r.fetchMembers,
			fmtBytes(int64(r.processAllocs)),
		)
	}
	t.Log(out)
}

func median(xs []float64) float64 {
	if len(xs) == 0 {
		return 0
	}
	c := make([]float64, len(xs))
	copy(c, xs)
	sort.Float64s(c)
	return c[len(c)/2]
}
