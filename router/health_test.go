package router

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestProviderHealth_Success(t *testing.T) {
	h := NewProviderHealth(3, 10*time.Second)
	h.RecordSuccess("p1")
	h.RecordSuccess("p1")

	snap := h.Snapshot()
	assert.Equal(t, int64(2), snap["p1"].Successes)
	assert.False(t, snap["p1"].CircuitOpen, "circuit should be closed")
}

func TestProviderHealth_CircuitBreaker(t *testing.T) {
	h := NewProviderHealth(3, 100*time.Millisecond)

	h.RecordFailure("p1")
	h.RecordFailure("p1")
	assert.False(t, h.IsCircuitOpen("p1"), "circuit should not be open after 2 failures (threshold=3)")

	h.RecordFailure("p1")
	assert.True(t, h.IsCircuitOpen("p1"), "circuit should be open after 3 consecutive failures")

	// Wait for cooldown
	time.Sleep(150 * time.Millisecond)
	assert.False(t, h.IsCircuitOpen("p1"), "circuit should auto-close after cooldown")
}

func TestProviderHealth_SuccessResetsConsecutive(t *testing.T) {
	h := NewProviderHealth(3, 10*time.Second)

	h.RecordFailure("p1")
	h.RecordFailure("p1")
	h.RecordSuccess("p1") // resets consecutive failures
	h.RecordFailure("p1")

	assert.False(t, h.IsCircuitOpen("p1"), "circuit should not be open — success reset consecutive count")
}

func TestProviderHealth_Timeout(t *testing.T) {
	h := NewProviderHealth(2, 10*time.Second)
	h.RecordTimeout("p1")
	h.RecordTimeout("p1")

	snap := h.Snapshot()
	assert.Equal(t, int64(2), snap["p1"].Timeouts)
	assert.True(t, snap["p1"].CircuitOpen, "circuit should be open after 2 timeouts (threshold=2)")
}
