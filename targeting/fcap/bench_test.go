package fcap

import (
	"testing"
)

// Benchmark profile representative of a hot-path identity match: a handful
// of user identities cross-producted against the package set for one
// seller. Compares the pooled IsCappedAny against the per-call-allocating
// IsCappedBatch to quantify the win.
func BenchmarkIsCappedAny(b *testing.B) {
	svc := New(NewMockStore())
	ctx := b.Context()
	identities := []string{"u1", "u2", "u3", "u4"}
	fields := make([]Field, 10)
	for i := range fields {
		fields[i] = Field{SellerAgentURL: "s", PackageID: "pkg-" + string(rune('a'+i))}
	}
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		_, _ = svc.IsCappedAny(ctx, identities, fields)
	}
}

func BenchmarkIsCappedBatch(b *testing.B) {
	svc := New(NewMockStore())
	ctx := b.Context()
	identities := []string{"u1", "u2", "u3", "u4"}
	pkgs := []string{"pkg-a", "pkg-b", "pkg-c", "pkg-d", "pkg-e", "pkg-f", "pkg-g", "pkg-h", "pkg-i", "pkg-j"}
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		lookups := make([]CapLookup, 0, len(identities)*len(pkgs))
		for _, u := range identities {
			for _, p := range pkgs {
				lookups = append(lookups, CapLookup{
					UserIdentity: u,
					Field:        Field{SellerAgentURL: "s", PackageID: p},
				})
			}
		}
		_, _ = svc.IsCappedBatch(ctx, lookups)
	}
}

