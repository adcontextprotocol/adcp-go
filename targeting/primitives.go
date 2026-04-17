package targeting

// Bitmap is a set of string values with O(1) membership test.
// For small sets (<10K), MapBitmap is a stdlib-only alternative.
type Bitmap interface {
	Contains(v string) bool
}

// MapBitmap is a stdlib-only Bitmap backed by a map.
type MapBitmap map[string]struct{}

// Contains reports whether v is in the bitmap.
func (m MapBitmap) Contains(v string) bool {
	_, ok := m[v]
	return ok
}

// NewMapBitmap creates a MapBitmap from a slice of values.
func NewMapBitmap(values ...string) MapBitmap {
	m := make(MapBitmap, len(values))
	for _, v := range values {
		m[v] = struct{}{}
	}
	return m
}

// PropertyList defines property-level targeting using bitmaps.
type PropertyList struct {
	// Global is the set of all property RIDs this agent handles.
	// A request for a property not in Global is rejected immediately.
	// Nil means no global filter (all properties pass).
	Global Bitmap

	// ByPackage maps package_id to a property RID bitmap.
	// If a package has an entry, only those properties are eligible.
	// If a package has no entry, all properties are eligible for it.
	ByPackage map[string]Bitmap
}

// ContainsGlobal checks if a property RID passes the global filter.
func (p *PropertyList) ContainsGlobal(rid string) bool {
	if p.Global == nil {
		return true
	}
	return p.Global.Contains(rid)
}

// ContainsPackage checks if a property RID is eligible for a package.
// Returns true if no per-package targeting is configured.
func (p *PropertyList) ContainsPackage(packageID string, rid string) bool {
	if p.ByPackage == nil {
		return true
	}
	bm, ok := p.ByPackage[packageID]
	if !ok {
		return true
	}
	return bm.Contains(rid)
}
