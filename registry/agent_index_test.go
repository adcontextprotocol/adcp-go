package registry

import (
	"sync"
	"testing"
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

	if idx.Count() != 1 {
		t.Fatalf("count = %d, want 1", idx.Count())
	}

	got, ok := idx.Get("https://agent.example.com")
	if !ok {
		t.Fatal("Get: not found")
	}
	if got.PropertyCount != 42 {
		t.Errorf("property_count = %d", got.PropertyCount)
	}

	// Update
	p2 := &AgentProfile{
		AgentURL:      "https://agent.example.com",
		Channels:      []string{"ctv", "display"},
		PropertyCount: 100,
	}
	idx.Put(p2)
	if idx.Count() != 1 {
		t.Errorf("count after update = %d, want 1", idx.Count())
	}
	got, _ = idx.Get("https://agent.example.com")
	if got.PropertyCount != 100 {
		t.Errorf("property_count after update = %d", got.PropertyCount)
	}

	// Remove
	if !idx.Remove("https://agent.example.com") {
		t.Error("Remove returned false for existing agent")
	}
	if idx.Remove("https://agent.example.com") {
		t.Error("Remove returned true for already-removed agent")
	}
	if idx.Count() != 0 {
		t.Errorf("count after remove = %d", idx.Count())
	}
}

func TestAgentIndex_List(t *testing.T) {
	idx := NewAgentIndex()

	idx.Put(&AgentProfile{AgentURL: "https://a.com"})
	idx.Put(&AgentProfile{AgentURL: "https://b.com"})

	list := idx.List()
	if len(list) != 2 {
		t.Fatalf("list = %d, want 2", len(list))
	}
}

func TestAgentIndex_GetMissing(t *testing.T) {
	idx := NewAgentIndex()
	_, ok := idx.Get("https://nonexistent.com")
	if ok {
		t.Error("should not find nonexistent agent")
	}
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
