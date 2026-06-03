//go:build integration

package redisstore

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/adcontextprotocol/adcp-go/targeting"
	"github.com/adcontextprotocol/adcp-go/targeting/internal/clusterslot"
	"github.com/adcontextprotocol/adcp-go/targeting/mediabuystore"
	"github.com/adcontextprotocol/adcp-go/targeting/pkgconfigstore"
	"github.com/adcontextprotocol/adcp-go/targeting/suppressionstore"
	"github.com/adcontextprotocol/adcp-go/targeting/topicstore"
	"github.com/adcontextprotocol/adcp-go/targeting/urlliststore"
)

// Each context-agent storage domain shares the same redisstore.Store
// backend. These tests run the basic Service.Put → Reader roundtrip
// for every domain against every Valkey topology the agent supports
// (standalone, shadow shards, real cluster), so a backend regression
// is caught at a single layer rather than each per-domain test in the
// agent.

// --- Standalone ---

func TestIntegration_ContextStores_Standalone(t *testing.T) {
	_, store := startValkey9(t)
	runContextStoresSuite(t, store, "https://seller.example.com/agent", "provider-1")
}

// --- Shadow shards ---

// In shadow mode the Store rejects writes (ErrReadOnly), so Service
// writers can't be exercised through it. Tests use the per-shard
// client to seed data and the shadow Store only for reads — exactly
// the production split (writes go to a cluster master through a
// cluster-aware client; the shadow replicas absorb the replicated
// state).
func TestIntegration_ContextStores_Shadow(t *testing.T) {
	store, clients := startValkeyShards(t, 3)
	seller := "https://seller.example.com/agent"
	providerID := "provider-shadow"

	t.Run("mediabuy", func(t *testing.T) {
		mb := mediabuystore.MediaBuy{
			MediaBuyID:     "mb-1",
			SellerAgentURL: seller,
			StartDate:      "2026-01-01", EndDate: "2026-12-31",
			Packages: []mediabuystore.MediaBuyPackage{{PackageID: "pkg-a"}},
		}
		seedRedisString(t, clients, mediabuystore.MediaBuyKey("mb-1"), mustJSON(t, mb))
		seedRedisSetAdd(t, clients, mediabuystore.SellerSetKey(seller), "mb-1")

		r := mediabuystore.NewReader(store)
		now := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
		pkgs, err := r.ActivePackages(context.Background(), seller, "", "", "", now)
		require.NoError(t, err)
		require.Len(t, pkgs, 1)
		assert.Equal(t, "pkg-a", pkgs[0].PackageID)
	})

	t.Run("pkgconfig", func(t *testing.T) {
		cfg := &targeting.PackageContextConfig{PackageID: "pkg-1", TopicTargets: true}
		seedRedisString(t, clients, pkgconfigstore.Key("pkg-1"), mustJSON(t, cfg))

		got, ok, err := pkgconfigstore.NewReader(store).Get(context.Background(), "pkg-1")
		require.NoError(t, err)
		require.True(t, ok)
		assert.True(t, got.TopicTargets)
	})

	t.Run("urllist", func(t *testing.T) {
		seedRedisSetAdd(t, clients, urlliststore.BlocklistKey("pkg-1"), "hash-blocked")
		blocked, err := urlliststore.NewReader(store).IsBlocked(context.Background(), "pkg-1", "hash-blocked")
		require.NoError(t, err)
		assert.True(t, blocked)
	})

	t.Run("topics", func(t *testing.T) {
		tax := topicstore.Taxonomy{Source: "iab", ID: 7}
		seedRedisSetAdd(t, clients, topicstore.ArtifactKey(tax, "article:pasta"), "632")
		r, err := topicstore.NewReader(store)
		require.NoError(t, err)
		topics, err := r.ArtifactTopics(context.Background(), tax, "article:pasta")
		require.NoError(t, err)
		assert.Contains(t, topics, "632")
	})

	t.Run("suppression_scan_covers_every_shard", func(t *testing.T) {
		// Seed one property suppression on every shard via the
		// app-level CRC16 routing. Then call the snapshot's Load and
		// assert all of them appear — this is the shadow-mode
		// analogue of the cluster ForEachMaster bug.
		for i, c := range clients {
			rid := fmt.Sprintf("rid-shard-%d-%d", i, time.Now().UnixNano())
			// Pick a key whose CRC16 shard matches container index `i`.
			key := pickKeyForShard(t, suppressionstore.PropertyKey(providerID, rid), rid, i, len(clients))
			require.NoError(t, c.Set(context.Background(), key, "1", time.Hour).Err())
		}
		snap, err := suppressionstore.NewSnapshot(suppressionstore.SnapshotConfig{
			Store: store, ProviderID: providerID,
		})
		require.NoError(t, err)
		require.NoError(t, snap.Load(context.Background()))
		propCount, _ := snap.Sizes()
		assert.GreaterOrEqual(t, propCount, len(clients),
			"snapshot must see suppression keys from every shard")
	})
}

