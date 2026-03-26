package e2e

import (
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/adcontextprotocol/adcp-go/tmp"
)

// TestPerformance_EndToEnd measures the full TMP exchange including HTTP
// serialization, routing, agent evaluation, and publisher-side join.
// This is the real end-to-end cost, not just serialization benchmarks.
func TestPerformance_EndToEnd(t *testing.T) {
	ctxAgent := httptest.NewServer(&simulatedContextAgent{
		name: "perf-ctx",
		modules: []interface {
			Evaluate(*tmp.ContextMatchRequest, tmp.AvailablePackage) (bool, float32)
		}{
			&TopicRelevanceModule{
				topicKeywords: map[string][]string{
					"pkg-food":    {"recipe", "cooking", "food"},
					"pkg-tech":    {"gadget", "review", "tech"},
					"pkg-auto":    {"car", "drive", "vehicle"},
					"pkg-travel":  {"hotel", "flight", "vacation"},
					"pkg-finance": {"invest", "market", "fund"},
				},
			},
		},
	})
	defer ctxAgent.Close()

	idAgent := newSimulatedIdentityAgent("perf-id",
		map[string]int{
			"pkg-food": 10, "pkg-tech": 10, "pkg-auto": 10,
			"pkg-travel": 10, "pkg-finance": 10,
		},
		nil,
	)
	idServer := httptest.NewServer(idAgent)
	defer idServer.Close()

	router := httptest.NewServer(&mockRouter{
		contextAgents:  []*httptest.Server{ctxAgent},
		identityAgents: []*httptest.Server{idServer},
	})
	defer router.Close()

	allPkgIDs := []string{
		"pkg-food", "pkg-tech", "pkg-auto", "pkg-travel", "pkg-finance",
		"pkg-health", "pkg-sports", "pkg-fashion", "pkg-home", "pkg-garden",
	}
	pkgs := []tmp.AvailablePackage{
		{PackageID: "pkg-food", MediaBuyID: "mb-1"},
		{PackageID: "pkg-tech", MediaBuyID: "mb-2"},
		{PackageID: "pkg-auto", MediaBuyID: "mb-3"},
		{PackageID: "pkg-travel", MediaBuyID: "mb-4"},
		{PackageID: "pkg-finance", MediaBuyID: "mb-5"},
	}

	// Warm up
	for i := 0; i < 10; i++ {
		postJSON(t, router.URL+"/tmp/context", tmp.ContextMatchRequest{
			RequestID: fmt.Sprintf("warmup-%d", i), PropertyID: "pub-perf",
			PlacementID: "main", Artifacts: []string{"article:cooking-recipe"},
			AvailablePkgs: pkgs,
		})
	}

	// --- Sequential performance ---
	t.Run("sequential_full_exchange", func(t *testing.T) {
		iterations := 100
		var totalCtx, totalId, totalJoin, totalE2E time.Duration

		for i := 0; i < iterations; i++ {
			e2eStart := time.Now()

			// Context match
			ctxStart := time.Now()
			ctxData := postJSON(t, router.URL+"/tmp/context", tmp.ContextMatchRequest{
				RequestID:     fmt.Sprintf("perf-ctx-%d", i),
				PropertyID:    "pub-perf",
				PlacementID:   "main",
				Artifacts:     []string{"article:cooking-recipe"},
				AvailablePkgs: pkgs,
			})
			totalCtx += time.Since(ctxStart)

			// Identity match
			idStart := time.Now()
			idData := postJSON(t, router.URL+"/tmp/identity", tmp.IdentityMatchRequest{
				RequestID:  fmt.Sprintf("perf-id-%d", i),
				UserToken:  fmt.Sprintf("tok-user-%d", i%50),
				PackageIDs: allPkgIDs,
			})
			totalId += time.Since(idStart)

			// Publisher join
			joinStart := time.Now()
			var cmResp tmp.ContextMatchResponse
			var imResp tmp.IdentityMatchResponse
			json.Unmarshal(ctxData, &cmResp)
			json.Unmarshal(idData, &imResp)

			eligMap := make(map[string]bool)
			for _, e := range imResp.Eligibility {
				eligMap[e.PackageID] = e.Eligible
			}
			var activated []string
			for _, o := range cmResp.Offers {
				if eligMap[o.PackageID] {
					activated = append(activated, o.PackageID)
				}
			}
			totalJoin += time.Since(joinStart)
			totalE2E += time.Since(e2eStart)
			_ = activated
		}

		t.Logf("Sequential full exchange (%d iterations):", iterations)
		t.Logf("  Context Match:   avg %v", totalCtx/time.Duration(iterations))
		t.Logf("  Identity Match:  avg %v", totalId/time.Duration(iterations))
		t.Logf("  Publisher Join:  avg %v", totalJoin/time.Duration(iterations))
		t.Logf("  End-to-End:      avg %v", totalE2E/time.Duration(iterations))
	})

	// --- Parallel performance (context + identity fire simultaneously) ---
	t.Run("parallel_full_exchange", func(t *testing.T) {
		iterations := 100
		var totalE2E time.Duration

		for i := 0; i < iterations; i++ {
			start := time.Now()
			var ctxData, idData []byte
			var wg sync.WaitGroup
			wg.Add(2)
			go func() {
				defer wg.Done()
				ctxData = postJSON(t, router.URL+"/tmp/context", tmp.ContextMatchRequest{
					RequestID:     fmt.Sprintf("par-ctx-%d", i),
					PropertyID:    "pub-perf",
					PlacementID:   "main",
					Artifacts:     []string{"article:cooking-recipe"},
					AvailablePkgs: pkgs,
				})
			}()
			go func() {
				defer wg.Done()
				idData = postJSON(t, router.URL+"/tmp/identity", tmp.IdentityMatchRequest{
					RequestID:  fmt.Sprintf("par-id-%d", i),
					UserToken:  fmt.Sprintf("tok-user-%d", i%50),
					PackageIDs: allPkgIDs,
				})
			}()
			wg.Wait()

			var cmResp tmp.ContextMatchResponse
			var imResp tmp.IdentityMatchResponse
			json.Unmarshal(ctxData, &cmResp)
			json.Unmarshal(idData, &imResp)
			totalE2E += time.Since(start)
			_ = ctxData
			_ = idData
		}

		t.Logf("Parallel full exchange (%d iterations):", iterations)
		t.Logf("  End-to-End:      avg %v", totalE2E/time.Duration(iterations))
	})

	// --- Throughput test ---
	t.Run("throughput", func(t *testing.T) {
		duration := 2 * time.Second
		var ops int64
		done := make(chan struct{})

		// Run concurrent workers
		workers := 8
		var wg sync.WaitGroup
		start := time.Now()

		go func() {
			time.Sleep(duration)
			close(done)
		}()

		for w := 0; w < workers; w++ {
			wg.Add(1)
			go func(workerID int) {
				defer wg.Done()
				i := 0
				for {
					select {
					case <-done:
						return
					default:
					}
					// Full exchange: context + identity in parallel, then join
					var ctxData, idData []byte
					var inner sync.WaitGroup
					inner.Add(2)
					go func() {
						defer inner.Done()
						ctxData = postJSON(t, router.URL+"/tmp/context", tmp.ContextMatchRequest{
							RequestID:     fmt.Sprintf("tp-%d-%d", workerID, i),
							PropertyID:    "pub-perf",
							PlacementID:   "main",
							Artifacts:     []string{"article:cooking-recipe"},
							AvailablePkgs: pkgs,
						})
					}()
					go func() {
						defer inner.Done()
						idData = postJSON(t, router.URL+"/tmp/identity", tmp.IdentityMatchRequest{
							RequestID:  fmt.Sprintf("tp-id-%d-%d", workerID, i),
							UserToken:  fmt.Sprintf("tok-%d-%d", workerID, i),
							PackageIDs: allPkgIDs,
						})
					}()
					inner.Wait()
					_ = ctxData
					_ = idData
					atomic.AddInt64(&ops, 1)
					i++
				}
			}(w)
		}

		wg.Wait()
		elapsed := time.Since(start)
		qps := float64(ops) / elapsed.Seconds()
		t.Logf("Throughput (%d workers, %v):", workers, duration)
		t.Logf("  Total exchanges: %d", ops)
		t.Logf("  QPS:             %.0f exchanges/sec", qps)
		t.Logf("  Each exchange = context match + identity match (parallel) + join")
	})
}

