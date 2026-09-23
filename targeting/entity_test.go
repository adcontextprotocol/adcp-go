package targeting

import (
	"encoding/json"
	"strconv"
	"testing"
)

// TestPackageContextConfig_LegacyMacrosKeyDecodes proves that operator
// configs persisted under the pre-3.2 "macros" wire key still load
// into the renamed CreativeData field. Regression guard for the AdCP
// 3.2 tag rename — dropping this alias silently strips CreativeData
// from every legacy config in storage until the operator re-Puts it.
func TestPackageContextConfig_LegacyMacrosKeyDecodes(t *testing.T) {
	// Legacy shape: only "macros" present. New field name populated.
	var cfg PackageContextConfig
	if err := json.Unmarshal([]byte(`{"package_id":"pkg-1","macros":{"promo":"XYZ"}}`), &cfg); err != nil {
		t.Fatalf("unmarshal legacy config: %v", err)
	}
	if got := cfg.CreativeData["promo"]; got != "XYZ" {
		t.Fatalf("legacy macros key must load into CreativeData: got %q, want %q", got, "XYZ")
	}

	// Canonical shape: only "creative_data" present. Same result.
	var cfg2 PackageContextConfig
	if err := json.Unmarshal([]byte(`{"package_id":"pkg-1","creative_data":{"promo":"XYZ"}}`), &cfg2); err != nil {
		t.Fatalf("unmarshal canonical config: %v", err)
	}
	if got := cfg2.CreativeData["promo"]; got != "XYZ" {
		t.Fatalf("canonical creative_data key must load into CreativeData: got %q, want %q", got, "XYZ")
	}

	// Both keys present: canonical wins. Documented tiebreaker.
	var cfg3 PackageContextConfig
	if err := json.Unmarshal([]byte(`{"package_id":"pkg-1","macros":{"promo":"OLD"},"creative_data":{"promo":"NEW"}}`), &cfg3); err != nil {
		t.Fatalf("unmarshal mixed config: %v", err)
	}
	if got := cfg3.CreativeData["promo"]; got != "NEW" {
		t.Fatalf("creative_data must win over macros: got %q, want %q", got, "NEW")
	}

	// Nested OfferConfigJSON gets the same treatment.
	var cfg4 PackageContextConfig
	if err := json.Unmarshal([]byte(`{"package_id":"pkg-1","offers":[{"deal_id":"d1","macros":{"k":"v"}}]}`), &cfg4); err != nil {
		t.Fatalf("unmarshal legacy offer: %v", err)
	}
	if len(cfg4.Offers) != 1 || cfg4.Offers[0].CreativeData["k"] != "v" {
		t.Fatalf("legacy offer macros key must load into Offer.CreativeData: got %+v", cfg4.Offers)
	}
}

func TestPackageContextConfig_ContainsPropertyRID_EmptyIsUnrestricted(t *testing.T) {
	// An empty PropertyRIDs list is "no gate" — every rid passes,
	// including the empty string. This preserves the semantics of the
	// pre-materialization implementation where the gate was skipped
	// entirely when len(PropertyRIDs) == 0.
	cfg := &PackageContextConfig{}
	if !cfg.ContainsPropertyRID("anything") {
		t.Fatalf("empty PropertyRIDs should be unrestricted; got false")
	}
	cfg.MaterializePropertyBitmap()
	if !cfg.ContainsPropertyRID("anything") {
		t.Fatalf("empty PropertyRIDs should stay unrestricted after Materialize; got false")
	}
}

func TestPackageContextConfig_ContainsPropertyRID_MaterializedHitAndMiss(t *testing.T) {
	cfg := &PackageContextConfig{PropertyRIDs: []string{"a", "b", "c"}}
	cfg.MaterializePropertyBitmap()

	for _, rid := range []string{"a", "b", "c"} {
		if !cfg.ContainsPropertyRID(rid) {
			t.Errorf("materialized bitmap: %q should be present", rid)
		}
	}
	if cfg.ContainsPropertyRID("d") {
		t.Errorf("materialized bitmap: %q should be absent", "d")
	}
}

