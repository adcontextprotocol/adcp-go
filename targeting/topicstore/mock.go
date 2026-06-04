package topicstore

import (
	"context"
	"sort"
	"sync"
)

// MockStore is a goroutine-safe in-memory Store for tests. It
// satisfies Store (both ReaderStore and WriterStore).
type MockStore struct {
	mu   sync.RWMutex
	sets map[string]map[string]struct{}
}

// NewMockStore returns an empty MockStore.
func NewMockStore() *MockStore {
	return &MockStore{sets: make(map[string]map[string]struct{})}
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

func (m *MockStore) SetIntersect(_ context.Context, keys ...string) ([]string, error) {
	if len(keys) == 0 {
		return nil, nil
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	first, ok := m.sets[keys[0]]
	if !ok {
		return nil, nil
	}
	result := make(map[string]struct{}, len(first))
	for k := range first {
		result[k] = struct{}{}
	}
	for _, key := range keys[1:] {
		s, ok := m.sets[key]
		if !ok {
			return nil, nil
		}
		for k := range result {
			if _, found := s[k]; !found {
				delete(result, k)
			}
		}
	}
	out := make([]string, 0, len(result))
	for k := range result {
		out = append(out, k)
	}
	sort.Strings(out)
	return out, nil
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
	}
	return nil
}
