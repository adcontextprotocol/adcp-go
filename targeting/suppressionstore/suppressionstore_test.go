package suppressionstore_test

import (
	"context"
	"testing"
	"time"

	"github.com/adcontextprotocol/adcp-go/targeting/suppressionstore"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const providerID = "provider-1"

func TestService_Roundtrip(t *testing.T) {
	ctx := context.Background()
	store := suppressionstore.NewMockStore()
	svc, err := suppressionstore.NewService(store)
	require.NoError(t, err)

	require.NoError(t, svc.SuppressProperty(ctx, providerID, "prop-A", time.Hour))
	require.NoError(t, svc.SuppressGeo(ctx, providerID, "US", time.Hour))

	snap, err := suppressionstore.NewSnapshot(suppressionstore.SnapshotConfig{
		Store: store, ProviderID: providerID,
	})
	require.NoError(t, err)
	require.NoError(t, snap.Load(ctx))

	got, _ := snap.IsPropertySuppressed(ctx, providerID, "prop-A")
	assert.True(t, got)
	got, _ = snap.IsPropertySuppressed(ctx, providerID, "prop-B")
	assert.False(t, got)
	got, _ = snap.IsGeoSuppressed(ctx, providerID, "US")
	assert.True(t, got)
	got, _ = snap.IsGeoSuppressed(ctx, providerID, "GB")
	assert.False(t, got)
}

func TestSnapshot_DifferentProviderIDReturnsFalse(t *testing.T) {
	ctx := context.Background()
	store := suppressionstore.NewMockStore()
	svc, _ := suppressionstore.NewService(store)
	require.NoError(t, svc.SuppressProperty(ctx, providerID, "prop-A", time.Hour))

	snap, _ := suppressionstore.NewSnapshot(suppressionstore.SnapshotConfig{Store: store, ProviderID: providerID})
	require.NoError(t, snap.Load(ctx))

	got, _ := snap.IsPropertySuppressed(ctx, "other-provider", "prop-A")
	assert.False(t, got, "snapshot must not answer for a different provider_id")
}

func TestSnapshot_RefreshPicksUpNewWrites(t *testing.T) {
	ctx := context.Background()
	store := suppressionstore.NewMockStore()
	svc, _ := suppressionstore.NewService(store)

	snap, _ := suppressionstore.NewSnapshot(suppressionstore.SnapshotConfig{Store: store, ProviderID: providerID})
	require.NoError(t, snap.Load(ctx))

	got, _ := snap.IsPropertySuppressed(ctx, providerID, "prop-A")
	assert.False(t, got)

	require.NoError(t, svc.SuppressProperty(ctx, providerID, "prop-A", time.Hour))
	require.NoError(t, snap.Load(ctx))

	got, _ = snap.IsPropertySuppressed(ctx, providerID, "prop-A")
	assert.True(t, got, "refresh must pick up the new write")
}

func TestSnapshot_UnsuppressDropsEntry(t *testing.T) {
	ctx := context.Background()
	store := suppressionstore.NewMockStore()
	svc, _ := suppressionstore.NewService(store)

	require.NoError(t, svc.SuppressProperty(ctx, providerID, "prop-A", time.Hour))
	require.NoError(t, svc.UnsuppressProperty(ctx, providerID, "prop-A"))

	snap, _ := suppressionstore.NewSnapshot(suppressionstore.SnapshotConfig{Store: store, ProviderID: providerID})
	require.NoError(t, snap.Load(ctx))

	got, _ := snap.IsPropertySuppressed(ctx, providerID, "prop-A")
	assert.False(t, got)
}

func TestService_RejectsNonPositiveTTL(t *testing.T) {
	ctx := context.Background()
	svc, _ := suppressionstore.NewService(suppressionstore.NewMockStore())
	assert.Error(t, svc.SuppressProperty(ctx, providerID, "prop-A", 0))
	assert.Error(t, svc.SuppressProperty(ctx, providerID, "prop-A", -time.Second))
	assert.Error(t, svc.SuppressGeo(ctx, providerID, "US", 0))
}

func TestSnapshot_HealthAccessors(t *testing.T) {
	ctx := context.Background()
	store := suppressionstore.NewMockStore()
	snap, _ := suppressionstore.NewSnapshot(suppressionstore.SnapshotConfig{Store: store, ProviderID: providerID})

	// Pre-load.
	assert.Equal(t, 0, snap.ConsecutiveFailures())
	assert.True(t, snap.LastSuccessfulRefresh().IsZero())

	// Successful load: failures reset, timestamp set.
	require.NoError(t, snap.Load(ctx))
	assert.Equal(t, 0, snap.ConsecutiveFailures())
	assert.False(t, snap.LastSuccessfulRefresh().IsZero())
}

func TestSnapshot_ExpiredKeysAreSkipped(t *testing.T) {
	ctx := context.Background()
	store := suppressionstore.NewMockStore()
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	store.Now = func() time.Time { return now }

	svc, _ := suppressionstore.NewService(store)
	require.NoError(t, svc.SuppressProperty(ctx, providerID, "prop-A", time.Hour))

	store.Now = func() time.Time { return now.Add(2 * time.Hour) }

	snap, _ := suppressionstore.NewSnapshot(suppressionstore.SnapshotConfig{Store: store, ProviderID: providerID})
	require.NoError(t, snap.Load(ctx))
	got, _ := snap.IsPropertySuppressed(ctx, providerID, "prop-A")
	assert.False(t, got, "TTL-expired entries should not surface in the snapshot")
}
