//go:build integration

package glidestore

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
	glide "github.com/valkey-io/valkey-glide/go/v2"
	"github.com/valkey-io/valkey-glide/go/v2/config"
	glideoptions "github.com/valkey-io/valkey-glide/go/v2/options"

	"github.com/adcontextprotocol/adcp-go/targeting/audience"
	"github.com/adcontextprotocol/adcp-go/targeting/fcap"
	"github.com/adcontextprotocol/adcp-go/targeting/internal/clusterslot"
	"github.com/adcontextprotocol/adcp-go/targeting/internal/identityhash"
)

// startValkeyShards spins up n independent Valkey 9 containers and
// returns a shadow-mode Store routing across them.
func startValkeyShards(t *testing.T, n int) (*Store, []*glide.Client) {
	t.Helper()
	ctx := context.Background()

	clients := make([]*glide.Client, n)
	for i := 0; i < n; i++ {
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
		c, err := glide.NewClient(cfg)
		require.NoError(t, err)
		t.Cleanup(c.Close)
		clients[i] = c
	}

	store, err := NewShadow(clients)
	require.NoError(t, err)
	return store, clients
}

// seedFcapShadow writes a single fcap marker directly to the shard that
// owns key.
func seedFcapShadow(t *testing.T, clients []*glide.Client, key, field string, expireAt time.Time) {
	t.Helper()
	idx := clusterslot.Shard(key, len(clients))
	opts := glideoptions.NewHSetExOptions().SetExpiry(glideoptions.NewExpiryAt(expireAt))
	_, err := clients[idx].HSetEx(context.Background(), key, map[string]string{field: "1"}, opts)
	require.NoError(t, err)
}

func TestIntegration_Shadow_SingleShard(t *testing.T) {
	store, clients := startValkeyShards(t, 1)
	require.Equal(t, 1, store.NumShards())
	require.Len(t, clients, 1)

	ctx := context.Background()
	key := "fcap:user-1"
	field := "https://seller.example.com/agent:pkg-1"
	seedFcapShadow(t, clients, key, field, time.Now().Add(2*time.Minute))

	exists, err := store.FieldExists(ctx, key, field)
	require.NoError(t, err)
	assert.True(t, exists)

	exists, err = store.FieldExists(ctx, key, "https://other:pkg-2")
	require.NoError(t, err)
	assert.False(t, exists)
}

func TestIntegration_Shadow_FcapBatchAcrossShards(t *testing.T) {
	store, clients := startValkeyShards(t, 3)
	require.Equal(t, 3, store.NumShards())
	ctx := context.Background()
	expireAt := time.Now().Add(5 * time.Minute)

	keys := make([]string, 3)
	for i := 0; i < 200 && (keys[0] == "" || keys[1] == "" || keys[2] == ""); i++ {
		k := fmt.Sprintf("fcap:user-%d", i)
		sh := clusterslot.Shard(k, 3)
		if keys[sh] == "" {
			keys[sh] = k
			seedFcapShadow(t, clients, k, "url:pkg", expireAt)
		}
	}
	for sh, k := range keys {
		require.NotEmpty(t, k, "no candidate key landed on shard %d", sh)
	}

	lookups := make([]fcap.FieldLookup, 0, len(keys)*2)
	want := make([]bool, 0, len(keys)*2)
	for _, k := range keys {
		lookups = append(lookups, fcap.FieldLookup{Key: k, Field: "url:pkg"})
		want = append(want, true)
		lookups = append(lookups, fcap.FieldLookup{Key: k, Field: "url:pkg-miss"})
		want = append(want, false)
	}

	got, err := store.FieldExistsBatch(ctx, lookups)
	require.NoError(t, err)
	assert.Equal(t, want, got)
}

