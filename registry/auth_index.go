package registry

import (
	"context"
	"log/slog"
	"sync"
)

// AuthorizationEntry represents a single authorization grant from a publisher
// to an agent, as declared in adagents.json.
type AuthorizationEntry struct {
	AgentURL          string   `json:"agent_url"`
	PublisherDomain   string   `json:"publisher_domain"`
	AuthorizationType string   `json:"authorization_type"` // property_ids, property_tags, publisher_properties, inline_properties
	AuthorizedFor     string   `json:"authorized_for,omitempty"`
	PropertyIDs       []string `json:"property_ids,omitempty"`
	PlacementIDs      []string `json:"placement_ids,omitempty"`
	Countries         []string `json:"countries,omitempty"`
	DelegationType    string   `json:"delegation_type,omitempty"`
	Exclusive         bool     `json:"exclusive,omitempty"`
}

// AuthIndex provides O(1) authorization checks and reverse lookups.
//
// Primary index: agent_url → publisher_domain → []AuthorizationEntry
// Reverse index: publisher_domain → set[agent_url]
//
// When a Store is attached via WithStore, every mutation dual-writes.
// Persistence failures are logged but never block the in-memory update.
type AuthIndex struct {
	mu      sync.RWMutex
	primary map[string]map[string][]AuthorizationEntry // agent → domain → entries
	reverse map[string]map[string]struct{}             // domain → set of agents
	store   Store
	log     *slog.Logger
}

// NewAuthIndex creates an empty authorization index.
func NewAuthIndex() *AuthIndex {
	return &AuthIndex{
		primary: make(map[string]map[string][]AuthorizationEntry),
		reverse: make(map[string]map[string]struct{}),
		log:     slog.Default().With("component", "registry-auth-index"),
	}
}

// WithStore enables dual-write persistence and hydration.
func (idx *AuthIndex) WithStore(s Store) *AuthIndex {
	idx.mu.Lock()
	defer idx.mu.Unlock()
	idx.store = s
	return idx
}

// Add inserts an authorization entry into both indexes. If an entry with
// the same agent, domain, and authorization type already exists, it is
// replaced rather than duplicated.
func (idx *AuthIndex) Add(entry AuthorizationEntry) {
	idx.mu.Lock()
	store := idx.store

	cp := cloneAuthEntry(entry)

	byDomain, ok := idx.primary[cp.AgentURL]
	if !ok {
		byDomain = make(map[string][]AuthorizationEntry)
		idx.primary[cp.AgentURL] = byDomain
	}

	entries := byDomain[cp.PublisherDomain]
	replaced := false
	for i, e := range entries {
		if e.AuthorizationType == cp.AuthorizationType {
			entries[i] = cp
			replaced = true
			break
		}
	}
	if !replaced {
		entries = append(entries, cp)
	}
	byDomain[cp.PublisherDomain] = entries

	agents, ok := idx.reverse[entry.PublisherDomain]
	if !ok {
		agents = make(map[string]struct{})
		idx.reverse[entry.PublisherDomain] = agents
	}
	agents[entry.AgentURL] = struct{}{}
	idx.mu.Unlock()

	if store != nil {
		if err := store.PutAuth(context.Background(), entry); err != nil {
			idx.log.Error("persist PutAuth failed",
				"agent_url", entry.AgentURL, "publisher_domain", entry.PublisherDomain, "error", err)
		}
	}
}

// RemoveEntry removes all authorization entries for an agent+domain pair.
func (idx *AuthIndex) RemoveEntry(agentURL, publisherDomain string) {
	idx.mu.Lock()
	store := idx.store

	if byDomain, ok := idx.primary[agentURL]; ok {
		delete(byDomain, publisherDomain)
		if len(byDomain) == 0 {
			delete(idx.primary, agentURL)
		}
	}

	if agents, ok := idx.reverse[publisherDomain]; ok {
		delete(agents, agentURL)
		if len(agents) == 0 {
			delete(idx.reverse, publisherDomain)
		}
	}
	idx.mu.Unlock()

	if store != nil {
		if err := store.RemoveAuthEntry(context.Background(), agentURL, publisherDomain); err != nil {
			idx.log.Error("persist RemoveAuthEntry failed",
				"agent_url", agentURL, "publisher_domain", publisherDomain, "error", err)
		}
	}
}

