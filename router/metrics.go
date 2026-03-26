package main

import (
	"encoding/json"
	"net/http"
	"sync/atomic"
)

// Metrics tracks request counts and provider health for the /metrics endpoint.
type Metrics struct {
	ContextRequests  atomic.Int64
	IdentityRequests atomic.Int64
	health           *ProviderHealth
	sigCache         *SignatureCache
}

// NewMetrics creates a metrics tracker.
func NewMetrics(health *ProviderHealth, sigCache *SignatureCache) *Metrics {
	return &Metrics{health: health, sigCache: sigCache}
}

// MetricsSnapshot is the JSON response for GET /metrics.
type MetricsSnapshot struct {
	Requests  RequestCounts                    `json:"requests"`
	Providers map[string]ProviderStatsSnapshot `json:"providers"`
	SigCache  *SigCacheStats                   `json:"signature_cache,omitempty"`
}

// RequestCounts tracks request volumes by endpoint.
type RequestCounts struct {
	Context  int64 `json:"context"`
	Identity int64 `json:"identity"`
}

// SigCacheStats reports signature cache utilization.
type SigCacheStats struct {
	Size     int   `json:"size"`
	MaxSize  int   `json:"max_size"`
	Hits     int64 `json:"hits"`
	Misses   int64 `json:"misses"`
}

// HandleMetrics serves GET /metrics.
func (m *Metrics) HandleMetrics(w http.ResponseWriter, _ *http.Request) {
	snap := MetricsSnapshot{
		Requests: RequestCounts{
			Context:  m.ContextRequests.Load(),
			Identity: m.IdentityRequests.Load(),
		},
		Providers: m.health.Snapshot(),
	}
	if m.sigCache != nil {
		snap.SigCache = m.sigCache.Stats()
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(snap)
}
