//go:build integration

package glidestore

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
	glide "github.com/valkey-io/valkey-glide/go/v2"
	"github.com/valkey-io/valkey-glide/go/v2/config"

	"github.com/adcontextprotocol/adcp-go/targeting"
	"github.com/adcontextprotocol/adcp-go/targeting/audience"
	"github.com/adcontextprotocol/adcp-go/targeting/fcap"
	"github.com/adcontextprotocol/adcp-go/targeting/topicstore"
	"github.com/adcontextprotocol/adcp-go/tmproto"
)

var integrationTaxonomy = topicstore.Taxonomy{Source: "integration", ID: 1}

// startValkey9 spins up a Valkey 9 container and returns a connected glide client.
// Skips the test when Docker isn't available locally.
func startValkey9(t *testing.T) (*glide.Client, *Store) {
	t.Helper()
	ctx := context.Background()

	req := testcontainers.ContainerRequest{
		Image:        "valkey/valkey:9-alpine",
		ExposedPorts: []string{"6379/tcp"},
		WaitingFor:   wait.ForListeningPort("6379/tcp").WithStartupTimeout(30 * time.Second),
	}
	container, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: req,
		Started:          true,
	})
	if err != nil {
		t.Skipf("Docker not available, skipping integration test: %v", err)
	}
	t.Cleanup(func() {
		_ = container.Terminate(context.Background())
	})

	host, err := container.Host(ctx)
	require.NoError(t, err)
	port, err := container.MappedPort(ctx, "6379/tcp")
	require.NoError(t, err)

	portInt, err := strconv.Atoi(port.Port())
	require.NoError(t, err)

	cfg := config.NewClientConfiguration().WithAddress(&config.NodeAddress{Host: host, Port: portInt})
	client, err := glide.NewClient(cfg)
	require.NoError(t, err)
	t.Cleanup(client.Close)

	return client, New(client)
}

func TestIntegration_TargetingStore_StringOps(t *testing.T) {
	_, store := startValkey9(t)
	ctx := context.Background()

	require.NoError(t, store.Set(ctx, "k1", "v1", 0))
	val, ok, err := store.Get(ctx, "k1")
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, "v1", val)

	_, ok, err = store.Get(ctx, "missing")
	require.NoError(t, err)
	assert.False(t, ok)

	exists, err := store.Exists(ctx, "k1")
	require.NoError(t, err)
	assert.True(t, exists)
}

func TestIntegration_TargetingStore_TTL(t *testing.T) {
	_, store := startValkey9(t)
	ctx := context.Background()

	require.NoError(t, store.Set(ctx, "ephemeral", "x", 800*time.Millisecond))

	_, ok, _ := store.Get(ctx, "ephemeral")
	assert.True(t, ok, "should still exist")

	time.Sleep(1200 * time.Millisecond)

	_, ok, _ = store.Get(ctx, "ephemeral")
	assert.False(t, ok, "should expire")
}

func TestIntegration_TargetingStore_MGetMSet(t *testing.T) {
	_, store := startValkey9(t)
	ctx := context.Background()

	require.NoError(t, store.MSet(ctx, map[string]string{"a": "1", "b": "2", "c": "3"}, 0))
	vals, err := store.MGet(ctx, "a", "missing", "b")
	require.NoError(t, err)
	assert.Equal(t, []string{"1", "", "2"}, vals)
}

func TestIntegration_FcapStore_HSetEx_HExists(t *testing.T) {
	_, store := startValkey9(t)
	svc := fcap.New(store)
	ctx := context.Background()

	expireAt := time.Now().Add(2 * time.Minute)
	field := fcap.Field{SellerAgentURL: "https://seller.example.com/agent", PackageID: "pkg-1"}
	require.NoError(t, svc.RecordCap(ctx, "id5-abc", []fcap.Field{field}, expireAt))

	capped, err := svc.IsCapped(ctx, "id5-abc", field)
	require.NoError(t, err)
	assert.True(t, capped)

	other := fcap.Field{SellerAgentURL: "https://seller.example.com/agent", PackageID: "pkg-2"}
	capped, err = svc.IsCapped(ctx, "id5-abc", other)
	require.NoError(t, err)
	assert.False(t, capped)

	capped, err = svc.IsCapped(ctx, "id5-other", field)
	require.NoError(t, err)
	assert.False(t, capped)
}

