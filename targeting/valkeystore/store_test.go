package valkeystore

import (
	"context"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func setup(t *testing.T) (*Store, *miniredis.Miniredis) {
	t.Helper()
	mr, err := miniredis.Run()
	require.NoError(t, err)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	return New(rdb), mr
}

func TestSetOperations(t *testing.T) {
	s, mr := setup(t)
	defer mr.Close()
	ctx := context.Background()

	mr.SAdd("colors", "red", "green", "blue")
	mr.SAdd("warm", "red", "orange")

	ok, err := s.SetIsMember(ctx, "colors", "red")
	require.NoError(t, err)
	assert.True(t, ok, "expected red in colors")

	ok, err = s.SetIsMember(ctx, "colors", "purple")
	require.NoError(t, err)
	assert.False(t, ok, "expected purple not in colors")

	result, err := s.SetIntersect(ctx, "colors", "warm")
	require.NoError(t, err)
	require.Len(t, result, 1)
	assert.Equal(t, "red", result[0])
}

func TestStringOperations(t *testing.T) {
	s, mr := setup(t)
	defer mr.Close()
	ctx := context.Background()

	_, ok, err := s.Get(ctx, "missing")
	require.NoError(t, err)
	assert.False(t, ok, "expected ok=false for missing key")

	err = s.Set(ctx, "name", "alice", 0)
	require.NoError(t, err)
	val, ok, err := s.Get(ctx, "name")
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, "alice", val)

	ok2, err := s.Exists(ctx, "name")
	require.NoError(t, err)
	assert.True(t, ok2, "expected key to exist")

	err = s.Set(ctx, "temp", "val", 10*time.Second)
	require.NoError(t, err)
	mr.FastForward(11 * time.Second)
	_, ok, err = s.Get(ctx, "temp")
	require.NoError(t, err)
	assert.False(t, ok, "expected expired key to be gone")
}

