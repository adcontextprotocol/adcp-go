package main

import "github.com/RoaringBitmap/roaring"

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
func (t *TargetingConfig) AddProperties(rids ...uint32) {
	for _, rid := range rids {
		t.PropertyBitmap.Add(rid)
	}
}

// AddPackageProperties adds property RIDs to a specific package's targeting bitmap.
func (t *TargetingConfig) AddPackageProperties(packageID string, rids ...uint32) {
	bm, ok := t.PackageTargets[packageID]
	if !ok {
		bm = roaring.New()
		t.PackageTargets[packageID] = bm
	}
	for _, rid := range rids {
		bm.Add(rid)
	}
}

// ContainsProperty checks if a property RID is in the global targeting set.
func (t *TargetingConfig) ContainsProperty(rid uint32) bool {
	return t.PropertyBitmap.Contains(rid)
}

// ContainsPackageProperty checks if a property RID is in a package's targeting set.
// Returns true if the package has no specific targeting (all properties allowed).
func (t *TargetingConfig) ContainsPackageProperty(packageID string, rid uint32) bool {
	bm, ok := t.PackageTargets[packageID]
	if !ok {
		return true // No per-package targeting means all properties allowed
	}
	return bm.Contains(rid)
}

// BuildFromRegistry populates the global bitmap from a registry and a list of targeted RIDs.
func (t *TargetingConfig) BuildFromRegistry(targetedRIDs []uint32) {
	t.PropertyBitmap = roaring.New()
	for _, rid := range targetedRIDs {
		t.PropertyBitmap.Add(rid)
	}
	t.PropertyBitmap.RunOptimize()
}
