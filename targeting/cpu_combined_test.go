package targeting

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/adcontextprotocol/adcp-go/tmproto"
)

// TestScale_IdentityMatch_CPU_Combined measures EvaluateIdentityResolved CPU
// across the combined dimensions that matter for production sizing:
// (candidate packages per request) × (exposure log entries per identity) ×
// (identities per request). All numbers are isolated from network I/O via
// the mock store, so they represent in-process CPU only.
func TestScale_IdentityMatch_CPU_Combined(t *testing.T) {
	pkgCounts := []int{10, 100, 1000}
	logSizes := []int{0, 100, 1000, 10000}
	identityCounts := []int{1, 3}

	t.Log("")
	t.Log("=== IdentityMatch CPU: packages × log_size × identities ===")
	t.Log("")
	t.Logf("  %-10s %-10s %-10s %-15s %-12s", "packages", "log_size", "identities", "ns/op", "µs/eval")
	t.Logf("  %-10s %-10s %-10s %-15s %-12s", "--------", "--------", "----------", "-----", "-------")

	for _, numPkgs := range pkgCounts {
		for _, logSize := range logSizes {
			for _, numIdentities := range identityCounts {
				now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
				store := NewMockStore()
				store.Now = func() time.Time { return now }

				// Set up N packages, each with one fcap rule of max=10/86400s.
				var pkgs []PackageConfig
				var pkgIDs []string
				idConfigs := make(map[string]*PackageIdentityConfig, numPkgs)
				for i := range numPkgs {
					pkgID := fmt.Sprintf("pkg-%d", i)
					pkgs = append(pkgs, PackageConfig{PackageID: pkgID})
					pkgIDs = append(pkgIDs, pkgID)
					idCfg := PackageIdentityConfig{
						FrequencyRules: []FrequencyRuleJSON{{MaxCount: 1_000_000, WindowSeconds: 86400}},
					}
					store.SetPackageIdentityConfig(pkgID, idCfg)
					idConfigs[pkgID] = &idCfg
				}

				// Build identities and write per-identity exposure logs of `logSize`.
				identities := make([]tmproto.IdentityToken, numIdentities)
				for i := range numIdentities {
					tok := fmt.Sprintf("tok-bench-%d", i)
					identities[i] = tmproto.IdentityToken{UserToken: tok}

					if logSize > 0 {
						entries := make([]ExposureEntry, 0, logSize)
						for j := range logSize {
							pkg := pkgIDs[j%numPkgs]
							entries = append(entries, ExposureEntry{
								ImpressionID: fmt.Sprintf("imp-%d-%d", i, j),
								PackageID:    pkg,
								SourceID:     "bench",
								Timestamp:    now.Add(-time.Duration(j) * time.Minute).Unix(),
							})
						}
						store.SetUserExposures(tok, entries)
					}
				}

				engine := NewEngine(EngineConfig{
					ProviderID: "bench",
					Store:      store,
					Packages:   pkgs,
				})
				engine.Now = func() time.Time { return now }

				resolved := &ResolvedPackages{IdentityConfigs: idConfigs}

				req := &tmproto.IdentityMatchRequest{
					RequestID:  "bench",
					Identities: identities,
					PackageIDs: pkgIDs,
				}

				// Warmup
				for range 10 {
					_, _ = engine.EvaluateIdentityResolved(context.Background(), resolved, req)
				}

				// Time
				const iterations = 200
				start := time.Now()
				for range iterations {
					_, _ = engine.EvaluateIdentityResolved(context.Background(), resolved, req)
				}
				elapsed := time.Since(start)
				perEval := elapsed / iterations
				nsPerOp := perEval.Nanoseconds()

				t.Logf("  %-10d %-10d %-10d %-15s %-12.2f",
					numPkgs, logSize, numIdentities,
					fmt.Sprintf("%d ns", nsPerOp),
					float64(perEval.Microseconds()),
				)
			}
		}
	}
	t.Log("")
}
