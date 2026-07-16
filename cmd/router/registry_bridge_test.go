package main

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/adcontextprotocol/adcp-go/router"
	"github.com/adcontextprotocol/adcp-go/tmproto"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// discardLogger keeps test output quiet without changing prod behavior.
func discardLogger() *slog.Logger {
	return slog.New(slog.NewJSONHandler(io.Discard, nil))
}

// A bare RegistryConfig with FeedURL empty means the router runs in
// seed-only mode; the bridge returns (nil, nil) so the caller falls
// through unchanged.
func TestBuildRegistryBridge_DisabledReturnsNil(t *testing.T) {
	b, err := buildRegistryBridge(router.RegistryConfig{}, router.NewRegistry("", ""), nil, tmproto.SigningKey{}, false, discardLogger())
	require.NoError(t, err)
	assert.Nil(t, b)
	// Shutdown on a nil bridge must be safe — main.go guards with an
	// explicit nil check, but the method itself must not panic either.
	b.Shutdown()
}

// End-to-end: bridge fetches from a stub feed carrying the bearer token,
// mirrors the resulting property into router.Registry, and the router's
// own signing key is projected onto the authorized RID in the same
// atomic snapshot swap.
func TestBuildRegistryBridge_MirrorsFeedIntoRouterRegistry(t *testing.T) {
	var (
		tokenSeen atomic.Value
		called    atomic.Int32
	)

	propertyJSON, err := json.Marshal(map[string]any{
		"property_id":   "pub/site",
		"property_rid":  "01890000-0000-7000-8000-000000000001",
		"property_type": "website",
		"domain":        "site.example",
	})
	require.NoError(t, err)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		tokenSeen.Store(req.Header.Get("Authorization"))
		called.Add(1)
		w.Header().Set("Content-Type", "application/json")
		// Minimal feed page with one property.updated event and no
		// further pages — enough to trigger OnSuccessfulPoll → mirror.
		_ = json.NewEncoder(w).Encode(map[string]any{
			"events": []map[string]any{
				{
					"event_id":    "e1",
					"event_type":  "property.updated",
					"entity_type": "property",
					"entity_id":   "pub/site",
					"payload":     json.RawMessage(propertyJSON),
					"created_at":  time.Now().UTC().Format(time.RFC3339),
				},
			},
			"cursor":   nil,
			"has_more": false,
		})
	}))
	defer srv.Close()

	rtr := router.NewRegistry("", "")
	authorizedRID := "01890000-0000-7000-8000-000000000099"
	jwk := tmproto.SigningKey{Kid: "router-kid", Alg: "EdDSA"}

	b, err := buildRegistryBridge(router.RegistryConfig{
		FeedURL:             srv.URL,
		FeedToken:           "expected-token",
		PollIntervalSeconds: 1,
	}, rtr, []string{authorizedRID}, jwk, true, discardLogger())
	require.NoError(t, err)
	require.NotNil(t, b)
	t.Cleanup(b.Shutdown)

	// Wait for at least one successful poll to complete and mirror the
	// feed property into the router's snapshot registry.
	require.Eventually(t, func() bool {
		_, ok := rtr.LookupByRID("01890000-0000-7000-8000-000000000001")
		return ok
	}, 2*time.Second, 20*time.Millisecond, "feed property should have mirrored into router.Registry")

	prop, ok := rtr.LookupByRID("01890000-0000-7000-8000-000000000001")
	require.True(t, ok)
	assert.Equal(t, "pub/site", prop.PropertyID)
	assert.Equal(t, "site.example", prop.Domain)

	// The router's own kid must be present on the authorized RID — the
	// atomic rebuild projects it into the same snapshot as the feed props.
	seedProp, ok := rtr.LookupByRID(authorizedRID)
	require.True(t, ok, "authorized-RID placeholder record should exist")
	require.Len(t, seedProp.SigningKeys, 1)
	assert.Equal(t, "router-kid", seedProp.SigningKeys[0].Kid)

	assert.Equal(t, "Bearer expected-token", tokenSeen.Load(),
		"bearer token from RegistryConfig.FeedToken must reach the feed endpoint")
	assert.GreaterOrEqual(t, called.Load(), int32(1))
}

