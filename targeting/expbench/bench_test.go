package expbench

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
)

// TestBench is the entry point for the storage-shape comparison.
// Run against a live valkey/redis with VALKEY_ADDR=host:port (default
// localhost:6380, matching the docker container started for this work).
//
//	VALKEY_ADDR=localhost:6380 go test -run TestBench -v -timeout 5m ./targeting/expbench/
func TestBench(t *testing.T) {
	addr := os.Getenv("VALKEY_ADDR")
	if addr == "" {
		addr = "localhost:6380"
	}
	rdb := redis.NewClient(&redis.Options{Addr: addr})
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	if err := rdb.Ping(ctx).Err(); err != nil {
		t.Skipf("valkey unreachable at %s: %v", addr, err)
	}

	variants := []Variant{
		NewBinaryStore(rdb),
		NewZSetArrayStore(rdb),
		NewZSetPerKeyStore(rdb),
		NewZSetPerKeyedStore(rdb),
		NewBucketDayStore(rdb),
		NewBucketCountStore(rdb),
	}
	profiles := []LoadProfile{
		{Name: "median 3K", NumImpressions: 3_000, Window: 30 * 24 * time.Hour, KeysPerImpression: 4},
		{Name: "heavy 30K", NumImpressions: 30_000, Window: 30 * 24 * time.Hour, KeysPerImpression: 4},
	}
	ruleSets := []struct {
		label string
		rules []FrequencyRule
	}{
		{"24h", []FrequencyRule{{MaxCount: 100, Window: 24 * time.Hour}}},
		{"30d", []FrequencyRule{{MaxCount: 100, Window: 30 * 24 * time.Hour}}},
	}

	now := time.Now().Unix()
	const iterations = 200
	const batchPool = 1000

	results := make([]Result, 0, len(variants)*len(profiles)*len(ruleSets))
	for _, v := range variants {
		for _, p := range profiles {
			for _, rs := range ruleSets {
				// Bucket-day is a singleton-per-day model — only meaningful
				// against day-window rules. The 30d rule case doesn't fit
				// its model; skip it.
				// Bucket variants are singleton/count-per-day models — only
				// meaningful against day-window rules.
				if (v.Name() == "bucket-day" || v.Name() == "bucket-count") && rs.label != "24h" {
					continue
				}
				userID := v.Name() + "-" + p.Name + "-" + rs.label
				t.Logf("running %s / %s / %s", v.Name(), p.Name, rs.label)
				r, err := RunBench(ctx, v, p, rs.rules, rs.label, userID, now, iterations, batchPool)
				if err != nil {
					t.Errorf("RunBench(%s, %s, %s): %v", v.Name(), p.Name, rs.label, err)
					continue
				}
				results = append(results, r)
				_ = v.Reset(ctx, userID)
			}
		}
	}

	t.Log("\n" + FormatResultsTable(results))
}

// TestSanity_Equivalence verifies that all three variants produce the same
// eligibility answer for the same workload. If they disagree, the perf
// comparison is meaningless because they aren't computing the same thing.
func TestSanity_Equivalence(t *testing.T) {
	addr := os.Getenv("VALKEY_ADDR")
	if addr == "" {
		addr = "localhost:6380"
	}
	rdb := redis.NewClient(&redis.Options{Addr: addr})
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := rdb.Ping(ctx).Err(); err != nil {
		t.Skipf("valkey unreachable at %s: %v", addr, err)
	}

	now := time.Now().Unix()
	imps := []Impression{
		{ImpressionID: "imp-1", Timestamp: now - 3600, FcapKeys: []string{"campaign:1", "advertiser:7"}},
		{ImpressionID: "imp-2", Timestamp: now - 1800, FcapKeys: []string{"campaign:1", "advertiser:7"}},
		{ImpressionID: "imp-3", Timestamp: now - 900, FcapKeys: []string{"campaign:1", "creative:3"}},
		{ImpressionID: "imp-4", Timestamp: now - 100, FcapKeys: []string{"campaign:2"}},
		{ImpressionID: "imp-old", Timestamp: now - 90*86400, FcapKeys: []string{"campaign:1"}},
	}
	rules := []FrequencyRule{{MaxCount: 3, Window: 24 * time.Hour}}
	keys := []string{"campaign:1", "advertiser:7", "creative:3", "campaign:2", "campaign:99"}

	type answer struct {
		capped map[string]bool
		latest map[string]int64
	}
	collect := func(v Variant) answer {
		userID := "sanity-" + v.Name()
		_ = v.Reset(ctx, userID)
		if err := v.Seed(ctx, userID, imps); err != nil {
			t.Fatalf("seed %s: %v", v.Name(), err)
		}
		out := answer{capped: map[string]bool{}, latest: map[string]int64{}}
		for _, k := range keys {
			capped, ts, err := v.ReadAndCheck(ctx, userID, HashKey(k), rules, now)
			if err != nil {
				t.Fatalf("read %s/%s: %v", v.Name(), k, err)
			}
			out.capped[k] = capped
			out.latest[k] = ts
		}
		_ = v.Reset(ctx, userID)
		return out
	}

	a := collect(NewBinaryStore(rdb))
	b := collect(NewZSetArrayStore(rdb))
	c := collect(NewZSetPerKeyStore(rdb))
	d := collect(NewZSetPerKeyedStore(rdb))

	for _, k := range keys {
		if a.capped[k] != b.capped[k] || a.capped[k] != c.capped[k] || a.capped[k] != d.capped[k] {
			t.Errorf("capped(%s): binary=%v zset-array=%v zset-perkey=%v zset-perkeyed=%v",
				k, a.capped[k], b.capped[k], c.capped[k], d.capped[k])
		}
		if a.latest[k] != b.latest[k] || a.latest[k] != c.latest[k] || a.latest[k] != d.latest[k] {
			t.Errorf("latest(%s): binary=%v zset-array=%v zset-perkey=%v zset-perkeyed=%v",
				k, a.latest[k], b.latest[k], c.latest[k], d.latest[k])
		}
	}

	// campaign:1 has 3 in 24h window (imp-1, imp-2, imp-3) → MaxCount=3 → capped.
	if !a.capped["campaign:1"] {
		t.Errorf("expected campaign:1 to be capped (3 hits in 24h, MaxCount=3); binary said no")
	}
	// campaign:99 should never be capped.
	if a.capped["campaign:99"] {
		t.Errorf("campaign:99 should not be capped")
	}
	// imp-old (90 days ago) must not contribute.
	if a.latest["campaign:1"] < now-3600 {
		t.Errorf("latest(campaign:1) = %d should be from imp-1 (now-3600), not imp-old", a.latest["campaign:1"])
	}
}
