package router

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type testMetrics struct {
	recovered atomic.Int64
	excluded  atomic.Int64
}

func (m *testMetrics) SetHealthStatus(string, bool)         {}
func (m *testMetrics) ObserveCheckDuration(string, float64) {}
func (m *testMetrics) IncExcluded(string)                   { m.excluded.Add(1) }
func (m *testMetrics) IncRecovered(string)                  { m.recovered.Add(1) }

// testHealthChecker creates a HealthChecker that bypasses safeDialContext for httptest.Server.
func testHealthChecker(ps *ProviderSet, health *ProviderHealth, opts ...HealthCheckerOption) *HealthChecker {
	allOpts := []HealthCheckerOption{
		WithHealthCheckClient(&http.Client{Timeout: 5 * time.Second}),
	}
	allOpts = append(allOpts, opts...)
	hc := NewHealthChecker(ps, health, HealthCheckConfig{IntervalSeconds: 0, TimeoutSeconds: 5}, allOpts...)
	hc.interval = 50 * time.Millisecond
	return hc
}

func TestHealthChecker_Preflight(t *testing.T) {
	healthy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer healthy.Close()

	unhealthy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer unhealthy.Close()

	ps := NewProviderSet([]ProviderConfig{
		{ID: "ok", Endpoint: healthy.URL, ContextMatch: true},
		{ID: "bad", Endpoint: unhealthy.URL, ContextMatch: true},
	})
	health := NewProviderHealth(3, 10*time.Second)
	hc := testHealthChecker(ps, health)

	hc.Preflight(context.Background())

	// Preflight logs warnings but doesn't call RecordFailure — it's diagnostic only.
}

func TestHealthChecker_BackgroundPolling(t *testing.T) {
	var callCount atomic.Int64
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer provider.Close()

	ps := NewProviderSet([]ProviderConfig{
		{ID: "p1", Endpoint: provider.URL, ContextMatch: true},
	})
	health := NewProviderHealth(3, 10*time.Second)
	hc := testHealthChecker(ps, health)

	hc.Start()
	time.Sleep(200 * time.Millisecond)
	hc.Stop()

	assert.GreaterOrEqual(t, callCount.Load(), int64(2), "expected at least 2 health checks")
}

func TestHealthChecker_CircuitOpensAfterFailures(t *testing.T) {
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer provider.Close()

	ps := NewProviderSet([]ProviderConfig{
		{ID: "p1", Endpoint: provider.URL, ContextMatch: true},
	})
	health := NewProviderHealth(2, 10*time.Second) // threshold=2
	hc := testHealthChecker(ps, health)

	hc.Start()
	time.Sleep(200 * time.Millisecond)
	hc.Stop()

	snap := health.Snapshot()
	assert.True(t, snap["p1"].CircuitOpen, "circuit should be open after multiple health check failures")
}

func TestHealthChecker_Recovery(t *testing.T) {
	var healthy atomic.Bool
	healthy.Store(false)

	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if healthy.Load() {
			w.WriteHeader(http.StatusOK)
		} else {
			w.WriteHeader(http.StatusServiceUnavailable)
		}
	}))
	defer provider.Close()

	ps := NewProviderSet([]ProviderConfig{
		{ID: "p1", Endpoint: provider.URL, ContextMatch: true},
	})
	health := NewProviderHealth(2, 100*time.Millisecond)
	m := &testMetrics{}
	hc := testHealthChecker(ps, health, WithHealthCheckMetrics(m))

	hc.Start()
	time.Sleep(200 * time.Millisecond) // let circuit open

	snap := health.Snapshot()
	require.True(t, snap["p1"].CircuitOpen, "circuit should be open")

	healthy.Store(true)
	time.Sleep(300 * time.Millisecond)
	hc.Stop()

	snap = health.Snapshot()
	assert.False(t, snap["p1"].CircuitOpen, "circuit should be closed after recovery")

	assert.GreaterOrEqual(t, m.recovered.Load(), int64(1), "expected at least 1 recovery")
}

func TestHealthChecker_SkipsInactiveProviders(t *testing.T) {
	var callCount atomic.Int64
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer provider.Close()

	ps := NewProviderSet([]ProviderConfig{
		{ID: "p1", Endpoint: provider.URL, ContextMatch: true, Status: ProviderStatusInactive},
	})
	health := NewProviderHealth(3, 10*time.Second)
	hc := testHealthChecker(ps, health)

	hc.Start()
	time.Sleep(200 * time.Millisecond)
	hc.Stop()

	assert.Equal(t, int64(0), callCount.Load(), "inactive provider should not be health checked")
}
