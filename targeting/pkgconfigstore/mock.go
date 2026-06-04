package pkgconfigstore

import (
	"context"
	"sync"
	"time"
)

// MockStore is a goroutine-safe in-memory Store for tests.
type MockStore struct {
	mu      sync.RWMutex
	strings map[string]string
}

// NewMockStore returns an empty MockStore.
func NewMockStore() *MockStore {
	return &MockStore{strings: make(map[string]string)}
}

func (m *MockStore) Get(_ context.Context, key string) (string, bool, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	v, ok := m.strings[key]
	return v, ok, nil
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

func (m *MockStore) Set(_ context.Context, key, value string, _ time.Duration) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.strings[key] = value
	return nil
}

func (m *MockStore) Del(_ context.Context, keys ...string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, k := range keys {
		delete(m.strings, k)
	}
	return nil
}
