package audience

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestService_Upsert_AddOnly(t *testing.T) {
	svc := New(NewMockStore())
	ctx := context.Background()

	require.NoError(t, svc.Upsert(ctx, AudienceUpsert{
		AudienceID: "cooking_fans",
		Add: []Member{
			{UserToken: "user-a", Score: 0.8},
			{UserToken: "user-b"},
		},
	}))

	capped, err := svc.IsMember(ctx, "user-a", "cooking_fans")
	require.NoError(t, err)
	assert.True(t, capped)

	memberships, err := svc.Memberships(ctx, "user-a")
	require.NoError(t, err)
	assert.Equal(t, 0.8, memberships["cooking_fans"])

	memberships, err = svc.Memberships(ctx, "user-b")
	require.NoError(t, err)
	assert.Equal(t, 0.0, memberships["cooking_fans"])
}

func TestService_Upsert_Remove(t *testing.T) {
	svc := New(NewMockStore())
	ctx := context.Background()

	require.NoError(t, svc.Upsert(ctx, AudienceUpsert{
		AudienceID: "tech",
		Add:        []Member{{UserToken: "user-x", Score: 1.0}},
	}))
	require.NoError(t, svc.Upsert(ctx, AudienceUpsert{
		AudienceID: "tech",
		Remove:     []string{"user-x"},
	}))

	in, err := svc.IsMember(ctx, "user-x", "tech")
	require.NoError(t, err)
	assert.False(t, in, "user-x should be removed")
}

func TestService_Upsert_Empty(t *testing.T) {
	svc := New(NewMockStore())
	ctx := context.Background()

	require.NoError(t, svc.Upsert(ctx, AudienceUpsert{AudienceID: "noop"}))
	require.NoError(t, svc.UpsertBatch(ctx, nil))
}

func TestService_UpsertBatch_MultipleAudiences(t *testing.T) {
	svc := New(NewMockStore())
	ctx := context.Background()

	require.NoError(t, svc.UpsertBatch(ctx, []AudienceUpsert{
		{AudienceID: "a1", Add: []Member{{UserToken: "u1"}, {UserToken: "u2"}}},
		{AudienceID: "a2", Add: []Member{{UserToken: "u1", Score: 0.5}}},
	}))

	m, err := svc.Memberships(ctx, "u1")
	require.NoError(t, err)
	assert.Equal(t, 0.0, m["a1"])
	assert.Equal(t, 0.5, m["a2"])

	m, err = svc.Memberships(ctx, "u2")
	require.NoError(t, err)
	_, has := m["a1"]
	assert.True(t, has)
	_, has = m["a2"]
	assert.False(t, has)
}

func TestService_DeleteAudience(t *testing.T) {
	svc := New(NewMockStore())
	ctx := context.Background()

	require.NoError(t, svc.UpsertBatch(ctx, []AudienceUpsert{
		{AudienceID: "del-me", Add: []Member{{UserToken: "u1"}, {UserToken: "u2"}}},
		{AudienceID: "keep-me", Add: []Member{{UserToken: "u1"}}},
	}))

	require.NoError(t, svc.DeleteAudience(ctx, "del-me"))

	in, _ := svc.IsMember(ctx, "u1", "del-me")
	assert.False(t, in)
	in, _ = svc.IsMember(ctx, "u2", "del-me")
	assert.False(t, in)
	in, _ = svc.IsMember(ctx, "u1", "keep-me")
	assert.True(t, in, "unrelated audience must survive delete")
}

func TestService_DeleteAudience_Missing(t *testing.T) {
	svc := New(NewMockStore())
	ctx := context.Background()
	require.NoError(t, svc.DeleteAudience(ctx, "never-existed"))
}

func TestService_IsMemberBatch(t *testing.T) {
	svc := New(NewMockStore())
	ctx := context.Background()

	require.NoError(t, svc.UpsertBatch(ctx, []AudienceUpsert{
		{AudienceID: "a1", Add: []Member{{UserToken: "u1"}}},
		{AudienceID: "a2", Add: []Member{{UserToken: "u2"}}},
	}))

	results, err := svc.IsMemberBatch(ctx, []MembershipLookup{
		{UserToken: "u1", AudienceID: "a1"},
		{UserToken: "u1", AudienceID: "a2"},
		{UserToken: "u2", AudienceID: "a2"},
		{UserToken: "u3", AudienceID: "a1"},
	})
	require.NoError(t, err)
	assert.Equal(t, []bool{true, false, true, false}, results)
}

func TestService_MembershipsBatch(t *testing.T) {
	svc := New(NewMockStore())
	ctx := context.Background()

	require.NoError(t, svc.UpsertBatch(ctx, []AudienceUpsert{
		{AudienceID: "cooking", Add: []Member{{UserToken: "alice", Score: 0.9}}},
		{AudienceID: "tech", Add: []Member{{UserToken: "alice", Score: 0.4}, {UserToken: "bob"}}},
	}))

	results, err := svc.MembershipsBatch(ctx, []string{"alice", "bob", "missing"})
	require.NoError(t, err)
	require.Len(t, results, 3)

	assert.Equal(t, 0.9, results[0]["cooking"])
	assert.Equal(t, 0.4, results[0]["tech"])

	assert.Equal(t, 0.0, results[1]["tech"])
	_, hasCooking := results[1]["cooking"]
	assert.False(t, hasCooking)

	assert.Empty(t, results[2])
}

func TestService_Upsert_AddThenReAddOverwritesScore(t *testing.T) {
	svc := New(NewMockStore())
	ctx := context.Background()

	require.NoError(t, svc.Upsert(ctx, AudienceUpsert{
		AudienceID: "premium",
		Add:        []Member{{UserToken: "u1", Score: 0.5}},
	}))
	require.NoError(t, svc.Upsert(ctx, AudienceUpsert{
		AudienceID: "premium",
		Add:        []Member{{UserToken: "u1", Score: 0.95}},
	}))

	m, err := svc.Memberships(ctx, "u1")
	require.NoError(t, err)
	assert.Equal(t, 0.95, m["premium"])
}

func TestHashIdentity_Stable(t *testing.T) {
	a := hashIdentity("user-abc")
	b := hashIdentity("user-abc")
	assert.Equal(t, a, b)
	assert.Equal(t, 32, len(a), "16 bytes hex = 32 chars")
}

func TestKeyPrefix_Pinned(t *testing.T) {
	assert.Equal(t, "audience:user:", userKeyPrefix)
	assert.Equal(t, "audience:list:", audienceKeyPrefix)
}
