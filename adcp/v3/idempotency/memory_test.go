package idempotency

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMemoryPutIfAbsent(t *testing.T) {
	b := NewMemoryBackend(0)
	defer b.Close()

	entry := &Entry{Hash: "h1", Response: []byte(`{}`), ExpiresAt: time.Now().Add(time.Hour)}
	existing, stored, err := b.PutIfAbsent(context.Background(), "s", "k", entry)
	require.NoError(t, err)
	assert.True(t, stored)
	assert.Nil(t, existing)

	entry2 := &Entry{Hash: "h2", Response: []byte(`{"x":1}`), ExpiresAt: time.Now().Add(time.Hour)}
	existing, stored, err = b.PutIfAbsent(context.Background(), "s", "k", entry2)
	require.NoError(t, err)
	assert.False(t, stored)
	require.NotNil(t, existing)
	assert.Equal(t, "h1", existing.Hash)
}

func TestMemoryGetMiss(t *testing.T) {
	b := NewMemoryBackend(0)
	defer b.Close()
	e, err := b.Get(context.Background(), "s", "missing")
	require.NoError(t, err)
	assert.Nil(t, e)
}

func TestMemorySweeperRemovesExpired(t *testing.T) {
	var clk atomic.Pointer[time.Time]
	initial := time.Now()
	clk.Store(&initial)
	b := newMemoryBackend(5*time.Millisecond, func() time.Time { return *clk.Load() })
	defer b.Close()

	entry := &Entry{Hash: "h", Response: []byte(`{}`), ExpiresAt: initial.Add(-time.Minute)}
	_, _, err := b.PutIfAbsent(context.Background(), "s", "k", entry)
	require.NoError(t, err)

	advanced := initial.Add(time.Hour)
	clk.Store(&advanced)
	assert.Eventually(t, func() bool {
		e, _ := b.Get(context.Background(), "s", "k")
		return e == nil
	}, time.Second, 5*time.Millisecond)
}

func TestMemoryReplaceIfHash(t *testing.T) {
	b := NewMemoryBackend(0)
	defer b.Close()
	ctx := context.Background()

	_, stored, err := b.PutIfAbsent(ctx, "s", "k", &Entry{Hash: "claim"})
	require.NoError(t, err)
	require.True(t, stored)

	ok, err := b.ReplaceIfHash(ctx, "s", "k", "wrong", &Entry{Hash: "final"})
	require.NoError(t, err)
	assert.False(t, ok)

	ok, err = b.ReplaceIfHash(ctx, "s", "k", "claim", &Entry{Hash: "final", Response: []byte(`{}`)})
	require.NoError(t, err)
	assert.True(t, ok)
	got, err := b.Get(ctx, "s", "k")
	require.NoError(t, err)
	assert.Equal(t, "final", got.Hash)
}

func TestMemorySweepKeepsUnresolvedClaims(t *testing.T) {
	now := time.Now()
	b := newMemoryBackend(0, func() time.Time { return now })
	ctx := context.Background()
	past := now.Add(-time.Hour)
	_, _, err := b.PutIfAbsent(ctx, "s", "done", &Entry{Hash: "h", Response: []byte(`{}`), ExpiresAt: past})
	require.NoError(t, err)
	claim, err := newClaimHash("h")
	require.NoError(t, err)
	_, _, err = b.PutIfAbsent(ctx, "s", "claim", &Entry{Hash: claim, ExpiresAt: past})
	require.NoError(t, err)

	b.sweep()

	got, err := b.Get(ctx, "s", "done")
	require.NoError(t, err)
	assert.Nil(t, got, "expired completed entry is swept")
	got, err = b.Get(ctx, "s", "claim")
	require.NoError(t, err)
	require.NotNil(t, got, "unresolved claim survives the sweep")
	assert.Equal(t, claim, got.Hash)
}
