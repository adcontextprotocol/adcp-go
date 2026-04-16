package router

import (
	"sync"
	"sync/atomic"
)

// ProviderSet holds the current set of providers with atomic read access.
// Reads (Active, All) are lock-free via atomic.Value.
// Writes (Swap, SetStatus) are serialized by a mutex.
type ProviderSet struct {
	v  atomic.Value // holds []ProviderConfig
	mu sync.Mutex   // serializes writes
}

// NewProviderSet creates a ProviderSet with the given initial providers.
func NewProviderSet(initial []ProviderConfig) *ProviderSet {
	ps := &ProviderSet{}
	if initial == nil {
		initial = []ProviderConfig{}
	}
	ps.v.Store(initial)
	return ps
}

// All returns a snapshot of all providers.
func (ps *ProviderSet) All() []ProviderConfig {
	return ps.v.Load().([]ProviderConfig)
}

// Active returns providers with effective status "active".
func (ps *ProviderSet) Active() []ProviderConfig {
	all := ps.All()
	out := make([]ProviderConfig, 0, len(all))
	for _, p := range all {
		if p.EffectiveStatus() == ProviderStatusActive {
			out = append(out, p)
		}
	}
	return out
}

// Swap atomically replaces the entire provider set.
func (ps *ProviderSet) Swap(next []ProviderConfig) {
	if next == nil {
		next = []ProviderConfig{}
	}
	ps.mu.Lock()
	defer ps.mu.Unlock()
	ps.v.Store(next)
}

// SetStatus updates a single provider's status via copy-on-write.
// Returns true if the provider was found.
func (ps *ProviderSet) SetStatus(id string, status ProviderStatus) bool {
	ps.mu.Lock()
	defer ps.mu.Unlock()
	current := ps.v.Load().([]ProviderConfig)
	for i, p := range current {
		if p.ID == id {
			next := make([]ProviderConfig, len(current))
			copy(next, current)
			next[i].Status = status
			ps.v.Store(next)
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
