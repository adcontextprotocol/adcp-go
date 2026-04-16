package router

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestProviderSet_ActiveFilters(t *testing.T) {
	ps := NewProviderSet([]ProviderConfig{
		{ID: "a", Status: ProviderStatusActive},
		{ID: "b", Status: ProviderStatusInactive},
		{ID: "c", Status: ProviderStatusDraining},
		{ID: "d"}, // empty = active
	})

	active := ps.Active()
	require.Len(t, active, 2, "expected 2 active providers")
	ids := map[string]bool{}
	for _, p := range active {
		ids[p.ID] = true
	}
	assert.True(t, ids["a"], "expected a to be active")
	assert.True(t, ids["d"], "expected d to be active")
}

func TestProviderSet_All(t *testing.T) {
	ps := NewProviderSet([]ProviderConfig{
		{ID: "a"},
		{ID: "b", Status: ProviderStatusInactive},
	})
	assert.Len(t, ps.All(), 2)
}

func TestProviderSet_Swap(t *testing.T) {
	ps := NewProviderSet([]ProviderConfig{{ID: "old"}})
	ps.Swap([]ProviderConfig{{ID: "new1"}, {ID: "new2"}})
	require.Len(t, ps.All(), 2, "expected 2 after swap")
	assert.Equal(t, "new1", ps.All()[0].ID)
}

func TestProviderSet_SwapNil(t *testing.T) {
	ps := NewProviderSet([]ProviderConfig{{ID: "a"}})
	ps.Swap(nil)
	assert.Len(t, ps.All(), 0, "expected 0 after nil swap")
}

func TestProviderSet_SetStatus(t *testing.T) {
	ps := NewProviderSet([]ProviderConfig{
		{ID: "a", Status: ProviderStatusActive},
		{ID: "b", Status: ProviderStatusActive},
	})

	assert.True(t, ps.SetStatus("a", ProviderStatusDraining), "expected SetStatus to return true for existing provider")
	p, ok := ps.Get("a")
	require.True(t, ok)
	assert.Equal(t, ProviderStatusDraining, p.Status)

	// b should be unchanged
	p, _ = ps.Get("b")
	assert.Equal(t, ProviderStatusActive, p.Status, "b should still be active")

	assert.False(t, ps.SetStatus("nonexistent", ProviderStatusInactive), "expected SetStatus to return false for nonexistent provider")
}

func TestProviderSet_Get(t *testing.T) {
	ps := NewProviderSet([]ProviderConfig{{ID: "a", Endpoint: "http://a.com"}})
	p, ok := ps.Get("a")
	require.True(t, ok)
	assert.Equal(t, "http://a.com", p.Endpoint)

	_, ok = ps.Get("missing")
	assert.False(t, ok, "expected not found")
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
	assert.Equal(t, int64(2), h.Inflight("p1"))

	h.DecrInflight("p1")
	assert.Equal(t, int64(1), h.Inflight("p1"))

	snap := h.Snapshot()
	assert.Equal(t, int64(1), snap["p1"].Inflight)
}

func TestDrainProvider(t *testing.T) {
	health := NewProviderHealth(3, 10*time.Second)
	r, _ := NewRouter(
		[]ProviderConfig{{ID: "p1", ContextMatch: true, Endpoint: "https://example.com"}},
		nil, health,
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
	require.NoError(t, err, "drain failed")

	p, ok := r.Providers().Get("p1")
	require.True(t, ok, "provider not found after drain")
	assert.Equal(t, ProviderStatusInactive, p.Status, "expected inactive after drain")
}

func TestDrainProvider_Timeout(t *testing.T) {
	health := NewProviderHealth(3, 10*time.Second)
	r, _ := NewRouter(
		[]ProviderConfig{{ID: "p1", ContextMatch: true, Endpoint: "https://example.com"}},
		nil, health,
	)

	// In-flight that never completes
	health.IncrInflight("p1")

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	err := r.DrainProvider(ctx, "p1")
	require.Error(t, err, "expected timeout error")

	// Should still be set to inactive on timeout
	p, _ := r.Providers().Get("p1")
	assert.Equal(t, ProviderStatusInactive, p.Status, "expected inactive after drain timeout")
}

func TestDrainProvider_NotFound(t *testing.T) {
	r, _ := NewRouter(nil, nil, nil)
	err := r.DrainProvider(context.Background(), "nonexistent")
	assert.Error(t, err, "expected error for nonexistent provider")
}
