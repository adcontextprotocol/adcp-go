package registry

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func TestSyncer_AppliesPropertyEvents(t *testing.T) {
	events := []FeedEvent{
		{
			EventID: "019414a0-0000-7000-0000-000000000001", EventType: "property.created",
			EntityType: "property", EntityID: "pub1.example.com/home",
			Payload: mustMarshal(Property{PropertyID: "pub1.example.com/home", PropertyRID: 1001, PropertyType: "website", Domain: "example.com"}),
			Actor: "pipeline:crawler",
		},
		{
			EventID: "019414a0-0000-7000-0000-000000000002", EventType: "property.updated",
			EntityType: "property", EntityID: "pub1.example.com/home",
			Payload: mustMarshal(Property{PropertyID: "pub1.example.com/home", PropertyRID: 1001, PropertyType: "website", Domain: "example.com", Placements: []string{"top"}}),
			Actor: "pipeline:crawler",
		},
	}

	cursor := "019414a0-0000-7000-0000-000000000002"
	srv := feedServer(t, []feedPage{
		{events: events, cursor: &cursor, hasMore: false},
	})
	defer srv.Close()

	props := NewPropertyIndex()
	syncer := newTestSyncer(srv.URL, props, NewAuthIndex(), NewAgentIndex())

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	go func() { _ = syncer.Run(ctx) }()

	waitFor(t, func() bool { return props.Count() == 1 })

	p, ok := props.LookupByID("pub1.example.com/home")
	if !ok {
		t.Fatal("property not found")
	}
	if p.PropertyRID != 1001 {
		t.Errorf("rid = %d", p.PropertyRID)
	}
	if len(p.Placements) != 1 || p.Placements[0] != "top" {
		t.Errorf("placements = %v", p.Placements)
	}

	// Reverse lookup: RID → property
	p2, ok := props.LookupByRID(1001)
	if !ok || p2.PropertyID != "pub1.example.com/home" {
		t.Error("reverse lookup by RID failed")
	}
}

func TestSyncer_AppliesPropertyRemoved(t *testing.T) {
	events := []feedPage{
		{
			events: []FeedEvent{
				{EventID: "e1", EventType: "property.created", EntityType: "property", EntityID: "prop1",
					Payload: mustMarshal(Property{PropertyID: "prop1", PropertyRID: 100, Domain: "example.com"}), Actor: "test"},
			},
			cursor: strPtr("e1"), hasMore: true,
		},
		{
			events: []FeedEvent{
				{EventID: "e2", EventType: "property.removed", EntityType: "property", EntityID: "prop1",
					Payload: mustMarshal(map[string]string{"property_id": "prop1"}), Actor: "test"},
			},
			cursor: strPtr("e2"), hasMore: false,
		},
	}

	srv := feedServer(t, events)
	defer srv.Close()

	props := NewPropertyIndex()
	syncer := newTestSyncer(srv.URL, props, NewAuthIndex(), NewAgentIndex())

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	go func() { _ = syncer.Run(ctx) }()

	waitFor(t, func() bool { return props.Count() == 0 })

	if _, ok := props.LookupByRID(100); ok {
		t.Error("removed property should not be in RID index")
	}
}

func TestSyncer_AppliesPropertyMerged(t *testing.T) {
	pages := []feedPage{
		{
			events: []FeedEvent{
				{EventID: "e1", EventType: "property.created", EntityType: "property", EntityID: "alias-prop",
					Payload: mustMarshal(Property{PropertyID: "alias-prop", PropertyRID: 100, Domain: "alias.com"}), Actor: "test"},
				{EventID: "e2", EventType: "property.created", EntityType: "property", EntityID: "canonical-prop",
					Payload: mustMarshal(Property{PropertyID: "canonical-prop", PropertyRID: 200, Domain: "canonical.com"}), Actor: "test"},
			},
			cursor: strPtr("e2"), hasMore: true,
		},
		{
			events: []FeedEvent{
				{EventID: "e3", EventType: "property.merged", EntityType: "property", EntityID: "alias-prop",
					Payload: mustMarshal(map[string]string{"alias_rid": "100", "canonical_rid": "200"}), Actor: "test"},
			},
			cursor: strPtr("e3"), hasMore: false,
		},
	}

	srv := feedServer(t, pages)
	defer srv.Close()

	props := NewPropertyIndex()
	syncer := newTestSyncer(srv.URL, props, NewAuthIndex(), NewAgentIndex())

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	go func() { _ = syncer.Run(ctx) }()

	waitFor(t, func() bool { return props.Count() == 1 })

	// Alias should be removed
	if _, ok := props.LookupByID("alias-prop"); ok {
		t.Error("alias property should be removed after merge")
	}
	if _, ok := props.LookupByRID(100); ok {
		t.Error("alias RID should be removed after merge")
	}

	// Canonical should remain
	if _, ok := props.LookupByID("canonical-prop"); !ok {
		t.Error("canonical property should still exist")
	}
}

