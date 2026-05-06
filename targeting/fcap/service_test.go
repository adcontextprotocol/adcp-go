package fcap

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestService_RecordCapAndIsCapped(t *testing.T) {
	store := NewMockStore()
	svc := New(store)
	ctx := context.Background()

	expireAt := time.Now().Add(time.Hour)
	field := Field{SellerAgentURL: "https://seller.example.com/agent", PackageID: "pkg-1"}

	require.NoError(t, svc.RecordCap(ctx, "user-123", []Field{field}, expireAt))

	capped, err := svc.IsCapped(ctx, "user-123", field)
	require.NoError(t, err)
	assert.True(t, capped, "should be capped after RecordCap")

	notCapped, err := svc.IsCapped(ctx, "user-456", field)
	require.NoError(t, err)
	assert.False(t, notCapped, "different user should not be capped")

	otherField := Field{SellerAgentURL: "https://seller.example.com/agent", PackageID: "pkg-2"}
	notCappedField, err := svc.IsCapped(ctx, "user-123", otherField)
	require.NoError(t, err)
	assert.False(t, notCappedField, "different package should not be capped")
}

func TestService_RecordCap_Empty(t *testing.T) {
	store := NewMockStore()
	svc := New(store)
	ctx := context.Background()

	require.NoError(t, svc.RecordCap(ctx, "user-x", nil, time.Now().Add(time.Hour)))
	require.NoError(t, svc.RecordCap(ctx, "user-x", []Field{}, time.Now().Add(time.Hour)))
}

func TestService_RecordCap_MultipleFieldsSameTTL(t *testing.T) {
	store := NewMockStore()
	svc := New(store)
	ctx := context.Background()

	expireAt := time.Now().Add(time.Hour)
	fields := []Field{
		{SellerAgentURL: "url1", PackageID: "pkg-1"},
		{SellerAgentURL: "url1", PackageID: "pkg-2"},
		{SellerAgentURL: "url2", PackageID: "pkg-1"},
	}
	require.NoError(t, svc.RecordCap(ctx, "user-multi", fields, expireAt))

	for _, f := range fields {
		capped, err := svc.IsCapped(ctx, "user-multi", f)
		require.NoError(t, err)
		assert.True(t, capped, "field %+v should be capped", f)
	}
}

func TestService_RecordCapBatch(t *testing.T) {
	store := NewMockStore()
	svc := New(store)
	ctx := context.Background()

	expire1 := time.Now().Add(time.Hour)
	expire2 := time.Now().Add(2 * time.Hour)

	batches := []CapBatch{
		{
			UserIdentity: "user-a",
			Fields:       []Field{{SellerAgentURL: "u1", PackageID: "p1"}},
			ExpireAt:     expire1,
		},
		{
			UserIdentity: "user-b",
			Fields: []Field{
				{SellerAgentURL: "u1", PackageID: "p1"},
				{SellerAgentURL: "u2", PackageID: "p2"},
			},
			ExpireAt: expire2,
		},
	}
	require.NoError(t, svc.RecordCapBatch(ctx, batches))

	capped, _ := svc.IsCapped(ctx, "user-a", Field{SellerAgentURL: "u1", PackageID: "p1"})
	assert.True(t, capped)
	capped, _ = svc.IsCapped(ctx, "user-b", Field{SellerAgentURL: "u2", PackageID: "p2"})
	assert.True(t, capped)
	capped, _ = svc.IsCapped(ctx, "user-a", Field{SellerAgentURL: "u2", PackageID: "p2"})
	assert.False(t, capped, "user-a should not have user-b's fields")
}

func TestService_RecordCapBatch_SkipsEmpty(t *testing.T) {
	store := NewMockStore()
	svc := New(store)
	ctx := context.Background()

	require.NoError(t, svc.RecordCapBatch(ctx, nil))
	require.NoError(t, svc.RecordCapBatch(ctx, []CapBatch{
		{UserIdentity: "user-empty", Fields: nil, ExpireAt: time.Now().Add(time.Hour)},
	}))
}

func TestService_IsCappedBatch(t *testing.T) {
	store := NewMockStore()
	svc := New(store)
	ctx := context.Background()

	expireAt := time.Now().Add(time.Hour)
	require.NoError(t, svc.RecordCap(ctx, "user-1", []Field{
		{SellerAgentURL: "u1", PackageID: "p1"},
	}, expireAt))
	require.NoError(t, svc.RecordCap(ctx, "user-2", []Field{
		{SellerAgentURL: "u1", PackageID: "p2"},
	}, expireAt))

	lookups := []CapLookup{
		{UserIdentity: "user-1", Field: Field{SellerAgentURL: "u1", PackageID: "p1"}}, // capped
		{UserIdentity: "user-1", Field: Field{SellerAgentURL: "u1", PackageID: "p2"}}, // not capped
		{UserIdentity: "user-2", Field: Field{SellerAgentURL: "u1", PackageID: "p2"}}, // capped
		{UserIdentity: "user-3", Field: Field{SellerAgentURL: "u1", PackageID: "p1"}}, // not capped
	}
	results, err := svc.IsCappedBatch(ctx, lookups)
	require.NoError(t, err)
	assert.Equal(t, []bool{true, false, true, false}, results)
}

func TestService_IsCappedBatch_Empty(t *testing.T) {
	store := NewMockStore()
	svc := New(store)
	ctx := context.Background()

	res, err := svc.IsCappedBatch(ctx, nil)
	require.NoError(t, err)
	assert.Nil(t, res)
}

func TestIdentityKey_Stable(t *testing.T) {
	k1 := identityKey("user-abc")
	k2 := identityKey("user-abc")
	assert.Equal(t, k1, k2)
	assert.True(t, len(k1) > len(keyPrefix), "expected hashed key")
	assert.True(t, hasPrefix(k1, keyPrefix))
}

// TestFieldString_FormatPinned locks down the exact on-disk field-name format.
// Changing the delimiter or order would silently invalidate every existing
// field across all running deployments — this test makes that change loud.
func TestFieldString_FormatPinned(t *testing.T) {
	f := Field{SellerAgentURL: "https://seller.example.com:8080/agent", PackageID: "pkg-1"}
	assert.Equal(t, "https://seller.example.com:8080/agent:pkg-1", fieldString(f))
	assert.Equal(t, ":", fieldDelimiter)
}

func TestMockStore_FieldExpiresAtTTL(t *testing.T) {
	store := NewMockStore()
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	store.Now = func() time.Time { return now }

	svc := New(store)
	ctx := context.Background()

	field := Field{SellerAgentURL: "url", PackageID: "pkg-1"}
	require.NoError(t, svc.RecordCap(ctx, "user-ttl", []Field{field}, now.Add(time.Hour)))

	capped, err := svc.IsCapped(ctx, "user-ttl", field)
	require.NoError(t, err)
	assert.True(t, capped, "before expireAt, field should be present")

	store.Now = func() time.Time { return now.Add(2 * time.Hour) }
	capped, err = svc.IsCapped(ctx, "user-ttl", field)
	require.NoError(t, err)
	assert.False(t, capped, "after expireAt, mock should treat field as absent")
}

func hasPrefix(s, prefix string) bool {
	return len(s) >= len(prefix) && s[:len(prefix)] == prefix
}
