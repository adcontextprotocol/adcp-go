package registry

import (
	"context"
	"log/slog"
	"sync"
)

// AgentIndex stores agent profiles by URL for simple lookups.
// Discovery-oriented search (filtered by channel/market/etc.) is handled by
// the server's search endpoint; this index exists for local CRUD and listing.
//
// When a Store is attached via WithStore, every mutation dual-writes.
// Persistence failures are logged but never block the in-memory update.
type AgentIndex struct {
	mu    sync.RWMutex
	byURL map[string]*AgentProfile
	store Store
	log   *slog.Logger
}

// NewAgentIndex creates an empty agent index.
func NewAgentIndex() *AgentIndex {
	return &AgentIndex{
		byURL: make(map[string]*AgentProfile),
		log:   slog.Default().With("component", "registry-agent-index"),
	}
}

// WithStore enables dual-write persistence and hydration.
func (idx *AgentIndex) WithStore(s Store) *AgentIndex {
	idx.mu.Lock()
	defer idx.mu.Unlock()
	idx.store = s
	return idx
}

// Put inserts or updates an agent profile. The index takes ownership of a
// deep copy; the caller may safely mutate p after Put returns.
func (idx *AgentIndex) Put(p *AgentProfile) {
	idx.mu.Lock()
	store := idx.store
	idx.byURL[p.AgentURL] = cloneAgentProfile(p)
	idx.mu.Unlock()

	if store != nil {
		if err := store.PutAgent(context.Background(), p); err != nil {
			idx.log.Error("persist PutAgent failed", "agent_url", p.AgentURL, "error", err)
		}
	}
}

// Remove deletes an agent profile. Returns true if it existed.
func (idx *AgentIndex) Remove(agentURL string) bool {
	idx.mu.Lock()
	store := idx.store
	_, ok := idx.byURL[agentURL]
	if ok {
		delete(idx.byURL, agentURL)
	}
	idx.mu.Unlock()

	if store != nil {
		if err := store.RemoveAgent(context.Background(), agentURL); err != nil {
			idx.log.Error("persist RemoveAgent failed", "agent_url", agentURL, "error", err)
		}
	}
	return ok
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
// the corresponding namespace is also wiped.
func (idx *AgentIndex) Clear() {
	idx.mu.Lock()
	store := idx.store
	idx.byURL = make(map[string]*AgentProfile)
	idx.mu.Unlock()

	if store != nil {
		if err := store.ClearAgents(context.Background()); err != nil {
			idx.log.Error("persist ClearAgents failed", "error", err)
		}
	}
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
// Store is attached.
func (idx *AgentIndex) Hydrate(ctx context.Context) error {
	idx.mu.RLock()
	store := idx.store
	idx.mu.RUnlock()
	if store == nil {
		return nil
	}
	agents, err := store.LoadAgents(ctx)
	if err != nil {
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
