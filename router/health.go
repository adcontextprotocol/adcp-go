package router

import (
	"log/slog"
	"sync"
	"sync/atomic"
	"time"
)

// ProviderHealth tracks success/failure/timeout stats and circuit breaker state.
type ProviderHealth struct {
	mu                 sync.RWMutex
	stats              map[string]*providerStats
	failureThreshold   int
	cooldownDuration   time.Duration
}

type providerStats struct {
	successes           atomic.Int64
	failures            atomic.Int64
	timeouts            atomic.Int64
	consecutiveFailures atomic.Int64
	circuitOpenUntil    atomic.Int64 // unix nano; 0 = closed
	inflight            atomic.Int64
}

// NewProviderHealth creates a health tracker.
// failureThreshold: consecutive failures before circuit opens.
// cooldown: how long circuit stays open before auto-recovery.
func NewProviderHealth(failureThreshold int, cooldown time.Duration) *ProviderHealth {
	return &ProviderHealth{
		stats:            make(map[string]*providerStats),
		failureThreshold: failureThreshold,
		cooldownDuration: cooldown,
	}
}

func (h *ProviderHealth) getOrCreate(providerID string) *providerStats {
	h.mu.RLock()
	s, ok := h.stats[providerID]
	h.mu.RUnlock()
	if ok {
		return s
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	s, ok = h.stats[providerID]
	if !ok {
		s = &providerStats{}
		h.stats[providerID] = s
	}
	return s
}

// RecordSuccess records a successful provider call.
func (h *ProviderHealth) RecordSuccess(providerID string) {
	s := h.getOrCreate(providerID)
	s.successes.Add(1)
	s.consecutiveFailures.Store(0)
}

// RecordFailure records a provider failure and potentially opens the circuit.
func (h *ProviderHealth) RecordFailure(providerID string) {
	s := h.getOrCreate(providerID)
	s.failures.Add(1)
	consec := s.consecutiveFailures.Add(1)
	if int(consec) >= h.failureThreshold {
		s.circuitOpenUntil.Store(time.Now().Add(h.cooldownDuration).UnixNano())
	}
}

// RecordTimeout records a provider timeout (counted as failure for circuit breaker).
func (h *ProviderHealth) RecordTimeout(providerID string) {
	s := h.getOrCreate(providerID)
	s.timeouts.Add(1)
	s.failures.Add(1)
	consec := s.consecutiveFailures.Add(1)
	if int(consec) >= h.failureThreshold {
		s.circuitOpenUntil.Store(time.Now().Add(h.cooldownDuration).UnixNano())
	}
}

// IsCircuitOpen returns true if the provider's circuit breaker is open.
func (h *ProviderHealth) IsCircuitOpen(providerID string) bool {
	s := h.getOrCreate(providerID)
	openUntil := s.circuitOpenUntil.Load()
	if openUntil == 0 {
		return false
	}
	if time.Now().UnixNano() < openUntil {
		return true
	}
	// Cooldown expired. Exactly one goroutine wins the CAS and becomes the probe;
	// it resets the circuit and clears consecutive failures. All other goroutines
	// that raced to this point lost the CAS, meaning the winner already cleared
	// openUntil — keep the circuit open for them so only the probe gets through.
	if s.circuitOpenUntil.CompareAndSwap(openUntil, 0) {
		s.consecutiveFailures.Store(0)
		return false // this goroutine is the probe
	}
	return true // circuit still open for all other racers
}

// IncrInflight increments the in-flight request count for a provider.
func (h *ProviderHealth) IncrInflight(providerID string) {
	h.getOrCreate(providerID).inflight.Add(1)
}

// DecrInflight decrements the in-flight request count for a provider.
func (h *ProviderHealth) DecrInflight(providerID string) {
	if n := h.getOrCreate(providerID).inflight.Add(-1); n < 0 {
		slog.Warn("inflight counter went negative", "provider", providerID, "value", n)
	}
}

// Inflight returns the current in-flight request count for a provider.
func (h *ProviderHealth) Inflight(providerID string) int64 {
	return h.getOrCreate(providerID).inflight.Load()
}

// ProviderStatsSnapshot is a point-in-time snapshot of provider health stats.
type ProviderStatsSnapshot struct {
	Successes           int64 `json:"successes"`
	Failures            int64 `json:"failures"`
	Timeouts            int64 `json:"timeouts"`
	ConsecutiveFailures int64 `json:"consecutive_failures"`
	CircuitOpen         bool  `json:"circuit_open"`
	Inflight            int64 `json:"inflight"`
}

// Snapshot returns a snapshot of all provider stats.
// Reads circuit state directly to avoid the write side-effect of IsCircuitOpen
// and to prevent a re-entrant lock attempt on the same RWMutex.
func (h *ProviderHealth) Snapshot() map[string]ProviderStatsSnapshot {
	h.mu.RLock()
	defer h.mu.RUnlock()
	out := make(map[string]ProviderStatsSnapshot, len(h.stats))
	for id, s := range h.stats {
		openUntil := s.circuitOpenUntil.Load()
		circuitOpen := openUntil != 0 && time.Now().UnixNano() < openUntil
		out[id] = ProviderStatsSnapshot{
			Successes:           s.successes.Load(),
			Failures:            s.failures.Load(),
			Timeouts:            s.timeouts.Load(),
			ConsecutiveFailures: s.consecutiveFailures.Load(),
			CircuitOpen:         circuitOpen,
			Inflight:            s.inflight.Load(),
		}
	}
	return out
}
