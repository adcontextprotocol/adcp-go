package targeting

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"sync"
	"time"
)

// MockStore is an in-memory Store for testing. It supports sets and strings
// with TTL expiry. All operations are goroutine-safe.
type MockStore struct {
	mu      sync.RWMutex
	sets    map[string]map[string]struct{}
	strings map[string]stringEntry

	// Now returns the current time. Defaults to time.Now.
	// Override in tests to control time.
	Now func() time.Time
}

type stringEntry struct {
	value  string
	expiry time.Time // zero means no expiry
}

// NewMockStore creates an empty MockStore.
func NewMockStore() *MockStore {
	return &MockStore{
		sets:    make(map[string]map[string]struct{}),
		strings: make(map[string]stringEntry),
		Now:     time.Now,
	}
}

// SetAdd adds members to a set. Test helper, not part of Store interface.
func (m *MockStore) SetAdd(key string, members ...string) {
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

// SetMediaBuy stores a media buy and adds it to the seller's set. Test helper.
func (m *MockStore) SetMediaBuy(mb MediaBuy) {
	data, _ := json.Marshal(mb)
	m.mu.Lock()
	defer m.mu.Unlock()
	sellerKey := "mediabuy:seller:" + mb.SellerID
	s, ok := m.sets[sellerKey]
	if !ok {
		s = make(map[string]struct{})
		m.sets[sellerKey] = s
	}
	s[mb.MediaBuyID] = struct{}{}
	m.strings["mediabuy:"+mb.MediaBuyID] = stringEntry{value: string(data)}
}

// SetPackageContextConfig stores context config for a package. Test helper.
func (m *MockStore) SetPackageContextConfig(pkgID string, cfg PackageContextConfig) {
	data, _ := json.Marshal(cfg)
	m.mu.Lock()
	defer m.mu.Unlock()
	m.strings[fmt.Sprintf("config:pkg:%s:context", pkgID)] = stringEntry{value: string(data)}
}

func (m *MockStore) SetIsMember(_ context.Context, key, member string) (bool, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	s, ok := m.sets[key]
	if !ok {
		return false, nil
	}
	_, found := s[member]
	return found, nil
}

func (m *MockStore) SetIntersect(_ context.Context, keys ...string) ([]string, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if len(keys) == 0 {
		return nil, nil
	}

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
		if len(result) == 0 {
			return nil, nil
		}
	}

	out := make([]string, 0, len(result))
	for k := range result {
		out = append(out, k)
	}
	sort.Strings(out)
	return out, nil
}

func (m *MockStore) Get(_ context.Context, key string) (string, bool, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	entry, ok := m.strings[key]
	if !ok {
		return "", false, nil
	}
	if !entry.expiry.IsZero() && m.Now().After(entry.expiry) {
		return "", false, nil
	}
	return entry.value, true, nil
}

func (m *MockStore) Set(_ context.Context, key, value string, ttl time.Duration) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	entry := stringEntry{value: value}
	if ttl > 0 {
		entry.expiry = m.Now().Add(ttl)
	}
	m.strings[key] = entry
	return nil
}

func (m *MockStore) Exists(_ context.Context, key string) (bool, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if entry, ok := m.strings[key]; ok {
		if !entry.expiry.IsZero() && m.Now().After(entry.expiry) {
			return false, nil
		}
		return true, nil
	}
	if _, ok := m.sets[key]; ok {
		return true, nil
	}
	return false, nil
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
	results := make([]string, len(keys))
	for i, key := range keys {
		entry, ok := m.strings[key]
		if !ok {
			continue
		}
		if !entry.expiry.IsZero() && m.Now().After(entry.expiry) {
			continue
		}
		results[i] = entry.value
	}
	return results, nil
}

func (m *MockStore) MSet(_ context.Context, kvs map[string]string, ttl time.Duration) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for k, v := range kvs {
		entry := stringEntry{value: v}
		if ttl > 0 {
			entry.expiry = m.Now().Add(ttl)
		}
		m.strings[k] = entry
	}
	return nil
}
