package idempotency

import (
	"context"
	"sync"
	"time"
)

// MemoryBackend is an in-process Backend suitable for tests and reference
// servers. A background sweeper removes entries once they are past ExpiresAt
// plus a retention grace of DefaultClockSkew (never unresolved in-flight
// claims); callers should invoke Close to stop it. The grace must be at least
// the Store's ClockSkew, which serves entries until then, so New panics on a
// larger ClockSkew with a MemoryBackend. That check only sees an unwrapped
// *MemoryBackend; the grace applies regardless of wrapping. The sweeper uses
// the backend's clock (time.Now), not Options.Clock, so a Store whose Clock
// runs behind real time can find entries already swept.
type MemoryBackend struct {
	mu      sync.Mutex
	entries map[string]*Entry
	clock   func() time.Time
	grace   time.Duration
	stop    chan struct{}
	stopped chan struct{}
}

// NewMemoryBackend returns a MemoryBackend with a TTL sweeper running at
// sweepInterval. A zero interval disables the sweeper (entries still become
// unobservable past TTL because the middleware checks ExpiresAt).
func NewMemoryBackend(sweepInterval time.Duration) *MemoryBackend {
	return newMemoryBackend(sweepInterval, time.Now)
}

func newMemoryBackend(sweepInterval time.Duration, clock func() time.Time) *MemoryBackend {
	b := &MemoryBackend{
		entries: map[string]*Entry{},
		clock:   clock,
		grace:   DefaultClockSkew,
		stop:    make(chan struct{}),
		stopped: make(chan struct{}),
	}
	if sweepInterval > 0 {
		go b.sweepLoop(sweepInterval)
	} else {
		close(b.stopped)
	}
	return b
}

// Close stops the TTL sweeper.
func (b *MemoryBackend) Close() {
	select {
	case <-b.stop:
		return
	default:
		close(b.stop)
	}
	<-b.stopped
}

func (b *MemoryBackend) sweepLoop(interval time.Duration) {
	defer close(b.stopped)
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-b.stop:
			return
		case <-t.C:
			b.sweep()
		}
	}
}

func (b *MemoryBackend) sweep() {
	now := b.clock()
	b.mu.Lock()
	defer b.mu.Unlock()
	for k, e := range b.entries {
		// Unresolved claims are never swept: deleting one would let the key
		// re-execute an outcome that may already have taken effect.
		if !e.ExpiresAt.IsZero() && now.After(e.ExpiresAt.Add(b.grace)) && !isClaimHash(e.Hash) {
			delete(b.entries, k)
		}
	}
}

func scopeKey(scope, key string) string {
	return scope + "\x00" + key
}

// Get implements Backend.
func (b *MemoryBackend) Get(_ context.Context, scope, key string) (*Entry, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if e, ok := b.entries[scopeKey(scope, key)]; ok {
		cp := *e
		return &cp, nil
	}
	return nil, nil
}

// PutIfAbsent implements Backend.
func (b *MemoryBackend) PutIfAbsent(_ context.Context, scope, key string, entry *Entry) (*Entry, bool, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	k := scopeKey(scope, key)
	if e, ok := b.entries[k]; ok {
		cp := *e
		return &cp, false, nil
	}
	cp := *entry
	b.entries[k] = &cp
	return nil, true, nil
}

var _ ClaimBackend = (*MemoryBackend)(nil)

// ReplaceIfHash implements ClaimBackend.
func (b *MemoryBackend) ReplaceIfHash(_ context.Context, scope, key, oldHash string, entry *Entry) (bool, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	k := scopeKey(scope, key)
	if e, ok := b.entries[k]; !ok || e.Hash != oldHash {
		return false, nil
	}
	cp := *entry
	b.entries[k] = &cp
	return true, nil
}
