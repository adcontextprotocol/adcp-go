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

// MemoryStore satisfies Store and is exercised end-to-end by the
// indexes' dual-write paths and the syncer's hydration logic. The same
// behaviors are validated against real Valkey/Redis in the
// {glide,redis}store packages' integration tests.

func TestPropertyIndex_DualWrite(t *testing.T) {
	store := NewMemoryStore()
	idx := NewPropertyIndex().WithStore(store)

	p := &Property{PropertyID: "pub1.example.com/home", PropertyRID: 1001, Domain: "example.com"}
	idx.Put(p)

	persisted, err := store.LoadProperties(context.Background())
	require.NoError(t, err)
	require.Len(t, persisted, 1)
	assert.Equal(t, uint64(1001), persisted[0].PropertyRID)

	idx.Remove("pub1.example.com/home")

	persisted, err = store.LoadProperties(context.Background())
	require.NoError(t, err)
	assert.Empty(t, persisted)
}

func TestPropertyIndex_HydrateLoadsFromStore(t *testing.T) {
	store := NewMemoryStore()
	require.NoError(t, store.PutProperty(context.Background(),
		&Property{PropertyID: "pub1", PropertyRID: 1001, Domain: "example.com"}))
	require.NoError(t, store.PutProperty(context.Background(),
		&Property{PropertyID: "pub2", PropertyRID: 1002, Domain: "two.example.com"}))

	idx := NewPropertyIndex().WithStore(store)
	require.NoError(t, idx.Hydrate(context.Background()))

	assert.Equal(t, 2, idx.Count())
	p, ok := idx.LookupByRID(1001)
	require.True(t, ok)
	assert.Equal(t, "pub1", p.PropertyID)

	// Domain side-index was rebuilt locally — the store only persisted
	// the canonical record.
	id, ok := idx.LookupByDomain("example.com")
	require.True(t, ok)
	assert.Equal(t, "pub1", id)
}

func TestPropertyIndex_ClearWipesStore(t *testing.T) {
	store := NewMemoryStore()
	idx := NewPropertyIndex().WithStore(store)
	idx.Put(&Property{PropertyID: "p1", PropertyRID: 1, Domain: "a"})
	idx.Put(&Property{PropertyID: "p2", PropertyRID: 2, Domain: "b"})

	idx.Clear()

	persisted, err := store.LoadProperties(context.Background())
	require.NoError(t, err)
	assert.Empty(t, persisted, "Clear should wipe persistent properties too")
	assert.Equal(t, 0, idx.Count())
}

func TestAuthIndex_DualWriteAndRemove(t *testing.T) {
	store := NewMemoryStore()
	idx := NewAuthIndex().WithStore(store)

	entry := AuthorizationEntry{
		AgentURL: "https://agent.example.com", PublisherDomain: "pub.example.com",
		AuthorizationType: "publisher_properties",
	}
	idx.Add(entry)

	persisted, err := store.LoadAuth(context.Background())
	require.NoError(t, err)
	require.Len(t, persisted, 1)

	idx.RemoveEntry("https://agent.example.com", "pub.example.com")

	persisted, err = store.LoadAuth(context.Background())
	require.NoError(t, err)
	assert.Empty(t, persisted)
}

func TestAuthIndex_RemoveAgentWipesPersistentAgentEntries(t *testing.T) {
	store := NewMemoryStore()
	idx := NewAuthIndex().WithStore(store)

	idx.Add(AuthorizationEntry{AgentURL: "https://a.com", PublisherDomain: "pub1", AuthorizationType: "publisher_properties"})
	idx.Add(AuthorizationEntry{AgentURL: "https://a.com", PublisherDomain: "pub2", AuthorizationType: "publisher_properties"})
	idx.Add(AuthorizationEntry{AgentURL: "https://b.com", PublisherDomain: "pub1", AuthorizationType: "publisher_properties"})

	idx.RemoveAgent("https://a.com")

	persisted, err := store.LoadAuth(context.Background())
	require.NoError(t, err)
	require.Len(t, persisted, 1)
	assert.Equal(t, "https://b.com", persisted[0].AgentURL)
}

func TestAuthIndex_HydrateRestoresReverseIndex(t *testing.T) {
	store := NewMemoryStore()
	require.NoError(t, store.PutAuth(context.Background(), AuthorizationEntry{
		AgentURL: "https://agent.com", PublisherDomain: "pub.com", AuthorizationType: "publisher_properties",
	}))

	idx := NewAuthIndex().WithStore(store)
	require.NoError(t, idx.Hydrate(context.Background()))

	assert.True(t, idx.Check("https://agent.com", "pub.com"))
	agents := idx.GetAuthorizedAgents("pub.com")
	require.Len(t, agents, 1)
	assert.Equal(t, "https://agent.com", agents[0])
}