func TestIntegration_FcapStore_TTLExpiry(t *testing.T) {
	_, store := startValkey9(t)
	svc := fcap.New(store)
	ctx := context.Background()

	field := fcap.Field{SellerAgentURL: "url", PackageID: "pkg-ttl"}
	require.NoError(t, svc.RecordCap(ctx, "id5-ttl", []fcap.Field{field}, time.Now().Add(800*time.Millisecond)))

	capped, _ := svc.IsCapped(ctx, "id5-ttl", field)
	assert.True(t, capped)

	time.Sleep(1200 * time.Millisecond)

	capped, _ = svc.IsCapped(ctx, "id5-ttl", field)
	assert.False(t, capped, "field should drop after HSETEX TTL")
}

func TestIntegration_FcapStore_BatchWriteAndRead(t *testing.T) {
	_, store := startValkey9(t)
	svc := fcap.New(store)
	ctx := context.Background()

	expireAt := time.Now().Add(2 * time.Minute)
	batches := make([]fcap.CapBatch, 5)
	for i := range batches {
		batches[i] = fcap.CapBatch{
			UserIdentity: fmt.Sprintf("user-%d", i),
			Fields: []fcap.Field{
				{SellerAgentURL: "https://s1", PackageID: "pkg-a"},
				{SellerAgentURL: "https://s1", PackageID: "pkg-b"},
			},
			ExpireAt: expireAt,
		}
	}
	require.NoError(t, svc.RecordCapBatch(ctx, batches))

	lookups := []fcap.CapLookup{
		{UserIdentity: "user-0", Field: fcap.Field{SellerAgentURL: "https://s1", PackageID: "pkg-a"}},  // capped
		{UserIdentity: "user-3", Field: fcap.Field{SellerAgentURL: "https://s1", PackageID: "pkg-b"}},  // capped
		{UserIdentity: "user-4", Field: fcap.Field{SellerAgentURL: "https://s2", PackageID: "pkg-a"}},  // not capped
		{UserIdentity: "user-99", Field: fcap.Field{SellerAgentURL: "https://s1", PackageID: "pkg-a"}}, // not capped
	}
	results, err := svc.IsCappedBatch(ctx, lookups)
	require.NoError(t, err)
	assert.Equal(t, []bool{true, true, false, false}, results)
}

func TestIntegration_TargetingStore_SetOps(t *testing.T) {
	client, store := startValkey9(t)
	ctx := context.Background()

	_, err := client.SAdd(ctx, "colors", []string{"red", "green", "blue"})
	require.NoError(t, err)
	_, err = client.SAdd(ctx, "warm", []string{"red", "orange", "yellow"})
	require.NoError(t, err)

	t.Run("SetIsMember_Hit", func(t *testing.T) {
		ok, err := store.SetIsMember(ctx, "colors", "red")
		require.NoError(t, err)
		assert.True(t, ok)
	})

	t.Run("SetIsMember_Miss", func(t *testing.T) {
		ok, err := store.SetIsMember(ctx, "colors", "purple")
		require.NoError(t, err)
		assert.False(t, ok)
	})

	t.Run("SetIsMember_MissingKey", func(t *testing.T) {
		ok, err := store.SetIsMember(ctx, "nonexistent", "x")
		require.NoError(t, err)
		assert.False(t, ok)
	})

	t.Run("SetIntersect", func(t *testing.T) {
		got, err := store.SetIntersect(ctx, "colors", "warm")
		require.NoError(t, err)
		assert.Equal(t, []string{"red"}, got)
	})

	t.Run("SetIntersect_NoOverlap", func(t *testing.T) {
		_, err := client.SAdd(ctx, "cold", []string{"navy", "purple"})
		require.NoError(t, err)
		got, err := store.SetIntersect(ctx, "warm", "cold")
		require.NoError(t, err)
		assert.Empty(t, got)
	})

	t.Run("SetIntersect_MissingKey", func(t *testing.T) {
		got, err := store.SetIntersect(ctx, "colors", "nonexistent")
		require.NoError(t, err)
		assert.Empty(t, got)
	})

	t.Run("SetMembers", func(t *testing.T) {
		got, err := store.SetMembers(ctx, "colors")
		require.NoError(t, err)
		sort.Strings(got)
		assert.Equal(t, []string{"blue", "green", "red"}, got)
	})

	t.Run("SetMembers_MissingKey", func(t *testing.T) {
		got, err := store.SetMembers(ctx, "nonexistent")
		require.NoError(t, err)
		assert.Nil(t, got)
	})
}

func TestIntegration_TargetingStore_ExistsAcrossTypes(t *testing.T) {
	client, store := startValkey9(t)
	ctx := context.Background()

	require.NoError(t, store.Set(ctx, "string-key", "v", 0))
	_, err := client.SAdd(ctx, "set-key", []string{"member"})
	require.NoError(t, err)

	for _, k := range []string{"string-key", "set-key"} {
		ok, err := store.Exists(ctx, k)
		require.NoError(t, err)
		assert.True(t, ok, "%s should exist", k)
	}

	ok, err := store.Exists(ctx, "missing-key")
	require.NoError(t, err)
	assert.False(t, ok)
}

