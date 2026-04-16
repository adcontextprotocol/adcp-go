package targeting

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestCache_SetAndGet(t *testing.T) {
	c := NewCache(10 * time.Second)
	c.Set("key", "value")

	val, ok := c.Get("key")
	assert.True(t, ok)
	assert.Equal(t, "value", val)
}

func TestCache_Miss(t *testing.T) {
	c := NewCache(10 * time.Second)
	_, ok := c.Get("missing")
	assert.False(t, ok, "expected miss")
}

func TestCache_TTLExpiry(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	c := NewCache(5 * time.Second)
	c.now = func() time.Time { return now }

	c.Set("key", "value")

	// Still valid.
	val, ok := c.Get("key")
	assert.True(t, ok, "expected hit before expiry")
	assert.Equal(t, "value", val)

	// Advance past TTL.
	c.now = func() time.Time { return now.Add(6 * time.Second) }

	_, ok = c.Get("key")
	assert.False(t, ok, "expected miss after expiry")
}

func TestCache_Overwrite(t *testing.T) {
	c := NewCache(10 * time.Second)
	c.Set("key", "v1")
	c.Set("key", "v2")

	val, ok := c.Get("key")
	assert.True(t, ok)
	assert.Equal(t, "v2", val)
}

func TestCache_DifferentTypes(t *testing.T) {
	c := NewCache(10 * time.Second)
	c.Set("str", "hello")
	c.Set("int", 42)
	c.Set("slice", []string{"a", "b"})

	val, _ := c.Get("str")
	assert.Equal(t, "hello", val)
	val, _ = c.Get("int")
	assert.Equal(t, 42, val)
	val, _ = c.Get("slice")
	s, ok := val.([]string)
	assert.True(t, ok, "expected []string type")
	assert.Len(t, s, 2)
}
