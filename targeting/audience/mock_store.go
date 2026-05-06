package audience

import (
	"context"
	"sync"
)

// MockStore is an in-memory Store for unit tests.
type MockStore struct {
	mu     sync.RWMutex
	hashes map[string]map[string]string  // key -> field -> value
	sets   map[string]map[string]struct{} // key -> set of members
}

// NewMockStore creates an empty MockStore.
func NewMockStore() *MockStore {
	return &MockStore{
		hashes: make(map[string]map[string]string),
		sets:   make(map[string]map[string]struct{}),
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

func (m *MockStore) HExists(_ context.Context, key, field string) (bool, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.hexistsLocked(key, field), nil
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
	for f, v := range h {
		out[f] = v
	}
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

func (m *MockStore) HDel(_ context.Context, key string, fields []string) error {
	if len(fields) == 0 {
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.hdelLocked(key, fields)
	return nil
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

func (m *MockStore) SAdd(_ context.Context, key string, members []string) error {
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

func (m *MockStore) SRem(_ context.Context, key string, members []string) error {
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

func (m *MockStore) SMembers(_ context.Context, key string) ([]string, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	s, ok := m.sets[key]
	if !ok {
		return nil, nil
	}
	out := make([]string, 0, len(s))
	for mem := range s {
		out = append(out, mem)
	}
	return out, nil
}

func (m *MockStore) Del(_ context.Context, key string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.hashes, key)
	delete(m.sets, key)
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
