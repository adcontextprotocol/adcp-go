package registry

import (
	"context"
	"log/slog"
	"sync"
	"sync/atomic"
)

// AgentIndex stores agent profiles by URL for simple lookups.
// Discovery-oriented search (filtered by channel/market/etc.) is handled by
// the server's search endpoint; this index exists for local CRUD and listing.
//
// When a Store is attached via WithStore, every mutation dual-writes.
// In-memory state is always updated; persistence errors are returned so
// the Syncer can refuse to advance the cursor.
//
// Note on cascade: AgentIndex.Remove does NOT remove an agent's auth
// entries — the Syncer's agent.removed handler also calls
// AuthIndex.RemoveAgent. Callers that bypass the Syncer (admin tools,
// custom replicators) must replicate that cascade or auth entries for
// the deleted agent will leak in both memory and the Store.
type AgentIndex struct {
	mu       sync.RWMutex
	byURL    map[string]*AgentProfile
	store    Store
	hydrated atomic.Bool
	log      *slog.Logger
}

// NewAgentIndex creates an empty agent index.
func NewAgentIndex() *AgentIndex {
	return &AgentIndex{
		byURL: make(map[string]*AgentProfile),
		log:   slog.Default().With("component", "registry-agent-index"),
	}
}

// WithStore enables dual-write persistence and hydration. Must be
// called once before any mutation.
func (idx *AgentIndex) WithStore(s Store) *AgentIndex {
	idx.store = s
	return idx
}

// Put inserts or updates an agent profile. The index takes ownership of a
// deep copy; the caller may safely mutate p after Put returns.
func (idx *AgentIndex) Put(ctx context.Context, p *AgentProfile) error {
	idx.mu.Lock()
	store := idx.store
	cp := cloneAgentProfile(p)
	idx.byURL[cp.AgentURL] = cp
	idx.mu.Unlock()

	if store == nil {
		return nil
	}
	if !idx.hydrated.Load() {
		idx.log.Warn("Put before Hydrate; persisted state may be inconsistent",
			"agent_url", p.AgentURL)
	}
	if err := store.PutAgent(ctx, cp); err != nil {
		idx.log.Error("persist PutAgent failed", "agent_url", p.AgentURL, "error", err)
		return err
	}
	return nil
}

// Remove deletes an agent profile. See the type-level doc note on
// cascade — auth entries for the agent are not removed here; the
// Syncer handles that orchestration.
func (idx *AgentIndex) Remove(ctx context.Context, agentURL string) error {
	idx.mu.Lock()
	store := idx.store
	delete(idx.byURL, agentURL)
	idx.mu.Unlock()

	if store == nil {
		return nil
	}
	if err := store.RemoveAgent(ctx, agentURL); err != nil {
		idx.log.Error("persist RemoveAgent failed", "agent_url", agentURL, "error", err)
		return err
	}
	return nil
}

// Get returns a deep copy of an agent profile by URL.
func (idx *AgentIndex) Get(agentURL string) (AgentProfile, bool) {
	idx.mu.RLock()
	defer idx.mu.RUnlock()
	p, ok := idx.byURL[agentURL]
	if !ok {
		return AgentProfile{}, false
	}
	return *cloneAgentProfile(p), true
}

// Clear removes all entries from the index. When a Store is attached,
// the corresponding namespace is also wiped. Memory is cleared
// regardless of the persistent store result.
func (idx *AgentIndex) Clear(ctx context.Context) error {
	idx.mu.Lock()
	store := idx.store
	idx.byURL = make(map[string]*AgentProfile)
	idx.mu.Unlock()

	if store == nil {
		return nil
	}
	if err := store.ClearAgents(ctx); err != nil {
		idx.log.Error("persist ClearAgents failed", "error", err)
		return err
	}
	return nil
}

// Count returns the number of agents in the index.
func (idx *AgentIndex) Count() int {
	idx.mu.RLock()
	defer idx.mu.RUnlock()
	return len(idx.byURL)
}

// List returns deep copies of all agent profiles.
func (idx *AgentIndex) List() []AgentProfile {
	idx.mu.RLock()
	defer idx.mu.RUnlock()
	out := make([]AgentProfile, 0, len(idx.byURL))
	for _, p := range idx.byURL {
		out = append(out, *cloneAgentProfile(p))
	}
	return out
}

// Hydrate loads persisted agent profiles into memory. No-op when no
// Store is attached. Idempotent: subsequent calls are no-ops.
func (idx *AgentIndex) Hydrate(ctx context.Context) error {
	if idx.store == nil {
		idx.hydrated.Store(true)
		return nil
	}
	if !idx.hydrated.CompareAndSwap(false, true) {
		return nil
	}
	agents, err := idx.store.LoadAgents(ctx)
	if err != nil {
		idx.hydrated.Store(false)
		return err
	}
	idx.mu.Lock()
	defer idx.mu.Unlock()
	for i := range agents {
		p := &agents[i]
		idx.byURL[p.AgentURL] = cloneAgentProfile(p)
	}
	return nil
}