func TestSyncer_AppliesAuthorizationEvents(t *testing.T) {
	events := []FeedEvent{
		{
			EventID: "e1", EventType: "authorization.granted", EntityType: "authorization",
			EntityID: "https://agent.com:example.com",
			Payload: mustMarshal(AuthorizationEntry{
				AgentURL: "https://agent.com", PublisherDomain: "example.com",
				AuthorizationType: "publisher_properties",
			}),
			Actor: "pipeline:crawler",
		},
	}

	cursor := "e1"
	srv := feedServer(t, []feedPage{{events: events, cursor: &cursor, hasMore: false}})
	defer srv.Close()

	auth := NewAuthIndex()
	syncer := newTestSyncer(srv.URL, NewPropertyIndex(), auth, NewAgentIndex())

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	go func() { _ = syncer.Run(ctx) }()

	waitFor(t, func() bool { return auth.Count() > 0 })

	if !auth.Check("https://agent.com", "example.com") {
		t.Error("agent should be authorized")
	}
}

func TestSyncer_AppliesAuthorizationRevoked(t *testing.T) {
	pages := []feedPage{
		{
			events: []FeedEvent{
				{EventID: "e1", EventType: "authorization.granted", EntityType: "authorization", EntityID: "a:d",
					Payload: mustMarshal(AuthorizationEntry{AgentURL: "https://agent.com", PublisherDomain: "pub.com", AuthorizationType: "publisher_properties"}),
					Actor: "test"},
			},
			cursor: strPtr("e1"), hasMore: true,
		},
		{
			events: []FeedEvent{
				{EventID: "e2", EventType: "authorization.revoked", EntityType: "authorization", EntityID: "a:d",
					Payload: mustMarshal(map[string]string{"agent_url": "https://agent.com", "publisher_domain": "pub.com"}),
					Actor: "test"},
			},
			cursor: strPtr("e2"), hasMore: false,
		},
	}

	srv := feedServer(t, pages)
	defer srv.Close()

	auth := NewAuthIndex()
	syncer := newTestSyncer(srv.URL, NewPropertyIndex(), auth, NewAgentIndex())

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	go func() { _ = syncer.Run(ctx) }()

	// First observe the grant (page 1 has hasMore=true, so page 2 follows immediately)
	waitFor(t, func() bool { return auth.Check("https://agent.com", "pub.com") })

	// Then observe the revocation
	waitFor(t, func() bool { return !auth.Check("https://agent.com", "pub.com") })
}

func TestSyncer_AppliesAgentEvents(t *testing.T) {
	events := []FeedEvent{
		{
			EventID: "e1", EventType: "agent.discovered", EntityType: "agent",
			EntityID: "https://agent.example.com",
			Payload:  mustMarshal(AgentProfile{AgentURL: "https://agent.example.com", Channels: []string{"ctv"}, PropertyCount: 10}),
			Actor:    "pipeline:crawler",
		},
	}

	cursor := "e1"
	srv := feedServer(t, []feedPage{{events: events, cursor: &cursor, hasMore: false}})
	defer srv.Close()

	agents := NewAgentIndex()
	syncer := newTestSyncer(srv.URL, NewPropertyIndex(), NewAuthIndex(), agents)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	go func() { _ = syncer.Run(ctx) }()

	waitFor(t, func() bool { return agents.Count() > 0 })

	p, ok := agents.Get("https://agent.example.com")
	if !ok {
		t.Fatal("agent not found")
	}
	if p.PropertyCount != 10 {
		t.Errorf("property_count = %d", p.PropertyCount)
	}
}

func TestSyncer_AgentRemovedCleansAuth(t *testing.T) {
	pages := []feedPage{
		{
			events: []FeedEvent{
				{EventID: "e1", EventType: "agent.discovered", EntityType: "agent", EntityID: "https://agent.com",
					Payload: mustMarshal(AgentProfile{AgentURL: "https://agent.com"}), Actor: "test"},
				{EventID: "e2", EventType: "authorization.granted", EntityType: "authorization", EntityID: "a:d",
					Payload: mustMarshal(AuthorizationEntry{AgentURL: "https://agent.com", PublisherDomain: "pub.com", AuthorizationType: "publisher_properties"}),
					Actor: "test"},
			},
			cursor: strPtr("e2"), hasMore: true,
		},
		{
			events: []FeedEvent{
				{EventID: "e3", EventType: "agent.removed", EntityType: "agent", EntityID: "https://agent.com",
					Payload: mustMarshal(map[string]string{"agent_url": "https://agent.com"}), Actor: "test"},
			},
			cursor: strPtr("e3"), hasMore: false,
		},
	}

	srv := feedServer(t, pages)
	defer srv.Close()

	agents := NewAgentIndex()
	auth := NewAuthIndex()
	syncer := newTestSyncer(srv.URL, NewPropertyIndex(), auth, agents)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	go func() { _ = syncer.Run(ctx) }()

	waitFor(t, func() bool { return agents.Count() == 0 })

	if auth.Check("https://agent.com", "pub.com") {
		t.Error("auth should be cleaned up when agent is removed")
	}
}

