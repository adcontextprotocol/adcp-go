package registry

import "sync"

// AgentIndex stores agent profiles by URL for simple lookups.
// Discovery-oriented search (filtered by channel/market/etc.) is handled by
// the server's search endpoint; this index exists for local CRUD and listing.
type AgentIndex struct {
	mu    sync.RWMutex
	byURL map[string]*AgentProfile
}

// NewAgentIndex creates an empty agent index.
func NewAgentIndex() *AgentIndex {
	return &AgentIndex{
		byURL: make(map[string]*AgentProfile),
	}
}

// Put inserts or updates an agent profile. The index takes ownership of a
// deep copy; the caller may safely mutate p after Put returns.
func (idx *AgentIndex) Put(p *AgentProfile) {
	idx.mu.Lock()
	defer idx.mu.Unlock()
	idx.byURL[p.AgentURL] = cloneAgentProfile(p)
}

// Remove deletes an agent profile. Returns true if it existed.
func (idx *AgentIndex) Remove(agentURL string) bool {
	idx.mu.Lock()
	defer idx.mu.Unlock()
	_, ok := idx.byURL[agentURL]
	if ok {
		delete(idx.byURL, agentURL)
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

// Clear removes all entries from the index.
func (idx *AgentIndex) Clear() {
	idx.mu.Lock()
	defer idx.mu.Unlock()
	idx.byURL = make(map[string]*AgentProfile)
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
