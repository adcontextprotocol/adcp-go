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

func TestCapabilityRoundTrip(t *testing.T) {
	s := New(Options{
		Backend: NewMemoryBackend(0),
		TTL:     24 * time.Hour,
	})
	caps := map[string]any{}
	s.MergeCapability(caps)
	ttl, err := ParseCapability(caps, "seller")
	require.NoError(t, err)
	assert.Equal(t, s.TTL(), ttl)
}

func TestMergeCapabilityNestedShape(t *testing.T) {
	s := New(Options{Backend: NewMemoryBackend(0), TTL: time.Hour})
	caps := map[string]any{"adcp": map[string]any{"other": 1}}
	s.MergeCapability(caps)
	adcp := caps["adcp"].(map[string]any)
	assert.Equal(t, 1, adcp["other"], "existing keys preserved")
	ide := adcp["idempotency"].(map[string]any)
	assert.Equal(t, int64(3600), ide["replay_ttl_seconds"])
}

func TestNewPanicsOnTTLBelowMin(t *testing.T) {
	defer func() { assert.NotNil(t, recover()) }()
	New(Options{Backend: NewMemoryBackend(0), TTL: 10 * time.Minute})
}

func TestNewPanicsOnTTLAboveMax(t *testing.T) {
	defer func() { assert.NotNil(t, recover()) }()
	New(Options{Backend: NewMemoryBackend(0), TTL: 30 * 24 * time.Hour})
}

func TestNewPanicsOnNegativeClockSkew(t *testing.T) {
	defer func() { assert.NotNil(t, recover()) }()
	New(Options{Backend: NewMemoryBackend(0), TTL: time.Hour, ClockSkew: -time.Second})
}

func TestClockSkewAllowsReplayPastTTL(t *testing.T) {
	now := time.Now().UTC()
	b := newMemoryBackend(0, func() time.Time { return now })
	s := New(Options{
		Backend:   b,
		TTL:       time.Hour,
		ClockSkew: 30 * time.Second,
		Clock:     func() time.Time { return now },
	})

	var calls int32
	wrapped := s.Wrap(func(context.Context, []byte) ([]byte, error) {
		atomic.AddInt32(&calls, 1)
		return []byte(`{}`), nil
	})
	ctx := WithPrincipal(context.Background(), "p1")
	key := Generate()
	req := mustJSON(t, map[string]any{"idempotency_key": key, "account": "a"})

	_, err := wrapped(ctx, req)
	require.NoError(t, err)

	// Within skew window: replay still succeeds.
	now = now.Add(time.Hour + 20*time.Second)
	r, err := wrapped(ctx, req)
	require.NoError(t, err)
	assert.True(t, r.Replayed, "within clock-skew window should replay")

	// Past skew window: reject as expired.
	now = now.Add(time.Minute)
	_, err = wrapped(ctx, req)
	var ee *ExpiredError
	assert.True(t, errors.As(err, &ee))
}

func TestKeyRequiredFalseAllowsMissingKey(t *testing.T) {
	now := time.Now().UTC()
	keyRequired := false
	s := New(Options{
		Backend:     NewMemoryBackend(0),
		TTL:         time.Hour,
		KeyRequired: &keyRequired,
		Clock:       func() time.Time { return now },
	})

	var calls int32
	wrapped := s.Wrap(func(context.Context, []byte) ([]byte, error) {
		atomic.AddInt32(&calls, 1)
		return []byte(`{"terminated":true}`), nil
	})
	ctx := WithPrincipal(context.Background(), "p1")

	// No key → handler runs uncached, Result.Key is empty, never replays.
	req := mustJSON(t, map[string]any{"session_id": "s1"})
	r1, err := wrapped(ctx, req)
	require.NoError(t, err)
	assert.False(t, r1.Replayed)
	assert.Empty(t, r1.Key)

	r2, err := wrapped(ctx, req)
	require.NoError(t, err)
	assert.False(t, r2.Replayed, "no-key calls must not replay")
	assert.Equal(t, int32(2), atomic.LoadInt32(&calls))

	// Key present → normal idempotent behavior.
	keyed := mustJSON(t, map[string]any{"idempotency_key": Generate(), "session_id": "s1"})
	_, err = wrapped(ctx, keyed)
	require.NoError(t, err)
	r4, err := wrapped(ctx, keyed)
	require.NoError(t, err)
	assert.True(t, r4.Replayed)
}
