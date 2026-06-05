package registry

import (
	"context"
	"sync/atomic"
	"testing"
	"time"
)

// TestSyncer_OnSuccessfulPollFires verifies the liveness-beacon hook
// is invoked once per cleanly-completed poll, including the
// zero-event poll the feed server returns after all pages drain.
func TestSyncer_OnSuccessfulPollFires(t *testing.T) {
	rid := uint64(7)
	pages := []feedPage{
		{
			events: []FeedEvent{
				{EventID: "e1", EventType: "property.created", EntityType: "property", EntityID: "p1",
					Payload: mustMarshal(Property{PropertyID: "p1", PropertyRID: rid, Domain: "example.com"}),
					Actor:   "test"},
			},
			cursor: strPtr("e1"), hasMore: false,
		},
	}

	srv := feedServer(t, pages)
	defer srv.Close()

	var calls atomic.Int32
	var lastCount atomic.Int32
	syncer := NewSyncer(
		NewClient(srv.URL, "test"),
		NewPropertyIndex(), NewAuthIndex(), NewAgentIndex(),
		&MemoryCursorStore{},
		SyncerConfig{
			PollInterval: 50 * time.Millisecond,
			OnSuccessfulPoll: func(count int) {
				calls.Add(1)
				lastCount.Store(int32(count))
			},
		},
	)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	go func() { _ = syncer.Run(ctx) }()

	// First poll applies the property event (count == 1).
	waitFor(t, func() bool { return lastCount.Load() == 1 })

	// A subsequent poll over the drained feed returns zero events but
	// still fires the hook, proving a quiescent feed keeps the beacon
	// alive.
	startCalls := calls.Load()
	waitFor(t, func() bool { return calls.Load() > startCalls && lastCount.Load() == 0 })
}
