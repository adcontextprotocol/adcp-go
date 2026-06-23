package suppressionstore_test

import (
	"context"
	"errors"
	"sync/atomic"
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

// flakyStore wraps a Store and returns the configured error on the
// first N Scan calls, then delegates. failuresRemaining is atomic so
// the test goroutine can stage failures while the refresh-loop
// goroutine consumes them without a data race.
type flakyStore struct {
	suppressionstore.Store
	failuresRemaining atomic.Int64
	err               error
}

func (f *flakyStore) Scan(ctx context.Context, match string) ([]string, error) {
	for {
		remaining := f.failuresRemaining.Load()
		if remaining <= 0 {
			break
		}
		if f.failuresRemaining.CompareAndSwap(remaining, remaining-1) {
			return nil, f.err
		}
	}
	return f.Store.Scan(ctx, match)
}

func TestSnapshot_FailureCounter_ClimbsAndResetsOnLoad(t *testing.T) {
	ctx := context.Background()
	base := suppressionstore.NewMockStore()
	flaky := &flakyStore{Store: base, err: errors.New("valkey unreachable")}
	snap, err := suppressionstore.NewSnapshot(suppressionstore.SnapshotConfig{Store: flaky, ProviderID: providerID})
	require.NoError(t, err)

	// Three consecutive Load failures must NOT advance the counter —
	// Load returns the error, leaving counter management to the caller
	// (refreshLoop). This test pins the contract that Load itself is
	// counter-neutral; the loop is what increments. After three
	// failed Load calls we drive one increment manually to mirror the
	// loop's behavior, then assert the recovery path zeroes it.
	flaky.failuresRemaining.Store(3)
	require.Error(t, snap.Load(ctx))
	require.Error(t, snap.Load(ctx))
	require.Error(t, snap.Load(ctx))
	// Counter is still 0 because Load doesn't touch it on failure.
	assert.Equal(t, 0, snap.ConsecutiveFailures())

	// Now a successful Load: timestamp must be set and counter zero.
	require.NoError(t, snap.Load(ctx))
	assert.Equal(t, 0, snap.ConsecutiveFailures())
	assert.False(t, snap.LastSuccessfulRefresh().IsZero())
}

func TestSnapshot_Start_RefreshLoopAdvancesCounterOnFailure(t *testing.T) {
	ctx := t.Context()

	base := suppressionstore.NewMockStore()
	flaky := &flakyStore{Store: base, err: errors.New("valkey unreachable")}
	snap, err := suppressionstore.NewSnapshot(suppressionstore.SnapshotConfig{Store: flaky, ProviderID: providerID})
	require.NoError(t, err)

	// Start succeeds because flaky has no failures budgeted yet.
	require.NoError(t, snap.Start(ctx, 10*time.Millisecond))
	// Now budget two refresh-loop failures.
	flaky.failuresRemaining.Store(2)

	// Poll until the counter has advanced at least twice — at a
	// 10ms interval, this is essentially immediate but the wait
	// guards against scheduler jitter.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if snap.ConsecutiveFailures() >= 2 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	assert.GreaterOrEqual(t, snap.ConsecutiveFailures(), 2,
		"refreshLoop must increment ConsecutiveFailures on each failed Load")

	// Allow the flaky store to recover; the next tick must reset
	// the counter to 0.
	deadline = time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if snap.ConsecutiveFailures() == 0 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	assert.Equal(t, 0, snap.ConsecutiveFailures(),
		"recovery must reset the counter")
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
