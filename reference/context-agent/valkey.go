package main

import (
	"context"
	"sync"
	"time"
)

// ValkeyClient abstracts the Valkey operations used by the context agent.
// In production this wraps github.com/valkey-io/valkey-go; in tests it
// uses an in-memory mock with simulated network latency.
type ValkeyClient interface {
	SIsMember(ctx context.Context, key, member string) (bool, error)
	SInter(ctx context.Context, keys ...string) ([]string, error)
	Exists(ctx context.Context, key string) (bool, error)
	Set(ctx context.Context, key, value string, ttl time.Duration) error
	Get(ctx context.Context, key string) (string, error)
}

// MockValkeyClient is an in-memory implementation of ValkeyClient for testing.
// It simulates ~50 microsecond network latency on each operation.
type MockValkeyClient struct {
	mu      sync.RWMutex
	sets    map[string]map[string]struct{}
	strings map[string]mockStringEntry
	latency time.Duration
}

type mockStringEntry struct {
	value   string
	expires time.Time
}

func NewMockValkeyClient() *MockValkeyClient {
	return &MockValkeyClient{
		sets:    make(map[string]map[string]struct{}),
		strings: make(map[string]mockStringEntry),
		latency: 50 * time.Microsecond,
	}
}

func (m *MockValkeyClient) simulateLatency() {
	if m.latency > 0 {
		time.Sleep(m.latency)
	}
}

func (m *MockValkeyClient) SAdd(key string, members ...string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.sets[key]
	if !ok {
		s = make(map[string]struct{})
		m.sets[key] = s
	}
	for _, member := range members {
		s[member] = struct{}{}
	}
}

func (m *MockValkeyClient) SIsMember(_ context.Context, key, member string) (bool, error) {
	m.simulateLatency()
	m.mu.RLock()
	defer m.mu.RUnlock()
	s, ok := m.sets[key]
	if !ok {
		return false, nil
	}
	_, found := s[member]
	return found, nil
}

func (m *MockValkeyClient) SInter(_ context.Context, keys ...string) ([]string, error) {
	m.simulateLatency()
	m.mu.RLock()
	defer m.mu.RUnlock()

	if len(keys) == 0 {
		return nil, nil
	}

	// Start with the first set
	first, ok := m.sets[keys[0]]
	if !ok {
		return nil, nil
	}

	// Intersect with remaining sets
	result := make(map[string]struct{})
	for member := range first {
		result[member] = struct{}{}
	}

	for _, key := range keys[1:] {
		s, ok := m.sets[key]
		if !ok {
			return nil, nil
		}
		for member := range result {
			if _, found := s[member]; !found {
				delete(result, member)
			}
		}
	}

	out := make([]string, 0, len(result))
	for member := range result {
		out = append(out, member)
	}
	return out, nil
}

func (m *MockValkeyClient) Exists(_ context.Context, key string) (bool, error) {
	m.simulateLatency()
	m.mu.RLock()
	defer m.mu.RUnlock()

	if _, ok := m.sets[key]; ok {
		return true, nil
	}

	entry, ok := m.strings[key]
	if !ok {
		return false, nil
	}
	if !entry.expires.IsZero() && time.Now().After(entry.expires) {
		return false, nil
	}
	return true, nil
}

func (m *MockValkeyClient) Set(_ context.Context, key, value string, ttl time.Duration) error {
	m.simulateLatency()
	m.mu.Lock()
	defer m.mu.Unlock()
	var expires time.Time
	if ttl > 0 {
		expires = time.Now().Add(ttl)
	}
	m.strings[key] = mockStringEntry{value: value, expires: expires}
	return nil
}

func (m *MockValkeyClient) Get(_ context.Context, key string) (string, error) {
	m.simulateLatency()
	m.mu.RLock()
	defer m.mu.RUnlock()
	entry, ok := m.strings[key]
	if !ok {
		return "", nil
	}
	if !entry.expires.IsZero() && time.Now().After(entry.expires) {
		return "", nil
	}
	return entry.value, nil
}