func TestAgentIndex_DualWrite(t *testing.T) {
	store := NewMemoryStore()
	idx := NewAgentIndex().WithStore(store)

	idx.Put(&AgentProfile{AgentURL: "https://agent.com", PropertyCount: 7})

	persisted, err := store.LoadAgents(context.Background())
	require.NoError(t, err)
	require.Len(t, persisted, 1)
	assert.Equal(t, 7, persisted[0].PropertyCount)

	idx.Remove("https://agent.com")

	persisted, err = store.LoadAgents(context.Background())
	require.NoError(t, err)
	assert.Empty(t, persisted)
}

// TestSyncer_HydratesBeforeFeedLoop covers the failure mode that
// motivated this whole package: a process restart where the persisted
// cursor would otherwise resume against empty indexes. With a Store
// attached, hydration runs before the feed loop so a cursor that points
// past the property's creation event still ends up with the property in
// memory.
func TestSyncer_HydratesBeforeFeedLoop(t *testing.T) {
	store := NewMemoryStore()
	// Pre-seed persisted state and a cursor that points past the events.
	require.NoError(t, store.PutProperty(context.Background(),
		&Property{PropertyID: "pre-seeded", PropertyRID: 999, Domain: "old.example.com"}))
	require.NoError(t, store.PutAgent(context.Background(),
		&AgentProfile{AgentURL: "https://pre-seeded.agent"}))
	require.NoError(t, store.Save(context.Background(), "cursor-past-events"))

	// Feed returns no new events for this cursor.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(FeedResponse{Events: nil, Cursor: strPtr("cursor-past-events"), HasMore: false})
	}))
	defer srv.Close()

	props := NewPropertyIndex().WithStore(store)
	auth := NewAuthIndex().WithStore(store)
	agents := NewAgentIndex().WithStore(store)
	syncer := NewSyncer(NewClient(srv.URL, "test"), props, auth, agents, store, SyncerConfig{PollInterval: 50 * time.Millisecond})

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	go func() { _ = syncer.Run(ctx) }()

	waitFor(t, func() bool { return props.Count() == 1 && agents.Count() == 1 })

	p, ok := props.LookupByRID(999)
	require.True(t, ok, "hydrated property should be looked up by RID")
	assert.Equal(t, "pre-seeded", p.PropertyID)
}

// TestSyncer_CursorExpiredWipesPersistentStore confirms that cursor
// expiry — the path that already calls Clear() on each index — also
// propagates to the persistent backend. Otherwise a restart after
// expiry would hydrate stale data.
func TestSyncer_CursorExpiredWipesPersistentStore(t *testing.T) {
	store := NewMemoryStore()
	// Pre-seed stale state and a doomed cursor.
	require.NoError(t, store.PutProperty(context.Background(),
		&Property{PropertyID: "stale", PropertyRID: 1, Domain: "stale.example.com"}))
	require.NoError(t, store.Save(context.Background(), "old-expired-cursor"))

	var callCount atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := callCount.Add(1)
		if n == 1 {
			w.WriteHeader(http.StatusGone)
			_ = json.NewEncoder(w).Encode(FeedError{Error: "cursor_expired", Message: "expired"})
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(FeedResponse{
			Events: []FeedEvent{
				{EventID: "fresh", EventType: "property.created", EntityType: "property", EntityID: "fresh-prop",
					Payload: mustMarshal(Property{PropertyID: "fresh-prop", PropertyRID: 2, Domain: "fresh.example.com"}), Actor: "test"},
			},
			Cursor: strPtr("fresh"), HasMore: false,
		})
	}))
	defer srv.Close()

	props := NewPropertyIndex().WithStore(store)
	auth := NewAuthIndex().WithStore(store)
	agents := NewAgentIndex().WithStore(store)
	syncer := NewSyncer(NewClient(srv.URL, "test"), props, auth, agents, store, SyncerConfig{PollInterval: 50 * time.Millisecond})

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	go func() { _ = syncer.Run(ctx) }()

	waitFor(t, func() bool {
		_, ok := props.LookupByID("fresh-prop")
		return ok
	})

	persisted, err := store.LoadProperties(context.Background())
	require.NoError(t, err)
	// Only the post-expiry record should remain — the pre-seeded "stale"
	// row was wiped by ClearProperties during cursor-expired handling.
	require.Len(t, persisted, 1)
	assert.Equal(t, "fresh-prop", persisted[0].PropertyID)
}

func TestPropertyIndex_NoStoreIsNoop(t *testing.T) {
	idx := NewPropertyIndex()
	idx.Put(&Property{PropertyID: "p1", PropertyRID: 1, Domain: "a"})
	assert.Equal(t, 1, idx.Count())
	idx.Remove("p1")
	assert.Equal(t, 0, idx.Count())
	// Hydrate without a store is a no-op and must not error.
	require.NoError(t, idx.Hydrate(context.Background()))
}
