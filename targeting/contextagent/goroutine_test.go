package contextagent

import (
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestSafeGoWithPanicSink_PanicSurfaces pins the contract that a Serve
// goroutine panicking surfaces on the supplied onPanic callback so the
// main lifecycle loop can tear the agent down. Without this path a
// panicking listener would leave running.Load() true and /live
// reporting OK with no actual listener (black hole).
func TestSafeGoWithPanicSink_PanicSurfaces(t *testing.T) {
	got := make(chan error, 1)
	safeGoWithPanicSink(nil, nil, "test-panic", func(err error) { got <- err }, func() {
		panic("boom")
	})
	select {
	case err := <-got:
		require.Error(t, err)
		assert.Contains(t, err.Error(), "test-panic")
		assert.Contains(t, err.Error(), "boom")
	case <-time.After(2 * time.Second):
		t.Fatal("onPanic callback never fired")
	}
}

// TestSafeGoWithPanicSink_NoPanicNoCallback verifies the callback only
// fires on panic, not on a normal goroutine return.
func TestSafeGoWithPanicSink_NoPanicNoCallback(t *testing.T) {
	got := make(chan error, 1)
	done := make(chan struct{})
	safeGoWithPanicSink(nil, nil, "test-clean", func(err error) { got <- err }, func() {
		close(done)
	})
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("goroutine never ran")
	}
	select {
	case err := <-got:
		t.Fatalf("onPanic must not fire on clean return; got %v", err)
	case <-time.After(50 * time.Millisecond):
	}
}

// TestSafeGo_StillSwallowsPanic verifies the plain safeGo wrapper still
// recovers panics (no onPanic callback) — keeping background subsystems
// like keystore-refresh from crashing the process.
func TestSafeGo_StillSwallowsPanic(t *testing.T) {
	done := make(chan struct{})
	safeGo(nil, nil, "test-swallow", func() {
		defer close(done)
		panic(errors.New("recovered"))
	})
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("goroutine never ran")
	}
}
