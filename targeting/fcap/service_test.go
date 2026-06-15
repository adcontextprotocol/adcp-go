package fcap

import (
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestService_RecordCapAndIsCapped(t *testing.T) {
	store := NewMockStore()
	svc := New(store)
	ctx := t.Context()

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
	ctx := t.Context()

	require.NoError(t, svc.RecordCap(ctx, "user-x", nil, time.Now().Add(time.Hour)))
	require.NoError(t, svc.RecordCap(ctx, "user-x", []Field{}, time.Now().Add(time.Hour)))
}

func TestService_RecordCap_MultipleFieldsSameTTL(t *testing.T) {
	store := NewMockStore()
	svc := New(store)
	ctx := t.Context()

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
	ctx := t.Context()

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
	ctx := t.Context()

	require.NoError(t, svc.RecordCapBatch(ctx, nil))
	require.NoError(t, svc.RecordCapBatch(ctx, []CapBatch{
		{UserIdentity: "user-empty", Fields: nil, ExpireAt: time.Now().Add(time.Hour)},
	}))
}

func TestService_IsCappedBatch(t *testing.T) {
	store := NewMockStore()
	svc := New(store)
	ctx := t.Context()

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
	ctx := t.Context()

	res, err := svc.IsCappedBatch(ctx, nil)
	require.NoError(t, err)
	assert.Nil(t, res)
}

func TestService_IsCappedAny(t *testing.T) {
	store := NewMockStore()
	svc := New(store)
	ctx := t.Context()

	expireAt := time.Now().Add(time.Hour)
	require.NoError(t, svc.RecordCap(ctx, "user-a", []Field{
		{SellerAgentURL: "s", PackageID: "p1"},
	}, expireAt))
	require.NoError(t, svc.RecordCap(ctx, "user-b", []Field{
		{SellerAgentURL: "s", PackageID: "p3"},
	}, expireAt))

	identities := []string{"user-a", "user-b", "user-c"}
	fields := []Field{
		{SellerAgentURL: "s", PackageID: "p1"}, // user-a is capped
		{SellerAgentURL: "s", PackageID: "p2"}, // no one capped
		{SellerAgentURL: "s", PackageID: "p3"}, // user-b is capped
	}

	got, err := svc.IsCappedAny(ctx, identities, fields)
	require.NoError(t, err)
	assert.Equal(t, []bool{true, false, true}, got)
}

func TestService_IsCappedAny_EmptyInputs(t *testing.T) {
	svc := New(NewMockStore())
	ctx := t.Context()

	// No identities but non-empty fields: nothing can be capped, so the result
	// is an all-false slice of length len(fields) — NOT nil. Callers index the
	// result by field, so a nil result here reads out of range and panics.
	got, err := svc.IsCappedAny(ctx, nil, []Field{
		{SellerAgentURL: "s", PackageID: "p1"},
		{SellerAgentURL: "s", PackageID: "p2"},
	})
	require.NoError(t, err)
	assert.Equal(t, []bool{false, false}, got)

	// Empty fields returns nil regardless of identities (no fields to report).
	got, err = svc.IsCappedAny(ctx, []string{"u"}, nil)
	require.NoError(t, err)
	assert.Nil(t, got)

	got, err = svc.IsCappedAny(ctx, nil, nil)
	require.NoError(t, err)
	assert.Nil(t, got)
}

// TestService_IsCappedAny_PoolReuse exercises the sync.Pool path across
// concurrent goroutines and varied batch sizes. A reused buffer that leaks
// state across calls would show up as wrong results, not as a panic, so the
// test reads results back rather than relying on the race detector alone.
// Run with `go test -race ./targeting/fcap/` for the full check.
func TestService_IsCappedAny_PoolReuse(t *testing.T) {
	store := NewMockStore()
	svc := New(store)
	ctx := t.Context()
	expireAt := time.Now().Add(time.Hour)
	require.NoError(t, svc.RecordCap(ctx, "u-hot", []Field{
		{SellerAgentURL: "s", PackageID: "hot"},
	}, expireAt))

	var wg sync.WaitGroup
	const workers = 16
	const iters = 200
	for range workers {
		wg.Go(func() {
			for i := range iters {
				identities := []string{"u-hot", "u-cold"}
				fields := []Field{
					{SellerAgentURL: "s", PackageID: "hot"},
					{SellerAgentURL: "s", PackageID: "cold"},
				}
				// Occasionally vary size to force the pool to grow.
				if i%5 == 0 {
					fields = append(fields, Field{SellerAgentURL: "s", PackageID: "extra"})
				}
				got, err := svc.IsCappedAny(ctx, identities, fields)
				require.NoError(t, err)
				require.Equal(t, len(fields), len(got))
				assert.True(t, got[0], "hot field should be capped on every call")
				assert.False(t, got[1], "cold field should never be capped")
				if len(got) > 2 {
					assert.False(t, got[2], "extra field should never be capped")
				}
			}
		})
	}
	wg.Wait()
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
	ctx := t.Context()

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
