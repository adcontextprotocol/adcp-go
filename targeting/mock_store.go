package targeting

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/adcontextprotocol/adcp-go/exposure"
)

// MockStore is an in-memory Store for testing. It supports sets, strings,
// and sorted sets with TTL expiry. All operations are goroutine-safe.
type MockStore struct {
	mu      sync.RWMutex
	sets    map[string]map[string]struct{}
	strings map[string]stringEntry
	zsets   map[string][]zsetMember
	expiry  map[string]time.Time // key -> expiry time

	// Clock drives TTL expiry checks. Share it with the Engine and any
	// exposure.Recorder using the same store so all three agree on "now."
	// Override in tests to control time.
	Clock exposure.Clock
}

type stringEntry struct {
	value  string
	expiry time.Time // zero means no expiry
}

type zsetMember struct {
	score  float64
	member string
}

// NewMockStore creates an empty MockStore with a wall-clock source.
func NewMockStore() *MockStore {
	return &MockStore{
		sets:    make(map[string]map[string]struct{}),
		strings: make(map[string]stringEntry),
		zsets:   make(map[string][]zsetMember),
		expiry:  make(map[string]time.Time),
		Clock:   exposure.ClockFunc(time.Now),
	}
}

// now returns the current time from the configured Clock, falling back to
// wall time when Clock is nil.
func (m *MockStore) now() time.Time {
	if m.Clock != nil {
		return m.Clock.Now()
	}
	return time.Now()
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

// SetPackageIdentityConfig stores identity config for a package. Test helper.
func (m *MockStore) SetPackageIdentityConfig(pkgID string, cfg exposure.PackageIdentityConfig) {
	data, _ := json.Marshal(cfg)
	m.mu.Lock()
	defer m.mu.Unlock()
	m.strings[fmt.Sprintf("config:pkg:%s", pkgID)] = stringEntry{value: string(data)}
}

// SetCampaignFreqConfig stores frequency config for a campaign. Test helper.
func (m *MockStore) SetCampaignFreqConfig(campaignID string, cfg exposure.CampaignFreqConfig) {
	data, _ := json.Marshal(cfg)
	m.mu.Lock()
	defer m.mu.Unlock()
	m.strings[fmt.Sprintf("config:campaign:%s", campaignID)] = stringEntry{value: string(data)}
}

// SetMediaBuy stores a media buy and adds it to the seller's set. Test helper.
func (m *MockStore) SetMediaBuy(mb MediaBuy) {
	data, _ := json.Marshal(mb)
	m.mu.Lock()
	defer m.mu.Unlock()
	// Add to seller set.
	sellerKey := "mediabuy:seller:" + mb.SellerID
	s, ok := m.sets[sellerKey]
	if !ok {
		s = make(map[string]struct{})
		m.sets[sellerKey] = s
	}
	s[mb.MediaBuyID] = struct{}{}
	// Store media buy JSON.
	m.strings["mediabuy:"+mb.MediaBuyID] = stringEntry{value: string(data)}
}

// SetUserProfile stores a user's segment memberships. Test helper.
func (m *MockStore) SetUserProfile(token string, segments map[string]float64) {
	hash := exposure.HashToken(token)
	profile := exposure.UserProfile{Segments: segments}
	data, _ := json.Marshal(profile)
	m.mu.Lock()
	defer m.mu.Unlock()
	m.strings["user:profile:"+hash] = stringEntry{value: string(data)}
}

// SetUserExposures stores a user's exposure log in binary format. Test helper.
func (m *MockStore) SetUserExposures(token string, entries []exposure.ExposureEntry) {
	hash := exposure.HashToken(token)
	bin := exposure.EncodeBinaryExposureLog(entries)
	m.mu.Lock()
	defer m.mu.Unlock()
	m.strings["user:exposures:"+hash] = stringEntry{value: string(bin)}
}

// AddExposure appends an exposure entry to a user's log. Test helper.
func (m *MockStore) AddExposure(token string, entry exposure.ExposureEntry) {
	hash := exposure.HashToken(token)
	key := "user:exposures:" + hash
	m.mu.Lock()
	defer m.mu.Unlock()
	existing := exposure.BinaryExposureLog(m.strings[key].value)
	newEntry := exposure.EncodeBinaryExposureLog(exposure.ExposureLog{entry})
	merged := exposure.MergeBinaryLogs(existing, newEntry)
	m.strings[key] = stringEntry{value: string(merged)}
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
	if m.isExpired(key) {
		return false, nil
	}
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

	// Start with the first set.
	first, ok := m.sets[keys[0]]
	if !ok || m.isExpired(keys[0]) {
		return nil, nil
	}

	result := make(map[string]struct{}, len(first))
	for k := range first {
		result[k] = struct{}{}
	}

	for _, key := range keys[1:] {
		s, ok := m.sets[key]
		if !ok || m.isExpired(key) {
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
	if !entry.expiry.IsZero() && m.now().After(entry.expiry) {
		return "", false, nil
	}
	return entry.value, true, nil
}

func (m *MockStore) Set(_ context.Context, key, value string, ttl time.Duration) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	entry := stringEntry{value: value}
	if ttl > 0 {
		entry.expiry = m.now().Add(ttl)
	}
	m.strings[key] = entry
	return nil
}

func (m *MockStore) Exists(_ context.Context, key string) (bool, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if entry, ok := m.strings[key]; ok {
		if !entry.expiry.IsZero() && m.now().After(entry.expiry) {
			return false, nil
		}
		return true, nil
	}
	if _, ok := m.sets[key]; ok && !m.isExpired(key) {
		return true, nil
	}
	if _, ok := m.zsets[key]; ok && !m.isExpired(key) {
		return true, nil
	}
	return false, nil
}

func (m *MockStore) ZAdd(_ context.Context, key string, score float64, member string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	// Match Redis semantics: update score if member already exists.
	members := m.zsets[key]
	for i, z := range members {
		if z.member == member {
			members[i].score = score
			return nil
		}
	}
	m.zsets[key] = append(members, zsetMember{score: score, member: member})
	return nil
}

func (m *MockStore) ZCount(_ context.Context, key string, min, max float64) (int64, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.isExpired(key) {
		return 0, nil
	}
	members, ok := m.zsets[key]
	if !ok {
		return 0, nil
	}
	var count int64
	for _, m := range members {
		if m.score >= min && m.score <= max {
			count++
		}
	}
	return count, nil
}

func (m *MockStore) ZExpire(_ context.Context, key string, ttl time.Duration) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if ttl > 0 {
		m.expiry[key] = m.now().Add(ttl)
	} else {
		delete(m.expiry, key)
	}
	return nil
}

func (m *MockStore) ZRemRangeByScore(_ context.Context, key string, min, max float64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	members := m.zsets[key]
	kept := members[:0]
	for _, z := range members {
		if z.score < min || z.score > max {
			kept = append(kept, z)
		}
	}
	m.zsets[key] = kept
	return nil
}

func (m *MockStore) SetMembers(_ context.Context, key string) ([]string, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.isExpired(key) {
		return nil, nil
	}
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
		if !entry.expiry.IsZero() && m.now().After(entry.expiry) {
			continue
		}
		results[i] = entry.value
	}
	return results, nil
}

// isExpired checks key-level expiry. Must be called with lock held.
func (m *MockStore) isExpired(key string) bool {
	exp, ok := m.expiry[key]
	if !ok {
		return false
	}
	return m.now().After(exp)
}
