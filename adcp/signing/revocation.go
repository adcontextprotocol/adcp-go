package signing

import "sync"

// RevocationSource reports whether a keyid is revoked and whether the verifier's
// view of the revocation list is fresh enough to be authoritative.
//
// Stale() returning true forces the verifier to emit request_signature_revocation_stale
// and reject new signed mutations until the list is refreshed.
type RevocationSource interface {
	Revoked(keyid string) bool
	Stale() bool
}

// StaticRevocationList is an in-memory RevocationSource useful for tests and
// for verifiers that populate the list from a separate polling loop.
type StaticRevocationList struct {
	mu      sync.RWMutex
	revoked map[string]struct{}
	stale   bool
}

// NewStaticRevocationList returns a RevocationSource with the given revoked kids.
func NewStaticRevocationList(revoked []string) *StaticRevocationList {
	m := map[string]struct{}{}
	for _, k := range revoked {
		m[k] = struct{}{}
	}
	return &StaticRevocationList{revoked: m}
}

// Revoked reports whether keyid is in the revocation set.
func (s *StaticRevocationList) Revoked(keyid string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	_, ok := s.revoked[keyid]
	return ok
}

// Stale reports whether the revocation list is considered stale.
func (s *StaticRevocationList) Stale() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.stale
}

// SetStale marks the list stale. Used by tests and by operator monitoring when
// the polling loop hasn't refreshed within grace.
func (s *StaticRevocationList) SetStale(stale bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.stale = stale
}

// SetRevoked replaces the full revocation set.
func (s *StaticRevocationList) SetRevoked(kids []string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	m := map[string]struct{}{}
	for _, k := range kids {
		m[k] = struct{}{}
	}
	s.revoked = m
}
