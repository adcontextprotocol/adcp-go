package audience

import (
	"context"
	"maps"
	"sync"
)

// MockStore is an in-memory Store for unit tests.
type MockStore struct {
	mu     sync.RWMutex
	hashes map[string]map[string]string // key -> field -> value
}

// NewMockStore creates an empty MockStore.
func NewMockStore() *MockStore {
	return &MockStore{
		hashes: make(map[string]map[string]string),
	}
}

func (m *MockStore) HSetBatch(_ context.Context, items []HSetItem) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, it := range items {
		m.hsetLocked(it.Key, it.Field, it.Value)
	}
	return nil
}

func (m *MockStore) HExistsBatch(_ context.Context, lookups []HLookup) ([]bool, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]bool, len(lookups))
	for i, l := range lookups {
		out[i] = m.hexistsLocked(l.Key, l.Field)
	}
	return out, nil
}

func (m *MockStore) HGetAll(_ context.Context, key string) (map[string]string, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	h, ok := m.hashes[key]
	if !ok {
		return map[string]string{}, nil
	}
	out := make(map[string]string, len(h))
	maps.Copy(out, h)
	return out, nil
}

func (m *MockStore) HGetAllBatch(ctx context.Context, keys []string) ([]map[string]string, error) {
	out := make([]map[string]string, len(keys))
	for i, k := range keys {
		v, err := m.HGetAll(ctx, k)
		if err != nil {
			return nil, err
		}
		out[i] = v
	}
	return out, nil
}

func (m *MockStore) HDelBatch(_ context.Context, items []HDelItem) error {
	if len(items) == 0 {
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, it := range items {
		if len(it.Fields) == 0 {
			continue
		}
		m.hdelLocked(it.Key, it.Fields)
	}
	return nil
}

// hsetLocked writes (key, field, value); caller holds the lock.
func (m *MockStore) hsetLocked(key, field, value string) {
	h, ok := m.hashes[key]
	if !ok {
		h = make(map[string]string)
		m.hashes[key] = h
	}
	h[field] = value
}

// hdelLocked removes fields from the hash at key, deleting the key when the
// hash becomes empty (matching Valkey HDEL semantics). Caller holds the lock.
func (m *MockStore) hdelLocked(key string, fields []string) {
	h, ok := m.hashes[key]
	if !ok {
		return
	}
	for _, f := range fields {
		delete(h, f)
	}
	if len(h) == 0 {
		delete(m.hashes, key)
	}
}

// hexistsLocked reports whether field exists under key; caller holds lock.
func (m *MockStore) hexistsLocked(key, field string) bool {
	h, ok := m.hashes[key]
	if !ok {
		return false
	}
	_, ok = h[field]
	return ok
}
