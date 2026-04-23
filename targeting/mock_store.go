package targeting

import (
	"context"
	"encoding/json"
	"sort"
	"strconv"
	"sync"
	"time"
)

// MockStore is an in-memory Store for testing. It supports sets, strings,
// sorted sets, and hashes with TTL expiry. All operations are goroutine-safe.
type MockStore struct {
	mu      sync.RWMutex
	sets    map[string]map[string]struct{}
	strings map[string]stringEntry
	zsets   map[string][]zsetMember
	hsets   map[string]map[string]string // key → field → value
	expiry  map[string]time.Time         // key -> expiry time

	// Now returns the current time. Defaults to time.Now.
	// Override in tests to control time.
	Now func() time.Time
}

type stringEntry struct {
	value  string
	expiry time.Time // zero means no expiry
}

type zsetMember struct {
	score  float64
	member string
}

// NewMockStore creates an empty MockStore.
func NewMockStore() *MockStore {
	return &MockStore{
		sets:    make(map[string]map[string]struct{}),
		strings: make(map[string]stringEntry),
		zsets:   make(map[string][]zsetMember),
		hsets:   make(map[string]map[string]string),
		expiry:  make(map[string]time.Time),
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

// SetPackageIdentityConfig stores identity config for a package. Test helper.
func (m *MockStore) SetPackageIdentityConfig(pkgID string, cfg PackageIdentityConfig) {
	data, _ := json.Marshal(cfg)
	m.mu.Lock()
	defer m.mu.Unlock()
	m.strings[keyPrefixConfigPkg+pkgID] = stringEntry{value: string(data)}
}

// SetCampaignFreqConfig stores frequency config for a campaign. Test helper.
func (m *MockStore) SetCampaignFreqConfig(campaignID string, cfg CampaignFreqConfig) {
	data, _ := json.Marshal(cfg)
	m.mu.Lock()
	defer m.mu.Unlock()
	m.strings[keyPrefixConfigCampaign+campaignID] = stringEntry{value: string(data)}
}

// SetMediaBuy stores a media buy and adds it to the seller's set. Test helper.
func (m *MockStore) SetMediaBuy(mb MediaBuy) {
	data, _ := json.Marshal(mb)
	m.mu.Lock()
	defer m.mu.Unlock()
	// Add to seller set.
	sellerKey := keyPrefixMediaBuySeller + mb.SellerID
	s, ok := m.sets[sellerKey]
	if !ok {
		s = make(map[string]struct{})
		m.sets[sellerKey] = s
	}
	s[mb.MediaBuyID] = struct{}{}
	// Store media buy JSON.
	m.strings[keyPrefixMediaBuy+mb.MediaBuyID] = stringEntry{value: string(data)}
}

// SetPackageUser adds a user to a package's audience. Test helper.
func (m *MockStore) SetPackageUser(pkgID, token string, intent float64) {
	pkgKey := keyPrefixPackageAudience + HashToken(pkgID)
	userHash := HashToken(token)
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.hsets[pkgKey] == nil {
		m.hsets[pkgKey] = make(map[string]string)
	}
	m.hsets[pkgKey][userHash] = strconv.FormatFloat(intent, 'f', -1, 64)
}

// SetPackageUsers adds multiple users to a package's audience. Test helper.
func (m *MockStore) SetPackageUsers(pkgID string, users map[string]float64) {
	pkgKey := keyPrefixPackageAudience + HashToken(pkgID)
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.hsets[pkgKey] == nil {
		m.hsets[pkgKey] = make(map[string]string)
	}
	for token, intent := range users {
		m.hsets[pkgKey][HashToken(token)] = strconv.FormatFloat(intent, 'f', -1, 64)
	}
}

// SetUserExposures stores a user's exposure log in binary format. Test helper.
func (m *MockStore) SetUserExposures(token string, entries []ExposureEntry) {
	hash := HashToken(token)
	bin := EncodeBinaryExposureLog(entries)
	m.mu.Lock()
	defer m.mu.Unlock()
	m.strings[keyPrefixUserExposures+hash] = stringEntry{value: string(bin)}
}

// AddExposure appends an exposure entry to a user's log. Test helper.
func (m *MockStore) AddExposure(token string, entry ExposureEntry) {
	hash := HashToken(token)
	key := keyPrefixUserExposures + hash
	m.mu.Lock()
	defer m.mu.Unlock()
	existing := BinaryExposureLog(m.strings[key].value)
	newEntry := EncodeBinaryExposureLog(ExposureLog{entry})
	merged := MergeBinaryLogs(existing, newEntry)
	m.strings[key] = stringEntry{value: string(merged)}
}

// SetPackageContextConfig stores context config for a package. Test helper.
func (m *MockStore) SetPackageContextConfig(pkgID string, cfg PackageContextConfig) {
	data, _ := json.Marshal(cfg)
	m.mu.Lock()
	defer m.mu.Unlock()
	m.strings[keyPrefixConfigPkg+pkgID+":context"] = stringEntry{value: string(data)}
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
		m.expiry[key] = m.Now().Add(ttl)
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

func (m *MockStore) Del(_ context.Context, key string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.deleteKey(key)
	return nil
}

func (m *MockStore) MDel(_ context.Context, keys ...string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, key := range keys {
		m.deleteKey(key)
	}
	return nil
}

// deleteKey removes a key from all data structures. Must be called with lock held.
func (m *MockStore) deleteKey(key string) {
	delete(m.strings, key)
	delete(m.sets, key)
	delete(m.zsets, key)
	delete(m.hsets, key)
	delete(m.expiry, key)
}

func (m *MockStore) HSet(_ context.Context, key, field, value string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.hsets[key] == nil {
		m.hsets[key] = make(map[string]string)
	}
	m.hsets[key][field] = value
	return nil
}

func (m *MockStore) HMSet(_ context.Context, key string, fields map[string]string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.hsets[key] == nil {
		m.hsets[key] = make(map[string]string)
	}
	for f, v := range fields {
		m.hsets[key][f] = v
	}
	return nil
}

func (m *MockStore) HGet(_ context.Context, key, field string) (string, bool, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	h, ok := m.hsets[key]
	if !ok {
		return "", false, nil
	}
	v, ok := h[field]
	return v, ok, nil
}

func (m *MockStore) HMGet(_ context.Context, key string, fields ...string) ([]string, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	h := m.hsets[key]
	results := make([]string, len(fields))
	for i, f := range fields {
		results[i] = h[f]
	}
	return results, nil
}

func (m *MockStore) HMGetBatch(_ context.Context, keys []string, fields []string) ([][]string, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	results := make([][]string, len(keys))
	for i, key := range keys {
		h := m.hsets[key]
		vals := make([]string, len(fields))
		for j, f := range fields {
			vals[j] = h[f]
		}
		results[i] = vals
	}
	return results, nil
}

func (m *MockStore) HDel(_ context.Context, key string, fields ...string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	h := m.hsets[key]
	if h == nil {
		return nil
	}
	for _, f := range fields {
		delete(h, f)
	}
	return nil
}

// isExpired checks key-level expiry. Must be called with lock held.
func (m *MockStore) isExpired(key string) bool {
	exp, ok := m.expiry[key]
	if !ok {
		return false
	}
	return m.Now().After(exp)
}