// When the feed emits the authorized-RID record itself (typical steady
// state), the router key is MERGED into that record rather than added
// as a placeholder — so /registry/snapshot carries one entry per RID
// with the router's key attached, not two entries competing for the
// same RID.
func TestBuildRegistryBridge_MergesRouterKeyIntoFeedAuthorizedRID(t *testing.T) {
	authorizedRID := "01890000-0000-7000-8000-000000000099"

	// Feed emits the authorized RID record with a real property_id and
	// domain — the router key must merge INTO this record, not clobber it
	// with a placeholder.
	propJSON, err := json.Marshal(map[string]any{
		"property_id":   "authoritative-slug",
		"property_rid":  authorizedRID,
		"property_type": "website",
		"domain":        "publisher.example",
	})
	require.NoError(t, err)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"events": []map[string]any{{
				"event_id":    "e1",
				"event_type":  "property.updated",
				"entity_type": "property",
				"entity_id":   "authoritative-slug",
				"payload":     json.RawMessage(propJSON),
				"created_at":  time.Now().UTC().Format(time.RFC3339),
			}},
			"cursor":   nil,
			"has_more": false,
		})
	}))
	defer srv.Close()

	rtr := router.NewRegistry("", "")
	jwk := tmproto.SigningKey{Kid: "router-kid", Alg: "EdDSA"}
	b, err := buildRegistryBridge(router.RegistryConfig{
		FeedURL:             srv.URL,
		PollIntervalSeconds: 1,
	}, rtr, []string{authorizedRID}, jwk, true, discardLogger())
	require.NoError(t, err)
	require.NotNil(t, b)
	t.Cleanup(b.Shutdown)

	require.Eventually(t, func() bool {
		p, ok := rtr.LookupByRID(authorizedRID)
		return ok && len(p.SigningKeys) > 0
	}, 2*time.Second, 20*time.Millisecond, "authorized RID should carry a signing key after first poll")

	prop, _ := rtr.LookupByRID(authorizedRID)
	// Merge, not clobber: feed metadata preserved AND router key attached.
	assert.Equal(t, "authoritative-slug", prop.PropertyID, "feed property_id must be preserved on merge")
	assert.Equal(t, "publisher.example", prop.Domain, "feed domain must be preserved on merge")
	require.Len(t, prop.SigningKeys, 1)
	assert.Equal(t, "router-kid", prop.SigningKeys[0].Kid)
}

// Steady-state regression: across multiple polls, a concurrent snapshot
// reader must NEVER observe (a) a sequence regression or (b) the router's
// own signing key missing from an authorized RID. The prior two-phase
// rebuild (LoadFromData followed by per-RID AttachSigningKey) violated
// both invariants; this test would fail against that design.
func TestBuildRegistryBridge_SteadyStateAtomicity(t *testing.T) {
	authorizedRID := "01890000-0000-7000-8000-000000000099"
	feedRID := "01890000-0000-7000-8000-000000000001"

	// Serve a small feed page on every request so successive polls both
	// have work to do (the rebuild path fires on every OnSuccessfulPoll,
	// including zero-event pages, so this just guarantees the mirror
	// actually rebuilds a non-trivial snapshot on each pass).
	propJSON, err := json.Marshal(map[string]any{
		"property_id":   "feed-slug",
		"property_rid":  feedRID,
		"property_type": "website",
		"domain":        "feed.example",
	})
	require.NoError(t, err)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"events": []map[string]any{{
				"event_id":    "e1",
				"event_type":  "property.updated",
				"entity_type": "property",
				"entity_id":   "feed-slug",
				"payload":     json.RawMessage(propJSON),
				"created_at":  time.Now().UTC().Format(time.RFC3339),
			}},
			"cursor":   nil,
			"has_more": false,
		})
	}))
	defer srv.Close()

	rtr := router.NewRegistry("", "")
	jwk := tmproto.SigningKey{Kid: "router-kid", Alg: "EdDSA"}
	b, err := buildRegistryBridge(router.RegistryConfig{
		FeedURL:             srv.URL,
		PollIntervalSeconds: 1,
	}, rtr, []string{authorizedRID}, jwk, true, discardLogger())
	require.NoError(t, err)
	require.NotNil(t, b)
	t.Cleanup(b.Shutdown)

	// Wait for the first rebuild to publish the authorized RID.
	require.Eventually(t, func() bool {
		p, ok := rtr.LookupByRID(authorizedRID)
		return ok && len(p.SigningKeys) > 0
	}, 2*time.Second, 5*time.Millisecond)

	// Continuously sample the wire-serving snapshot for a window that
	// spans at least two poll boundaries. Each sample must see a
	// non-decreasing sequence AND the router key attached to the
	// authorized RID. The two-phase-rebuild bug would surface here as
	// intermittent "no signing keys" or a sequence decrease.
	done := make(chan struct{})
	var samples atomic.Int64
	var seqRegressed atomic.Bool
	var keyMissing atomic.Bool
	go func() {
		defer close(done)
		var lastSeq uint64
		deadline := time.Now().Add(2500 * time.Millisecond)
		for time.Now().Before(deadline) {
			seq := rtr.Sequence()
			if seq < lastSeq {
				seqRegressed.Store(true)
			}
			lastSeq = seq
			p, ok := rtr.LookupByRID(authorizedRID)
			if !ok || len(p.SigningKeys) == 0 {
				keyMissing.Store(true)
			}
			samples.Add(1)
		}
	}()
	<-done

	assert.False(t, seqRegressed.Load(), "sequence must never decrease across polls")
	assert.False(t, keyMissing.Load(), "router key must be present on authorized RID on every sample")
	assert.Greater(t, samples.Load(), int64(1000), "sanity: sampled at least 1000 times")
	// The final sequence must have advanced past 1 — proves multiple
	// polls fired during the sampling window rather than the invariants
	// holding only because nothing happened.
	assert.Greater(t, rtr.Sequence(), uint64(1), "at least two polls should have completed")
}
