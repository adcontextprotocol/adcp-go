package main

import (
	"testing"
	"time"
)

func TestProviderHealth_Success(t *testing.T) {
	h := NewProviderHealth(3, 10*time.Second)
	h.RecordSuccess("p1")
	h.RecordSuccess("p1")

	snap := h.Snapshot()
	if snap["p1"].Successes != 2 {
		t.Errorf("expected 2 successes, got %d", snap["p1"].Successes)
	}
	if snap["p1"].CircuitOpen {
		t.Error("circuit should be closed")
	}
}

func TestProviderHealth_CircuitBreaker(t *testing.T) {
	h := NewProviderHealth(3, 100*time.Millisecond)

	h.RecordFailure("p1")
	h.RecordFailure("p1")
	if h.IsCircuitOpen("p1") {
		t.Error("circuit should not be open after 2 failures (threshold=3)")
	}

	h.RecordFailure("p1")
	if !h.IsCircuitOpen("p1") {
		t.Error("circuit should be open after 3 consecutive failures")
	}

	// Wait for cooldown
	time.Sleep(150 * time.Millisecond)
	if h.IsCircuitOpen("p1") {
		t.Error("circuit should auto-close after cooldown")
	}
}

func TestProviderHealth_SuccessResetsConsecutive(t *testing.T) {
	h := NewProviderHealth(3, 10*time.Second)

	h.RecordFailure("p1")
	h.RecordFailure("p1")
	h.RecordSuccess("p1") // resets consecutive failures
	h.RecordFailure("p1")

	if h.IsCircuitOpen("p1") {
		t.Error("circuit should not be open — success reset consecutive count")
	}
}

func TestProviderHealth_Timeout(t *testing.T) {
	h := NewProviderHealth(2, 10*time.Second)
	h.RecordTimeout("p1")
	h.RecordTimeout("p1")

	snap := h.Snapshot()
	if snap["p1"].Timeouts != 2 {
		t.Errorf("expected 2 timeouts, got %d", snap["p1"].Timeouts)
	}
	if !snap["p1"].CircuitOpen {
		t.Error("circuit should be open after 2 timeouts (threshold=2)")
	}
}
