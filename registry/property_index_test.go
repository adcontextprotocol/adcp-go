package registry

import (
	"sync"
	"testing"
)

func TestPropertyIndex_PutAndLookup(t *testing.T) {
	idx := NewPropertyIndex()

	p := &Property{
		PropertyID:   "pub1.example.com/homepage",
		PropertyRID:  1001,
		PropertyType: "website",
		Domain:       "example.com",
		Placements:   []string{"top-banner", "sidebar"},
	}
	idx.Put(p)

	if idx.Count() != 1 {
		t.Fatalf("count = %d, want 1", idx.Count())
	}

	// Lookup by ID
	got, ok := idx.LookupByID("pub1.example.com/homepage")
	if !ok {
		t.Fatal("LookupByID: not found")
	}
	if got.PropertyRID != 1001 {
		t.Errorf("rid = %d, want 1001", got.PropertyRID)
	}

	// Lookup by RID (reverse direction)
	got, ok = idx.LookupByRID(1001)
	if !ok {
		t.Fatal("LookupByRID: not found")
	}
	if got.PropertyID != "pub1.example.com/homepage" {
		t.Errorf("id = %q", got.PropertyID)
	}
	if got.Domain != "example.com" {
		t.Errorf("domain = %q", got.Domain)
	}

	// Lookup by domain
	id, ok := idx.LookupByDomain("example.com")
	if !ok {
		t.Fatal("LookupByDomain: not found")
	}
	if id != "pub1.example.com/homepage" {
		t.Errorf("id = %q", id)
	}

	// PropertyRID shortcut
	if rid := idx.PropertyRID("pub1.example.com/homepage"); rid != 1001 {
		t.Errorf("PropertyRID = %d, want 1001", rid)
	}
	if rid := idx.PropertyRID("nonexistent"); rid != 0 {
		t.Errorf("PropertyRID(missing) = %d, want 0", rid)
	}
}

func TestPropertyIndex_Update(t *testing.T) {
	idx := NewPropertyIndex()

	idx.Put(&Property{PropertyID: "prop1", PropertyRID: 100, Domain: "old.com", PropertyType: "website"})

	// Update: RID and domain change
	idx.Put(&Property{PropertyID: "prop1", PropertyRID: 200, Domain: "new.com", PropertyType: "ctv_app"})

	if idx.Count() != 1 {
		t.Fatalf("count = %d, want 1", idx.Count())
	}

	// Old RID should not resolve
	if _, ok := idx.LookupByRID(100); ok {
		t.Error("old RID 100 should be removed")
	}

	// New RID should resolve
	got, ok := idx.LookupByRID(200)
	if !ok {
		t.Fatal("new RID 200 not found")
	}
	if got.PropertyType != "ctv_app" {
		t.Errorf("type = %q, want ctv_app", got.PropertyType)
	}

	// Old domain should not resolve
	if _, ok := idx.LookupByDomain("old.com"); ok {
		t.Error("old domain should be removed")
	}

	// New domain should resolve
	if id, ok := idx.LookupByDomain("new.com"); !ok || id != "prop1" {
		t.Errorf("LookupByDomain(new.com) = %q, %v", id, ok)
	}
}

func TestPropertyIndex_Remove(t *testing.T) {
	idx := NewPropertyIndex()

	idx.Put(&Property{PropertyID: "prop1", PropertyRID: 100, Domain: "example.com"})

	if !idx.Remove("prop1") {
		t.Error("Remove returned false for existing property")
	}
	if idx.Remove("prop1") {
		t.Error("Remove returned true for already-removed property")
	}
	if idx.Count() != 0 {
		t.Errorf("count = %d, want 0", idx.Count())
	}
	if _, ok := idx.LookupByRID(100); ok {
		t.Error("RID should be removed")
	}
	if _, ok := idx.LookupByDomain("example.com"); ok {
		t.Error("domain should be removed")
	}
}

func TestPropertyIndex_NoDomain(t *testing.T) {
	idx := NewPropertyIndex()

	idx.Put(&Property{PropertyID: "prop1", PropertyRID: 100}) // no domain

	if _, ok := idx.LookupByID("prop1"); !ok {
		t.Error("should find by ID")
	}
	if _, ok := idx.LookupByRID(100); !ok {
		t.Error("should find by RID")
	}
	// No domain entry should exist
	if idx.Remove("prop1"); idx.Count() != 0 {
		t.Error("should be empty after remove")
	}
}

func TestPropertyIndex_Concurrent(t *testing.T) {
	idx := NewPropertyIndex()
	var wg sync.WaitGroup

	// Concurrent writers
	for i := range 100 {
		wg.Add(1)
		go func(n uint64) {
			defer wg.Done()
			p := &Property{
				PropertyID:  "prop",
				PropertyRID: n,
				Domain:      "example.com",
			}
			idx.Put(p)
		}(uint64(i)) //nolint:gosec // test values are small
	}

	// Concurrent readers
	for range 100 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			idx.LookupByID("prop")
			idx.LookupByRID(50)
			idx.LookupByDomain("example.com")
			idx.PropertyRID("prop")
			idx.Count()
		}()
	}

	wg.Wait()

	// Should have exactly one property (all writers target the same ID)
	if idx.Count() != 1 {
		t.Errorf("count = %d, want 1", idx.Count())
	}
}
