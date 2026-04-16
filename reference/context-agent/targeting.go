package contextagent

import "github.com/adcontextprotocol/adcp-go/targeting"

// SetBitmap is a string-set adapter satisfying targeting.Bitmap.
type SetBitmap struct {
	set map[string]struct{}
}

// NewSetBitmap creates an empty SetBitmap.
func NewSetBitmap() *SetBitmap {
	return &SetBitmap{set: make(map[string]struct{})}
}

// Contains reports whether v is in the set.
func (s *SetBitmap) Contains(v string) bool {
	_, ok := s.set[v]
	return ok
}

// Add inserts v into the set.
func (s *SetBitmap) Add(v string) {
	s.set[v] = struct{}{}
}

// Remove deletes v from the set.
func (s *SetBitmap) Remove(v string) {
	delete(s.set, v)
}

// Ensure SetBitmap satisfies the targeting.Bitmap interface.
var _ targeting.Bitmap = (*SetBitmap)(nil)

// TargetingConfig holds string-set bitmaps for property-level targeting.
// The PropertyBitmap is the global pre-filter: if a property RID is not
// in this bitmap, the request is rejected before any other evaluation.
// PackageTargets holds per-package bitmaps for finer-grained control.
type TargetingConfig struct {
	PropertyBitmap *SetBitmap            // Global property targeting set
	PackageTargets map[string]*SetBitmap // package_id -> property bitmap
}

// NewTargetingConfig creates an empty targeting config.
func NewTargetingConfig() *TargetingConfig {
	return &TargetingConfig{
		PropertyBitmap: NewSetBitmap(),
		PackageTargets: make(map[string]*SetBitmap),
	}
}

// AddProperties adds property RIDs to the global targeting bitmap.
func (t *TargetingConfig) AddProperties(rids ...string) {
	for _, rid := range rids {
		t.PropertyBitmap.Add(rid)
	}
}

// AddPackageProperties adds property RIDs to a specific package's targeting bitmap.
func (t *TargetingConfig) AddPackageProperties(packageID string, rids ...string) {
	bm, ok := t.PackageTargets[packageID]
	if !ok {
		bm = NewSetBitmap()
		t.PackageTargets[packageID] = bm
	}
	for _, rid := range rids {
		bm.Add(rid)
	}
}

// ContainsProperty checks if a property RID is in the global targeting set.
func (t *TargetingConfig) ContainsProperty(rid string) bool {
	return t.PropertyBitmap.Contains(rid)
}

// ContainsPackageProperty checks if a property RID is in a package's targeting set.
// Returns true if the package has no specific targeting (all properties allowed).
func (t *TargetingConfig) ContainsPackageProperty(packageID string, rid string) bool {
	bm, ok := t.PackageTargets[packageID]
	if !ok {
		return true // No per-package targeting means all properties allowed
	}
	return bm.Contains(rid)
}

// BuildFromRegistry populates the global bitmap from a list of targeted RIDs.
func (t *TargetingConfig) BuildFromRegistry(targetedRIDs []string) {
	t.PropertyBitmap = NewSetBitmap()
	for _, rid := range targetedRIDs {
		t.PropertyBitmap.Add(rid)
	}
}
