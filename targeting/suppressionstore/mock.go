package suppressionstore

import (
	"context"
	"strings"
	"sync"
	"time"
)

// MockStore is a goroutine-safe in-memory Store for tests. TTL is
// honored: Set stamps a deadline and Scan / Get respect it. Now is
// overridable.
type MockStore struct {
	mu      sync.RWMutex
	entries map[string]entry
	Now     func() time.Time
}

type entry struct {
	value  string
	expiry time.Time // zero = no expiry
}

// NewMockStore returns an empty MockStore.
func NewMockStore() *MockStore {
	return &MockStore{entries: make(map[string]entry), Now: time.Now}
}

func (m *MockStore) Set(_ context.Context, key, value string, ttl time.Duration) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	e := entry{value: value}
	if ttl > 0 {
		e.expiry = m.Now().Add(ttl)
	}
	m.entries[key] = e
	return nil
}

func (m *MockStore) Del(_ context.Context, keys ...string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, k := range keys {
		delete(m.entries, k)
	}
	return nil
}

// Scan returns every live key matching match. The supported pattern is
// the simple trailing '*' wildcard; sufficient for this package's
// usage and avoids pulling in a regex dependency.
func (m *MockStore) Scan(_ context.Context, match string) ([]string, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	now := m.Now()
	wildcard := strings.HasSuffix(match, "*")
	prefix := match
	if wildcard {
		prefix = match[:len(match)-1]
	}
	var out []string
	for k, e := range m.entries {
		if !e.expiry.IsZero() && now.After(e.expiry) {
			continue
		}
		if wildcard {
			if strings.HasPrefix(k, prefix) {
				out = append(out, k)
			}
		} else if k == match {
			out = append(out, k)
		}
	}
	return out, nil
}
