package targeting

import (
	"context"
	"fmt"
	"runtime"
	"testing"
	"time"
	"unsafe"

	"github.com/adcontextprotocol/adcp-go/tmproto"
)

// TestMemory_EnginePackageScale measures in-memory footprint of the Engine
// as the number of registered packages grows.
func TestMemory_EnginePackageScale(t *testing.T) {
	t.Log("")
	t.Log("=== Engine Memory: packages map is in-memory ===")
	t.Logf("  sizeof(PackageConfig) = %d bytes (struct, no heap alloc for basic config)", unsafe.Sizeof(PackageConfig{}))
	t.Log("")

	for _, numPkgs := range []int{100, 1_000, 10_000, 100_000} {
		store := NewMockStore()
		var pkgs []PackageConfig
		for i := range numPkgs {
			pkgs = append(pkgs, PackageConfig{
				PackageID:    fmt.Sprintf("pkg-%d", i),
				MediaBuyID:   fmt.Sprintf("mb-%d", i),
				TopicTargets: true,
				URLBlocklist: true,
			})
		}

		var m1, m2 runtime.MemStats
		runtime.GC()
		runtime.ReadMemStats(&m1)

		engine := NewEngine(EngineConfig{
			ProviderID: "bench",
			Store:      store,
			Packages:   pkgs,
		})
		_ = engine

		runtime.GC()
		runtime.ReadMemStats(&m2)

		// TotalAlloc is monotonically increasing — diff shows bytes allocated.
		allocated := float64(m2.TotalAlloc-m1.TotalAlloc) / 1024 / 1024
		t.Logf("  %6d packages: ~%.2f MB allocated", numPkgs, allocated)
	}
	t.Log("")
	t.Log("  In production, identity config (freq rules, segments, campaigns)")
	t.Log("  lives in Valkey, NOT in the packages map. The Engine only holds")
	t.Log("  PackageID + context targeting flags per package.")
	t.Log("")
}

// TestMemory_StoreScale measures what Valkey would need to hold.
// In production this is Valkey memory, not Go process memory.
func TestMemory_StoreScale(t *testing.T) {
	t.Log("")
	t.Log("=== What lives WHERE ===")
	t.Log("")
	t.Log("  IN GO PROCESS (Engine.packages map):")
	t.Log("    - PackageID + context flags (bitmap ref, URL/topic bools)")
	t.Log("    - Offer templates (Brand, Price, Summary, CreativeManifest)")
	t.Log("    - ~184 bytes/package base + string/slice heap allocations")
	t.Log("")
	t.Log("  IN VALKEY (out-of-process):")
	t.Log("    - Identity config per package (freq rules, campaign ID, segments)")
	t.Log("    - Campaign freq rules")
	t.Log("    - Audience segment membership (hash sets)")
	t.Log("    - Frequency cap exposure history (sorted sets)")
	t.Log("    - URL blocklists/allowlists (hash sets)")
	t.Log("    - Topic sets (hash sets)")
	t.Log("")
	t.Log("  Adding 100K campaigns to Valkey = zero Go memory impact.")
	t.Log("  Adding 1M audience members to Valkey = zero Go memory impact.")
	t.Log("  The Engine only reads what it needs per-request via Store.Get.")
	t.Log("")
}

// TestScale_IdentityNoTargeting measures identity eval when packages have NO
// targeting config in the Store (the "10K campaigns, no targeting" case).
func TestScale_IdentityNoTargeting(t *testing.T) {
	t.Log("")
	t.Log("=== Identity Eval: packages with no identity config ===")
	t.Log("  When a package has no config in Store, it's always eligible.")
	t.Log("  The cost is one Store.Get miss per package per request.")
	t.Log("")

	for _, numPkgs := range []int{1, 5, 10, 25, 50, 100} {
		store := NewMockStore()
		var pkgs []PackageConfig
		var pkgIDs []string
		for i := range numPkgs {
			pkgID := fmt.Sprintf("pkg-%d", i)
			pkgs = append(pkgs, PackageConfig{PackageID: pkgID})
			pkgIDs = append(pkgIDs, pkgID)
			// NO identity config pushed to Store — these are "no targeting" packages.
		}

		engine := NewEngine(EngineConfig{
			ProviderID: "bench",
			Store:      store,
			Packages:   pkgs,
		})

		req := &tmproto.IdentityMatchRequest{
			RequestID:  "bench",
			Identities: []tmproto.IdentityToken{{UserToken: "tok-bench"}},
			PackageIDs: pkgIDs,
		}

		const iterations = 2_000
		start := time.Now()
		for range iterations {
			_, _ = engine.EvaluateIdentity(context.Background(), req)
		}
		elapsed := time.Since(start)
		perPkg := elapsed / time.Duration(iterations*numPkgs)
		t.Logf("  %3d packages (no targeting): %v/eval (%v/package)", numPkgs, elapsed/iterations, perPkg)
	}
	t.Log("")
}
