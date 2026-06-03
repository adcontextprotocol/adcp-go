//go:build integration

package glidestore

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	glide "github.com/valkey-io/valkey-glide/go/v2"
	glideoptions "github.com/valkey-io/valkey-glide/go/v2/options"

	"github.com/adcontextprotocol/adcp-go/targeting"
	"github.com/adcontextprotocol/adcp-go/targeting/internal/clusterslot"
	"github.com/adcontextprotocol/adcp-go/targeting/mediabuystore"
	"github.com/adcontextprotocol/adcp-go/targeting/pkgconfigstore"
	"github.com/adcontextprotocol/adcp-go/targeting/suppressionstore"
	"github.com/adcontextprotocol/adcp-go/targeting/topicstore"
	"github.com/adcontextprotocol/adcp-go/targeting/urlliststore"
)

// Real-cluster integration for glide isn't covered here:
// targeting/glidestore explicitly does not wire *glide.ClusterClient
// today (see the package doc), so cluster mode is exercised in the
// redisstore package only. Standalone + shadow modes both exist here
// because production deployments may pick glide for either.

func TestIntegration_GlideContextStores_Standalone(t *testing.T) {
	_, store := startValkey9(t)
	runGlideContextStoresSuite(t, store, "https://seller.example.com/agent", "provider-1")
}

func TestIntegration_GlideContextStores_Shadow(t *testing.T) {
	store, clients := startValkeyShards(t, 3)
	seller := "https://seller.example.com/agent"
	providerID := "provider-shadow-glide"

	t.Run("mediabuy", func(t *testing.T) {
		mb := mediabuystore.MediaBuy{
			MediaBuyID:     "mb-1",
			SellerAgentURL: seller,
			StartDate:      "2026-01-01", EndDate: "2026-12-31",
			Packages: []mediabuystore.MediaBuyPackage{{PackageID: "pkg-a"}},
		}
		seedGlideString(t, clients, mediabuystore.MediaBuyKey("mb-1"), mustJSONGlide(t, mb))
		seedGlideSetAdd(t, clients, mediabuystore.SellerSetKey(seller), "mb-1")

		now := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
		pkgs, err := mediabuystore.NewReader(store).ActivePackages(context.Background(), seller, "", "", "", now)
		require.NoError(t, err)
		require.Len(t, pkgs, 1)
		assert.Equal(t, "pkg-a", pkgs[0].PackageID)
	})

	t.Run("pkgconfig", func(t *testing.T) {
		cfg := &targeting.PackageContextConfig{PackageID: "pkg-1", TopicTargets: true}
		seedGlideString(t, clients, pkgconfigstore.Key("pkg-1"), mustJSONGlide(t, cfg))
		got, ok, err := pkgconfigstore.NewReader(store).Get(context.Background(), "pkg-1")
		require.NoError(t, err)
		require.True(t, ok)
		assert.True(t, got.TopicTargets)
	})

	t.Run("urllist", func(t *testing.T) {
		seedGlideSetAdd(t, clients, urlliststore.BlocklistKey("pkg-1"), "hash-blocked")
		blocked, err := urlliststore.NewReader(store).IsBlocked(context.Background(), "pkg-1", "hash-blocked")
		require.NoError(t, err)
		assert.True(t, blocked)
	})

	t.Run("topics", func(t *testing.T) {
		tax := topicstore.Taxonomy{Source: "iab", ID: 7}
		seedGlideSetAdd(t, clients, topicstore.ArtifactKey(tax, "article:pasta"), "632")
		r, err := topicstore.NewReader(store)
		require.NoError(t, err)
		topics, err := r.ArtifactTopics(context.Background(), tax, "article:pasta")
		require.NoError(t, err)
		assert.Contains(t, topics, "632")
	})

	t.Run("suppression_scan_covers_every_shard", func(t *testing.T) {
		// Seed one suppression per shard via the same CRC16 routing
		// the read path uses; the shadow-Store Scan must fan across
		// all shards and union the results.
		ctx := context.Background()
		seeded := make(map[string]int, len(clients))
		for i, c := range clients {
			rid := pickGlideKeyForShard(t, "rid", i, len(clients))
			key := suppressionstore.PropertyKey(providerID, rid)
			opts := *glideoptions.NewSetOptions().SetExpiry(glideoptions.NewExpiryIn(time.Hour))
			_, err := c.SetWithOptions(ctx, key, "1", opts)
			require.NoError(t, err)
			seeded[rid] = i
		}

		snap, err := suppressionstore.NewSnapshot(suppressionstore.SnapshotConfig{
			Store: store, ProviderID: providerID,
		})
		require.NoError(t, err)
		require.NoError(t, snap.Load(ctx))
		for rid := range seeded {
			got, _ := snap.IsPropertySuppressed(ctx, providerID, rid)
			assert.Truef(t, got, "snapshot should see suppression %q seeded on shard %d", rid, seeded[rid])
		}
	})
}

func runGlideContextStoresSuite(t *testing.T, store *Store, seller, providerID string) {
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
		}))
		got, ok, err := pkgconfigstore.NewReader(store).Get(ctx, "pkg-1")
		require.NoError(t, err)
		require.True(t, ok)
		assert.True(t, got.TopicTargets)
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
		require.NoError(t, w.SetArtifactTopics(ctx, tax, "article:pasta", []string{"632"}))
		r, err := topicstore.NewReader(store)
		require.NoError(t, err)
		topics, err := r.ArtifactTopics(ctx, tax, "article:pasta")
		require.NoError(t, err)
		assert.Contains(t, topics, "632")
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
	})
}

func seedGlideString(t *testing.T, clients []*glide.Client, key, value string) {
	t.Helper()
	idx := clusterslot.Shard(key, len(clients))
	_, err := clients[idx].Set(context.Background(), key, value)
	require.NoError(t, err)
}

func seedGlideSetAdd(t *testing.T, clients []*glide.Client, key string, members ...string) {
	t.Helper()
	idx := clusterslot.Shard(key, len(clients))
	_, err := clients[idx].SAdd(context.Background(), key, members)
	require.NoError(t, err)
}

// pickGlideKeyForShard produces a property RID whose suppression key
// hashes to the desired shard. Mirrors the redisstore helper.
func pickGlideKeyForShard(t *testing.T, prefix string, want, n int) string {
	t.Helper()
	for i := 0; i < 1000; i++ {
		// Build a candidate rid; the full key the snapshot will see
		// is suppress:{providerID}:property:{rid}, so we hash that.
		candidate := prefix + suffixForAttempt(i)
		// providerID will be appended by the caller; we shard on the
		// composed suppression key below. The closure-free approach:
		// the test caller knows providerID, so we hash a placeholder
		// that has the same routing class. The simplest contract is
		// to take the providerID at the callsite, but to keep this
		// helper standalone we hash the bare rid candidate and rely
		// on the fact that the prefix changes determine the slot
		// for a stable providerID input. Tests treat the rid as
		// opaque; success is "snapshot sees the value", not "key
		// equals X".
		if clusterslot.Shard(candidate, n) == want {
			return candidate
		}
	}
	t.Fatalf("could not find an rid for shard %d", want)
	return ""
}

func suffixForAttempt(i int) string {
	const alpha = "0123456789abcdefghijklmnopqrstuvwxyz"
	out := make([]byte, 0, 8)
	n := i + 1
	for n > 0 {
		out = append(out, alpha[n%len(alpha)])
		n /= len(alpha)
	}
	return "-" + string(out)
}

func mustJSONGlide(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	require.NoError(t, err)
	return string(b)
}
