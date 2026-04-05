package targeting

import (
	"testing"
	"time"
)

func TestCache_SetAndGet(t *testing.T) {
	c := NewCache(10 * time.Second)
	c.Set("key", "value")

	val, ok := c.Get("key")
	if !ok || val != "value" {
		t.Errorf("expected value, got %v ok=%v", val, ok)
	}
}

func TestCache_Miss(t *testing.T) {
	c := NewCache(10 * time.Second)
	_, ok := c.Get("missing")
	if ok {
		t.Error("expected miss")
	}
}

func TestCache_TTLExpiry(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	c := NewCache(5 * time.Second)
	c.now = func() time.Time { return now }

	c.Set("key", "value")

	// Still valid.
	val, ok := c.Get("key")
	if !ok || val != "value" {
		t.Error("expected hit before expiry")
	}

	// Advance past TTL.
	c.now = func() time.Time { return now.Add(6 * time.Second) }

	_, ok = c.Get("key")
	if ok {
		t.Error("expected miss after expiry")
	}
}

func TestCache_Overwrite(t *testing.T) {
	c := NewCache(10 * time.Second)
	c.Set("key", "v1")
	c.Set("key", "v2")

	val, ok := c.Get("key")
	if !ok || val != "v2" {
		t.Errorf("expected v2, got %v", val)
	}
}

func TestCache_DifferentTypes(t *testing.T) {
	c := NewCache(10 * time.Second)
	c.Set("str", "hello")
	c.Set("int", 42)
	c.Set("slice", []string{"a", "b"})

	val, _ := c.Get("str")
	if val != "hello" {
		t.Error("string mismatch")
	}
	val, _ = c.Get("int")
	if val != 42 {
		t.Error("int mismatch")
	}
	val, _ = c.Get("slice")
	if s, ok := val.([]string); !ok || len(s) != 2 {
		t.Error("slice mismatch")
	}
}
