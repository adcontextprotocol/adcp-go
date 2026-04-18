package idempotency

import (
	"context"
	"sync"
	"time"
)

// MemoryBackend is an in-process Backend suitable for tests and reference
// servers. A background sweeper removes expired entries; callers should invoke
// Close to stop it.
type MemoryBackend struct {
	mu      sync.Mutex
	entries map[string]*Entry
	clock   func() time.Time
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
		if !e.ExpiresAt.IsZero() && now.After(e.ExpiresAt) {
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

