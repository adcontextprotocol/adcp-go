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
	b, err := buildRegistryBridge(router.RegistryConfig{}, router.NewRegistry("", ""), nil, discardLogger())
	require.NoError(t, err)
	assert.Nil(t, b)
	// Shutdown on a nil bridge must be safe — main.go guards with an
	// explicit nil check, but the method itself must not panic either.
	b.Shutdown()
}

// End-to-end: bridge fetches from a stub feed carrying the bearer token,
// mirrors the resulting property into router.Registry, and the reseed
// callback re-attaches the router's own signing key after each rebuild.
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
	reseed := reseedSigningPropertiesFactory([]string{authorizedRID}, jwk)

	b, err := buildRegistryBridge(router.RegistryConfig{
		FeedURL:             srv.URL,
		FeedToken:           "expected-token",
		PollIntervalSeconds: 1,
	}, rtr, reseed, discardLogger())
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

	// The router's own kid must still be attached to the authorized RID
	// after the map swap performed by LoadFromData.
	seedProp, ok := rtr.LookupByRID(authorizedRID)
	require.True(t, ok, "authorized RID seed record should survive rebuild")
	require.Len(t, seedProp.SigningKeys, 1)
	assert.Equal(t, "router-kid", seedProp.SigningKeys[0].Kid)

	assert.Equal(t, "Bearer expected-token", tokenSeen.Load(),
		"bearer token from RegistryConfig.FeedToken must reach the feed endpoint")
	assert.GreaterOrEqual(t, called.Load(), int32(1))
}

// Sequence must advance monotonically across seedSigningProperties (called
// before the bridge starts) and the first feed-driven mirror. A regression
// here would let downstream consumers observe /registry/snapshot going
// backward in sequence — a delta-tracking hazard.
func TestBuildRegistryBridge_SequenceMonotonicAfterSeed(t *testing.T) {
	rtr := router.NewRegistry("", "")
	// Simulate seedSigningProperties bumping the sequence to 3 before the
	// bridge takes over — the router's real init path does this for every
	// authorized RID via ApplyUpdate(Sequence()+1).
	seedRIDs := []string{"seed-a", "seed-b", "seed-c"}
	seedSigningProperties(rtr, seedRIDs, tmproto.SigningKey{Kid: "kid1", Alg: "EdDSA"})
	require.Equal(t, uint64(3), rtr.Sequence(), "sanity: seed bumped sequence to 3")

	// Empty feed — succeeds immediately with zero events; OnSuccessfulPoll
	// still fires, exercising the mirror path.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"events":   []any{},
			"cursor":   nil,
			"has_more": false,
		})
	}))
	defer srv.Close()

	b, err := buildRegistryBridge(router.RegistryConfig{
		FeedURL:             srv.URL,
		PollIntervalSeconds: 1,
	}, rtr, reseedSigningPropertiesFactory(seedRIDs, tmproto.SigningKey{Kid: "kid1", Alg: "EdDSA"}), discardLogger())
	require.NoError(t, err)
	require.NotNil(t, b)
	t.Cleanup(b.Shutdown)

	require.Eventually(t, func() bool {
		return rtr.Sequence() > 3
	}, 2*time.Second, 20*time.Millisecond, "sequence must advance past the seed value after first mirror")
}
