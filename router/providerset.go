package router

import (
	"sync"
	"sync/atomic"
)

// providerSnapshot holds both the full provider list and the pre-filtered active subset.
type providerSnapshot struct {
	all    []ProviderConfig
	active []ProviderConfig
}

// ProviderSet holds the current set of providers with atomic read access.
// Reads (Active, All) are lock-free via atomic.Value.
// Writes (Swap, SetStatus) are serialized by a mutex.
type ProviderSet struct {
	v  atomic.Value // holds providerSnapshot
	mu sync.Mutex   // serializes writes
}

// NewProviderSet creates a ProviderSet with the given initial providers.
func NewProviderSet(initial []ProviderConfig) *ProviderSet {
	ps := &ProviderSet{}
	if initial == nil {
		initial = []ProviderConfig{}
	}
	ps.v.Store(buildSnapshot(initial))
	return ps
}

func buildSnapshot(all []ProviderConfig) providerSnapshot {
	active := make([]ProviderConfig, 0, len(all))
	for _, p := range all {
		if p.EffectiveStatus() == ProviderStatusActive {
			active = append(active, p)
		}
	}
	return providerSnapshot{all: all, active: active}
}

func (ps *ProviderSet) snapshot() providerSnapshot {
	return ps.v.Load().(providerSnapshot)
}

// All returns a snapshot of all providers.
func (ps *ProviderSet) All() []ProviderConfig {
	return ps.snapshot().all
}

// Active returns providers with effective status "active".
// This is a cached snapshot — no allocation on the read path.
func (ps *ProviderSet) Active() []ProviderConfig {
	return ps.snapshot().active
}

// Swap atomically replaces the entire provider set.
func (ps *ProviderSet) Swap(next []ProviderConfig) {
	if next == nil {
		next = []ProviderConfig{}
	}
	ps.mu.Lock()
	defer ps.mu.Unlock()
	ps.v.Store(buildSnapshot(next))
}

// SetStatus updates a single provider's status via copy-on-write.
// Returns true if the provider was found.
func (ps *ProviderSet) SetStatus(id string, status ProviderStatus) bool {
	ps.mu.Lock()
	defer ps.mu.Unlock()
	current := ps.snapshot().all
	for i, p := range current {
		if p.ID == id {
			next := make([]ProviderConfig, len(current))
			copy(next, current)
			next[i].Status = status
			ps.v.Store(buildSnapshot(next))
			return true
		}
	}
	return false
}

// Get returns the config for a single provider by ID.
func (ps *ProviderSet) Get(id string) (ProviderConfig, bool) {
	for _, p := range ps.All() {
		if p.ID == id {
			return p, true
		}
	}
	return ProviderConfig{}, false
}
