package webhook

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/adcontextprotocol/adcp-go/adcp/idempotency"
)

const testKey = "whk_01HW9D2T3VXQ5M7K9N1P3R5S7U"

func newTestStore(t *testing.T) (*Store, *idempotency.MemoryBackend) {
	t.Helper()
	backend := idempotency.NewMemoryBackend(0)
	t.Cleanup(backend.Close)
	return NewStore(Options{Backend: backend, TTL: time.Hour}), backend
}

func TestStoreRunsHandlerOnFirstDelivery(t *testing.T) {
	store, _ := newTestStore(t)
	ctx := WithSender(context.Background(), "sender-A")
	body := []byte(`{"idempotency_key":"` + testKey + `","event":"x"}`)

	var calls atomic.Int32
	h := func(_ context.Context, _ []byte) error { calls.Add(1); return nil }

	res, err := store.Dedup(ctx, body, h)
	require.NoError(t, err)
	assert.False(t, res.Replayed)
	assert.Equal(t, int32(1), calls.Load())
	assert.Equal(t, testKey, res.Key)
}

func TestStoreReplaySkipsHandler(t *testing.T) {
	store, _ := newTestStore(t)
	ctx := WithSender(context.Background(), "sender-A")
	body := []byte(`{"idempotency_key":"` + testKey + `","event":"x"}`)

	var calls atomic.Int32
	h := func(_ context.Context, _ []byte) error { calls.Add(1); return nil }

	_, err := store.Dedup(ctx, body, h)
	require.NoError(t, err)
	res, err := store.Dedup(ctx, body, h)
	require.NoError(t, err)
	assert.True(t, res.Replayed)
	assert.Equal(t, int32(1), calls.Load(), "handler must not run on replay")
}

func TestStoreConflictOnSameKeyDifferentBody(t *testing.T) {
	store, _ := newTestStore(t)
	ctx := WithSender(context.Background(), "sender-A")
	body1 := []byte(`{"idempotency_key":"` + testKey + `","event":"one"}`)
	body2 := []byte(`{"idempotency_key":"` + testKey + `","event":"two"}`)

	h := func(_ context.Context, _ []byte) error { return nil }

	_, err := store.Dedup(ctx, body1, h)
	require.NoError(t, err)

	_, err = store.Dedup(ctx, body2, h)
	require.Error(t, err)
	var conflict *idempotency.ConflictError
	assert.ErrorAs(t, err, &conflict)
}

func TestStoreHandlerErrorDoesNotStore(t *testing.T) {
	store, _ := newTestStore(t)
	ctx := WithSender(context.Background(), "sender-A")
	body := []byte(`{"idempotency_key":"` + testKey + `","event":"x"}`)

	var calls atomic.Int32
	failing := func(_ context.Context, _ []byte) error { calls.Add(1); return errors.New("boom") }

	_, err := store.Dedup(ctx, body, failing)
	require.Error(t, err)

	// Retry with a passing handler must re-enter the handler — a handler
	// error must NOT commit the dedup record.
	passing := func(_ context.Context, _ []byte) error { calls.Add(1); return nil }
	res, err := store.Dedup(ctx, body, passing)
	require.NoError(t, err)
	assert.False(t, res.Replayed)
	assert.Equal(t, int32(2), calls.Load())
}

func TestStoreKeysScopedPerSender(t *testing.T) {
	store, _ := newTestStore(t)
	body := []byte(`{"idempotency_key":"` + testKey + `","event":"x"}`)
	h := func(_ context.Context, _ []byte) error { return nil }

	_, err := store.Dedup(WithSender(context.Background(), "sender-A"), body, h)
	require.NoError(t, err)

	// Same key, different sender: must NOT be deduped. Per spec, keys from
	// different senders are independent.
	res, err := store.Dedup(WithSender(context.Background(), "sender-B"), body, h)
	require.NoError(t, err)
	assert.False(t, res.Replayed)
}

func TestStoreRejectsMissingSender(t *testing.T) {
	store, _ := newTestStore(t)
	body := []byte(`{"idempotency_key":"` + testKey + `","event":"x"}`)
	h := func(_ context.Context, _ []byte) error { return nil }

	_, err := store.Dedup(context.Background(), body, h)
	require.Error(t, err)
}

func TestStoreRejectsMissingKey(t *testing.T) {
	store, _ := newTestStore(t)
	ctx := WithSender(context.Background(), "sender-A")
	body := []byte(`{"event":"x"}`) // missing idempotency_key
	h := func(_ context.Context, _ []byte) error { return nil }

	_, err := store.Dedup(ctx, body, h)
	var missing *idempotency.MissingKeyError
	assert.ErrorAs(t, err, &missing)
}

func TestStoreExpiredKeyReturnsExpiredError(t *testing.T) {
	// Inject a clock so we can cross the TTL boundary without sleeping.
	now := time.Unix(1_900_000_000, 0).UTC()
	clock := func() time.Time { return now }
	backend := idempotency.NewMemoryBackend(0)
	t.Cleanup(backend.Close)
	store := NewStore(Options{
		Backend:   backend,
		TTL:       time.Hour,
		ClockSkew: time.Second,
		Clock:     clock,
	})

	ctx := WithSender(context.Background(), "sender-A")
	body := []byte(`{"idempotency_key":"` + testKey + `","event":"x"}`)
	h := func(_ context.Context, _ []byte) error { return nil }

	_, err := store.Dedup(ctx, body, h)
	require.NoError(t, err)

	// Advance past TTL + skew.
	now = now.Add(time.Hour + 30*time.Second)
	_, err = store.Dedup(ctx, body, h)
	var expired *idempotency.ExpiredError
	assert.ErrorAs(t, err, &expired, "past TTL should surface ExpiredError so HTTPHandler can return 410")
}

func TestStoreScopeDisjointFromRequestScope(t *testing.T) {
	// Concrete verification of the doc claim: a Backend shared with request
	// idempotency cannot have key collisions, because the webhook scope
	// carries a "webhook:" prefix while PrincipalScope does not.
	backend := idempotency.NewMemoryBackend(0)
	defer backend.Close()

	webhookStore := NewStore(Options{Backend: backend, TTL: time.Hour})
	requestStore := idempotency.New(idempotency.Options{Backend: backend, TTL: time.Hour})

	body := []byte(`{"idempotency_key":"` + testKey + `","event":"x"}`)
	var calls atomic.Int32
	h := func(_ context.Context, _ []byte) error { calls.Add(1); return nil }

	_, err := webhookStore.Dedup(WithSender(context.Background(), "alice"), body, h)
	require.NoError(t, err)

	reqCtx := idempotency.WithPrincipal(context.Background(), "alice")
	wrapped := requestStore.Wrap(func(_ context.Context, _ []byte) ([]byte, error) {
		calls.Add(1)
		return []byte(`{"ok":true}`), nil
	})
	res, err := wrapped(reqCtx, body)
	require.NoError(t, err)
	assert.False(t, res.Replayed, "request and webhook scopes must be disjoint even with identical (principal, key, body)")
	assert.Equal(t, int32(2), calls.Load())
}