func TestIntegration_Shadow_WritesAreReadOnly(t *testing.T) {
	store, _ := startValkeyShards(t, 2)
	ctx := context.Background()
	expireAt := time.Now().Add(time.Minute)

	err := store.SetFields(ctx, "fcap:x", map[string]string{"f": "1"}, expireAt)
	assert.True(t, errors.Is(err, ErrReadOnly))
	err = store.SetFieldsBatch(ctx, []fcap.FieldsBatch{{Key: "fcap:x", Fields: map[string]string{"f": "1"}, ExpireAt: expireAt}})
	assert.True(t, errors.Is(err, ErrReadOnly))
	err = store.HSetBatch(ctx, []audience.HSetItem{{Key: "audience:user:x", Field: "a", Value: "1"}})
	assert.True(t, errors.Is(err, ErrReadOnly))
	err = store.HDelBatch(ctx, []audience.HDelItem{{Key: "audience:user:x", Fields: []string{"a"}}})
	assert.True(t, errors.Is(err, ErrReadOnly))
	err = store.Set(ctx, "k", "v", 0)
	assert.True(t, errors.Is(err, ErrReadOnly))
	err = store.MSet(ctx, map[string]string{"k": "v"}, 0)
	assert.True(t, errors.Is(err, ErrReadOnly))
}

func TestIntegration_Shadow_AudienceMultiShard(t *testing.T) {
	store, clients := startValkeyShards(t, 3)
	ctx := context.Background()

	keys := make([]string, 0, 6)
	for i := 0; len(keys) < 6; i++ {
		k := fmt.Sprintf("audience:user:%d", i)
		idx := clusterslot.Shard(k, 3)
		_, err := clients[idx].HSet(ctx, k, map[string]string{"cooking": "0.9", "tech": "0.4"})
		require.NoError(t, err)
		keys = append(keys, k)
	}

	m, err := store.HGetAll(ctx, keys[0])
	require.NoError(t, err)
	assert.Equal(t, "0.9", m["cooking"])

	maps, err := store.HGetAllBatch(ctx, append(keys, "audience:user:missing"))
	require.NoError(t, err)
	require.Len(t, maps, len(keys)+1)
	for i := range keys {
		assert.Equal(t, "0.9", maps[i]["cooking"])
		assert.Equal(t, "0.4", maps[i]["tech"])
	}
	assert.Equal(t, map[string]string{}, maps[len(keys)])

	lookups := []audience.HLookup{
		{Key: keys[0], Field: "cooking"},
		{Key: keys[1], Field: "missing"},
		{Key: "audience:user:missing", Field: "cooking"},
	}
	hits, err := store.HExistsBatch(ctx, lookups)
	require.NoError(t, err)
	assert.Equal(t, []bool{true, false, false}, hits)
}

func TestIntegration_Shadow_FcapServiceWiring(t *testing.T) {
	store, clients := startValkeyShards(t, 3)
	svc := fcap.New(store)
	ctx := context.Background()

	expireAt := time.Now().Add(2 * time.Minute)
	identities := []string{"id5-a", "id5-b", "id5-c"}
	for _, id := range identities {
		seedFcapShadow(t, clients, "fcap:"+identityhash.Hash(id), "https://seller:pkg-a", expireAt)
	}

	field := fcap.Field{SellerAgentURL: "https://seller", PackageID: "pkg-a"}
	hits, err := svc.IsCappedBatch(ctx, []fcap.CapLookup{
		{UserIdentity: "id5-a", Field: field},
		{UserIdentity: "id5-b", Field: field},
		{UserIdentity: "id5-c", Field: field},
		{UserIdentity: "id5-unknown", Field: field},
	})
	require.NoError(t, err)
	assert.Equal(t, []bool{true, true, true, false}, hits)
}

func TestIntegration_Shadow_SetIntersect_CrossShardFails(t *testing.T) {
	store, _ := startValkeyShards(t, 3)
	ctx := context.Background()

	var a, b string
	for i := 0; i < 200; i++ {
		k := fmt.Sprintf("k-%d", i)
		if a == "" {
			a = k
			continue
		}
		if clusterslot.Shard(k, 3) != clusterslot.Shard(a, 3) {
			b = k
			break
		}
	}
	require.NotEmpty(t, b, "could not find cross-shard candidate")

	_, err := store.SetIntersect(ctx, a, b)
	assert.True(t, errors.Is(err, ErrCrossShard))
}