func TestSyncer_CursorPersisted(t *testing.T) {
	cursor := "e1"
	srv := feedServer(t, []feedPage{
		{events: []FeedEvent{{EventID: "e1", EventType: "agent.discovered", EntityType: "agent", EntityID: "a",
			Payload: mustMarshal(AgentProfile{AgentURL: "a"}), Actor: "test"}},
			cursor: &cursor, hasMore: false},
	})
	defer srv.Close()

	store := &MemoryCursorStore{}
	syncer := NewSyncer(
		NewClient(srv.URL, "test"),
		NewPropertyIndex(), NewAuthIndex(), NewAgentIndex(),
		store, SyncerConfig{PollInterval: 50 * time.Millisecond},
	)

	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	go func() { _ = syncer.Run(ctx) }()

	waitFor(t, func() bool {
		c, _ := store.Load(context.Background())
		return c == "e1"
	})
}

func TestSyncer_CursorExpiredClearsAndRebootstraps(t *testing.T) {
	var callCount atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := callCount.Add(1)
		if n == 1 {
			// First call with old cursor: return 410
			w.WriteHeader(http.StatusGone)
			_ = json.NewEncoder(w).Encode(FeedError{Error: "cursor_expired", Message: "expired"})
			return
		}
		// Second call without cursor: return only the fresh agent
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(FeedResponse{
			Events: []FeedEvent{
				{EventID: "fresh-1", EventType: "agent.discovered", EntityType: "agent", EntityID: "https://fresh.com",
					Payload: mustMarshal(AgentProfile{AgentURL: "https://fresh.com"}), Actor: "test"},
			},
			Cursor:  strPtr("fresh-1"),
			HasMore: false,
		})
	}))
	defer srv.Close()

	agents := NewAgentIndex()
	// Pre-populate with stale data that should be cleared on re-bootstrap
	agents.Put(&AgentProfile{AgentURL: "https://stale.com"})

	store := &MemoryCursorStore{cursor: "old-expired-cursor"}

	syncer := NewSyncer(
		NewClient(srv.URL, "test"),
		NewPropertyIndex(), NewAuthIndex(), agents,
		store, SyncerConfig{PollInterval: 50 * time.Millisecond},
	)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	go func() { _ = syncer.Run(ctx) }()

	waitFor(t, func() bool {
		_, hasFresh := agents.Get("https://fresh.com")
		return hasFresh
	})

	// Stale data should have been cleared
	if _, ok := agents.Get("https://stale.com"); ok {
		t.Error("stale agent should be cleared on cursor-expired re-bootstrap")
	}
}

func TestSyncer_HasMoreDrainsImmediately(t *testing.T) {
	var callCount atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := callCount.Add(1)
		w.Header().Set("Content-Type", "application/json")
		if n == 1 {
			_ = json.NewEncoder(w).Encode(FeedResponse{
				Events:  []FeedEvent{{EventID: "e1", EventType: "agent.discovered", EntityType: "agent", EntityID: "a1", Payload: mustMarshal(AgentProfile{AgentURL: "a1"}), Actor: "test"}},
				Cursor:  strPtr("e1"),
				HasMore: true,
			})
		} else {
			_ = json.NewEncoder(w).Encode(FeedResponse{
				Events:  []FeedEvent{{EventID: "e2", EventType: "agent.discovered", EntityType: "agent", EntityID: "a2", Payload: mustMarshal(AgentProfile{AgentURL: "a2"}), Actor: "test"}},
				Cursor:  strPtr("e2"),
				HasMore: false,
			})
		}
	}))
	defer srv.Close()

	agents := NewAgentIndex()
	syncer := newTestSyncer(srv.URL, NewPropertyIndex(), NewAuthIndex(), agents)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	go func() { _ = syncer.Run(ctx) }()

	waitFor(t, func() bool { return agents.Count() >= 2 })

	if callCount.Load() < 2 {
		t.Error("should have made at least 2 calls to drain has_more")
	}
}

// --- Helpers ---

type feedPage struct {
	events  []FeedEvent
	cursor  *string
	hasMore bool
}

func feedServer(t *testing.T, pages []feedPage) *httptest.Server {
	t.Helper()
	var callCount atomic.Int32
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		idx := int(callCount.Add(1)) - 1
		if idx >= len(pages) {
			// After all pages consumed, return empty
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(FeedResponse{Events: []FeedEvent{}, HasMore: false})
			return
		}
		page := pages[idx]
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(FeedResponse{
			Events:  page.events,
			Cursor:  page.cursor,
			HasMore: page.hasMore,
		})
	}))
}

func newTestSyncer(baseURL string, props *PropertyIndex, auth *AuthIndex, agents *AgentIndex) *Syncer {
	return NewSyncer(
		NewClient(baseURL, "test"),
		props, auth, agents,
		&MemoryCursorStore{},
		SyncerConfig{PollInterval: 50 * time.Millisecond},
	)
}

func mustMarshal(v any) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return b
}

func waitFor(t *testing.T, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("condition not met within timeout")
}
