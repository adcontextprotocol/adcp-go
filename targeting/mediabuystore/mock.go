package mediabuystore

import (
	"context"
	"sort"
	"sync"
	"time"
)

// MockStore is a goroutine-safe in-memory Store for tests. Implements
// the same subset of Valkey operations the real backends do.
type MockStore struct {
	mu      sync.RWMutex
	sets    map[string]map[string]struct{}
	strings map[string]string
}

// NewMockStore returns an empty MockStore.
func NewMockStore() *MockStore {
	return &MockStore{
		sets:    make(map[string]map[string]struct{}),
		strings: make(map[string]string),
	}
}

func (m *MockStore) SetMembers(_ context.Context, key string) ([]string, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	s, ok := m.sets[key]
	if !ok {
		return nil, nil
	}
	out := make([]string, 0, len(s))
	for k := range s {
		out = append(out, k)
	}
	sort.Strings(out)
	return out, nil
}

func (m *MockStore) MGet(_ context.Context, keys ...string) ([]string, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]string, len(keys))
	for i, k := range keys {
		out[i] = m.strings[k]
	}
	return out, nil
}

func (m *MockStore) Get(_ context.Context, key string) (string, bool, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	v, ok := m.strings[key]
	return v, ok, nil
}

func (m *MockStore) Set(_ context.Context, key, value string, _ time.Duration) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.strings[key] = value
	return nil
}

func (m *MockStore) SetAdd(_ context.Context, key string, members ...string) error {
	if len(members) == 0 {
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.sets[key]
	if !ok {
		s = make(map[string]struct{})
		m.sets[key] = s
	}
	for _, mem := range members {
		s[mem] = struct{}{}
	}
	return nil
}

func (m *MockStore) SetRemove(_ context.Context, key string, members ...string) error {
	if len(members) == 0 {
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.sets[key]
	if !ok {
		return nil
	}
	for _, mem := range members {
		delete(s, mem)
	}
	if len(s) == 0 {
		delete(m.sets, key)
	}
	return nil
}

func (m *MockStore) Del(_ context.Context, keys ...string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, k := range keys {
		delete(m.sets, k)
		delete(m.strings, k)
	}
	return nil
}
