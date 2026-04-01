package contextagent

import (
	"math"

	"github.com/RoaringBitmap/roaring"
)

// TargetingConfig holds Roaring bitmaps for property-level targeting.
// The PropertyBitmap is the global pre-filter: if a property RID is not
// in this bitmap, the request is rejected before any other evaluation.
// PackageTargets holds per-package bitmaps for finer-grained control.
type TargetingConfig struct {
	PropertyBitmap *roaring.Bitmap            // Global property targeting set
	PackageTargets map[string]*roaring.Bitmap // package_id -> property bitmap
}

// NewTargetingConfig creates an empty targeting config.
func NewTargetingConfig() *TargetingConfig {
	return &TargetingConfig{
		PropertyBitmap: roaring.New(),
		PackageTargets: make(map[string]*roaring.Bitmap),
	}
}

// AddProperties adds property RIDs to the global targeting bitmap.
// RIDs exceeding uint32 range are silently skipped (Roaring bitmap limitation).
func (t *TargetingConfig) AddProperties(rids ...uint64) {
	for _, rid := range rids {
		if rid <= math.MaxUint32 {
			t.PropertyBitmap.Add(uint32(rid))
		}
	}
}

// AddPackageProperties adds property RIDs to a specific package's targeting bitmap.
func (t *TargetingConfig) AddPackageProperties(packageID string, rids ...uint64) {
	bm, ok := t.PackageTargets[packageID]
	if !ok {
		bm = roaring.New()
		t.PackageTargets[packageID] = bm
	}
	for _, rid := range rids {
		if rid <= math.MaxUint32 {
			bm.Add(uint32(rid))
		}
	}
}

// ContainsProperty checks if a property RID is in the global targeting set.
// Returns false for RIDs exceeding uint32 range.
func (t *TargetingConfig) ContainsProperty(rid uint64) bool {
	if rid > math.MaxUint32 {
		return false
	}
	return t.PropertyBitmap.Contains(uint32(rid))
}

// ContainsPackageProperty checks if a property RID is in a package's targeting set.
// Returns true if the package has no specific targeting (all properties allowed).
func (t *TargetingConfig) ContainsPackageProperty(packageID string, rid uint64) bool {
	bm, ok := t.PackageTargets[packageID]
	if !ok {
		return true // No per-package targeting means all properties allowed
	}
	if rid > math.MaxUint32 {
		return false
	}
	return bm.Contains(uint32(rid))
}

// BuildFromRegistry populates the global bitmap from a list of targeted RIDs.
func (t *TargetingConfig) BuildFromRegistry(targetedRIDs []uint64) {
	t.PropertyBitmap = roaring.New()
	for _, rid := range targetedRIDs {
		if rid <= math.MaxUint32 {
			t.PropertyBitmap.Add(uint32(rid))
		}
	}
	t.PropertyBitmap.RunOptimize()
}
