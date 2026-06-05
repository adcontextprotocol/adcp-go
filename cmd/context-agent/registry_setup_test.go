package main

import (
	"context"
	"testing"
	"time"

	"github.com/adcontextprotocol/adcp-go/registry"
)

func TestRegistryPropertyBitmap_MatchesByRID(t *testing.T) {
	const rid = "0190a1b2-c3d4-7e5f-8a9b-0c1d2e3f4a5b"
	idx := registry.NewPropertyIndex()
	ctx := context.Background()
	if err := idx.Put(ctx, &registry.Property{
		PropertyID:  "cnn-homepage",
		PropertyRID: rid,
		Domain:      "cnn.com",
	}); err != nil {
		t.Fatalf("seed property: %v", err)
	}

	bm := &registryPropertyBitmap{idx: idx}

	tests := []struct {
		name   string
		rid    string
		expect bool
	}{
		{"matches the UUID-v7 property_rid", rid, true},
		{"does not match the property_id slug", "cnn-homepage", false},
		{"missing rid", "0190a1b2-c3d4-7e5f-8a9b-ffffffffffff", false},
		{"empty input is never a match", "", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := bm.Contains(tc.rid)
			if got != tc.expect {
				t.Fatalf("Contains(%q) = %v, want %v", tc.rid, got, tc.expect)
			}
		})
	}
}

func TestRegistryPropertyBitmap_NilIsSafe(t *testing.T) {
	var bm *registryPropertyBitmap
	if bm.Contains("anything") {
		t.Fatal("nil bitmap must not match anything")
	}
	bm = &registryPropertyBitmap{idx: nil}
	if bm.Contains("anything") {
		t.Fatal("bitmap with nil idx must not match anything")
	}
}

func TestRegistryBundle_ShutdownIdempotent(t *testing.T) {
	// Construct the bundle inline without buildRegistry so the test
	// does not require a reachable registry feed. Verifies that
	// calling Shutdown twice (and on a zero-value bundle) is safe.
	done := make(chan struct{})
	close(done) // syncer "exited" cleanly
	bundle := &registryBundle{
		properties:  nil,
		cancel:      func() {},
		syncDone:    done,
		redisCloser: nil,
	}
	bundle.Shutdown()
	bundle.Shutdown() // second call must not panic or deadlock

	var nilBundle *registryBundle
	nilBundle.Shutdown() // nil receiver must be a no-op
}

func TestRegistryBundle_LivenessCheck_FailsWhenGoroutineExited(t *testing.T) {
	done := make(chan struct{})
	close(done)
	bundle := &registryBundle{syncDone: done}

	check := bundle.LivenessCheck()
	if err := check.Fn(); err == nil {
		t.Fatal("expected liveness to fail when sync goroutine has exited")
	}
}

func TestRegistryBundle_LivenessCheck_PassesWhenRecentSuccess(t *testing.T) {
	bundle := &registryBundle{syncDone: make(chan struct{})}
	bundle.lastSuccess.Store(time.Now().Unix())

	check := bundle.LivenessCheck()
	if err := check.Fn(); err != nil {
		t.Fatalf("expected liveness to pass on recent success, got %v", err)
	}
}

func TestRegistryBundle_LivenessCheck_FailsWhenStale(t *testing.T) {
	bundle := &registryBundle{syncDone: make(chan struct{})}
	// Force a timestamp older than registrySyncStaleThreshold.
	bundle.lastSuccess.Store(time.Now().Add(-2 * registrySyncStaleThreshold).Unix())

	check := bundle.LivenessCheck()
	if err := check.Fn(); err == nil {
		t.Fatal("expected liveness to fail when lastSuccess is older than the staleness threshold")
	}
}
