package legacypurchase

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestMemoryBackend_ReturnedRecordMutationDoesNotCorruptStore proves every
// Backend accessor returns a record isolated from MemoryBackend's stored
// copy: mutating a byte in a caller's returned record — Result or
// ObservedPayload — must not change what a later call observes.
func TestMemoryBackend_ReturnedRecordMutationDoesNotCorruptStore(t *testing.T) {
	now := time.Now().UTC()
	b := newMemoryBackend(0, 0, func() time.Time { return now })
	ctx := context.Background()

	rec := &ContinuationRecord{
		Continuation: Continuation{
			Token:           "continuation-token-0123456789",
			Principal:       "buyer-agent-1",
			ObservedPayload: []byte(`[{"product_id":"prod-a"}]`),
		},
		State: StateOffered,
	}
	require.NoError(t, b.PutContinuation(ctx, rec))

	// Mutating the caller's own rec after Put must not reach the store.
	rec.ObservedPayload[0] = 'X'

	got, err := b.GetContinuation(ctx, rec.Token)
	require.NoError(t, err)
	assert.JSONEq(t, `[{"product_id":"prod-a"}]`, string(got.ObservedPayload), "PutContinuation must not alias the caller's backing array")

	// Mutating a byte in a returned record must not corrupt the store.
	got.ObservedPayload[0] = 'Y'
	got2, err := b.GetContinuation(ctx, rec.Token)
	require.NoError(t, err)
	assert.JSONEq(t, `[{"product_id":"prod-a"}]`, string(got2.ObservedPayload), "GetContinuation must not alias its stored backing array")

	claimed, won, err := b.ClaimPending(ctx, rec.Token, "idem-1", "hash-1", now)
	require.NoError(t, err)
	require.True(t, won)
	claimed.Result = []byte("mutate-me")

	completed, ok, err := b.CompletePending(ctx, rec.Token, "idem-1", []byte(`{"media_buy_id":"mb-1"}`), now)
	require.NoError(t, err)
	require.True(t, ok)

	// Changing a byte in the first replay response must not change what the
	// next retry sees — the exact bug reported against this package.
	completed.Result[0] = 'Z'
	replayed, err := b.GetContinuation(ctx, rec.Token)
	require.NoError(t, err)
	assert.JSONEq(t, `{"media_buy_id":"mb-1"}`, string(replayed.Result), "a caller mutating a returned replay response must not change the next retry's response")
}
