package registry

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// MemoryStore is exercised end-to-end here as a stand-in for real
// backends; the {glide,redis}store packages run the same shapes against
// a Valkey 9 testcontainer.

func TestPropertyIndex_DualWrite(t *testing.T) {
	store := NewMemoryStore()
	idx := NewPropertyIndex().WithStore(store)
	require.NoError(t, idx.Hydrate(context.Background()))

	p := &Property{PropertyID: "pub1.example.com/home", PropertyRID: 1001, Domain: "example.com"}
	require.NoError(t, idx.Put(context.Background(), p))

	persisted, err := store.LoadProperties(context.Background())
	require.NoError(t, err)
	require.Len(t, persisted, 1)
	assert.Equal(t, uint64(1001), persisted[0].PropertyRID)

	require.NoError(t, idx.Remove(context.Background(), "pub1.example.com/home"))

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

	id, ok := idx.LookupByDomain("example.com")
	require.True(t, ok)
	assert.Equal(t, "pub1", id)
}

func TestPropertyIndex_HydrateIsIdempotent(t *testing.T) {
	store := NewMemoryStore()
	require.NoError(t, store.PutProperty(context.Background(),
		&Property{PropertyID: "p1", PropertyRID: 1, Domain: "example.com"}))

	idx := NewPropertyIndex().WithStore(store)
	require.NoError(t, idx.Hydrate(context.Background()))
	// Mutating the store after first Hydrate must not affect a second
	// Hydrate call.
	require.NoError(t, store.PutProperty(context.Background(),
		&Property{PropertyID: "p2", PropertyRID: 2, Domain: "two.example.com"}))
	require.NoError(t, idx.Hydrate(context.Background()))

	assert.Equal(t, 1, idx.Count(), "second Hydrate must be a no-op")
}

func TestPropertyIndex_ClearWipesStore(t *testing.T) {
	store := NewMemoryStore()
	idx := NewPropertyIndex().WithStore(store)
	require.NoError(t, idx.Hydrate(context.Background()))
	require.NoError(t, idx.Put(context.Background(), &Property{PropertyID: "p1", PropertyRID: 1, Domain: "a"}))
	require.NoError(t, idx.Put(context.Background(), &Property{PropertyID: "p2", PropertyRID: 2, Domain: "b"}))

	require.NoError(t, idx.Clear(context.Background()))

	persisted, err := store.LoadProperties(context.Background())
	require.NoError(t, err)
	assert.Empty(t, persisted, "Clear should wipe persistent properties too")
	assert.Equal(t, 0, idx.Count())
}

func TestAuthIndex_DualWriteAndRemove(t *testing.T) {
	store := NewMemoryStore()
	idx := NewAuthIndex().WithStore(store)
	require.NoError(t, idx.Hydrate(context.Background()))

	entry := AuthorizationEntry{
		AgentURL: "https://agent.example.com", PublisherDomain: "pub.example.com",
		AuthorizationType: "publisher_properties",
	}
	require.NoError(t, idx.Add(context.Background(), entry))

	persisted, err := store.LoadAuth(context.Background())
	require.NoError(t, err)
	require.Len(t, persisted, 1)

	require.NoError(t, idx.RemoveEntry(context.Background(), "https://agent.example.com", "pub.example.com"))

	persisted, err = store.LoadAuth(context.Background())
	require.NoError(t, err)
	assert.Empty(t, persisted)
}

