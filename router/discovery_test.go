package router

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
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
	if len(all) != 2 {
		t.Fatalf("expected 2 providers, got %d", len(all))
	}
	if all[0].EffectiveStatus() != ProviderStatusActive {
		t.Errorf("new provider should be active, got %v", all[0].Status)
	}
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
	if len(all) != 1 {
		t.Fatalf("expected 1 provider, got %d", len(all))
	}
	if all[0].ID != "kept" {
		t.Errorf("expected kept, got %s", all[0].ID)
	}
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
	if len(all) != 2 {
		t.Fatalf("expected 2 providers (kept + draining), got %d", len(all))
	}
	byID := map[string]ProviderConfig{}
	for _, p := range all {
		byID[p.ID] = p
	}
	if byID["draining"].Status != ProviderStatusDraining {
		t.Errorf("expected draining, got %v", byID["draining"].Status)
	}
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
	if !ok {
		t.Fatal("p1 should still exist")
	}
	if p.Endpoint != "https://new-endpoint.com" {
		t.Errorf("endpoint should be updated, got %s", p.Endpoint)
	}
	if p.Status != ProviderStatusActive {
		t.Errorf("status should be preserved as active, got %v", p.Status)
	}
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
	if len(all) != 1 {
		t.Fatalf("expected 1 valid provider, got %d", len(all))
	}
	if all[0].ID != "valid" {
		t.Errorf("expected valid, got %s", all[0].ID)
	}
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
	if len(all) != 1 || all[0].ID != "existing" {
		t.Errorf("existing providers should be preserved on discovery failure, got %v", all)
	}

	if callCount.Load() < 2 {
		t.Errorf("expected at least 2 poll attempts, got %d", callCount.Load())
	}
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
	if len(all) != 1 || all[0].ID != "existing" {
		t.Errorf("empty discovery should preserve current providers, got %v", all)
	}
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
	if len(result) != 1 {
		t.Fatalf("expected 1, got %d", len(result))
	}
	if result[0].Status != ProviderStatusDraining {
		t.Errorf("status should be preserved, got %v", result[0].Status)
	}
}
