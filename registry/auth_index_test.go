package registry

import (
	"sort"
	"sync"
	"testing"
)

func TestAuthIndex_AddAndCheck(t *testing.T) {
	idx := NewAuthIndex()

	idx.Add(AuthorizationEntry{
		AgentURL:          "https://agent1.example.com",
		PublisherDomain:   "pub.com",
		AuthorizationType: "publisher_properties",
	})

	if !idx.Check("https://agent1.example.com", "pub.com") {
		t.Error("should be authorized")
	}
	if idx.Check("https://agent1.example.com", "other.com") {
		t.Error("should not be authorized for other domain")
	}
	if idx.Check("https://unknown.com", "pub.com") {
		t.Error("unknown agent should not be authorized")
	}
	if idx.Count() != 1 {
		t.Errorf("count = %d, want 1", idx.Count())
	}
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
	if len(entries) != 2 {
		t.Fatalf("entries = %d, want 2", len(entries))
	}
	if idx.Count() != 2 {
		t.Errorf("count = %d, want 2", idx.Count())
	}
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
	if len(entries) != 1 {
		t.Fatalf("entries = %d, want 1 (dedup by type)", len(entries))
	}
	if len(entries[0].PropertyIDs) != 1 || entries[0].PropertyIDs[0] != "prop-b" {
		t.Errorf("expected replaced entry with prop-b, got %v", entries[0].PropertyIDs)
	}
	if idx.Count() != 1 {
		t.Errorf("count = %d, want 1", idx.Count())
	}
}

func TestAuthIndex_ReverseIndex(t *testing.T) {
	idx := NewAuthIndex()

	idx.Add(AuthorizationEntry{AgentURL: "https://agent1.com", PublisherDomain: "pub.com", AuthorizationType: "publisher_properties"})
	idx.Add(AuthorizationEntry{AgentURL: "https://agent2.com", PublisherDomain: "pub.com", AuthorizationType: "publisher_properties"})
	idx.Add(AuthorizationEntry{AgentURL: "https://agent1.com", PublisherDomain: "other.com", AuthorizationType: "publisher_properties"})

	agents := idx.GetAuthorizedAgents("pub.com")
	sort.Strings(agents)
	if len(agents) != 2 {
		t.Fatalf("agents = %d, want 2", len(agents))
	}
	if agents[0] != "https://agent1.com" || agents[1] != "https://agent2.com" {
		t.Errorf("agents = %v", agents)
	}

	agents = idx.GetAuthorizedAgents("other.com")
	if len(agents) != 1 || agents[0] != "https://agent1.com" {
		t.Errorf("other.com agents = %v", agents)
	}

	if agents := idx.GetAuthorizedAgents("unknown.com"); agents != nil {
		t.Errorf("unknown domain should return nil, got %v", agents)
	}
}

func TestAuthIndex_RemoveEntry(t *testing.T) {
	idx := NewAuthIndex()

	idx.Add(AuthorizationEntry{AgentURL: "https://agent1.com", PublisherDomain: "pub.com", AuthorizationType: "publisher_properties"})
	idx.Add(AuthorizationEntry{AgentURL: "https://agent1.com", PublisherDomain: "other.com", AuthorizationType: "publisher_properties"})

	idx.RemoveEntry("https://agent1.com", "pub.com")

	if idx.Check("https://agent1.com", "pub.com") {
		t.Error("should no longer be authorized for pub.com")
	}
	if !idx.Check("https://agent1.com", "other.com") {
		t.Error("should still be authorized for other.com")
	}
	if agents := idx.GetAuthorizedAgents("pub.com"); agents != nil {
		t.Errorf("reverse index for pub.com should be empty, got %v", agents)
	}
}

func TestAuthIndex_RemoveAgent(t *testing.T) {
	idx := NewAuthIndex()

	idx.Add(AuthorizationEntry{AgentURL: "https://agent1.com", PublisherDomain: "pub1.com", AuthorizationType: "publisher_properties"})
	idx.Add(AuthorizationEntry{AgentURL: "https://agent1.com", PublisherDomain: "pub2.com", AuthorizationType: "publisher_properties"})
	idx.Add(AuthorizationEntry{AgentURL: "https://agent2.com", PublisherDomain: "pub1.com", AuthorizationType: "publisher_properties"})

	idx.RemoveAgent("https://agent1.com")

	if idx.Check("https://agent1.com", "pub1.com") {
		t.Error("agent1 should be fully removed")
	}
	if idx.Check("https://agent1.com", "pub2.com") {
		t.Error("agent1 should be fully removed")
	}

	// agent2 should be unaffected
	if !idx.Check("https://agent2.com", "pub1.com") {
		t.Error("agent2 should still be authorized")
	}

	// Reverse index should be updated
	agents := idx.GetAuthorizedAgents("pub1.com")
	if len(agents) != 1 || agents[0] != "https://agent2.com" {
		t.Errorf("pub1.com agents = %v", agents)
	}
	if agents := idx.GetAuthorizedAgents("pub2.com"); agents != nil {
		t.Errorf("pub2.com should be empty, got %v", agents)
	}
}

func TestAuthIndex_GetEntries_ReturnsCopy(t *testing.T) {
	idx := NewAuthIndex()

	idx.Add(AuthorizationEntry{AgentURL: "https://agent1.com", PublisherDomain: "pub.com", AuthorizationType: "publisher_properties"})

	entries := idx.GetEntries("https://agent1.com", "pub.com")
	entries[0].AuthorizationType = "mutated"

	// Original should be unchanged
	original := idx.GetEntries("https://agent1.com", "pub.com")
	if original[0].AuthorizationType != "publisher_properties" {
		t.Error("GetEntries should return a copy")
	}
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
