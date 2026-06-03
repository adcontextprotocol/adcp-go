package urlliststore_test

import (
	"context"
	"testing"
	"time"

	"github.com/adcontextprotocol/adcp-go/targeting/urlliststore"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestService_RoundTrip(t *testing.T) {
	ctx := context.Background()
	store := urlliststore.NewMockStore()
	svc, err := urlliststore.NewService(store)
	require.NoError(t, err)
	r := urlliststore.NewReader(store)

	require.NoError(t, svc.AddToBlocklist(ctx, "pkg-1", "h1", "h2"))
	require.NoError(t, svc.AddToAllowlist(ctx, "pkg-1", "ha"))

	blocked, err := r.IsBlocked(ctx, "pkg-1", "h1")
	require.NoError(t, err)
	assert.True(t, blocked)

	blocked, err = r.IsBlocked(ctx, "pkg-1", "h-missing")
	require.NoError(t, err)
	assert.False(t, blocked)

	allowed, err := r.IsAllowed(ctx, "pkg-1", "ha")
	require.NoError(t, err)
	assert.True(t, allowed)
}

func TestService_RemoveAndClear(t *testing.T) {
	ctx := context.Background()
	store := urlliststore.NewMockStore()
	svc, _ := urlliststore.NewService(store)
	r := urlliststore.NewReader(store)

	require.NoError(t, svc.AddToBlocklist(ctx, "pkg-1", "h1", "h2"))
	require.NoError(t, svc.RemoveFromBlocklist(ctx, "pkg-1", "h1"))

	blocked, _ := r.IsBlocked(ctx, "pkg-1", "h1")
	assert.False(t, blocked)
	blocked, _ = r.IsBlocked(ctx, "pkg-1", "h2")
	assert.True(t, blocked)

	require.NoError(t, svc.ClearBlocklist(ctx, "pkg-1"))
	blocked, _ = r.IsBlocked(ctx, "pkg-1", "h2")
	assert.False(t, blocked)
}

func TestCache_HitsAvoidBackingCalls(t *testing.T) {
	ctx := context.Background()
	base := urlliststore.NewMockStore()
	svc, _ := urlliststore.NewService(base)
	require.NoError(t, svc.AddToBlocklist(ctx, "pkg-1", "h-blocked"))

	counting := &countingStore{Store: base}
	r := urlliststore.WithCache(urlliststore.NewReader(counting), urlliststore.CacheConfig{Size: 16, TTL: time.Minute})

	for range 5 {
		_, err := r.IsBlocked(ctx, "pkg-1", "h-blocked")
		require.NoError(t, err)
		_, err = r.IsBlocked(ctx, "pkg-1", "h-missing")
		require.NoError(t, err)
	}
	assert.Equal(t, 2, counting.isMemberCalls,
		"two distinct (pkg, hash) tuples should each cost one backing call")
}

type countingStore struct {
	urlliststore.Store
	isMemberCalls int
}

func (c *countingStore) SetIsMember(ctx context.Context, key, member string) (bool, error) {
	c.isMemberCalls++
	return c.Store.SetIsMember(ctx, key, member)
}
