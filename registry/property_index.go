package registry

import (
	"context"
	"log/slog"
	"sync"
)

// Property represents a property in the registry catalog.
type Property struct {
	PropertyID   string   `json:"property_id"`
	PropertyRID  uint64   `json:"property_rid"`
	PropertyType string   `json:"property_type"`
	Domain       string   `json:"domain"`
	Placements   []string `json:"placements,omitempty"`
}

// PropertyIndex provides O(1) bidirectional lookups between property_id,
// property_rid, and domain. Thread-safe for concurrent reads with periodic
// writes from the sync loop.
//
// When an optional Store is attached via WithStore, every mutation
// dual-writes to that store. Persistence failures are logged but never
// block the in-memory update — the feed cursor must keep advancing.
type PropertyIndex struct {
	mu       sync.RWMutex
	byID     map[string]*Property
	byRID    map[uint64]*Property
	byDomain map[string]string // domain → property_id
	store    Store
	log      *slog.Logger
}

// NewPropertyIndex creates an empty property index.
func NewPropertyIndex() *PropertyIndex {
	return &PropertyIndex{
		byID:     make(map[string]*Property),
		byRID:    make(map[uint64]*Property),
		byDomain: make(map[string]string),
		log:      slog.Default().With("component", "registry-property-index"),
	}
}

// WithStore enables dual-write persistence and hydration. Pass nil to
// detach. Returns the receiver for chaining.
func (idx *PropertyIndex) WithStore(s Store) *PropertyIndex {
	idx.mu.Lock()
	defer idx.mu.Unlock()
	idx.store = s
	return idx
}

// Put inserts or updates a property, maintaining all indexes.
func (idx *PropertyIndex) Put(p *Property) {
	idx.mu.Lock()
	store := idx.store
	if existing, ok := idx.byID[p.PropertyID]; ok {
		delete(idx.byRID, existing.PropertyRID)
		if existing.Domain != "" {
			delete(idx.byDomain, existing.Domain)
		}
	}
	cp := cloneProperty(p)
	idx.byID[cp.PropertyID] = cp
	idx.byRID[cp.PropertyRID] = cp
	if cp.Domain != "" {
		idx.byDomain[cp.Domain] = cp.PropertyID
	}
	idx.mu.Unlock()

	if store != nil {
		if err := store.PutProperty(context.Background(), p); err != nil {
			idx.log.Error("persist PutProperty failed", "property_id", p.PropertyID, "error", err)
		}
	}
}

// Remove deletes a property from all indexes. Returns true if it existed.
func (idx *PropertyIndex) Remove(propertyID string) bool {
	idx.mu.Lock()
	store := idx.store
	existing, ok := idx.byID[propertyID]
	if !ok {
		idx.mu.Unlock()
		if store != nil {
			if err := store.RemoveProperty(context.Background(), propertyID); err != nil {
				idx.log.Error("persist RemoveProperty failed", "property_id", propertyID, "error", err)
			}
		}
		return false
	}
	delete(idx.byID, existing.PropertyID)
	delete(idx.byRID, existing.PropertyRID)
	if existing.Domain != "" {
		delete(idx.byDomain, existing.Domain)
	}
	idx.mu.Unlock()

	if store != nil {
		if err := store.RemoveProperty(context.Background(), propertyID); err != nil {
			idx.log.Error("persist RemoveProperty failed", "property_id", propertyID, "error", err)
		}
	}
	return true
}

// LookupByID returns a copy of a property by its string ID.
func (idx *PropertyIndex) LookupByID(propertyID string) (Property, bool) {
	idx.mu.RLock()
	defer idx.mu.RUnlock()
	p, ok := idx.byID[propertyID]
	if !ok {
		return Property{}, false
	}
	return *p, true
}

// LookupByRID returns a copy of a property by its integer registry ID.
func (idx *PropertyIndex) LookupByRID(rid uint64) (Property, bool) {
	idx.mu.RLock()
	defer idx.mu.RUnlock()
	p, ok := idx.byRID[rid]
	if !ok {
		return Property{}, false
	}
	return *p, true
}

// LookupByDomain returns the property_id for a domain.
func (idx *PropertyIndex) LookupByDomain(domain string) (string, bool) {
	idx.mu.RLock()
	defer idx.mu.RUnlock()
	id, ok := idx.byDomain[domain]
	return id, ok
}

// PropertyRID returns the integer registry ID for a property_id.
// Returns 0 if not found.
func (idx *PropertyIndex) PropertyRID(propertyID string) uint64 {
	idx.mu.RLock()
	defer idx.mu.RUnlock()
	if p, ok := idx.byID[propertyID]; ok {
		return p.PropertyRID
	}
	return 0
}

// Clear removes all entries from the index. When a Store is attached,
// the corresponding namespace is also wiped.
func (idx *PropertyIndex) Clear() {
	idx.mu.Lock()
	store := idx.store
	idx.byID = make(map[string]*Property)
	idx.byRID = make(map[uint64]*Property)
	idx.byDomain = make(map[string]string)
	idx.mu.Unlock()

	if store != nil {
		if err := store.ClearProperties(context.Background()); err != nil {
			idx.log.Error("persist ClearProperties failed", "error", err)
		}
	}
}

// Count returns the number of properties in the index.
func (idx *PropertyIndex) Count() int {
	idx.mu.RLock()
	defer idx.mu.RUnlock()
	return len(idx.byID)
}

// Hydrate loads persisted properties into the in-memory maps. No-op when
// no Store is attached. Existing in-memory entries are replaced; entries
// not present in the store are left untouched (full reset uses Clear).
func (idx *PropertyIndex) Hydrate(ctx context.Context) error {
	idx.mu.RLock()
	store := idx.store
	idx.mu.RUnlock()
	if store == nil {
		return nil
	}
	props, err := store.LoadProperties(ctx)
	if err != nil {
		return err
	}
	idx.mu.Lock()
	defer idx.mu.Unlock()
	for i := range props {
		p := &props[i]
		if existing, ok := idx.byID[p.PropertyID]; ok {
			delete(idx.byRID, existing.PropertyRID)
			if existing.Domain != "" {
				delete(idx.byDomain, existing.Domain)
			}
		}
		cp := cloneProperty(p)
		idx.byID[cp.PropertyID] = cp
		idx.byRID[cp.PropertyRID] = cp
		if cp.Domain != "" {
			idx.byDomain[cp.Domain] = cp.PropertyID
		}
	}
	return nil
}
