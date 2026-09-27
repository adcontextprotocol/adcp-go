package router

import (
	"reflect"
	"slices"
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
// Reads take an atomic snapshot and return ownership-isolated copies.
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
	owned := cloneProviderConfigs(all)
	active := make([]ProviderConfig, 0, len(owned))
	for _, p := range owned {
		if p.EffectiveStatus() == ProviderStatusActive {
			active = append(active, p)
		}
	}
	return providerSnapshot{all: owned, active: active, revision: revision}
}

func cloneProviderConfigs(src []ProviderConfig) []ProviderConfig {
	if src == nil {
		return nil
	}
	dst := make([]ProviderConfig, len(src))
	for i := range src {
		dst[i] = cloneProviderConfig(src[i])
	}
	return dst
}

func cloneProviderConfig(src ProviderConfig) ProviderConfig {
	dst := src
	dst.WireFormats = slices.Clone(src.WireFormats)
	dst.PropertyIDs = slices.Clone(src.PropertyIDs)
	dst.PropertyRIDs = slices.Clone(src.PropertyRIDs)
	dst.ExcludePropertyIDs = slices.Clone(src.ExcludePropertyIDs)
	dst.PropertyTypes = slices.Clone(src.PropertyTypes)
	dst.PackageIDs = slices.Clone(src.PackageIDs)
	dst.Countries = slices.Clone(src.Countries)
	dst.UIDTypes = slices.Clone(src.UIDTypes)
	dst.TmpxSlots = slices.Clone(src.TmpxSlots)
	dst.AudienceKIDs = slices.Clone(src.AudienceKIDs)
	return dst
}

func (ps *ProviderSet) snapshot() providerSnapshot {
	return ps.v.Load().(providerSnapshot)
}

// All returns a snapshot of all providers.
func (ps *ProviderSet) All() []ProviderConfig {
	return cloneProviderConfigs(ps.snapshot().all)
}

// Active returns an ownership-isolated copy of providers with effective status
// "active" from the cached snapshot.
func (ps *ProviderSet) Active() []ProviderConfig {
	return cloneProviderConfigs(ps.snapshot().active)
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
			return cloneProviderConfig(p), snapshot.revision, true
		}
	}
	return ProviderConfig{}, snapshot.revision, false
}

// Get returns the config for a single provider by ID.
func (ps *ProviderSet) Get(id string) (ProviderConfig, bool) {
	for _, p := range ps.snapshot().all {
		if p.ID == id {
			return cloneProviderConfig(p), true
		}
	}
	return ProviderConfig{}, false
}
