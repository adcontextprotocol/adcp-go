//go:build integration

package redisstore

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	tcnetwork "github.com/testcontainers/testcontainers-go/network"
	"github.com/testcontainers/testcontainers-go/wait"

	"github.com/adcontextprotocol/adcp-go/targeting/internal/clusterslot"
)

// startValkeyCluster bootstraps a real N-primary Valkey 9 cluster via
// `valkey-cli --cluster create` and returns a standalone client to
// each node in the same order they were passed to cluster create.
func startValkeyCluster(t *testing.T, n int) []*redis.Client {
	t.Helper()
	ctx := context.Background()

	net, err := tcnetwork.New(ctx)
	if err != nil {
		t.Skipf("Docker network unavailable, skipping cluster parity test: %v", err)
	}
	t.Cleanup(func() { _ = net.Remove(context.Background()) })

	containers := make([]testcontainers.Container, n)
	standalone := make([]*redis.Client, n)
	internalAddrs := make([]string, n)

	for i := 0; i < n; i++ {
		alias := fmt.Sprintf("vk-%d", i)
		req := testcontainers.ContainerRequest{
			Image: "valkey/valkey:9-alpine",
			Cmd: []string{
				"valkey-server",
				"--port", "6379",
				"--cluster-enabled", "yes",
				"--cluster-config-file", "nodes.conf",
				"--cluster-node-timeout", "5000",
				"--appendonly", "no",
				"--save", "",
			},
			ExposedPorts: []string{"6379/tcp"},
			Hostname:     alias,
			Networks:     []string{net.Name},
			NetworkAliases: map[string][]string{
				net.Name: {alias},
			},
			WaitingFor: wait.ForListeningPort("6379/tcp").WithStartupTimeout(30 * time.Second),
		}
		container, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
			ContainerRequest: req,
			Started:          true,
		})
		if err != nil {
			t.Skipf("Docker unavailable, skipping cluster parity test: %v", err)
		}
		t.Cleanup(func() { _ = container.Terminate(context.Background()) })
		containers[i] = container

		host, err := container.Host(ctx)
		require.NoError(t, err)
		port, err := container.MappedPort(ctx, "6379/tcp")
		require.NoError(t, err)
		c := redis.NewClient(&redis.Options{Addr: fmt.Sprintf("%s:%s", host, port.Port())})
		t.Cleanup(func() { _ = c.Close() })
		standalone[i] = c
		internalAddrs[i] = fmt.Sprintf("%s:6379", alias)
	}

	createCmd := append([]string{"valkey-cli", "--cluster", "create"}, internalAddrs...)
	createCmd = append(createCmd, "--cluster-replicas", "0", "--cluster-yes")
	code, _, err := containers[0].Exec(ctx, createCmd)
	require.NoErrorf(t, err, "cluster create exec: %v", err)
	require.Equalf(t, 0, code, "cluster create returned non-zero exit")

	require.Eventuallyf(t, func() bool {
		info, err := standalone[0].ClusterInfo(ctx).Result()
		return err == nil && strings.Contains(info, "cluster_state:ok")
	}, 30*time.Second, 200*time.Millisecond, "cluster did not reach state:ok")

	return standalone
}

// reportedBoundaries returns, in node-ordinal order, the highest slot
// each cluster node owns according to CLUSTER SLOTS on that node. Node
// ordinals match the order passed to `valkey-cli --cluster create`,
// which we identify by querying each standalone client for its node
// id and matching against the CLUSTER SLOTS report.
func reportedBoundaries(t *testing.T, clients []*redis.Client) []int {
	t.Helper()
	ctx := context.Background()

	nodeIDs := make([]string, len(clients))
	for i, c := range clients {
		id, err := c.ClusterMyID(ctx).Result()
		require.NoErrorf(t, err, "CLUSTER MYID for client %d", i)
		require.NotEmptyf(t, id, "empty node id for client %d", i)
		nodeIDs[i] = id
	}

	slots, err := clients[0].ClusterSlots(ctx).Result()
	require.NoError(t, err)

	endByNode := make(map[string]int, len(clients))
	for _, sr := range slots {
		require.NotEmpty(t, sr.Nodes, "CLUSTER SLOTS returned a range with no nodes")
		nodeID := sr.Nodes[0].ID
		if sr.End > endByNode[nodeID] {
			endByNode[nodeID] = sr.End
		}
	}

	out := make([]int, len(clients))
	for i, id := range nodeIDs {
		end, ok := endByNode[id]
		require.Truef(t, ok, "node %d (id=%s) has no slots in CLUSTER SLOTS", i, id)
		out[i] = end
	}
	return out
}

// TestIntegration_ShardMapMatchesClusterSlots is the safety net for
// frequency-writer interop: it bootstraps a real Valkey cluster the
// same way rtkv-bootstrap does, reads back the slot allocation Valkey
// produced, and asserts every boundary matches ShardMap's. If a
// future Valkey release changes `--cluster create`'s slot
// distribution, this test fails loudly.
func TestIntegration_ShardMapMatchesClusterSlots(t *testing.T) {
	for _, n := range []int{3, 4, 5} {
		t.Run(fmt.Sprintf("N=%d", n), func(t *testing.T) {
			clients := startValkeyCluster(t, n)
			want := reportedBoundaries(t, clients)
			got := clusterslot.NewShardMap(n).LastSlots()
			assert.Equalf(t, want, got, "ShardMap boundaries must match Valkey cluster slot allocation for N=%d", n)
		})
	}
}
