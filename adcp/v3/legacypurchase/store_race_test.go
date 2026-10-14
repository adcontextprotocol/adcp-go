package legacypurchase

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/adcontextprotocol/adcp-go/adcp/v3/idempotency"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestContinueLegacyPurchase_ConcurrentDistinctKeysCannotPurchaseTwice is
// the sharpest proof of the first acceptance criterion: N goroutines, each
// with its own distinct idempotency_key (simulating N independent,
// concurrent redemption attempts — the scenario a single-use claim exists
// to prevent), race to redeem the *same* continuation token. Under -race,
// exactly one must win the claim and call Executor; every other goroutine
// must be rejected before Executor runs. If two goroutines ever both
// observed a successful claim, the underlying product would be purchased
// twice.
func TestContinueLegacyPurchase_ConcurrentDistinctKeysCannotPurchaseTwice(t *testing.T) {
	now := time.Now().UTC()
	s, _ := newTestStore(func() time.Time { return now })
	c, base := validFixture(t, now)
	require.NoError(t, s.RegisterContinuation(context.Background(), c))

	var execCalls int32
	exec := func(context.Context, json.RawMessage) ([]byte, error) {
		atomic.AddInt32(&execCalls, 1)
		return []byte(`{"media_buy_id":"mb-race"}`), nil
	}

	const n = 64
	inputs := make([]*CompatibilityPurchaseCoordinatorInput, n)
	for i := range inputs {
		in := *base
		in.IdempotencyKey = idempotency.Generate()
		inputs[i] = &in
	}

	var wg sync.WaitGroup
	wg.Add(n)
	results := make([]*Result, n)
	errs := make([]error, n)
	start := make(chan struct{})
	ctx := ctxWithPrincipal()
	for i := range n {
		go func(i int) {
			defer wg.Done()
			<-start
			results[i], errs[i] = s.ContinueLegacyPurchase(ctx, inputs[i], exec)
		}(i)
	}
	close(start)
	wg.Wait()

	winners := 0
	rejected := 0
	for i := range n {
		if errs[i] == nil {
			winners++
			assert.False(t, results[i].Replayed, "the single winner's own call must not be a replay")
			continue
		}
		var ac *AlreadyClaimedError
		assert.True(t, errors.As(errs[i], &ac), "loser must fail with AlreadyClaimedError, got %v", errs[i])
		rejected++
	}
	assert.Equal(t, 1, winners, "exactly one distinct-key claim must win")
	assert.Equal(t, n-1, rejected)
	assert.Equal(t, int32(1), atomic.LoadInt32(&execCalls), "exec must run exactly once no matter how many goroutines raced for the token")
}

// TestContinueLegacyPurchase_ConcurrentSameKeyExecutesOnceAndAgreesOnResult
// races many goroutines using the *same* idempotency_key against a fresh
// continuation (the realistic retry-storm case — a client that times out
// and retries with the same key while its first attempt is still in
// flight). Exactly one call may observe a fresh Executor run; every other
// call must either see the deterministic replayed result or a
// same-key-in-flight signal — never a distinct result, and Executor must
// never run more than once.
func TestContinueLegacyPurchase_ConcurrentSameKeyExecutesOnceAndAgreesOnResult(t *testing.T) {
	now := time.Now().UTC()
	s, _ := newTestStore(func() time.Time { return now })
	s.opts.PendingLeaseTimeout = time.Minute
	c, input := validFixture(t, now)
	require.NoError(t, s.RegisterContinuation(context.Background(), c))

	var execCalls int32
	release := make(chan struct{})
	exec := func(context.Context, json.RawMessage) ([]byte, error) {
		atomic.AddInt32(&execCalls, 1)
		<-release // hold the claim pending long enough for others to race in
		return []byte(`{"media_buy_id":"mb-same-key"}`), nil
	}

	const n = 16
	var wg sync.WaitGroup
	wg.Add(n)
	results := make([]*Result, n)
	errs := make([]error, n)
	start := make(chan struct{})
	ctx := ctxWithPrincipal()
	for i := range n {
		go func(i int) {
			defer wg.Done()
			<-start
			results[i], errs[i] = s.ContinueLegacyPurchase(ctx, input, exec)
		}(i)
	}
	close(start)
	// Give every goroutine a chance to reach the backend before the winner
	// finishes executing.
	time.Sleep(20 * time.Millisecond)
	close(release)
	wg.Wait()

	freshOrReplayed := 0
	for i := range n {
		if errs[i] == nil {
			freshOrReplayed++
			assert.JSONEq(t, `{"media_buy_id":"mb-same-key"}`, string(results[i].Response))
			continue
		}
		var inFlight *InFlightError
		assert.True(t, errors.As(errs[i], &inFlight), "a same-key loser must see InFlightError while pending, got %v", errs[i])
	}
	assert.GreaterOrEqual(t, freshOrReplayed, 1, "at least the winner must succeed")
	assert.Equal(t, int32(1), atomic.LoadInt32(&execCalls), "exec must run exactly once for one idempotency_key regardless of concurrent retries")

	// A final retry after everything has settled must see the same
	// deterministic result, not a distinct one.
	final, err := s.ContinueLegacyPurchase(ctx, input, exec)
	require.NoError(t, err)
	assert.True(t, final.Replayed)
	assert.JSONEq(t, `{"media_buy_id":"mb-same-key"}`, string(final.Response))
	assert.Equal(t, int32(1), atomic.LoadInt32(&execCalls))
}
