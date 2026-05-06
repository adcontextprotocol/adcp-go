package audience

import (
	"context"
	"math"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// isMember is a single-pair helper around IsMemberBatch.
func isMember(t *testing.T, svc *Service, userToken, audienceID string) bool {
	t.Helper()
	results, err := svc.IsMemberBatch(context.Background(), []MembershipLookup{
		{UserToken: userToken, AudienceID: audienceID},
	})
	require.NoError(t, err)
	require.Len(t, results, 1)
	return results[0]
}

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

	assert.True(t, isMember(t, svc, "user-a", "cooking_fans"))

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

	assert.False(t, isMember(t, svc, "user-x", "tech"), "user-x should be removed")
}

func TestService_Upsert_AddAndRemove_AddWins(t *testing.T) {
	// Pins behaviour: when a single Upsert contains both Add and Remove for
	// the same (user, audience), removes run first so the Add is the final state.
	svc := New(NewMockStore())
	ctx := context.Background()

	require.NoError(t, svc.Upsert(ctx, AudienceUpsert{
		AudienceID: "promo",
		Add:        []Member{{UserToken: "u-flip", Score: 0.7}},
		Remove:     []string{"u-flip"},
	}))

	assert.True(t, isMember(t, svc, "u-flip", "promo"), "Add must win over Remove in the same Upsert")

	m, err := svc.Memberships(ctx, "u-flip")
	require.NoError(t, err)
	assert.Equal(t, 0.7, m["promo"])
}

func TestService_Upsert_RejectsNonFiniteScore(t *testing.T) {
	svc := New(NewMockStore())
	ctx := context.Background()

	for name, score := range map[string]float64{
		"NaN":     math.NaN(),
		"+Inf":    math.Inf(1),
		"-Inf":    math.Inf(-1),
	} {
		t.Run(name, func(t *testing.T) {
			err := svc.Upsert(ctx, AudienceUpsert{
				AudienceID: "x",
				Add:        []Member{{UserToken: "u", Score: score}},
			})
			require.Error(t, err)
			assert.Contains(t, err.Error(), "non-finite")
		})
	}
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

	assert.False(t, isMember(t, svc, "u1", "del-me"))
	assert.False(t, isMember(t, svc, "u2", "del-me"))
	assert.True(t, isMember(t, svc, "u1", "keep-me"), "unrelated audience must survive delete")
}

func TestService_DeleteAudience_ThenReUpsert(t *testing.T) {
	svc := New(NewMockStore())
	ctx := context.Background()

	require.NoError(t, svc.Upsert(ctx, AudienceUpsert{
		AudienceID: "transient",
		Add:        []Member{{UserToken: "u1"}},
	}))
	require.NoError(t, svc.DeleteAudience(ctx, "transient"))
	require.NoError(t, svc.Upsert(ctx, AudienceUpsert{
		AudienceID: "transient",
		Add:        []Member{{UserToken: "u2", Score: 0.3}},
	}))

	assert.False(t, isMember(t, svc, "u1", "transient"), "old member must not resurface")
	assert.True(t, isMember(t, svc, "u2", "transient"))

	m, err := svc.Memberships(ctx, "u2")
	require.NoError(t, err)
	assert.Equal(t, 0.3, m["transient"])
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

func TestService_Memberships_MissingUser(t *testing.T) {
	svc := New(NewMockStore())
	ctx := context.Background()

	m, err := svc.Memberships(ctx, "ghost")
	require.NoError(t, err)
	assert.NotNil(t, m, "Memberships must return a non-nil map for missing users")
	assert.Empty(t, m)
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

	assert.NotNil(t, results[2], "missing user must produce a non-nil empty map")
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

func TestKeyPrefix_Pinned(t *testing.T) {
	assert.Equal(t, "audience:user:", userKeyPrefix)
	assert.Equal(t, "audience:list:", audienceKeyPrefix)
}
