package signing

import (
	"sync"
	"time"
)

// ReplayStore deduplicates (keyid, nonce) pairs within the signature validity
// window. Step 9a reads via HitCap; step 12 reads via Seen; step 13 writes via
// Insert.
//
// Nonce values passed to ReplayStore are the decoded nonce bytes, NOT the
// base64url string from the Signature-Input header — this keeps dedup robust
// against future encoding variants. Implementations should treat nonce as an
// opaque byte string.
//
// Single-process deployments do not require atomicity between HitCap and
// Insert; distributed deployments (shared Redis) SHOULD make the step-13
// insert atomic with a cap check to prevent cap drift when concurrent
// verifiers race.
type ReplayStore interface {
	// HitCap returns true if the per-keyid entry cap has been reached for keyid.
	// Called at step 9a before crypto verify.
	HitCap(keyid string) bool

	// Seen returns true if the (keyid, nonce) pair is present.
	// Called at step 12 after crypto verify.
	Seen(keyid, nonce string) bool

	// Insert records the (keyid, nonce) pair with the given TTL.
	// Called at step 13 only after all prior checks have passed.
	// Returns true if the insert succeeded; false if the cap was hit (the
	// verifier should then reject with request_signature_rate_abuse to match
	// the single-process / distributed semantics).
	Insert(keyid, nonce string, ttl time.Duration) bool
}

// MemoryReplayStore is an in-memory ReplayStore with TTL eviction and a
// per-keyid entry cap. Safe for concurrent use.
type MemoryReplayStore struct {
	mu       sync.Mutex
	entries  map[replayKey]time.Time // nonce TTL expiry
	perKey   map[string]int          // count per keyid
	capPerK  int
	capHits map[string]bool // test-only: keyids whose cap is artificially marked hit
	now      func() time.Time
}

type replayKey struct{ keyid, nonce string }

// NewMemoryReplayStore returns an in-memory replay store. perKeyidCap of 0
// defaults to 1,000,000 per the spec recommendation.
func NewMemoryReplayStore(perKeyidCap int) *MemoryReplayStore {
	if perKeyidCap <= 0 {
		perKeyidCap = defaultKeyIDCap
	}
	return &MemoryReplayStore{
		entries:  make(map[replayKey]time.Time),
		perKey:   make(map[string]int),
		capPerK:  perKeyidCap,
		capHits: map[string]bool{},
		now:      time.Now,
	}
}

// withClock lets tests pin a deterministic clock.
func (m *MemoryReplayStore) withClock(now func() time.Time) {
	m.now = now
}

// HitCap implements ReplayStore.
func (m *MemoryReplayStore) HitCap(keyid string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.capHits[keyid] {
		return true
	}
	return m.perKey[keyid] >= m.capPerK
}

// Seen implements ReplayStore.
func (m *MemoryReplayStore) Seen(keyid, nonce string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sweepLocked()
	_, ok := m.entries[replayKey{keyid, nonce}]
	return ok
}

// Insert implements ReplayStore.
func (m *MemoryReplayStore) Insert(keyid, nonce string, ttl time.Duration) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sweepLocked()
	if m.capHits[keyid] || m.perKey[keyid] >= m.capPerK {
		return false
	}
	m.entries[replayKey{keyid, nonce}] = m.now().Add(ttl)
	m.perKey[keyid]++
	return true
}

// Preload is a test helper to seed the replay cache.
func (m *MemoryReplayStore) Preload(keyid, nonce string, ttl time.Duration) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.entries[replayKey{keyid, nonce}] = m.now().Add(ttl)
	m.perKey[keyid]++
}

// MarkKeyIDAtCap forces HitCap to report true for keyid. Used by conformance
// tests whose test_harness_state sets replay_cache_per_keyid_cap_hit. Production
// code should NOT use this method.
func (m *MemoryReplayStore) MarkKeyIDAtCap(keyid string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.capHits[keyid] = true
}

// sweepLocked removes expired entries. Caller holds m.mu.
func (m *MemoryReplayStore) sweepLocked() {
	now := m.now()
	for k, exp := range m.entries {
		if now.After(exp) {
			delete(m.entries, k)
			if m.perKey[k.keyid] > 0 {
				m.perKey[k.keyid]--
			}
			if m.perKey[k.keyid] == 0 {
				delete(m.perKey, k.keyid)
			}
		}
	}
}
