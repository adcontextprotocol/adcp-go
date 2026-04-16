package router

import (
	"context"
	"sync"
	"testing"
	"time"
)

func TestProviderSet_ActiveFilters(t *testing.T) {
	ps := NewProviderSet([]ProviderConfig{
		{ID: "a", Status: ProviderStatusActive},
		{ID: "b", Status: ProviderStatusInactive},
		{ID: "c", Status: ProviderStatusDraining},
		{ID: "d"}, // empty = active
	})

	active := ps.Active()
	if len(active) != 2 {
		t.Fatalf("expected 2 active providers, got %d", len(active))
	}
	ids := map[string]bool{}
	for _, p := range active {
		ids[p.ID] = true
	}
	if !ids["a"] || !ids["d"] {
		t.Errorf("expected a and d, got %v", ids)
	}
}

func TestProviderSet_All(t *testing.T) {
	ps := NewProviderSet([]ProviderConfig{
		{ID: "a"},
		{ID: "b", Status: ProviderStatusInactive},
	})
	if len(ps.All()) != 2 {
		t.Errorf("expected 2, got %d", len(ps.All()))
	}
}

func TestProviderSet_Swap(t *testing.T) {
	ps := NewProviderSet([]ProviderConfig{{ID: "old"}})
	ps.Swap([]ProviderConfig{{ID: "new1"}, {ID: "new2"}})
	if len(ps.All()) != 2 {
		t.Fatalf("expected 2 after swap, got %d", len(ps.All()))
	}
	if ps.All()[0].ID != "new1" {
		t.Errorf("expected new1, got %s", ps.All()[0].ID)
	}
}

func TestProviderSet_SwapNil(t *testing.T) {
	ps := NewProviderSet([]ProviderConfig{{ID: "a"}})
	ps.Swap(nil)
	if len(ps.All()) != 0 {
		t.Errorf("expected 0 after nil swap, got %d", len(ps.All()))
	}
}

func TestProviderSet_SetStatus(t *testing.T) {
	ps := NewProviderSet([]ProviderConfig{
		{ID: "a", Status: ProviderStatusActive},
		{ID: "b", Status: ProviderStatusActive},
	})

	if !ps.SetStatus("a", ProviderStatusDraining) {
		t.Error("expected SetStatus to return true for existing provider")
	}
	p, ok := ps.Get("a")
	if !ok || p.Status != ProviderStatusDraining {
		t.Errorf("expected draining, got %v", p.Status)
	}

	// b should be unchanged
	p, _ = ps.Get("b")
	if p.Status != ProviderStatusActive {
		t.Errorf("b should still be active, got %v", p.Status)
	}

	if ps.SetStatus("nonexistent", ProviderStatusInactive) {
		t.Error("expected SetStatus to return false for nonexistent provider")
	}
}

func TestProviderSet_Get(t *testing.T) {
	ps := NewProviderSet([]ProviderConfig{{ID: "a", Endpoint: "http://a.com"}})
	p, ok := ps.Get("a")
	if !ok || p.Endpoint != "http://a.com" {
		t.Errorf("expected to find provider a")
	}
	_, ok = ps.Get("missing")
	if ok {
		t.Error("expected not found")
	}
}

func TestProviderSet_ConcurrentReadWrite(t *testing.T) {
	ps := NewProviderSet([]ProviderConfig{{ID: "a"}})
	var wg sync.WaitGroup

	// Concurrent readers
	for range 10 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 100 {
				_ = ps.Active()
				_ = ps.All()
			}
		}()
	}

	// Concurrent writer
	wg.Add(1)
	go func() {
		defer wg.Done()
		for range 100 {
			ps.Swap([]ProviderConfig{{ID: "a"}, {ID: "b"}})
		}
	}()

	wg.Wait()
}

func TestInflightTracking(t *testing.T) {
	h := NewProviderHealth(3, 10*time.Second)

	h.IncrInflight("p1")
	h.IncrInflight("p1")
	if got := h.Inflight("p1"); got != 2 {
		t.Errorf("expected 2 inflight, got %d", got)
	}

	h.DecrInflight("p1")
	if got := h.Inflight("p1"); got != 1 {
		t.Errorf("expected 1 inflight, got %d", got)
	}

	snap := h.Snapshot()
	if snap["p1"].Inflight != 1 {
		t.Errorf("snapshot inflight: expected 1, got %d", snap["p1"].Inflight)
	}
}

func TestDrainProvider(t *testing.T) {
	health := NewProviderHealth(3, 10*time.Second)
	r, _ := NewRouter(
		[]ProviderConfig{{ID: "p1", ContextMatch: true, Endpoint: "https://example.com"}},
		nil, nil, health,
	)

	// Simulate an in-flight request that completes after 200ms
	health.IncrInflight("p1")
	go func() {
		time.Sleep(200 * time.Millisecond)
		health.DecrInflight("p1")
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	err := r.DrainProvider(ctx, "p1")
	if err != nil {
		t.Fatalf("drain failed: %v", err)
	}

	p, ok := r.Providers().Get("p1")
	if !ok {
		t.Fatal("provider not found after drain")
	}
	if p.Status != ProviderStatusInactive {
		t.Errorf("expected inactive after drain, got %v", p.Status)
	}
}

func TestDrainProvider_Timeout(t *testing.T) {
	health := NewProviderHealth(3, 10*time.Second)
	r, _ := NewRouter(
		[]ProviderConfig{{ID: "p1", ContextMatch: true, Endpoint: "https://example.com"}},
		nil, nil, health,
	)

	// In-flight that never completes
	health.IncrInflight("p1")

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	err := r.DrainProvider(ctx, "p1")
	if err == nil {
		t.Fatal("expected timeout error")
	}

	// Should still be set to inactive on timeout
	p, _ := r.Providers().Get("p1")
	if p.Status != ProviderStatusInactive {
		t.Errorf("expected inactive after drain timeout, got %v", p.Status)
	}
}

func TestDrainProvider_NotFound(t *testing.T) {
	r, _ := NewRouter(nil, nil, nil, nil)
	err := r.DrainProvider(context.Background(), "nonexistent")
	if err == nil {
		t.Error("expected error for nonexistent provider")
	}
}