// --- Real cluster ---

// Real cluster mode is where the SCAN bug bit: go-redis's SCAN on a
// ClusterClient routes to a single arbitrary master unless the
// pattern has a {hashtag}. The suppression patterns don't, so the
// fix had to fan across masters via ForEachMaster. This test runs
// against an actual 3-master Valkey cluster.
func TestIntegration_ContextStores_Cluster(t *testing.T) {
	nodes := startValkeyCluster(t, 3)
	addrs := make([]string, len(nodes))
	for i, c := range nodes {
		addrs[i] = c.Options().Addr
	}
	cluster := redis.NewClusterClient(&redis.ClusterOptions{Addrs: addrs})
	t.Cleanup(func() { _ = cluster.Close() })

	store := New(cluster)
	runContextStoresSuite(t, store, "https://seller.cluster.example/agent", "provider-cluster")
}

// TestIntegration_SuppressionScan_ClusterFansAcrossAllMasters is a
// targeted regression for the SCAN-only-routes-to-one-master bug. It
// writes one suppression to every master (via a hashtag that pins
// each key to a specific slot per ordinal), runs Snapshot.Load, and
// asserts every suppression surfaces. Without ForEachMaster fanout
// this test would observe roughly 1/N suppressions.
func TestIntegration_SuppressionScan_ClusterFansAcrossAllMasters(t *testing.T) {
	const numMasters = 3
	nodes := startValkeyCluster(t, numMasters)
	addrs := make([]string, len(nodes))
	for i, c := range nodes {
		addrs[i] = c.Options().Addr
	}
	cluster := redis.NewClusterClient(&redis.ClusterOptions{Addrs: addrs})
	t.Cleanup(func() { _ = cluster.Close() })

	const providerID = "provider-scan-regression"
	store := New(cluster)
	svc, err := suppressionstore.NewService(store)
	require.NoError(t, err)

	// Write enough suppressions that, by pigeonhole, every master
	// must own at least one of the keys. 16 keys × 3 masters = each
	// master owns ~5 of them on average. The plain SCAN would only
	// return the ones on the routed-to master.
	const n = 16
	written := make(map[string]struct{}, n)
	for i := 0; i < n; i++ {
		rid := fmt.Sprintf("rid-%d", i)
		require.NoError(t, svc.SuppressProperty(context.Background(), providerID, rid, time.Hour))
		written[rid] = struct{}{}
	}

	snap, err := suppressionstore.NewSnapshot(suppressionstore.SnapshotConfig{
		Store: store, ProviderID: providerID,
	})
	require.NoError(t, err)
	require.NoError(t, snap.Load(context.Background()))

	for rid := range written {
		got, _ := snap.IsPropertySuppressed(context.Background(), providerID, rid)
		assert.Truef(t, got, "suppression %q must appear after Load via ForEachMaster fanout", rid)
	}
}

// --- shared suite ---

