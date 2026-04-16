package registry

import (
	"sort"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAuthIndex_AddAndCheck(t *testing.T) {
	idx := NewAuthIndex()

	idx.Add(AuthorizationEntry{
		AgentURL:          "https://agent1.example.com",
		PublisherDomain:   "pub.com",
		AuthorizationType: "publisher_properties",
	})

	assert.True(t, idx.Check("https://agent1.example.com", "pub.com"), "should be authorized")
	assert.False(t, idx.Check("https://agent1.example.com", "other.com"), "should not be authorized for other domain")
	assert.False(t, idx.Check("https://unknown.com", "pub.com"), "unknown agent should not be authorized")
	assert.Equal(t, 1, idx.Count())
}

func TestAuthIndex_MultipleEntries(t *testing.T) {
	idx := NewAuthIndex()

	idx.Add(AuthorizationEntry{
		AgentURL:          "https://agent1.example.com",
		PublisherDomain:   "pub.com",
		AuthorizationType: "property_ids",
		PropertyIDs:       []string{"prop-a"},
	})
	idx.Add(AuthorizationEntry{
		AgentURL:          "https://agent1.example.com",
		PublisherDomain:   "pub.com",
		AuthorizationType: "publisher_properties",
	})

	entries := idx.GetEntries("https://agent1.example.com", "pub.com")
	require.Len(t, entries, 2)
	assert.Equal(t, 2, idx.Count())
}

func TestAuthIndex_DeduplicatesByType(t *testing.T) {
	idx := NewAuthIndex()

	idx.Add(AuthorizationEntry{
		AgentURL:          "https://agent1.example.com",
		PublisherDomain:   "pub.com",
		AuthorizationType: "property_ids",
		PropertyIDs:       []string{"prop-a"},
	})
	// Same agent+domain+type: should replace, not append
	idx.Add(AuthorizationEntry{
		AgentURL:          "https://agent1.example.com",
		PublisherDomain:   "pub.com",
		AuthorizationType: "property_ids",
		PropertyIDs:       []string{"prop-b"},
	})

	entries := idx.GetEntries("https://agent1.example.com", "pub.com")
	require.Len(t, entries, 1, "dedup by type")
	require.Len(t, entries[0].PropertyIDs, 1)
	assert.Equal(t, "prop-b", entries[0].PropertyIDs[0])
	assert.Equal(t, 1, idx.Count())
}

func TestAuthIndex_ReverseIndex(t *testing.T) {
	idx := NewAuthIndex()

	idx.Add(AuthorizationEntry{AgentURL: "https://agent1.com", PublisherDomain: "pub.com", AuthorizationType: "publisher_properties"})
	idx.Add(AuthorizationEntry{AgentURL: "https://agent2.com", PublisherDomain: "pub.com", AuthorizationType: "publisher_properties"})
	idx.Add(AuthorizationEntry{AgentURL: "https://agent1.com", PublisherDomain: "other.com", AuthorizationType: "publisher_properties"})

	agents := idx.GetAuthorizedAgents("pub.com")
	sort.Strings(agents)
	require.Len(t, agents, 2)
	assert.Equal(t, "https://agent1.com", agents[0])
	assert.Equal(t, "https://agent2.com", agents[1])

	agents = idx.GetAuthorizedAgents("other.com")
	require.Len(t, agents, 1)
	assert.Equal(t, "https://agent1.com", agents[0])

	assert.Nil(t, idx.GetAuthorizedAgents("unknown.com"), "unknown domain should return nil")
}

func TestAuthIndex_RemoveEntry(t *testing.T) {
	idx := NewAuthIndex()

	idx.Add(AuthorizationEntry{AgentURL: "https://agent1.com", PublisherDomain: "pub.com", AuthorizationType: "publisher_properties"})
	idx.Add(AuthorizationEntry{AgentURL: "https://agent1.com", PublisherDomain: "other.com", AuthorizationType: "publisher_properties"})

	idx.RemoveEntry("https://agent1.com", "pub.com")

	assert.False(t, idx.Check("https://agent1.com", "pub.com"), "should no longer be authorized for pub.com")
	assert.True(t, idx.Check("https://agent1.com", "other.com"), "should still be authorized for other.com")
	assert.Nil(t, idx.GetAuthorizedAgents("pub.com"), "reverse index for pub.com should be empty")
}

func TestAuthIndex_RemoveAgent(t *testing.T) {
	idx := NewAuthIndex()

	idx.Add(AuthorizationEntry{AgentURL: "https://agent1.com", PublisherDomain: "pub1.com", AuthorizationType: "publisher_properties"})
	idx.Add(AuthorizationEntry{AgentURL: "https://agent1.com", PublisherDomain: "pub2.com", AuthorizationType: "publisher_properties"})
	idx.Add(AuthorizationEntry{AgentURL: "https://agent2.com", PublisherDomain: "pub1.com", AuthorizationType: "publisher_properties"})

	idx.RemoveAgent("https://agent1.com")

	assert.False(t, idx.Check("https://agent1.com", "pub1.com"), "agent1 should be fully removed")
	assert.False(t, idx.Check("https://agent1.com", "pub2.com"), "agent1 should be fully removed")

	// agent2 should be unaffected
	assert.True(t, idx.Check("https://agent2.com", "pub1.com"), "agent2 should still be authorized")

	// Reverse index should be updated
	agents := idx.GetAuthorizedAgents("pub1.com")
	require.Len(t, agents, 1)
	assert.Equal(t, "https://agent2.com", agents[0])
	assert.Nil(t, idx.GetAuthorizedAgents("pub2.com"), "pub2.com should be empty")
}

func TestAuthIndex_GetEntries_ReturnsCopy(t *testing.T) {
	idx := NewAuthIndex()

	idx.Add(AuthorizationEntry{AgentURL: "https://agent1.com", PublisherDomain: "pub.com", AuthorizationType: "publisher_properties"})

	entries := idx.GetEntries("https://agent1.com", "pub.com")
	entries[0].AuthorizationType = "mutated"

	// Original should be unchanged
	original := idx.GetEntries("https://agent1.com", "pub.com")
	assert.Equal(t, "publisher_properties", original[0].AuthorizationType, "GetEntries should return a copy")
}

func TestAuthIndex_Concurrent(t *testing.T) {
	idx := NewAuthIndex()
	var wg sync.WaitGroup

	for i := range 50 {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			idx.Add(AuthorizationEntry{
				AgentURL:          "https://agent.com",
				PublisherDomain:   "pub.com",
				AuthorizationType: "publisher_properties",
			})
			idx.Check("https://agent.com", "pub.com")
			idx.GetAuthorizedAgents("pub.com")
			if n%3 == 0 {
				idx.RemoveEntry("https://agent.com", "pub.com")
			}
		}(i)
	}

	wg.Wait()
}