func TestPackageContextConfig_ContainsPropertyRID_SliceFallback(t *testing.T) {
	// Directly-constructed configs (no MaterializePropertyBitmap call)
	// must still return the correct answer via the slice-scan fallback.
	cfg := &PackageContextConfig{PropertyRIDs: []string{"a", "b", "c"}}
	if !cfg.ContainsPropertyRID("b") {
		t.Errorf("slice fallback: %q should be present", "b")
	}
	if cfg.ContainsPropertyRID("z") {
		t.Errorf("slice fallback: %q should be absent", "z")
	}
}

func TestPackageContextConfig_MaterializePropertyBitmap_Idempotent(t *testing.T) {
	cfg := &PackageContextConfig{PropertyRIDs: []string{"a", "b"}}
	cfg.MaterializePropertyBitmap()
	first := cfg.propertyRIDBitmap
	cfg.MaterializePropertyBitmap()
	if cfg.propertyRIDBitmap == nil {
		t.Fatalf("second Materialize left nil bitmap")
	}
	// The second call rebuilds — that's the documented "last call wins"
	// contract, mirroring the pattern used elsewhere for lazy-init helpers
	// in this package. Just assert both builds answer identically.
	if first == nil {
		t.Fatalf("first Materialize left nil bitmap")
	}
	for _, rid := range []string{"a", "b", "z"} {
		if first.Contains(rid) != cfg.propertyRIDBitmap.Contains(rid) {
			t.Errorf("idempotent Materialize disagreed on %q", rid)
		}
	}
}

func TestPackageContextConfig_ContainsPropertyRID_SurvivesStructCopy(t *testing.T) {
	// The pkgconfigstore cache clones configs via `out := *cfg` before
	// deep-copying slice/map fields. That memcopy propagates the
	// interface field, so the clone shares the bitmap pointer with the
	// original. Verify the clone still resolves via the O(1) path.
	orig := &PackageContextConfig{PropertyRIDs: []string{"a", "b", "c"}}
	orig.MaterializePropertyBitmap()

	clone := *orig
	clone.PropertyRIDs = append([]string(nil), orig.PropertyRIDs...)

	if clone.propertyRIDBitmap == nil {
		t.Fatalf("clone lost propertyRIDBitmap after struct memcopy")
	}
	if !clone.ContainsPropertyRID("b") {
		t.Errorf("clone should still contain %q", "b")
	}
	if clone.ContainsPropertyRID("d") {
		t.Errorf("clone should still reject %q", "d")
	}
}

func benchPropertyRIDs(n int) []string {
	rids := make([]string, n)
	for i := range n {
		rids[i] = "rid-" + strconv.Itoa(i)
	}
	return rids
}

// BenchmarkContainsPropertyRID_Materialized measures the hot path with
// the bitmap prebuilt (production path via pkgconfigstore).
func BenchmarkContainsPropertyRID_Materialized(b *testing.B) {
	cfg := &PackageContextConfig{PropertyRIDs: benchPropertyRIDs(65_000)}
	cfg.MaterializePropertyBitmap()
	target := cfg.PropertyRIDs[32_500]

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if !cfg.ContainsPropertyRID(target) {
			b.Fatal("expected hit")
		}
	}
}

// BenchmarkContainsPropertyRID_RebuildPerCall reproduces the shape of
// the pre-materialization implementation for comparison: a fresh
// MapBitmap allocated on every call.
func BenchmarkContainsPropertyRID_RebuildPerCall(b *testing.B) {
	cfg := &PackageContextConfig{PropertyRIDs: benchPropertyRIDs(65_000)}
	target := cfg.PropertyRIDs[32_500]

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		var pkgBitmap Bitmap = NewMapBitmap(cfg.PropertyRIDs...)
		if !pkgBitmap.Contains(target) {
			b.Fatal("expected hit")
		}
	}
}
