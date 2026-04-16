package router

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// testDiscovery creates a Discovery that bypasses safeDialContext for httptest.Server.
func testDiscovery(ps *ProviderSet, health *ProviderHealth, endpoint string, budget time.Duration) *Discovery {
	d := NewDiscovery(ps, health, DiscoveryConfig{Endpoint: endpoint}, budget,
		WithDiscoveryClient(&http.Client{Timeout: 5 * time.Second}))
	d.interval = 50 * time.Millisecond
	return d
}

func TestDiscovery_AddsNewProviders(t *testing.T) {
	providers := []ProviderConfig{
		{ID: "new-1", Endpoint: "https://example.com", ContextMatch: true},
		{ID: "new-2", Endpoint: "https://example.com", IdentityMatch: true, Countries: []string{"US"}, UIDTypes: []string{"uid2"}},
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(providers)
	}))
	defer srv.Close()

	ps := NewProviderSet(nil)
	d := testDiscovery(ps, nil, srv.URL, 50*time.Millisecond)

	d.Start()
	time.Sleep(100 * time.Millisecond)
	d.Stop()

	all := ps.All()
	require.Len(t, all, 2, "expected 2 providers")
	assert.Equal(t, ProviderStatusActive, all[0].EffectiveStatus(), "new provider should be active")
}

func TestDiscovery_RemovesProviders(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode([]ProviderConfig{
			{ID: "kept", Endpoint: "https://example.com", ContextMatch: true},
		})
	}))
	defer srv.Close()

	ps := NewProviderSet([]ProviderConfig{
		{ID: "kept", Endpoint: "https://example.com", ContextMatch: true},
		{ID: "removed", Endpoint: "https://example.com", ContextMatch: true},
	})
	d := testDiscovery(ps, nil, srv.URL, 50*time.Millisecond)

	d.Start()
	time.Sleep(100 * time.Millisecond)
	d.Stop()

	all := ps.All()
	require.Len(t, all, 1, "expected 1 provider")
	assert.Equal(t, "kept", all[0].ID)
}

func TestDiscovery_DrainsRemovedWithInflight(t *testing.T) {
	// Discovery returns a single provider; the "draining" provider from current set
	// is not in the response but has inflight > 0.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode([]ProviderConfig{
			{ID: "kept", Endpoint: "https://example.com", ContextMatch: true},
		})
	}))
	defer srv.Close()

	health := NewProviderHealth(3, 10*time.Second)
	health.IncrInflight("draining")

	ps := NewProviderSet([]ProviderConfig{
		{ID: "kept", Endpoint: "https://example.com", ContextMatch: true},
		{ID: "draining", Endpoint: "https://example.com", ContextMatch: true},
	})
	d := testDiscovery(ps, health, srv.URL, 50*time.Millisecond)

	d.Start()
	time.Sleep(100 * time.Millisecond)
	d.Stop()

	all := ps.All()
	require.Len(t, all, 2, "expected 2 providers (kept + draining)")
	byID := map[string]ProviderConfig{}
	for _, p := range all {
		byID[p.ID] = p
	}
	assert.Equal(t, ProviderStatusDraining, byID["draining"].Status)
}

func TestDiscovery_UpdatesChangedProviders(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode([]ProviderConfig{
			{ID: "p1", Endpoint: "https://new-endpoint.com", ContextMatch: true, Timeout: 25 * time.Millisecond},
		})
	}))
	defer srv.Close()

	ps := NewProviderSet([]ProviderConfig{
		{ID: "p1", Endpoint: "https://old-endpoint.com", ContextMatch: true, Status: ProviderStatusActive},
	})
	d := testDiscovery(ps, nil, srv.URL, 50*time.Millisecond)

	d.Start()
	time.Sleep(100 * time.Millisecond)
	d.Stop()

	p, ok := ps.Get("p1")
	require.True(t, ok, "p1 should still exist")
	assert.Equal(t, "https://new-endpoint.com", p.Endpoint, "endpoint should be updated")
	assert.Equal(t, ProviderStatusActive, p.Status, "status should be preserved as active")
}

func TestDiscovery_SkipsInvalidProviders(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode([]ProviderConfig{
			{ID: "valid", Endpoint: "https://example.com", ContextMatch: true},
			{ID: "no-match", Endpoint: "https://example.com"}, // neither context nor identity
			{ID: "bad-endpoint", Endpoint: "http://localhost:8080", ContextMatch: true},
		})
	}))
	defer srv.Close()

	ps := NewProviderSet(nil)
	d := testDiscovery(ps, nil, srv.URL, 50*time.Millisecond)

	d.Start()
	time.Sleep(100 * time.Millisecond)
	d.Stop()

	all := ps.All()
	require.Len(t, all, 1, "expected 1 valid provider")
	assert.Equal(t, "valid", all[0].ID)
}

func TestDiscovery_EndpointFailure(t *testing.T) {
	var callCount atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	ps := NewProviderSet([]ProviderConfig{
		{ID: "existing", Endpoint: "https://example.com", ContextMatch: true},
	})
	d := testDiscovery(ps, nil, srv.URL, 50*time.Millisecond)

	d.Start()
	time.Sleep(200 * time.Millisecond)
	d.Stop()

	// Existing providers should be preserved on failure.
	all := ps.All()
	require.Len(t, all, 1, "existing providers should be preserved on discovery failure")
	assert.Equal(t, "existing", all[0].ID)

	assert.GreaterOrEqual(t, callCount.Load(), int64(2), "expected at least 2 poll attempts")
}

func TestDiscovery_EmptyResponsePreservesCurrent(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode([]ProviderConfig{})
	}))
	defer srv.Close()

	ps := NewProviderSet([]ProviderConfig{
		{ID: "existing", Endpoint: "https://example.com", ContextMatch: true},
	})
	d := testDiscovery(ps, nil, srv.URL, 50*time.Millisecond)

	d.Start()
	time.Sleep(100 * time.Millisecond)
	d.Stop()

	all := ps.All()
	require.Len(t, all, 1, "empty discovery should preserve current providers")
	assert.Equal(t, "existing", all[0].ID)
}

func TestReconcile_PreservesStatus(t *testing.T) {
	d := &Discovery{latencyBudget: 50 * time.Millisecond}

	current := []ProviderConfig{
		{ID: "p1", Endpoint: "https://example.com", ContextMatch: true, Status: ProviderStatusDraining},
	}
	incoming := []ProviderConfig{
		{ID: "p1", Endpoint: "https://example.com", ContextMatch: true}, // no status in incoming
	}

	result := d.reconcile(current, incoming)
	require.Len(t, result, 1)
	assert.Equal(t, ProviderStatusDraining, result[0].Status, "status should be preserved")
}
