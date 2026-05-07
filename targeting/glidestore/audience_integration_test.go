//go:build integration

package glidestore

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/adcontextprotocol/adcp-go/targeting/audience"
	"github.com/adcontextprotocol/adcp-go/targeting/internal/identityhash"
)

// audienceMember is a single-pair helper around IsMemberBatch.
func audienceMember(t *testing.T, svc *audience.Service, userToken, audienceID string) bool {
	t.Helper()
	r, err := svc.IsMemberBatch(context.Background(), []audience.MembershipLookup{
		{UserToken: userToken, AudienceID: audienceID},
	})
	require.NoError(t, err)
	require.Len(t, r, 1)
	return r[0]
}

func TestIntegration_AudienceStore_UpsertAndIsMember(t *testing.T) {
	_, store := startValkey9(t)
	svc := audience.New(store)

	require.NoError(t, svc.Upsert(context.Background(), audience.AudienceUpsert{
		AudienceID: "cooking_fans",
		Add: []audience.Member{
			{UserToken: "id5-alice", Score: 0.9},
			{UserToken: "id5-bob"},
		},
	}))

	assert.True(t, audienceMember(t, svc, "id5-alice", "cooking_fans"))
	assert.False(t, audienceMember(t, svc, "id5-charlie", "cooking_fans"))
}

func TestIntegration_AudienceStore_Memberships(t *testing.T) {
	_, store := startValkey9(t)
	svc := audience.New(store)
	ctx := context.Background()

	require.NoError(t, svc.UpsertBatch(ctx, []audience.AudienceUpsert{
		{AudienceID: "cooking", Add: []audience.Member{{UserToken: "alice", Score: 0.9}}},
		{AudienceID: "tech", Add: []audience.Member{{UserToken: "alice", Score: 0.4}, {UserToken: "bob"}}},
	}))

	m, err := svc.Memberships(ctx, "alice")
	require.NoError(t, err)
	assert.Equal(t, 0.9, m["cooking"])
	assert.Equal(t, 0.4, m["tech"])

	results, err := svc.MembershipsBatch(ctx, []string{"alice", "bob", "missing"})
	require.NoError(t, err)
	require.Len(t, results, 3)
	assert.Equal(t, 0.9, results[0]["cooking"])
	assert.Equal(t, 0.4, results[0]["tech"])
	_, bobInTech := results[1]["tech"]
	assert.True(t, bobInTech, "bob should be in tech (score 0, membership-only)")
	assert.Equal(t, 0.0, results[1]["tech"])
	assert.Empty(t, results[2])
}

func TestIntegration_AudienceStore_Remove(t *testing.T) {
	_, store := startValkey9(t)
	svc := audience.New(store)
	ctx := context.Background()

	require.NoError(t, svc.UpsertBatch(ctx, []audience.AudienceUpsert{
		{AudienceID: "promo", Add: []audience.Member{{UserToken: "u1"}, {UserToken: "u2"}, {UserToken: "u3"}}},
		{AudienceID: "keep-me", Add: []audience.Member{{UserToken: "u1"}}},
	}))

	require.NoError(t, svc.Upsert(ctx, audience.AudienceUpsert{
		AudienceID: "promo",
		Remove:     []string{"u3"},
	}))

	assert.False(t, audienceMember(t, svc, "u3", "promo"), "u3 removed individually")
	assert.True(t, audienceMember(t, svc, "u1", "promo"), "u1 still a member")
	assert.True(t, audienceMember(t, svc, "u2", "promo"), "u2 still a member")
	assert.True(t, audienceMember(t, svc, "u1", "keep-me"), "unrelated audience unaffected")
}

func TestIntegration_AudienceStore_IsMemberBatch(t *testing.T) {
	_, store := startValkey9(t)
	svc := audience.New(store)
	ctx := context.Background()

	require.NoError(t, svc.UpsertBatch(ctx, []audience.AudienceUpsert{
		{AudienceID: "a1", Add: []audience.Member{{UserToken: "u1"}}},
		{AudienceID: "a2", Add: []audience.Member{{UserToken: "u2"}}},
	}))

	results, err := svc.IsMemberBatch(ctx, []audience.MembershipLookup{
		{UserToken: "u1", AudienceID: "a1"},
		{UserToken: "u1", AudienceID: "a2"},
		{UserToken: "u2", AudienceID: "a2"},
		{UserToken: "u3", AudienceID: "a1"},
	})
	require.NoError(t, err)
	assert.Equal(t, []bool{true, false, true, false}, results)
}

func TestIntegration_AudienceStore_RawHashShape(t *testing.T) {
	client, store := startValkey9(t)
	svc := audience.New(store)
	ctx := context.Background()

	require.NoError(t, svc.Upsert(ctx, audience.AudienceUpsert{
		AudienceID: "premium",
		Add:        []audience.Member{{UserToken: "u-shape", Score: 0.75}},
	}))

	// Validate the on-disk schema directly: user-keyed hash holds the score.
	hash := identityhash.Hash("u-shape")
	userKey := "audience:user:" + hash

	fields, err := client.HGetAll(ctx, userKey)
	require.NoError(t, err)
	assert.Equal(t, "0.75", fields["premium"])
}
