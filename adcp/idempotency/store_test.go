package idempotency

import (
	"context"
	"encoding/json"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newTestStore(t *testing.T, now *time.Time) (*Store, *MemoryBackend) {
	t.Helper()
	b := newMemoryBackend(0, func() time.Time { return *now })
	s := New(Options{
		Backend: b,
		TTL:     1 * time.Hour,
		Clock:   func() time.Time { return *now },
	})
	return s, b
}

func TestWrapRequiresIdempotencyKey(t *testing.T) {
	now := time.Now().UTC()
	s, _ := newTestStore(t, &now)

	wrapped := s.Wrap(func(context.Context, []byte) ([]byte, error) {
		t.Fatal("handler should not run when key missing")
		return nil, nil
	})
	ctx := WithPrincipal(context.Background(), "p1")
	_, err := wrapped(ctx, []byte(`{"account":"a"}`))
	var mk *MissingKeyError
	assert.True(t, errors.As(err, &mk))
}

func TestWrapValidatesKeyFormat(t *testing.T) {
	now := time.Now().UTC()
	s, _ := newTestStore(t, &now)
	wrapped := s.Wrap(func(context.Context, []byte) ([]byte, error) { return []byte(`{}`), nil })
	ctx := WithPrincipal(context.Background(), "p1")
	_, err := wrapped(ctx, []byte(`{"idempotency_key":"bad","account":"a"}`))
	var ik *InvalidKeyError
	assert.True(t, errors.As(err, &ik))
}

func TestWrapCachesAndReplays(t *testing.T) {
	now := time.Now().UTC()
	s, _ := newTestStore(t, &now)

	var calls int32
	wrapped := s.Wrap(func(_ context.Context, req []byte) ([]byte, error) {
		atomic.AddInt32(&calls, 1)
		return []byte(`{"media_buy_id":"mb-1"}`), nil
	})
	ctx := WithPrincipal(context.Background(), "p1")
	key := Generate()
	req := mustJSON(t, map[string]any{"idempotency_key": key, "account": "a"})

	r1, err := wrapped(ctx, req)
	require.NoError(t, err)
	assert.False(t, r1.Replayed)
	assert.Equal(t, key, r1.Key)
	assert.JSONEq(t, `{"media_buy_id":"mb-1"}`, string(r1.Response))

	r2, err := wrapped(ctx, req)
	require.NoError(t, err)
	assert.True(t, r2.Replayed)
	assert.Equal(t, r1.Response, r2.Response)
	assert.Equal(t, int32(1), atomic.LoadInt32(&calls))
}

func TestWrapDetectsConflict(t *testing.T) {
	now := time.Now().UTC()
	s, _ := newTestStore(t, &now)
	wrapped := s.Wrap(func(context.Context, []byte) ([]byte, error) { return []byte(`{}`), nil })
	ctx := WithPrincipal(context.Background(), "p1")
	key := Generate()

	req1 := mustJSON(t, map[string]any{"idempotency_key": key, "account": "a", "budget": 100})
	_, err := wrapped(ctx, req1)
	require.NoError(t, err)

	req2 := mustJSON(t, map[string]any{"idempotency_key": key, "account": "a", "budget": 200})
	_, err = wrapped(ctx, req2)
	var ce *ConflictError
	require.True(t, errors.As(err, &ce))
	assert.Equal(t, key, ce.Key)
}

func TestWrapContextFieldsIgnoredForHash(t *testing.T) {
	now := time.Now().UTC()
	s, _ := newTestStore(t, &now)
	wrapped := s.Wrap(func(context.Context, []byte) ([]byte, error) { return []byte(`{}`), nil })
	ctx := WithPrincipal(context.Background(), "p1")
	key := Generate()

	req1 := mustJSON(t, map[string]any{
		"idempotency_key": key, "account": "a", "context": map[string]any{"trace": "t1"},
	})
	req2 := mustJSON(t, map[string]any{
		"idempotency_key": key, "account": "a", "context": map[string]any{"trace": "t2"},
	})

	_, err := wrapped(ctx, req1)
	require.NoError(t, err)
	r2, err := wrapped(ctx, req2)
	require.NoError(t, err)
	assert.True(t, r2.Replayed, "differing context must still replay as a match")
}

func TestWrapRejectsExpired(t *testing.T) {
	now := time.Now().UTC()
	s, _ := newTestStore(t, &now)
	wrapped := s.Wrap(func(context.Context, []byte) ([]byte, error) { return []byte(`{}`), nil })
	ctx := WithPrincipal(context.Background(), "p1")
	key := Generate()
	req := mustJSON(t, map[string]any{"idempotency_key": key, "account": "a"})

	_, err := wrapped(ctx, req)
	require.NoError(t, err)

	now = now.Add(2 * time.Hour)
	_, err = wrapped(ctx, req)
	var ee *ExpiredError
	assert.True(t, errors.As(err, &ee))
}

func TestWrapHandlerErrorNotCached(t *testing.T) {
	now := time.Now().UTC()
	s, _ := newTestStore(t, &now)

	var calls int32
	wrapped := s.Wrap(func(context.Context, []byte) ([]byte, error) {
		n := atomic.AddInt32(&calls, 1)
		if n == 1 {
			return nil, errors.New("transient")
		}
		return []byte(`{"ok":true}`), nil
	})
	ctx := WithPrincipal(context.Background(), "p1")
	key := Generate()
	req := mustJSON(t, map[string]any{"idempotency_key": key, "account": "a"})

	_, err := wrapped(ctx, req)
	require.Error(t, err)
	r, err := wrapped(ctx, req)
	require.NoError(t, err)
	assert.False(t, r.Replayed, "retry after error must re-execute, not replay")
	assert.Equal(t, int32(2), atomic.LoadInt32(&calls))
}

func TestWrapPerPrincipalScope(t *testing.T) {
	now := time.Now().UTC()
	s, _ := newTestStore(t, &now)

	var calls int32
	wrapped := s.Wrap(func(context.Context, []byte) ([]byte, error) {
		atomic.AddInt32(&calls, 1)
		return []byte(`{}`), nil
	})
	key := Generate()
	req := mustJSON(t, map[string]any{"idempotency_key": key, "account": "a"})

	_, err := wrapped(WithPrincipal(context.Background(), "p1"), req)
	require.NoError(t, err)
	r2, err := wrapped(WithPrincipal(context.Background(), "p2"), req)
	require.NoError(t, err)
	assert.False(t, r2.Replayed, "different principals must not observe each other's cache")
	assert.Equal(t, int32(2), atomic.LoadInt32(&calls))
}

func TestWrapRequiresPrincipal(t *testing.T) {
	now := time.Now().UTC()
	s, _ := newTestStore(t, &now)
	wrapped := s.Wrap(func(context.Context, []byte) ([]byte, error) { return []byte(`{}`), nil })
	req := mustJSON(t, map[string]any{"idempotency_key": Generate(), "account": "a"})
	_, err := wrapped(context.Background(), req)
	assert.Error(t, err)
}

func TestSessionScope(t *testing.T) {
	now := time.Now().UTC()
	b := newMemoryBackend(0, func() time.Time { return now })
	s := New(Options{
		Backend: b,
		TTL:     time.Hour,
		Scope:   SessionScope("session_id"),
		Clock:   func() time.Time { return now },
	})

	var calls int32
	wrapped := s.Wrap(func(context.Context, []byte) ([]byte, error) {
		atomic.AddInt32(&calls, 1)
		return []byte(`{}`), nil
	})
	ctx := WithPrincipal(context.Background(), "p1")
	key := Generate()

	req1 := mustJSON(t, map[string]any{"idempotency_key": key, "session_id": "sess-A", "message": "hi"})
	req2 := mustJSON(t, map[string]any{"idempotency_key": key, "session_id": "sess-B", "message": "hi"})

	_, err := wrapped(ctx, req1)
	require.NoError(t, err)
	r2, err := wrapped(ctx, req2)
	require.NoError(t, err)
	assert.False(t, r2.Replayed)
	assert.Equal(t, int32(2), atomic.LoadInt32(&calls))

	rReplay, err := wrapped(ctx, req1)
	require.NoError(t, err)
	assert.True(t, rReplay.Replayed)
}

func TestCapabilityFragment(t *testing.T) {
	now := time.Now().UTC()
	s, _ := newTestStore(t, &now)
	cap := s.Capability()
	assert.Equal(t, int64(3600), cap["replay_ttl_seconds"])
}

func TestNewPanicsOnMissingBackend(t *testing.T) {
	defer func() {
		r := recover()
		assert.NotNil(t, r)
	}()
	New(Options{TTL: time.Hour})
}

func TestNewPanicsOnZeroTTL(t *testing.T) {
	defer func() {
		r := recover()
		assert.NotNil(t, r)
	}()
	New(Options{Backend: NewMemoryBackend(0)})
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	require.NoError(t, err)
	return b
}
