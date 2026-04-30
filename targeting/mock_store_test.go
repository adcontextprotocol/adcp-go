package targeting

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMockStore_SetOperations(t *testing.T) {
	ctx := context.Background()
	s := NewMockStore()

	s.SetAdd("colors", "red", "green", "blue")
	s.SetAdd("warm", "red", "orange", "yellow")

	t.Run("IsMember", func(t *testing.T) {
		ok, err := s.SetIsMember(ctx, "colors", "red")
		require.NoError(t, err)
		assert.True(t, ok, "expected red in colors")

		ok, err = s.SetIsMember(ctx, "colors", "purple")
		require.NoError(t, err)
		assert.False(t, ok, "expected purple not in colors")
	})

	t.Run("IsMember_MissingKey", func(t *testing.T) {
		ok, err := s.SetIsMember(ctx, "nonexistent", "x")
		require.NoError(t, err)
		assert.False(t, ok, "expected false for missing key")
	})

	t.Run("Intersect", func(t *testing.T) {
		result, err := s.SetIntersect(ctx, "colors", "warm")
		require.NoError(t, err)
		assert.Equal(t, []string{"red"}, result)
	})

	t.Run("Intersect_NoOverlap", func(t *testing.T) {
		s.SetAdd("cold", "blue", "purple")
		result, err := s.SetIntersect(ctx, "warm", "cold")
		require.NoError(t, err)
		assert.Empty(t, result)
	})

	t.Run("Intersect_MissingKey", func(t *testing.T) {
		result, err := s.SetIntersect(ctx, "colors", "nonexistent")
		require.NoError(t, err)
		assert.Empty(t, result)
	})
}

func TestMockStore_StringOperations(t *testing.T) {
	ctx := context.Background()
	s := NewMockStore()

	t.Run("GetMissing", func(t *testing.T) {
		_, ok, err := s.Get(ctx, "missing")
		require.NoError(t, err)
		assert.False(t, ok, "expected ok=false for missing key")
	})

	t.Run("SetAndGet", func(t *testing.T) {
		err := s.Set(ctx, "name", "alice", 0)
		require.NoError(t, err)
		val, ok, err := s.Get(ctx, "name")
		require.NoError(t, err)
		assert.True(t, ok)
		assert.Equal(t, "alice", val)
	})

	t.Run("TTLExpiry", func(t *testing.T) {
		now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
		s.Now = func() time.Time { return now }

		err := s.Set(ctx, "temp", "value", 10*time.Second)
		require.NoError(t, err)

		val, ok, err := s.Get(ctx, "temp")
		require.NoError(t, err)
		assert.True(t, ok)
		assert.Equal(t, "value", val)

		s.Now = func() time.Time { return now.Add(11 * time.Second) }

		_, ok, err = s.Get(ctx, "temp")
		require.NoError(t, err)
		assert.False(t, ok, "expected expired key to be gone")
	})

	t.Run("Exists", func(t *testing.T) {
		err := s.Set(ctx, "exists-test", "yes", 0)
		require.NoError(t, err)
		ok, err := s.Exists(ctx, "exists-test")
		require.NoError(t, err)
		assert.True(t, ok, "expected key to exist")

		ok, err = s.Exists(ctx, "nope")
		require.NoError(t, err)
		assert.False(t, ok, "expected key not to exist")
	})
}