func TestAuthIndex_RemoveAgentWipesPersistentAgentEntries(t *testing.T) {
	store := NewMemoryStore()
	idx := NewAuthIndex().WithStore(store)
	require.NoError(t, idx.Hydrate(context.Background()))

	require.NoError(t, idx.Add(context.Background(), AuthorizationEntry{AgentURL: "https://a.com", PublisherDomain: "pub1", AuthorizationType: "publisher_properties"}))
	require.NoError(t, idx.Add(context.Background(), AuthorizationEntry{AgentURL: "https://a.com", PublisherDomain: "pub2", AuthorizationType: "publisher_properties"}))
	require.NoError(t, idx.Add(context.Background(), AuthorizationEntry{AgentURL: "https://b.com", PublisherDomain: "pub1", AuthorizationType: "publisher_properties"}))

	require.NoError(t, idx.RemoveAgent(context.Background(), "https://a.com"))

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
	require.NoError(t, idx.Hydrate(context.Background()))

	require.NoError(t, idx.Put(context.Background(), &AgentProfile{AgentURL: "https://agent.com", PropertyCount: 7}))

	persisted, err := store.LoadAgents(context.Background())
	require.NoError(t, err)
	require.Len(t, persisted, 1)
	assert.Equal(t, 7, persisted[0].PropertyCount)

	require.NoError(t, idx.Remove(context.Background(), "https://agent.com"))

	persisted, err = store.LoadAgents(context.Background())
	require.NoError(t, err)
	assert.Empty(t, persisted)
}

