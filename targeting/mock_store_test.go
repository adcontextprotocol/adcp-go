package targeting

import (
	"context"
	"testing"
	"time"
)

func TestMockStore_SetOperations(t *testing.T) {
	ctx := context.Background()
	s := NewMockStore()

	s.SetAdd("colors", "red", "green", "blue")
	s.SetAdd("warm", "red", "orange", "yellow")

	t.Run("IsMember", func(t *testing.T) {
		ok, err := s.SetIsMember(ctx, "colors", "red")
		if err != nil {
			t.Fatal(err)
		}
		if !ok {
			t.Error("expected red in colors")
		}

		ok, err = s.SetIsMember(ctx, "colors", "purple")
		if err != nil {
			t.Fatal(err)
		}
		if ok {
			t.Error("expected purple not in colors")
		}
	})

	t.Run("IsMember_MissingKey", func(t *testing.T) {
		ok, err := s.SetIsMember(ctx, "nonexistent", "x")
		if err != nil {
			t.Fatal(err)
		}
		if ok {
			t.Error("expected false for missing key")
		}
	})

	t.Run("Intersect", func(t *testing.T) {
		result, err := s.SetIntersect(ctx, "colors", "warm")
		if err != nil {
			t.Fatal(err)
		}
		if len(result) != 1 || result[0] != "red" {
			t.Errorf("expected [red], got %v", result)
		}
	})

	t.Run("Intersect_NoOverlap", func(t *testing.T) {
		s.SetAdd("cold", "blue", "purple")
		result, err := s.SetIntersect(ctx, "warm", "cold")
		if err != nil {
			t.Fatal(err)
		}
		if len(result) != 0 {
			t.Errorf("expected empty intersection, got %v", result)
		}
	})

	t.Run("Intersect_MissingKey", func(t *testing.T) {
		result, err := s.SetIntersect(ctx, "colors", "nonexistent")
		if err != nil {
			t.Fatal(err)
		}
		if len(result) != 0 {
			t.Errorf("expected empty for missing key, got %v", result)
		}
	})
}

func TestMockStore_StringOperations(t *testing.T) {
	ctx := context.Background()
	s := NewMockStore()

	t.Run("GetMissing", func(t *testing.T) {
		_, ok, err := s.Get(ctx, "missing")
		if err != nil {
			t.Fatal(err)
		}
		if ok {
			t.Error("expected ok=false for missing key")
		}
	})

	t.Run("SetAndGet", func(t *testing.T) {
		if err := s.Set(ctx, "name", "alice", 0); err != nil {
			t.Fatal(err)
		}
		val, ok, err := s.Get(ctx, "name")
		if err != nil {
			t.Fatal(err)
		}
		if !ok || val != "alice" {
			t.Errorf("expected alice, got %q ok=%v", val, ok)
		}
	})

	t.Run("TTLExpiry", func(t *testing.T) {
		now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
		s.Now = func() time.Time { return now }

		if err := s.Set(ctx, "temp", "value", 10*time.Second); err != nil {
			t.Fatal(err)
		}

		val, ok, err := s.Get(ctx, "temp")
		if err != nil {
			t.Fatal(err)
		}
		if !ok || val != "value" {
			t.Error("expected value before expiry")
		}

		// Advance past TTL.
		s.Now = func() time.Time { return now.Add(11 * time.Second) }

		_, ok, err = s.Get(ctx, "temp")
		if err != nil {
			t.Fatal(err)
		}
		if ok {
			t.Error("expected expired key to be gone")
		}
	})

	t.Run("Exists", func(t *testing.T) {
		if err := s.Set(ctx, "exists-test", "yes", 0); err != nil {
			t.Fatal(err)
		}
		ok, err := s.Exists(ctx, "exists-test")
		if err != nil {
			t.Fatal(err)
		}
		if !ok {
			t.Error("expected key to exist")
		}

		ok, err = s.Exists(ctx, "nope")
		if err != nil {
			t.Fatal(err)
		}
		if ok {
			t.Error("expected key not to exist")
		}
	})
}

func TestMockStore_SortedSetOperations(t *testing.T) {
	ctx := context.Background()
	s := NewMockStore()

	t.Run("ZAddAndCount", func(t *testing.T) {
		if err := s.ZAdd(ctx, "events", 100, "a"); err != nil {
			t.Fatal(err)
		}
		if err := s.ZAdd(ctx, "events", 200, "b"); err != nil {
			t.Fatal(err)
		}
		if err := s.ZAdd(ctx, "events", 300, "c"); err != nil {
			t.Fatal(err)
		}

		count, err := s.ZCount(ctx, "events", 150, 350)
		if err != nil {
			t.Fatal(err)
		}
		if count != 2 {
			t.Errorf("expected 2, got %d", count)
		}

		count, err = s.ZCount(ctx, "events", 0, 500)
		if err != nil {
			t.Fatal(err)
		}
		if count != 3 {
			t.Errorf("expected 3, got %d", count)
		}
	})

	t.Run("ZCount_MissingKey", func(t *testing.T) {
		count, err := s.ZCount(ctx, "nonexistent", 0, 100)
		if err != nil {
			t.Fatal(err)
		}
		if count != 0 {
			t.Errorf("expected 0, got %d", count)
		}
	})

	t.Run("ZExpire", func(t *testing.T) {
		now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
		s.Now = func() time.Time { return now }

		if err := s.ZAdd(ctx, "expiring", 1, "x"); err != nil {
			t.Fatal(err)
		}
		if err := s.ZExpire(ctx, "expiring", 5*time.Second); err != nil {
			t.Fatal(err)
		}

		count, err := s.ZCount(ctx, "expiring", 0, 10)
		if err != nil {
			t.Fatal(err)
		}
		if count != 1 {
			t.Errorf("expected 1 before expiry, got %d", count)
		}

		s.Now = func() time.Time { return now.Add(6 * time.Second) }

		count, err = s.ZCount(ctx, "expiring", 0, 10)
		if err != nil {
			t.Fatal(err)
		}
		if count != 0 {
			t.Errorf("expected 0 after expiry, got %d", count)
		}
	})
}
