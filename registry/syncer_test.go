package registry

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSyncer_AppliesPropertyEvents(t *testing.T) {
	events := []FeedEvent{
		{
			EventID: "019414a0-0000-7000-0000-000000000001", EventType: "property.created",
			EntityType: "property", EntityID: "pub1.example.com/home",
			Payload: mustMarshal(Property{PropertyID: "pub1.example.com/home", PropertyRID: "0190a1b2-c3d4-7e5f-8a9b-000000001001", PropertyType: "website", Domain: "example.com"}),
			Actor: "pipeline:crawler",
		},
		{
			EventID: "019414a0-0000-7000-0000-000000000002", EventType: "property.updated",
			EntityType: "property", EntityID: "pub1.example.com/home",
			Payload: mustMarshal(Property{PropertyID: "pub1.example.com/home", PropertyRID: "0190a1b2-c3d4-7e5f-8a9b-000000001001", PropertyType: "website", Domain: "example.com", Placements: []string{"top"}}),
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
	require.True(t, ok, "property not found")
	assert.Equal(t, "0190a1b2-c3d4-7e5f-8a9b-000000001001", p.PropertyRID)
	require.Len(t, p.Placements, 1)
	assert.Equal(t, "top", p.Placements[0])

	// Reverse lookup: RID → property
	p2, ok := props.LookupByRID("0190a1b2-c3d4-7e5f-8a9b-000000001001")
	require.True(t, ok, "reverse lookup by RID failed")
	assert.Equal(t, "pub1.example.com/home", p2.PropertyID)
}

func TestSyncer_AppliesPropertyRemoved(t *testing.T) {
	events := []feedPage{
		{
			events: []FeedEvent{
				{EventID: "e1", EventType: "property.created", EntityType: "property", EntityID: "prop1",
					Payload: mustMarshal(Property{PropertyID: "prop1", PropertyRID: "100", Domain: "example.com"}), Actor: "test"},
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

	_, ok := props.LookupByRID("100")
	assert.False(t, ok, "removed property should not be in RID index")
}

func TestSyncer_AppliesPropertyMerged(t *testing.T) {
	pages := []feedPage{
		{
			events: []FeedEvent{
				{EventID: "e1", EventType: "property.created", EntityType: "property", EntityID: "alias-prop",
					Payload: mustMarshal(Property{PropertyID: "alias-prop", PropertyRID: "100", Domain: "alias.com"}), Actor: "test"},
				{EventID: "e2", EventType: "property.created", EntityType: "property", EntityID: "canonical-prop",
					Payload: mustMarshal(Property{PropertyID: "canonical-prop", PropertyRID: "200", Domain: "canonical.com"}), Actor: "test"},
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
	_, ok := props.LookupByID("alias-prop")
	assert.False(t, ok, "alias property should be removed after merge")
	_, ok = props.LookupByRID("100")
	assert.False(t, ok, "alias RID should be removed after merge")

	// Canonical should remain
	_, ok = props.LookupByID("canonical-prop")
	assert.True(t, ok, "canonical property should still exist")
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

	assert.True(t, auth.Check("https://agent.com", "example.com"), "agent should be authorized")
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
	require.True(t, ok, "agent not found")
	assert.Equal(t, 10, p.PropertyCount)
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

	assert.False(t, auth.Check("https://agent.com", "pub.com"), "auth should be cleaned up when agent is removed")
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
	_ = agents.Put(context.Background(), &AgentProfile{AgentURL: "https://stale.com"})

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
	_, ok := agents.Get("https://stale.com")
	assert.False(t, ok, "stale agent should be cleared on cursor-expired re-bootstrap")
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

	assert.GreaterOrEqual(t, callCount.Load(), int32(2), "should have made at least 2 calls to drain has_more")
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