// TestSyncer_HydratesBeforeFeedLoop covers the failure mode that
// motivated this whole package: a process restart where the persisted
// cursor would otherwise resume against empty indexes.
func TestSyncer_HydratesBeforeFeedLoop(t *testing.T) {
	store := NewMemoryStore()
	require.NoError(t, store.PutProperty(context.Background(),
		&Property{PropertyID: "pre-seeded", PropertyRID: 999, Domain: "old.example.com"}))
	require.NoError(t, store.PutAgent(context.Background(),
		&AgentProfile{AgentURL: "https://pre-seeded.agent"}))
	require.NoError(t, store.Save(context.Background(), "cursor-past-events"))

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
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
// propagates to the persistent backend.
func TestSyncer_CursorExpiredWipesPersistentStore(t *testing.T) {
	store := NewMemoryStore()
	require.NoError(t, store.PutProperty(context.Background(),
		&Property{PropertyID: "stale", PropertyRID: 1, Domain: "stale.example.com"}))
	require.NoError(t, store.Save(context.Background(), "old-expired-cursor"))

	var callCount atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
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
	require.Len(t, persisted, 1)
	assert.Equal(t, "fresh-prop", persisted[0].PropertyID)
}

func TestPropertyIndex_NoStoreIsNoop(t *testing.T) {
	idx := NewPropertyIndex()
	require.NoError(t, idx.Put(context.Background(), &Property{PropertyID: "p1", PropertyRID: 1, Domain: "a"}))
	assert.Equal(t, 1, idx.Count())
	require.NoError(t, idx.Remove(context.Background(), "p1"))
	assert.Equal(t, 0, idx.Count())
	require.NoError(t, idx.Hydrate(context.Background()))
}

// failingStore wraps MemoryStore and toggles failure for specific
// operations so the cursor-gating behaviour can be observed.
type failingStore struct {
	*MemoryStore
	failPutProperty   atomic.Bool
	failClearProperty atomic.Bool
}

func newFailingStore() *failingStore {
	return &failingStore{MemoryStore: NewMemoryStore()}
}

var errInjected = errors.New("injected persist failure")

func (f *failingStore) PutProperty(ctx context.Context, p *Property) error {
	if f.failPutProperty.Load() {
		return errInjected
	}
	return f.MemoryStore.PutProperty(ctx, p)
}

func (f *failingStore) ClearProperties(ctx context.Context) error {
	if f.failClearProperty.Load() {
		return errInjected
	}
	return f.MemoryStore.ClearProperties(ctx)
}

// TestSyncer_PersistFailureBlocksCursor verifies that a transient
// persistent-store error during event apply keeps the saved cursor at
// its old value, so the next FetchFeed re-delivers the unpersisted
// events.
func TestSyncer_PersistFailureBlocksCursor(t *testing.T) {
	store := newFailingStore()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(FeedResponse{
			Events: []FeedEvent{
				{EventID: "e1", EventType: "property.created", EntityType: "property", EntityID: "p1",
					Payload: mustMarshal(Property{PropertyID: "p1", PropertyRID: 1, Domain: "example.com"}), Actor: "test"},
			},
			Cursor: strPtr("e1"), HasMore: false,
		})
	}))
	defer srv.Close()

	props := NewPropertyIndex().WithStore(store)
	auth := NewAuthIndex().WithStore(store)
	agents := NewAgentIndex().WithStore(store)
	syncer := NewSyncer(NewClient(srv.URL, "test"), props, auth, agents, store, SyncerConfig{PollInterval: 20 * time.Millisecond})

	store.failPutProperty.Store(true)

	// 4s ctx covers two backoff cycles (1s + 2s + headroom) so the
	// post-flip retry has time to fire.
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	go func() { _ = syncer.Run(ctx) }()

	// Memory still applies despite persist failure — that's the contract.
	waitFor(t, func() bool { return props.Count() == 1 })

	// Cursor must NOT have been saved while persist is failing.
	cur, err := store.Load(context.Background())
	require.NoError(t, err)
	assert.Empty(t, cur, "cursor must not advance while persist is failing")

	// Flip the switch — cursor must now advance on the next retry. The
	// first retry happens after a 1s backoff, so a fresh 3s deadline is
	// enough headroom for a doubled retry too.
	store.failPutProperty.Store(false)
	deadline := time.Now().Add(3500 * time.Millisecond)
	for time.Now().Before(deadline) {
		if c, _ := store.Load(context.Background()); c == "e1" {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("cursor never advanced to e1 after flipping persist failure off")
}

// TestSyncer_CursorExpiredClearFailureKeepsCursor: if Clear fails on
// the persistent store during cursor-expired handling, the empty
// cursor must NOT be saved — otherwise on restart we'd hydrate stale
// data with a fresh bootstrap cursor, permanently losing the
// pre-expiry revocations from the feed.
func TestSyncer_CursorExpiredClearFailureKeepsCursor(t *testing.T) {
	store := newFailingStore()
	require.NoError(t, store.PutProperty(context.Background(),
		&Property{PropertyID: "stale", PropertyRID: 1, Domain: "stale.example.com"}))
	require.NoError(t, store.Save(context.Background(), "old-expired-cursor"))

	var callCount atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		callCount.Add(1)
		// Always return 410 — never lets the syncer past cursor-expired
		// until we flip the flag.
		w.WriteHeader(http.StatusGone)
		_ = json.NewEncoder(w).Encode(FeedError{Error: "cursor_expired", Message: "expired"})
	}))
	defer srv.Close()

	props := NewPropertyIndex().WithStore(store)
	auth := NewAuthIndex().WithStore(store)
	agents := NewAgentIndex().WithStore(store)
	syncer := NewSyncer(NewClient(srv.URL, "test"), props, auth, agents, store, SyncerConfig{PollInterval: 20 * time.Millisecond})

	store.failClearProperty.Store(true)

	ctx, cancel := context.WithTimeout(context.Background(), 600*time.Millisecond)
	defer cancel()
	go func() { _ = syncer.Run(ctx) }()

	// Give the loop time to attempt and fail.
	time.Sleep(200 * time.Millisecond)

	cur, err := store.Load(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "old-expired-cursor", cur, "cursor must not be wiped while Clear is failing")
}

func TestValidatePublisherDomain(t *testing.T) {
	cases := []struct {
		name    string
		domain  string
		wantErr bool
	}{
		{"happy path", "example.com", false},
		{"empty", "", true},
		{"pipe", "foo|bar", true},
		{"newline", "foo\nbar", true},
		{"space", "foo bar", true},
		{"del", "foo\x7fbar", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidatePublisherDomain(tc.domain)
			if tc.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}
