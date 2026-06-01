//go:build integration

package redisstore

import (
	"context"
	"fmt"
	"sort"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"

	"github.com/adcontextprotocol/adcp-go/registry"
)

// startValkey9 spins up a Valkey 9 container and returns a connected
// go-redis client wired to a fresh Store. Skipped when Docker isn't
// reachable.
func startValkey9(t *testing.T) (*redis.Client, *Store) {
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
	t.Cleanup(func() { _ = container.Terminate(context.Background()) })

	host, err := container.Host(ctx)
	require.NoError(t, err)
	port, err := container.MappedPort(ctx, "6379/tcp")
	require.NoError(t, err)

	client := redis.NewClient(&redis.Options{Addr: fmt.Sprintf("%s:%s", host, port.Port())})
	t.Cleanup(func() { _ = client.Close() })

	return client, New(client, Options{KeyPrefix: fmt.Sprintf("test:%d", time.Now().UnixNano())})
}

func TestIntegration_Cursor(t *testing.T) {
	_, store := startValkey9(t)
	ctx := context.Background()

	cur, err := store.Load(ctx)
	require.NoError(t, err)
	assert.Empty(t, cur)

	require.NoError(t, store.Save(ctx, "01HGW..."))
	cur, err = store.Load(ctx)
	require.NoError(t, err)
	assert.Equal(t, "01HGW...", cur)

	require.NoError(t, store.Save(ctx, ""))
	cur, err = store.Load(ctx)
	require.NoError(t, err)
	assert.Empty(t, cur)
}

func TestIntegration_Properties(t *testing.T) {
	_, store := startValkey9(t)
	ctx := context.Background()

	props, err := store.LoadProperties(ctx)
	require.NoError(t, err)
	assert.Empty(t, props)

	p1 := &registry.Property{PropertyID: "pub1.example.com/home", PropertyRID: 1001, PropertyType: "website", Domain: "example.com", Placements: []string{"top"}}
	p2 := &registry.Property{PropertyID: "pub2.example.com/home", PropertyRID: 1002, PropertyType: "website", Domain: "two.example.com"}

	require.NoError(t, store.PutProperty(ctx, p1))
	require.NoError(t, store.PutProperty(ctx, p2))
	require.NoError(t, store.PutProperty(ctx, p1)) // idempotent

	props, err = store.LoadProperties(ctx)
	require.NoError(t, err)
	sort.Slice(props, func(i, j int) bool { return props[i].PropertyID < props[j].PropertyID })
	require.Len(t, props, 2)
	assert.Equal(t, uint64(1001), props[0].PropertyRID)
	assert.Equal(t, []string{"top"}, props[0].Placements)
	assert.Equal(t, uint64(1002), props[1].PropertyRID)

	require.NoError(t, store.RemoveProperty(ctx, "pub1.example.com/home"))
	require.NoError(t, store.RemoveProperty(ctx, "pub1.example.com/home")) // idempotent

	props, err = store.LoadProperties(ctx)
	require.NoError(t, err)
	require.Len(t, props, 1)
	assert.Equal(t, "pub2.example.com/home", props[0].PropertyID)

	require.NoError(t, store.ClearProperties(ctx))
	props, err = store.LoadProperties(ctx)
	require.NoError(t, err)
	assert.Empty(t, props)
}

func TestIntegration_Agents(t *testing.T) {
	_, store := startValkey9(t)
	ctx := context.Background()

	a := &registry.AgentProfile{AgentURL: "https://agent.example.com", Channels: []string{"ctv", "web"}, PropertyCount: 42}
	require.NoError(t, store.PutAgent(ctx, a))
	require.NoError(t, store.PutAgent(ctx, a)) // idempotent

	agents, err := store.LoadAgents(ctx)
	require.NoError(t, err)
	require.Len(t, agents, 1)
	assert.Equal(t, 42, agents[0].PropertyCount)
	assert.Equal(t, []string{"ctv", "web"}, agents[0].Channels)

	require.NoError(t, store.RemoveAgent(ctx, "https://agent.example.com"))
	require.NoError(t, store.RemoveAgent(ctx, "https://agent.example.com")) // idempotent

	agents, err = store.LoadAgents(ctx)
	require.NoError(t, err)
	assert.Empty(t, agents)
}

func TestIntegration_Auth(t *testing.T) {
	_, store := startValkey9(t)
	ctx := context.Background()

	entry1 := registry.AuthorizationEntry{
		AgentURL: "https://a.example.com", PublisherDomain: "pub.example.com",
		AuthorizationType: "publisher_properties",
	}
	entry2 := registry.AuthorizationEntry{
		AgentURL: "https://a.example.com", PublisherDomain: "pub.example.com",
		AuthorizationType: "property_ids", PropertyIDs: []string{"prop-1", "prop-2"},
	}
	entry3 := registry.AuthorizationEntry{
		AgentURL: "https://a.example.com", PublisherDomain: "other.example.com",
		AuthorizationType: "publisher_properties",
	}
	entry4 := registry.AuthorizationEntry{
		AgentURL: "https://b.example.com", PublisherDomain: "pub.example.com",
		AuthorizationType: "publisher_properties",
	}

	for _, e := range []registry.AuthorizationEntry{entry1, entry2, entry3, entry4} {
		require.NoError(t, store.PutAuth(ctx, e))
	}
	require.NoError(t, store.PutAuth(ctx, entry1)) // idempotent

	loaded, err := store.LoadAuth(ctx)
	require.NoError(t, err)
	assert.Len(t, loaded, 4)

	require.NoError(t, store.RemoveAuthEntry(ctx, "https://a.example.com", "pub.example.com"))
	require.NoError(t, store.RemoveAuthEntry(ctx, "https://a.example.com", "pub.example.com")) // idempotent

	loaded, err = store.LoadAuth(ctx)
	require.NoError(t, err)
	assert.Len(t, loaded, 2)

	require.NoError(t, store.RemoveAuthAgent(ctx, "https://a.example.com"))
	require.NoError(t, store.RemoveAuthAgent(ctx, "https://a.example.com")) // idempotent

	loaded, err = store.LoadAuth(ctx)
	require.NoError(t, err)
	require.Len(t, loaded, 1)
	assert.Equal(t, "https://b.example.com", loaded[0].AgentURL)

	require.NoError(t, store.ClearAuth(ctx))
	loaded, err = store.LoadAuth(ctx)
	require.NoError(t, err)
	assert.Empty(t, loaded)
}

func TestIntegration_KeyPrefixIsolation(t *testing.T) {
	_, baseStore := startValkey9(t)
	client := baseStore.client

	storeA := New(client, Options{KeyPrefix: "envA"})
	storeB := New(client, Options{KeyPrefix: "envB"})
	ctx := context.Background()

	require.NoError(t, storeA.PutProperty(ctx, &registry.Property{PropertyID: "p-a", PropertyRID: 1, Domain: "a.example"}))
	require.NoError(t, storeB.PutProperty(ctx, &registry.Property{PropertyID: "p-b", PropertyRID: 2, Domain: "b.example"}))

	propsA, err := storeA.LoadProperties(ctx)
	require.NoError(t, err)
	require.Len(t, propsA, 1)
	assert.Equal(t, "p-a", propsA[0].PropertyID)

	propsB, err := storeB.LoadProperties(ctx)
	require.NoError(t, err)
	require.Len(t, propsB, 1)
	assert.Equal(t, "p-b", propsB[0].PropertyID)
}
