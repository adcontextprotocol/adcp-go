package idempotency

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestWrapConcurrentSameKeySamePayload forces the PutIfAbsent race: many
// goroutines issue the same idempotent request; exactly one handler run must
// be observable, the rest must see Replayed=true.
func TestWrapConcurrentSameKeySamePayload(t *testing.T) {
	now := time.Now().UTC()
	b := newMemoryBackend(0, func() time.Time { return now })
	s := New(Options{
		Backend: b,
		TTL:     time.Hour,
		Clock:   func() time.Time { return now },
	})

	var handlerCalls int32
	wrapped := s.Wrap(func(context.Context, []byte) ([]byte, error) {
		atomic.AddInt32(&handlerCalls, 1)
		return []byte(`{"mb":"mb-1"}`), nil
	})

	ctx := WithPrincipal(context.Background(), "p1")
	req := mustJSON(t, map[string]any{"idempotency_key": Generate(), "account": "a"})

	const N = 32
	var wg sync.WaitGroup
	wg.Add(N)
	results := make([]*Result, N)
	errs := make([]error, N)
	start := make(chan struct{})
	for i := 0; i < N; i++ {
		go func(i int) {
			defer wg.Done()
			<-start
			results[i], errs[i] = wrapped(ctx, req)
		}(i)
	}
	close(start)
	wg.Wait()

	freshCount := 0
	replayCount := 0
	for i, r := range results {
		require.NoError(t, errs[i])
		require.NotNil(t, r)
		if r.Replayed {
			replayCount++
		} else {
			freshCount++
		}
	}
	assert.Equal(t, 1, freshCount, "exactly one goroutine should see fresh execution")
	assert.Equal(t, N-1, replayCount, "all others should replay")
	// Handler may run more than once when multiple goroutines race past the
	// initial Get miss, but the middleware MUST collapse duplicates to a
	// single authoritative response.
	assert.GreaterOrEqual(t, atomic.LoadInt32(&handlerCalls), int32(1))
}

// TestWrapConflictOnPutIfAbsentPath drives the race branch directly: Get
// returns nil (forcing handler execution), then PutIfAbsent reports the slot
// is already taken by a different hash. The middleware MUST emit
// ConflictError rather than the freshly-computed response.
func TestWrapConflictOnPutIfAbsentPath(t *testing.T) {
	now := time.Now().UTC()
	stub := &stubBackend{
		getResult: nil, // force handler to run
		putExisting: &Entry{
			Hash:      "different-hash",
			Response:  []byte(`{"other":true}`),
			ExpiresAt: now.Add(time.Hour),
		},
	}
	s := New(Options{Backend: stub, TTL: time.Hour, Clock: func() time.Time { return now }})

	wrapped := s.Wrap(func(context.Context, []byte) ([]byte, error) {
		return []byte(`{"ours":true}`), nil
	})
	ctx := WithPrincipal(context.Background(), "p1")
	req := mustJSON(t, map[string]any{"idempotency_key": Generate(), "account": "a"})

	_, err := wrapped(ctx, req)
	var ce *ConflictError
	assert.True(t, errors.As(err, &ce))
}

type stubBackend struct {
	getResult   *Entry
	putExisting *Entry
}

func (b *stubBackend) Get(context.Context, string, string) (*Entry, error) {
	return b.getResult, nil
}
func (b *stubBackend) PutIfAbsent(_ context.Context, _, _ string, _ *Entry) (*Entry, bool, error) {
	if b.putExisting != nil {
		return b.putExisting, false, nil
	}
	return nil, true, nil
}