// TestPerformance_FrequencyCapping measures the overhead of frequency cap
// checks across a realistic user session.
func TestPerformance_FrequencyCapping(t *testing.T) {
	idAgent := newSimulatedIdentityAgent("freq-perf",
		map[string]int{"pkg-a": 3, "pkg-b": 5, "pkg-c": 10},
		nil,
	)
	idServer := httptest.NewServer(idAgent)
	defer idServer.Close()

	router := httptest.NewServer(&mockRouter{
		identityAgents: []*httptest.Server{idServer},
	})
	defer router.Close()

	// Simulate 100 users, each with a session of 20 page views
	users := 100
	pagesPerUser := 20
	allPkgIDs := []string{"pkg-a", "pkg-b", "pkg-c", "pkg-d", "pkg-e"}

	start := time.Now()
	totalRequests := 0
	totalExposures := 0
	cappedCount := 0

	for u := 0; u < users; u++ {
		token := fmt.Sprintf("tok-freq-%d", u)
		for p := 0; p < pagesPerUser; p++ {
			// Identity match
			idData := postJSON(t, router.URL+"/tmp/identity", tmp.IdentityMatchRequest{
				RequestID:  fmt.Sprintf("freq-%d-%d", u, p),
				UserToken:  token,
				PackageIDs: allPkgIDs,
			})
			totalRequests++

			var imResp tmp.IdentityMatchResponse
			json.Unmarshal(idData, &imResp)

			// Find best eligible package and expose
			for _, e := range imResp.Eligibility {
				if e.Eligible && e.PackageID == "pkg-a" {
					postJSON(t, idServer.URL+"/tmp/expose", tmp.ExposeRequest{
						UserToken: token,
						PackageID: "pkg-a",
					})
					totalExposures++
					break
				}
				if !e.Eligible && e.PackageID == "pkg-a" {
					cappedCount++
					break
				}
			}
		}
	}

	elapsed := time.Since(start)
	t.Logf("Frequency capping simulation:")
	t.Logf("  Users: %d, Pages/user: %d", users, pagesPerUser)
	t.Logf("  Total identity requests: %d", totalRequests)
	t.Logf("  Total exposures (pkg-a): %d", totalExposures)
	t.Logf("  Times capped (pkg-a):    %d", cappedCount)
	t.Logf("  Total time: %v", elapsed)
	t.Logf("  Avg per identity match: %v", elapsed/time.Duration(totalRequests))
	t.Logf("  Effective cap rate:     %.1f%% of sessions hit cap", float64(cappedCount)/float64(users*pagesPerUser)*100)
}