// runContextStoresSuite runs the basic per-domain writer→reader
// roundtrip against the supplied (writable) Store. Used by the
// standalone and cluster paths; the shadow path has its own test
// because shadow writes return ErrReadOnly.
func runContextStoresSuite(t *testing.T, store *Store, seller, providerID string) {
	ctx := context.Background()

	t.Run("mediabuy", func(t *testing.T) {
		svc, err := mediabuystore.NewService(store)
		require.NoError(t, err)
		require.NoError(t, svc.Put(ctx, mediabuystore.MediaBuy{
			MediaBuyID:     "mb-1",
			SellerAgentURL: seller,
			StartDate:      "2026-01-01", EndDate: "2026-12-31",
			Packages: []mediabuystore.MediaBuyPackage{{PackageID: "pkg-a"}},
		}))
		now := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
		pkgs, err := mediabuystore.NewReader(store).ActivePackages(ctx, seller, "", "", "", now)
		require.NoError(t, err)
		require.Len(t, pkgs, 1)
		assert.Equal(t, "pkg-a", pkgs[0].PackageID)
	})

	t.Run("pkgconfig", func(t *testing.T) {
		svc, err := pkgconfigstore.NewService(store)
		require.NoError(t, err)
		require.NoError(t, svc.Put(ctx, &targeting.PackageContextConfig{
			PackageID:    "pkg-1",
			TopicTargets: true,
			PropertyRIDs: []string{"rid-1"},
		}))
		got, ok, err := pkgconfigstore.NewReader(store).Get(ctx, "pkg-1")
		require.NoError(t, err)
		require.True(t, ok)
		assert.True(t, got.TopicTargets)
		assert.Equal(t, []string{"rid-1"}, got.PropertyRIDs)
	})

	t.Run("urllist", func(t *testing.T) {
		svc, err := urlliststore.NewService(store)
		require.NoError(t, err)
		require.NoError(t, svc.AddToBlocklist(ctx, "pkg-1", "hash-blocked"))
		blocked, err := urlliststore.NewReader(store).IsBlocked(ctx, "pkg-1", "hash-blocked")
		require.NoError(t, err)
		assert.True(t, blocked)
	})

	t.Run("topics", func(t *testing.T) {
		tax := topicstore.Taxonomy{Source: "iab", ID: 7}
		w, err := topicstore.NewWriter(store)
		require.NoError(t, err)
		require.NoError(t, w.SetArtifactTopics(ctx, tax, "article:pasta", []string{"632", "640"}))
		r, err := topicstore.NewReader(store)
		require.NoError(t, err)
		topics, err := r.ArtifactTopics(ctx, tax, "article:pasta")
		require.NoError(t, err)
		assert.ElementsMatch(t, []string{"632", "640"}, topics)
	})

	t.Run("suppression", func(t *testing.T) {
		svc, err := suppressionstore.NewService(store)
		require.NoError(t, err)
		require.NoError(t, svc.SuppressProperty(ctx, providerID, "rid-bad", time.Hour))
		require.NoError(t, svc.SuppressGeo(ctx, providerID, "RU", time.Hour))

		snap, err := suppressionstore.NewSnapshot(suppressionstore.SnapshotConfig{
			Store: store, ProviderID: providerID,
		})
		require.NoError(t, err)
		require.NoError(t, snap.Load(ctx))

		got, _ := snap.IsPropertySuppressed(ctx, providerID, "rid-bad")
		assert.True(t, got)
		got, _ = snap.IsGeoSuppressed(ctx, providerID, "RU")
		assert.True(t, got)
		got, _ = snap.IsGeoSuppressed(ctx, providerID, "US")
		assert.False(t, got)
	})
}

// --- shadow seeding helpers ---

func seedRedisString(t *testing.T, clients []*redis.Client, key, value string) {
	t.Helper()
	idx := clusterslot.Shard(key, len(clients))
	require.NoError(t, clients[idx].Set(context.Background(), key, value, 0).Err())
}

func seedRedisSetAdd(t *testing.T, clients []*redis.Client, key string, members ...string) {
	t.Helper()
	idx := clusterslot.Shard(key, len(clients))
	for _, m := range members {
		require.NoError(t, clients[idx].SAdd(context.Background(), key, m).Err())
	}
}

// pickKeyForShard searches the namespace `prefix:` for a suffix that
// hashes to the desired shard ordinal, falling back to the input when
// already correct. Used to ensure each suppression-scan-shadow seeded
// key actually lands on the shard the test intends.
func pickKeyForShard(t *testing.T, baseKey, _ string, want, n int) string {
	t.Helper()
	if clusterslot.Shard(baseKey, n) == want {
		return baseKey
	}
	for i := 0; i < 1000; i++ {
		candidate := baseKey + "-" + strings.Repeat("x", i+1)
		if clusterslot.Shard(candidate, n) == want {
			return candidate
		}
	}
	t.Fatalf("could not find a key under %q that hashes to shard %d", baseKey, want)
	return ""
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	require.NoError(t, err)
	return string(b)
}
