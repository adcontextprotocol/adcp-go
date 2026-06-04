//go:build integration

package glidestore

import (
	"context"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
	glide "github.com/valkey-io/valkey-glide/go/v2"
	"github.com/valkey-io/valkey-glide/go/v2/config"
)

// startValkey9 spins up a single Valkey 9 container and returns a
// connected glide.Client plus a standalone Store wrapping it. Skips
// the test when Docker is unavailable.
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
	t.Cleanup(func() { _ = container.Terminate(context.Background()) })

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
