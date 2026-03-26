package main

import (
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
	successes          atomic.Int64
	failures           atomic.Int64
	timeouts           atomic.Int64
	consecutiveFailures atomic.Int64
	circuitOpenUntil   atomic.Int64 // unix nano; 0 = closed
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
	if time.Now().UnixNano() >= openUntil {
		// Cooldown expired, allow a probe request (half-open)
		s.circuitOpenUntil.Store(0)
		s.consecutiveFailures.Store(0)
		return false
	}
	return true
}

// ProviderStats returns stats for a single provider.
type ProviderStatsSnapshot struct {
	Successes          int64 `json:"successes"`
	Failures           int64 `json:"failures"`
	Timeouts           int64 `json:"timeouts"`
	ConsecutiveFailures int64 `json:"consecutive_failures"`
	CircuitOpen        bool  `json:"circuit_open"`
}

// Snapshot returns a snapshot of all provider stats.
func (h *ProviderHealth) Snapshot() map[string]ProviderStatsSnapshot {
	h.mu.RLock()
	defer h.mu.RUnlock()
	out := make(map[string]ProviderStatsSnapshot, len(h.stats))
	for id, s := range h.stats {
		out[id] = ProviderStatsSnapshot{
			Successes:           s.successes.Load(),
			Failures:            s.failures.Load(),
			Timeouts:            s.timeouts.Load(),
			ConsecutiveFailures: s.consecutiveFailures.Load(),
			CircuitOpen:         h.IsCircuitOpen(id),
		}
	}
	return out
}