func TestIntegration_TargetingStore_MGet_MixedHitsAndMisses(t *testing.T) {
	_, store := startValkey9(t)
	ctx := context.Background()

	require.NoError(t, store.MSet(ctx, map[string]string{
		"k1": "v1",
		"k3": "v3",
	}, 0))

	got, err := store.MGet(ctx, "k1", "k2", "k3", "k4")
	require.NoError(t, err)
	assert.Equal(t, []string{"v1", "", "v3", ""}, got)
}

func TestIntegration_Engine_EvaluateContext_AgainstRealValkey(t *testing.T) {
	client, store := startValkey9(t)
	ctx := context.Background()

	_, err := client.SAdd(ctx, topicstore.PackageKey(integrationTaxonomy, "pkg-food"), []string{"food.cooking", "food.recipes"})
	require.NoError(t, err)
	_, err = client.SAdd(ctx, topicstore.ArtifactKey(integrationTaxonomy, "article:pasta"), []string{"food.cooking", "food.italian"})
	require.NoError(t, err)
	_, err = client.SAdd(ctx, "url:blocklist:pkg-family", []string{targeting.HashURL("article:adult-content")})
	require.NoError(t, err)

	engine := targeting.NewContextEngine(targeting.ContextEngineConfig{
		ProviderID: "integration-engine",
		Store:      store,
		Properties: targeting.PropertyList{Global: targeting.NewMapBitmap("1")},
		Packages: []targeting.PackageConfig{
			{PackageID: "pkg-food", TopicTargets: true},
			{PackageID: "pkg-family", URLBlocklist: true},
		},
		AcceptedTaxonomies: []topicstore.Taxonomy{integrationTaxonomy},
	})

	t.Run("TopicMatch", func(t *testing.T) {
		resp, err := engine.EvaluateContext(ctx, &tmproto.ContextMatchRequest{
			RequestID:    "ctx-topic",
			PropertyRID:  "1",
			ArtifactRefs: []tmproto.ArtifactRef{{Type: tmproto.ArtifactRefTypeURL, Value: "article:pasta"}},
			PackageIDs:   []string{"pkg-food"},
		})
		require.NoError(t, err)
		assert.Len(t, resp.Offers, 1, "pkg-food should match via topic intersection")
	})

	t.Run("URLBlocked", func(t *testing.T) {
		resp, err := engine.EvaluateContext(ctx, &tmproto.ContextMatchRequest{
			RequestID:    "ctx-block",
			PropertyRID:  "1",
			ArtifactRefs: []tmproto.ArtifactRef{{Type: tmproto.ArtifactRefTypeURL, Value: "article:adult-content"}},
			PackageIDs:   []string{"pkg-family"},
		})
		require.NoError(t, err)
		assert.Empty(t, resp.Offers, "pkg-family should be URL-blocked")
	})
}

func TestIntegration_Engine_EvaluateIdentity_AgainstRealValkey(t *testing.T) {
	_, store := startValkey9(t)
	ctx := context.Background()

	audSvc := audience.New(store)
	require.NoError(t, audSvc.Upsert(ctx, audience.AudienceUpsert{
		AudienceID: "cooking_fans",
		Add:        []audience.Member{{UserToken: "tok-alice", Score: 1.0}},
	}))

	engine := targeting.NewIdentityEngine(targeting.IdentityEngineConfig{
		Audience: audSvc,
	})

	resolved := &targeting.ResolvedPackages{
		IdentityConfigs: map[string]*targeting.PackageIdentityConfig{
			"pkg-food": {TargetSegments: &targeting.SegmentRule{AnyOf: []string{"cooking_fans"}}},
		},
	}

	t.Run("InSegment", func(t *testing.T) {
		resp, err := engine.EvaluateIdentityResolved(ctx, resolved, &tmproto.IdentityMatchRequest{
			RequestID:  "id-hit",
			Identities: []tmproto.IdentityToken{{UserToken: "tok-alice"}},
			PackageIDs: []string{"pkg-food"},
		})
		require.NoError(t, err)
		assert.True(t, resp.Eligibility[0].Eligible)
	})

	t.Run("NotInSegment", func(t *testing.T) {
		resp, err := engine.EvaluateIdentityResolved(ctx, resolved, &tmproto.IdentityMatchRequest{
			RequestID:  "id-miss",
			Identities: []tmproto.IdentityToken{{UserToken: "tok-bob"}},
			PackageIDs: []string{"pkg-food"},
		})
		require.NoError(t, err)
		assert.False(t, resp.Eligibility[0].Eligible)
	})
}
