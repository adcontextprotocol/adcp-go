package registry

import (
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAgentIndex_CRUD(t *testing.T) {
	idx := NewAgentIndex()

	p := &AgentProfile{
		AgentURL:      "https://agent.example.com",
		Channels:      []string{"ctv"},
		Markets:       []string{"US"},
		PropertyCount: 42,
		HasTMP:        true,
	}

	idx.Put(p)

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
	idx.Put(p2)
	assert.Equal(t, 1, idx.Count(), "count after update")
	got, _ = idx.Get("https://agent.example.com")
	assert.Equal(t, 100, got.PropertyCount, "property_count after update")

	// Remove
	assert.True(t, idx.Remove("https://agent.example.com"), "Remove returned false for existing agent")
	assert.False(t, idx.Remove("https://agent.example.com"), "Remove returned true for already-removed agent")
	assert.Equal(t, 0, idx.Count(), "count after remove")
}

func TestAgentIndex_List(t *testing.T) {
	idx := NewAgentIndex()

	idx.Put(&AgentProfile{AgentURL: "https://a.com"})
	idx.Put(&AgentProfile{AgentURL: "https://b.com"})

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
	var wg sync.WaitGroup

	for i := range 100 {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			idx.Put(&AgentProfile{AgentURL: "https://agent.com"})
			idx.Get("https://agent.com")
			idx.List()
			if n%3 == 0 {
				idx.Remove("https://agent.com")
			}
		}(i)
	}
	wg.Wait()
}
