package valkeystore

import (
	"context"
	"math"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

func setup(t *testing.T) (*Store, *miniredis.Miniredis) {
	t.Helper()
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatal(err)
	}
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

	result, err := s.SetIntersect(ctx, "colors", "warm")
	if err != nil {
		t.Fatal(err)
	}
	if len(result) != 1 || result[0] != "red" {
		t.Errorf("expected [red], got %v", result)
	}
}

func TestStringOperations(t *testing.T) {
	s, mr := setup(t)
	defer mr.Close()
	ctx := context.Background()

	_, ok, err := s.Get(ctx, "missing")
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Error("expected ok=false for missing key")
	}

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

	ok2, err := s.Exists(ctx, "name")
	if err != nil {
		t.Fatal(err)
	}
	if !ok2 {
		t.Error("expected key to exist")
	}

	if err := s.Set(ctx, "temp", "val", 10*time.Second); err != nil {
		t.Fatal(err)
	}
	mr.FastForward(11 * time.Second)
	_, ok, err = s.Get(ctx, "temp")
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Error("expected expired key to be gone")
	}
}

func TestSortedSetOperations(t *testing.T) {
	s, mr := setup(t)
	defer mr.Close()
	ctx := context.Background()

	if err := s.ZAdd(ctx, "events", 100, "a"); err != nil {
		t.Fatal(err)
	}
	if err := s.ZAdd(ctx, "events", 200, "b"); err != nil {
		t.Fatal(err)
	}
	if err := s.ZAdd(ctx, "events", 300, "c"); err != nil {
		t.Fatal(err)
	}

	count, err := s.ZCount(ctx, "events", 150, math.MaxFloat64)
	if err != nil {
		t.Fatal(err)
	}
	if count != 2 {
		t.Errorf("expected 2, got %d", count)
	}

	if err := s.ZExpire(ctx, "events", 5*time.Second); err != nil {
		t.Fatal(err)
	}
	mr.FastForward(6 * time.Second)
	count, err = s.ZCount(ctx, "events", 0, math.MaxFloat64)
	if err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Errorf("expected 0 after expiry, got %d", count)
	}
}
