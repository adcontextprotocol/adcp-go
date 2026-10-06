package idempotency

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestWrapClaimBlocksConcurrentDuplicate(t *testing.T) {
	now := time.Now().UTC()
	s, _ := newTestStore(t, &now)

	started := make(chan struct{})
	release := make(chan struct{})
	var calls int32
	wrapped := s.Wrap(func(context.Context, []byte) ([]byte, error) {
		if atomic.AddInt32(&calls, 1) == 1 {
			close(started)
			<-release
		}
		return []byte(`{"media_buy_id":"mb-1"}`), nil
	})

	ctx := WithPrincipal(context.Background(), "p1")
	key := Generate()
	req := mustJSON(t, map[string]any{"idempotency_key": key, "budget": 100})

	type outcome struct {
		res *Result
		err error
	}
	first := make(chan outcome, 1)
	go func() {
		r, err := wrapped(ctx, req)
		first <- outcome{r, err}
	}()
	<-started

	_, err := wrapped(ctx, req)
	var inFlight *InFlightError
	require.ErrorAs(t, err, &inFlight)
	assert.Equal(t, 30*time.Second, inFlight.RetryAfter)
	assert.Equal(t, CodeIdempotencyInFlight, inFlight.Code())

	_, err = wrapped(ctx, mustJSON(t, map[string]any{"idempotency_key": key, "budget": 200}))
	var conflict *ConflictError
	require.ErrorAs(t, err, &conflict)

	close(release)
	got := <-first
	require.NoError(t, got.err)
	assert.False(t, got.res.Replayed)

	replay, err := wrapped(ctx, req)
	require.NoError(t, err)
	assert.True(t, replay.Replayed)
	assert.JSONEq(t, `{"media_buy_id":"mb-1"}`, string(replay.Response))
	assert.EqualValues(t, 1, atomic.LoadInt32(&calls))
}

func TestWrapClaimReleasedOnHandlerError(t *testing.T) {
	now := time.Now().UTC()
	s, _ := newTestStore(t, &now)

	var calls int32
	wrapped := s.Wrap(func(context.Context, []byte) ([]byte, error) {
		if atomic.AddInt32(&calls, 1) == 1 {
			return nil, errors.New("upstream timeout")
		}
		return []byte(`{"ok":true}`), nil
	})
	ctx := WithPrincipal(context.Background(), "p1")
	req := mustJSON(t, map[string]any{"idempotency_key": Generate()})

	_, err := wrapped(ctx, req)
	require.EqualError(t, err, "upstream timeout")

	res, err := wrapped(ctx, req)
	require.NoError(t, err)
	assert.False(t, res.Replayed)
	assert.EqualValues(t, 2, atomic.LoadInt32(&calls))
}

func TestWrapExpiredClaimIsNotReexecuted(t *testing.T) {
	now := time.Now().UTC()
	s, b := newTestStore(t, &now)

	ctx := WithPrincipal(context.Background(), "p1")
	key := Generate()
	req := mustJSON(t, map[string]any{"idempotency_key": key})
	reqHash, err := s.opts.Hash(req)
	require.NoError(t, err)
	claimHash, err := newClaimHash(reqHash)
	require.NoError(t, err)
	scope, err := s.opts.Scope(ctx, req)
	require.NoError(t, err)
	_, stored, err := b.PutIfAbsent(ctx, scope, key, &Entry{
		Hash:      claimHash,
		Response:  []byte{},
		ExpiresAt: now.Add(-2 * time.Hour),
	})
	require.NoError(t, err)
	require.True(t, stored)

	var calls int32
	wrapped := s.Wrap(func(context.Context, []byte) ([]byte, error) {
		atomic.AddInt32(&calls, 1)
		return []byte(`{}`), nil
	})
	_, err = wrapped(ctx, req)
	var expired *ExpiredError
	require.ErrorAs(t, err, &expired)
	assert.Zero(t, atomic.LoadInt32(&calls))
}

func TestInFlightRetryAfterBounds(t *testing.T) {
	now := time.Now()
	tests := []struct {
		name      string
		expiresAt time.Time
		want      time.Duration
	}{
		{"capped at 30s", now.Add(time.Hour), 30 * time.Second},
		{"remaining time", now.Add(7 * time.Second), 7 * time.Second},
		{"floored at 1s", now.Add(100 * time.Millisecond), time.Second},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, inFlightRetryAfter(tt.expiresAt, now))
		})
	}
}
