package registry

import (
	"context"
	"log/slog"
	"sync"
	"sync/atomic"
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
// dual-writes to that store. The in-memory map is always updated; if
// the persistent write fails, the mutator returns the error so the
// caller (Syncer) can refuse to advance the feed cursor.
//
// Quiescence invariant: callers must not mutate the index between
// WithStore and the first Hydrate / Syncer.Run. Doing so produces
// undefined state because Hydrate's loader does not coordinate with
// in-flight Puts.
type PropertyIndex struct {
	mu       sync.RWMutex
	byID     map[string]*Property
	byRID    map[uint64]*Property
	byDomain map[string]string // domain → property_id
	store    Store
	hydrated atomic.Bool
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

// WithStore enables dual-write persistence and hydration. Must be
// called once before any mutation; concurrent calls with mutators
// produce undefined state.
func (idx *PropertyIndex) WithStore(s Store) *PropertyIndex {
	idx.store = s
	return idx
}

// Put inserts or updates a property, maintaining all indexes. Returns
// an error only when the persistent store rejected the write; the
// in-memory update always succeeds.
func (idx *PropertyIndex) Put(ctx context.Context, p *Property) error {
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

	if store == nil {
		return nil
	}
	if !idx.hydrated.Load() {
		idx.log.Warn("Put before Hydrate; persisted state may be inconsistent",
			"property_id", p.PropertyID)
	}
	if err := store.PutProperty(ctx, cp); err != nil {
		idx.log.Error("persist PutProperty failed", "property_id", p.PropertyID, "error", err)
		return err
	}
	return nil
}

// Remove deletes a property from all indexes.
func (idx *PropertyIndex) Remove(ctx context.Context, propertyID string) error {
	idx.mu.Lock()
	store := idx.store
	if existing, ok := idx.byID[propertyID]; ok {
		delete(idx.byID, existing.PropertyID)
		delete(idx.byRID, existing.PropertyRID)
		if existing.Domain != "" {
			delete(idx.byDomain, existing.Domain)
		}
	}
	idx.mu.Unlock()

	if store == nil {
		return nil
	}
	if err := store.RemoveProperty(ctx, propertyID); err != nil {
		idx.log.Error("persist RemoveProperty failed", "property_id", propertyID, "error", err)
		return err
	}
	return nil
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
// the corresponding namespace is also wiped. Memory is cleared
// regardless of the persistent store result.
func (idx *PropertyIndex) Clear(ctx context.Context) error {
	idx.mu.Lock()
	store := idx.store
	idx.byID = make(map[string]*Property)
	idx.byRID = make(map[uint64]*Property)
	idx.byDomain = make(map[string]string)
	idx.mu.Unlock()

	if store == nil {
		return nil
	}
	if err := store.ClearProperties(ctx); err != nil {
		idx.log.Error("persist ClearProperties failed", "error", err)
		return err
	}
	return nil
}

// Count returns the number of properties in the index.
func (idx *PropertyIndex) Count() int {
	idx.mu.RLock()
	defer idx.mu.RUnlock()
	return len(idx.byID)
}

// Hydrate loads persisted properties into the in-memory maps. No-op
// when no Store is attached, and idempotent: calling twice is a no-op
// the second time. Hydration runs under the write lock so it does not
// race with feed-loop applies — but quiescence is the caller's
// responsibility for any non-feed-loop writer.
func (idx *PropertyIndex) Hydrate(ctx context.Context) error {
	if idx.store == nil {
		idx.hydrated.Store(true)
		return nil
	}
	if !idx.hydrated.CompareAndSwap(false, true) {
		return nil
	}
	props, err := idx.store.LoadProperties(ctx)
	if err != nil {
		idx.hydrated.Store(false)
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
