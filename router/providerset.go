package router

import (
	"reflect"
	"sync"
	"sync/atomic"
)

// providerSnapshot holds both the full provider list and the pre-filtered active subset.
type providerSnapshot struct {
	all      []ProviderConfig
	active   []ProviderConfig
	revision uint64
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
	ps.v.Store(buildSnapshot(initial, 1))
	return ps
}

func buildSnapshot(all []ProviderConfig, revision uint64) providerSnapshot {
	active := make([]ProviderConfig, 0, len(all))
	for _, p := range all {
		if p.EffectiveStatus() == ProviderStatusActive {
			active = append(active, p)
		}
	}
	return providerSnapshot{all: all, active: active, revision: revision}
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
	current := ps.snapshot()
	if reflect.DeepEqual(current.all, next) {
		return
	}
	ps.v.Store(buildSnapshot(next, current.revision+1))
}

// SetStatus updates a single provider's status via copy-on-write.
// Returns true if the provider was found.
func (ps *ProviderSet) SetStatus(id string, status ProviderStatus) bool {
	ps.mu.Lock()
	defer ps.mu.Unlock()
	current := ps.snapshot().all
	for i, p := range current {
		if p.ID == id {
			if p.Status == status {
				return true
			}
			next := make([]ProviderConfig, len(current))
			copy(next, current)
			next[i].Status = status
			ps.v.Store(buildSnapshot(next, ps.snapshot().revision+1))
			return true
		}
	}
	return false
}

// GetWithRevision returns one provider and the monotonic configuration
// revision captured from the same atomic snapshot. The revision advances on
// effective provider-set changes and prevents remove/re-add or endpoint/config
// replacement from resurrecting cache entries created under an older set.
func (ps *ProviderSet) GetWithRevision(id string) (ProviderConfig, uint64, bool) {
	snapshot := ps.snapshot()
	for _, p := range snapshot.all {
		if p.ID == id {
			return p, snapshot.revision, true
		}
	}
	return ProviderConfig{}, snapshot.revision, false
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
