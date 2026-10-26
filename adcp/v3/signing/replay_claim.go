package signing

import "time"

// ReplayClaim is the outcome of ReplayClaimer.Claim.
type ReplayClaim int

const (
	// ReplayClaimed: the (keyid, nonce) pair was absent and is now recorded.
	ReplayClaimed ReplayClaim = iota
	// ReplayReplayed: the pair was already recorded within its lifetime.
	ReplayReplayed
	// ReplayCapacity: the keyid is at its entry cap; nothing was recorded.
	ReplayCapacity
)

// ReplayClaimer is an optional ReplayStore extension that checks the cap
// and records a nonce in one atomic step (Redis SET NX, a unique insert).
// With separate Seen and Insert calls, two concurrent copies of one signed
// request can both pass Seen before either inserts; the Web Bot Auth
// verifier uses Claim when the store implements it. MemoryReplayStore
// does.
type ReplayClaimer interface {
	Claim(keyid, nonce string, ttl time.Duration) ReplayClaim
}

// Claim implements ReplayClaimer.
func (m *MemoryReplayStore) Claim(keyid, nonce string, ttl time.Duration) ReplayClaim {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sweepLocked()
	k := replayKey{keyid, nonce}
	if _, seen := m.entries[k]; seen {
		return ReplayReplayed
	}
	if m.capHits[keyid] || m.perKey[keyid] >= m.capPerK {
		return ReplayCapacity
	}
	m.entries[k] = m.now().Add(ttl)
	m.perKey[keyid]++
	return ReplayClaimed
}
