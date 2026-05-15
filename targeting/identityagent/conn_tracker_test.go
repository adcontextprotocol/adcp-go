package identityagent

import (
	"net"
	"sync"
	"testing"
	"time"
)

// TestTrackingListener_AcceptAndClose exercises the basic increment/decrement
// flow over a real localhost listener so the test catches any misuse of the
// net.Listener interface (Accept returning a tracking-wrapped conn that the
// net stack actually treats as a Conn). Run with -race to catch any sharing
// hazards on the atomic counter.
func TestTrackingListener_AcceptAndClose(t *testing.T) {
	tracker := &connTracker{}
	base, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	ln := &trackingListener{Listener: base, tracker: tracker}
	defer func() { _ = ln.Close() }()

	const dialN = 8
	dialErr := make(chan error, dialN)
	conns := make([]net.Conn, 0, dialN)
	var mu sync.Mutex

	acceptDone := make(chan struct{})
	go func() {
		defer close(acceptDone)
		for range dialN {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			mu.Lock()
			conns = append(conns, c)
			mu.Unlock()
		}
	}()

	for range dialN {
		go func() {
			c, err := net.DialTimeout("tcp", base.Addr().String(), 2*time.Second)
			if err != nil {
				dialErr <- err
				return
			}
			dialErr <- nil
			_ = c.Close()
		}()
	}

	<-acceptDone
	for range dialN {
		if err := <-dialErr; err != nil {
			t.Fatalf("dial: %v", err)
		}
	}

	if got := tracker.Open(); got != int64(dialN) {
		t.Fatalf("after accepts: open=%d, want %d", got, dialN)
	}

	mu.Lock()
	defer mu.Unlock()
	for _, c := range conns {
		_ = c.Close()
		// Idempotency: a second Close must not double-decrement.
		_ = c.Close()
	}
	if got := tracker.Open(); got != 0 {
		t.Fatalf("after closes: open=%d, want 0", got)
	}
}
