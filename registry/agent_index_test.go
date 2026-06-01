package registry

import (
	"context"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAgentIndex_CRUD(t *testing.T) {
	idx := NewAgentIndex()
	ctx := context.Background()

	p := &AgentProfile{
		AgentURL:      "https://agent.example.com",
		Channels:      []string{"ctv"},
		Markets:       []string{"US"},
		PropertyCount: 42,
		HasTMP:        true,
	}

	require.NoError(t, idx.Put(ctx, p))
	require.Equal(t, 1, idx.Count(), "count after put")

	got, ok := idx.Get("https://agent.example.com")
	require.True(t, ok, "Get: not found")
	assert.Equal(t, 42, got.PropertyCount)

	// Update
	p2 := &AgentProfile{
		AgentURL:      "https://agent.example.com",
		Channels:      []string{"ctv", "display"},
		PropertyCount: 100,
	}
	require.NoError(t, idx.Put(ctx, p2))
	assert.Equal(t, 1, idx.Count(), "count after update")
	got, _ = idx.Get("https://agent.example.com")
	assert.Equal(t, 100, got.PropertyCount, "property_count after update")

	// Remove
	_, existed := idx.Get("https://agent.example.com")
	assert.True(t, existed, "should exist before remove")
	require.NoError(t, idx.Remove(ctx, "https://agent.example.com"))
	_, existed = idx.Get("https://agent.example.com")
	assert.False(t, existed, "should not exist after remove")
	// Idempotent: removing again is a no-op error-free.
	require.NoError(t, idx.Remove(ctx, "https://agent.example.com"))
	assert.Equal(t, 0, idx.Count(), "count after remove")
}

func TestAgentIndex_List(t *testing.T) {
	idx := NewAgentIndex()
	ctx := context.Background()

	require.NoError(t, idx.Put(ctx, &AgentProfile{AgentURL: "https://a.com"}))
	require.NoError(t, idx.Put(ctx, &AgentProfile{AgentURL: "https://b.com"}))

	list := idx.List()
	require.Len(t, list, 2)
}

func TestAgentIndex_GetMissing(t *testing.T) {
	idx := NewAgentIndex()
	_, ok := idx.Get("https://nonexistent.com")
	assert.False(t, ok, "should not find nonexistent agent")
}

func TestAgentIndex_Concurrent(t *testing.T) {
	idx := NewAgentIndex()
	ctx := context.Background()
	var wg sync.WaitGroup

	for i := range 100 {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			_ = idx.Put(ctx, &AgentProfile{AgentURL: "https://agent.com"})
			idx.Get("https://agent.com")
			idx.List()
			if n%3 == 0 {
				_ = idx.Remove(ctx, "https://agent.com")
			}
		}(i)
	}
	wg.Wait()
}
