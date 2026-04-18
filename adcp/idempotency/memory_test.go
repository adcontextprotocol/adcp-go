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

