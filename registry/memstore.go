package registry

import (
	"context"
	"strings"
	"sync"
)

// MemoryStore is an in-memory implementation of Store. It is useful in
// tests and as a reference for the contract real backends must satisfy.
// Not intended for production: a process restart loses all state, which
// defeats the purpose of attaching a Store.
type MemoryStore struct {
	mu         sync.Mutex
	cursor     string
	properties map[string]Property
	agents     map[string]AgentProfile
	// auth keyed by agent_url → "publisher_domain|authorization_type" → entry
	auth map[string]map[string]AuthorizationEntry
}

var _ Store = (*MemoryStore)(nil)

// NewMemoryStore creates an empty MemoryStore.
func NewMemoryStore() *MemoryStore {
	return &MemoryStore{
		properties: make(map[string]Property),
		agents:     make(map[string]AgentProfile),
		auth:       make(map[string]map[string]AuthorizationEntry),
	}
}

func (m *MemoryStore) Load(_ context.Context) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.cursor, nil
}

func (m *MemoryStore) Save(_ context.Context, cursor string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.cursor = cursor
	return nil
}

func (m *MemoryStore) PutProperty(_ context.Context, p *Property) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.properties[p.PropertyID] = *cloneProperty(p)
	return nil
}

func (m *MemoryStore) RemoveProperty(_ context.Context, propertyID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.properties, propertyID)
	return nil
}

func (m *MemoryStore) ClearProperties(_ context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.properties = make(map[string]Property)
	return nil
}

func (m *MemoryStore) LoadProperties(_ context.Context) ([]Property, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]Property, 0, len(m.properties))
	for _, p := range m.properties {
		out = append(out, *cloneProperty(&p))
	}
	return out, nil
}

func (m *MemoryStore) PutAuth(_ context.Context, e AuthorizationEntry) error {
	if err := ValidatePublisherDomain(e.PublisherDomain); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	byKey, ok := m.auth[e.AgentURL]
	if !ok {
		byKey = make(map[string]AuthorizationEntry)
		m.auth[e.AgentURL] = byKey
	}
	byKey[authFieldKey(e.PublisherDomain, e.AuthorizationType)] = cloneAuthEntry(e)
	return nil
}

func (m *MemoryStore) RemoveAuthEntry(_ context.Context, agentURL, publisherDomain string) error {
	if err := ValidatePublisherDomain(publisherDomain); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	byKey, ok := m.auth[agentURL]
	if !ok {
		return nil
	}
	prefix := publisherDomain + "|"
	for k := range byKey {
		if strings.HasPrefix(k, prefix) {
			delete(byKey, k)
		}
	}
	if len(byKey) == 0 {
		delete(m.auth, agentURL)
	}
	return nil
}

func (m *MemoryStore) RemoveAuthAgent(_ context.Context, agentURL string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.auth, agentURL)
	return nil
}

func (m *MemoryStore) ClearAuth(_ context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.auth = make(map[string]map[string]AuthorizationEntry)
	return nil
}

func (m *MemoryStore) LoadAuth(_ context.Context) ([]AuthorizationEntry, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]AuthorizationEntry, 0)
	for _, byKey := range m.auth {
		for _, e := range byKey {
			out = append(out, cloneAuthEntry(e))
		}
	}
	return out, nil
}

func (m *MemoryStore) PutAgent(_ context.Context, p *AgentProfile) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.agents[p.AgentURL] = *cloneAgentProfile(p)
	return nil
}

func (m *MemoryStore) RemoveAgent(_ context.Context, agentURL string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.agents, agentURL)
	return nil
}

func (m *MemoryStore) ClearAgents(_ context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.agents = make(map[string]AgentProfile)
	return nil
}

func (m *MemoryStore) LoadAgents(_ context.Context) ([]AgentProfile, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]AgentProfile, 0, len(m.agents))
	for _, p := range m.agents {
		out = append(out, *cloneAgentProfile(&p))
	}
	return out, nil
}

// authFieldKey is the canonical encoding used by every Store backend so
// the (publisher_domain, authorization_type) tuple round-trips through
// HSET/HDEL/HSCAN field names. publisher_domain is validated by
// ValidatePublisherDomain to ensure '|' is unambiguous as a separator.
func authFieldKey(publisherDomain, authorizationType string) string {
	return publisherDomain + "|" + authorizationType
}
