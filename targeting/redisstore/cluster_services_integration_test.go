//go:build integration

package redisstore

import (
	"context"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/adcontextprotocol/adcp-go/targeting/audience"
	"github.com/adcontextprotocol/adcp-go/targeting/fcap"
	"github.com/adcontextprotocol/adcp-go/targeting/identityhash"
)

// Standalone (single-instance) and shadow modes are already covered by
// audience_integration_test.go / shadow_integration_test.go. Cluster
// mode for the identity-agent surfaces (audience.Service, fcap.Service)
// was previously only exercised through cluster_parity_integration_test
// (slot-math correctness), not the Service round-trips. These tests
// close that gap so an actual Valkey-cluster regression in
// Upsert/IsMemberBatch or RecordCap/IsCappedAny surfaces here rather
// than at deploy time.

func TestIntegration_Cluster_AudienceServiceRoundtrip(t *testing.T) {
	nodes := startValkeyCluster(t, 3)
	addrs := make([]string, len(nodes))
	for i, c := range nodes {
		addrs[i] = c.Options().Addr
	}
	cluster := redis.NewClusterClient(&redis.ClusterOptions{Addrs: addrs})
	t.Cleanup(func() { _ = cluster.Close() })

	store := New(cluster)
	svc := audience.New(store)
	ctx := context.Background()

	require.NoError(t, svc.UpsertBatch(ctx, []audience.AudienceUpsert{
		{AudienceID: "cooking", Add: []audience.Member{
			{UserToken: identityhash.Hash("alice"), Score: 0.9},
			{UserToken: identityhash.Hash("bob")},
		}},
		{AudienceID: "tech", Add: []audience.Member{
			{UserToken: identityhash.Hash("alice"), Score: 0.4},
		}},
	}))

	results, err := svc.IsMemberBatch(ctx, []audience.MembershipLookup{
		{UserToken: identityhash.Hash("alice"), AudienceID: "cooking"},
		{UserToken: identityhash.Hash("alice"), AudienceID: "tech"},
		{UserToken: identityhash.Hash("bob"), AudienceID: "tech"},
	})
	require.NoError(t, err)
	require.Len(t, results, 3)
	assert.True(t, results[0], "alice ∈ cooking")
	assert.True(t, results[1], "alice ∈ tech")
	assert.False(t, results[2], "bob ∉ tech")
}

func TestIntegration_Cluster_FCapServiceRoundtrip(t *testing.T) {
	nodes := startValkeyCluster(t, 3)
	addrs := make([]string, len(nodes))
	for i, c := range nodes {
		addrs[i] = c.Options().Addr
	}
	cluster := redis.NewClusterClient(&redis.ClusterOptions{Addrs: addrs})
	t.Cleanup(func() { _ = cluster.Close() })

	store := New(cluster)
	svc := fcap.New(store)
	ctx := context.Background()

	const (
		userA = "user-a"
		userB = "user-b"
		pkg   = "pkg-1"
		seller = "https://seller.example.com/agent"
	)
	expireAt := time.Now().Add(time.Hour)

	require.NoError(t, svc.RecordCap(ctx, userA, []fcap.Field{
		{SellerAgentURL: seller, PackageID: pkg},
	}, expireAt))

	capped, err := svc.IsCappedAny(ctx, []string{userA, userB}, []fcap.Field{
		{SellerAgentURL: seller, PackageID: pkg},
	})
	require.NoError(t, err)
	require.Len(t, capped, 1)
	assert.True(t, capped[0],
		"package is capped because userA recorded a marker, and IsCappedAny is true when ANY identity has the marker")

	cappedAlone, err := svc.IsCappedBatch(ctx, []fcap.CapLookup{
		{UserIdentity: userB, Field: fcap.Field{SellerAgentURL: seller, PackageID: pkg}},
	})
	require.NoError(t, err)
	require.Len(t, cappedAlone, 1)
	assert.False(t, cappedAlone[0], "userB has no marker → not capped")
}