// RemoveAgent removes all authorization entries for an agent across all domains.
func (idx *AuthIndex) RemoveAgent(agentURL string) {
	idx.mu.Lock()
	store := idx.store

	byDomain, ok := idx.primary[agentURL]
	if !ok {
		idx.mu.Unlock()
		if store != nil {
			if err := store.RemoveAuthAgent(context.Background(), agentURL); err != nil {
				idx.log.Error("persist RemoveAuthAgent failed", "agent_url", agentURL, "error", err)
			}
		}
		return
	}

	for domain := range byDomain {
		if agents, ok := idx.reverse[domain]; ok {
			delete(agents, agentURL)
			if len(agents) == 0 {
				delete(idx.reverse, domain)
			}
		}
	}

	delete(idx.primary, agentURL)
	idx.mu.Unlock()

	if store != nil {
		if err := store.RemoveAuthAgent(context.Background(), agentURL); err != nil {
			idx.log.Error("persist RemoveAuthAgent failed", "agent_url", agentURL, "error", err)
		}
	}
}

// Clear removes all entries from the index. When a Store is attached,
// the corresponding namespace is also wiped.
func (idx *AuthIndex) Clear() {
	idx.mu.Lock()
	store := idx.store
	idx.primary = make(map[string]map[string][]AuthorizationEntry)
	idx.reverse = make(map[string]map[string]struct{})
	idx.mu.Unlock()

	if store != nil {
		if err := store.ClearAuth(context.Background()); err != nil {
			idx.log.Error("persist ClearAuth failed", "error", err)
		}
	}
}

// Check returns whether agentURL has any authorization for publisherDomain.
func (idx *AuthIndex) Check(agentURL, publisherDomain string) bool {
	idx.mu.RLock()
	defer idx.mu.RUnlock()

	byDomain, ok := idx.primary[agentURL]
	if !ok {
		return false
	}
	entries, ok := byDomain[publisherDomain]
	return ok && len(entries) > 0
}

// GetEntries returns the authorization entries for an agent+domain pair.
func (idx *AuthIndex) GetEntries(agentURL, publisherDomain string) []AuthorizationEntry {
	idx.mu.RLock()
	defer idx.mu.RUnlock()

	byDomain, ok := idx.primary[agentURL]
	if !ok {
		return nil
	}
	entries := byDomain[publisherDomain]
	out := make([]AuthorizationEntry, len(entries))
	for i, e := range entries {
		out[i] = cloneAuthEntry(e)
	}
	return out
}

// GetAuthorizedAgents returns all agent URLs authorized for a publisher domain.
func (idx *AuthIndex) GetAuthorizedAgents(publisherDomain string) []string {
	idx.mu.RLock()
	defer idx.mu.RUnlock()

	agents, ok := idx.reverse[publisherDomain]
	if !ok {
		return nil
	}
	out := make([]string, 0, len(agents))
	for url := range agents {
		out = append(out, url)
	}
	return out
}

// Count returns the total number of authorization entries.
func (idx *AuthIndex) Count() int {
	idx.mu.RLock()
	defer idx.mu.RUnlock()

	n := 0
	for _, byDomain := range idx.primary {
		for _, entries := range byDomain {
			n += len(entries)
		}
	}
	return n
}

// Hydrate loads persisted authorization entries into memory. No-op when
// no Store is attached.
func (idx *AuthIndex) Hydrate(ctx context.Context) error {
	idx.mu.RLock()
	store := idx.store
	idx.mu.RUnlock()
	if store == nil {
		return nil
	}
	entries, err := store.LoadAuth(ctx)
	if err != nil {
		return err
	}
	idx.mu.Lock()
	defer idx.mu.Unlock()
	for _, entry := range entries {
		cp := cloneAuthEntry(entry)
		byDomain, ok := idx.primary[cp.AgentURL]
		if !ok {
			byDomain = make(map[string][]AuthorizationEntry)
			idx.primary[cp.AgentURL] = byDomain
		}
		existing := byDomain[cp.PublisherDomain]
		replaced := false
		for i, e := range existing {
			if e.AuthorizationType == cp.AuthorizationType {
				existing[i] = cp
				replaced = true
				break
			}
		}
		if !replaced {
			existing = append(existing, cp)
		}
		byDomain[cp.PublisherDomain] = existing

		agents, ok := idx.reverse[cp.PublisherDomain]
		if !ok {
			agents = make(map[string]struct{})
			idx.reverse[cp.PublisherDomain] = agents
		}
		agents[cp.AgentURL] = struct{}{}
	}
	return nil
}
