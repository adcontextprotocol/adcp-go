package fcap

import (
	"context"
	"sync"
	"time"
)

// MockStore is an in-memory Store for unit tests.
// Field-level TTLs are tracked per (key, field) pair.
type MockStore struct {
	mu     sync.RWMutex
	fields map[string]map[string]mockField

	// Now controls clock for expiry checks. Defaults to time.Now.
	Now func() time.Time
}

type mockField struct {
	value    string
	expireAt time.Time
}

// NewMockStore creates an empty MockStore.
func NewMockStore() *MockStore {
	return &MockStore{
		fields: make(map[string]map[string]mockField),
		Now:    time.Now,
	}
}

func (m *MockStore) SetFields(_ context.Context, key string, fields map[string]string, expireAt time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	hash, ok := m.fields[key]
	if !ok {
		hash = make(map[string]mockField)
		m.fields[key] = hash
	}
	for f, v := range fields {
		hash[f] = mockField{value: v, expireAt: expireAt}
	}
	return nil
}

func (m *MockStore) SetFieldsBatch(ctx context.Context, batches []FieldsBatch) error {
	for _, b := range batches {
		if err := m.SetFields(ctx, b.Key, b.Fields, b.ExpireAt); err != nil {
			return err
		}
	}
	return nil
}

func (m *MockStore) FieldExists(_ context.Context, key, field string) (bool, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.exists(key, field), nil
}

func (m *MockStore) FieldExistsBatch(_ context.Context, lookups []FieldLookup) ([]bool, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]bool, len(lookups))
	for i, l := range lookups {
		out[i] = m.exists(l.Key, l.Field)
	}
	return out, nil
}

// exists reports field presence, treating expired entries as absent.
// Caller holds the lock.
func (m *MockStore) exists(key, field string) bool {
	hash, ok := m.fields[key]
	if !ok {
		return false
	}
	entry, ok := hash[field]
	if !ok {
		return false
	}
	if !entry.expireAt.IsZero() && m.Now().After(entry.expireAt) {
		return false
	}
	return true
}
