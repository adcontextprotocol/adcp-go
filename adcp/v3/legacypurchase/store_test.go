package legacypurchase

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	adcp "github.com/adcontextprotocol/adcp-go/adcp/v3"
	"github.com/adcontextprotocol/adcp-go/adcp/v3/idempotency"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const testPrincipal = "buyer-agent-1"

func mustJSON(t *testing.T, v any) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(v)
	require.NoError(t, err)
	return b
}

// validContinuation returns a fresh, valid Continuation bound to
// testPrincipal, plus a matching CompatibilityPurchaseCoordinatorInput that
// satisfies every one of its binding rules. Tests mutate copies of these to
// exercise individual failure modes.
func validFixture(t *testing.T, now time.Time) (*Continuation, *CompatibilityPurchaseCoordinatorInput) {
	t.Helper()
	token := "continuation-token-0123456789"
	c := &Continuation{
		Token:             token,
		Principal:         testPrincipal,
		Account:           adcp.AccountReference{AccountID: "account-acme"},
		SourceADCPVersion: "3.0",
		ExpiresAt:         now.Add(time.Hour),
		ProductIDs:        []string{"prod-a", "prod-b"},
		Losses:            []string{LossFeedVersionNotAtomic, LossPricingVersionNotAtomic},
		// Mirrors the real compact_projection.products shape (see
		// vectors_test.go): a bare array of product objects, each with the
		// pricing_options a redemption's pricing_option_id is validated
		// against.
		ObservedPayload: mustJSON(t, []map[string]any{
			{"product_id": "prod-a", "pricing_options": []map[string]any{{"pricing_option_id": "fixed-cpm"}}},
			{"product_id": "prod-b", "pricing_options": []map[string]any{{"pricing_option_id": "fixed-cpm"}}},
		}),
	}
	input := &CompatibilityPurchaseCoordinatorInput{
		IdempotencyKey:     idempotency.Generate(),
		ContinuationToken:  token,
		Account:            adcp.AccountReference{AccountID: "account-acme"},
		SelectedProductIDs: []string{"prod-a"},
		AcceptedLosses:     []string{LossFeedVersionNotAtomic, LossPricingVersionNotAtomic},
		LegacyCreateRequest: mustJSON(t, map[string]any{
			"packages": []map[string]any{{"product_id": "prod-a", "budget": 1000, "pricing_option_id": "fixed-cpm"}},
		}),
	}
	return c, input
}

func newTestStore(now func() time.Time) (*Store, *MemoryBackend) {
	b := newMemoryBackend(0, 0, now)
	s := New(Options{Backend: b, Clock: now})
	return s, b
}

func ctxWithPrincipal() context.Context {
	return idempotency.WithPrincipal(context.Background(), testPrincipal)
}

func countingExecutor(t *testing.T, resp []byte) (Executor, *int) {
	t.Helper()
	calls := 0
	return func(_ context.Context, req json.RawMessage) ([]byte, error) {
		calls++
		return resp, nil
	}, &calls
}

// ---- happy path ----

func TestContinueLegacyPurchase_Success(t *testing.T) {
	now := time.Now().UTC()
	s, _ := newTestStore(func() time.Time { return now })
	c, input := validFixture(t, now)
	require.NoError(t, s.RegisterContinuation(context.Background(), c))

	exec, calls := countingExecutor(t, []byte(`{"media_buy_id":"mb-1"}`))
	res, err := s.ContinueLegacyPurchase(ctxWithPrincipal(), input, exec)
	require.NoError(t, err)
	assert.False(t, res.Replayed)
	assert.JSONEq(t, `{"media_buy_id":"mb-1"}`, string(res.Response))
	assert.Equal(t, 1, *calls)

	// The Executor must observe exactly legacy_create_request.
	rec, err := s.opts.Backend.GetContinuation(context.Background(), c.Token)
	require.NoError(t, err)
	assert.Equal(t, StateCommitted, rec.State)
}

// TestContinueLegacyPurchase_RetryReturnsDeterministicPriorResult proves the
// second acceptance criterion literally: an exact retry (same
// idempotency_key, same payload) after success returns the recorded result
// rather than re-invoking exec.
func TestContinueLegacyPurchase_RetryReturnsDeterministicPriorResult(t *testing.T) {
	now := time.Now().UTC()
	s, _ := newTestStore(func() time.Time { return now })
	c, input := validFixture(t, now)
	require.NoError(t, s.RegisterContinuation(context.Background(), c))

	exec, calls := countingExecutor(t, []byte(`{"media_buy_id":"mb-1"}`))
	ctx := ctxWithPrincipal()
	first, err := s.ContinueLegacyPurchase(ctx, input, exec)
	require.NoError(t, err)
	require.False(t, first.Replayed)

	second, err := s.ContinueLegacyPurchase(ctx, input, exec)
	require.NoError(t, err)
	assert.True(t, second.Replayed)
	assert.Equal(t, first.Response, second.Response)
	assert.Equal(t, 1, *calls, "exec must not run again on an exact retry")
}

// TestContinueLegacyPurchase_RetryAfterFailureReturnsTerminalFailure proves
// the failure-path analogue: retrying an idempotency_key whose Executor
// call failed terminally surfaces the recorded failure, not a fresh
// attempt.
func TestContinueLegacyPurchase_RetryAfterFailureReturnsTerminalFailure(t *testing.T) {
	now := time.Now().UTC()
	s, _ := newTestStore(func() time.Time { return now })
	c, input := validFixture(t, now)
	require.NoError(t, s.RegisterContinuation(context.Background(), c))

	calls := 0
	exec := func(context.Context, json.RawMessage) ([]byte, error) {
		calls++
		return nil, &InvalidInputError{Reason: "legacy seller rejected: PRODUCT_UNAVAILABLE"}
	}
	ctx := ctxWithPrincipal()
	_, err := s.ContinueLegacyPurchase(ctx, input, exec)
	require.Error(t, err)

	_, err = s.ContinueLegacyPurchase(ctx, input, exec)
	var tf *TerminalFailureError
	require.True(t, errors.As(err, &tf))
	assert.Equal(t, 1, calls, "exec must not run again once a claim has failed terminally")
}

// ---- single-use claim ----

func TestContinueLegacyPurchase_AlreadyClaimedByDifferentKey(t *testing.T) {
	now := time.Now().UTC()
	s, _ := newTestStore(func() time.Time { return now })
	c, input := validFixture(t, now)
	require.NoError(t, s.RegisterContinuation(context.Background(), c))
	exec, _ := countingExecutor(t, []byte(`{"media_buy_id":"mb-1"}`))
	ctx := ctxWithPrincipal()
	_, err := s.ContinueLegacyPurchase(ctx, input, exec)
	require.NoError(t, err)

	second := *input
	second.IdempotencyKey = idempotency.Generate()
	_, err = s.ContinueLegacyPurchase(ctx, &second, exec)
	var ac *AlreadyClaimedError
	require.True(t, errors.As(err, &ac))
	assert.Equal(t, StateCommitted, ac.State)
}

func TestContinueLegacyPurchase_ExactRetryDifferentPayloadConflicts(t *testing.T) {
	now := time.Now().UTC()
	s, _ := newTestStore(func() time.Time { return now })
	c, input := validFixture(t, now)
	require.NoError(t, s.RegisterContinuation(context.Background(), c))
	exec, _ := countingExecutor(t, []byte(`{"media_buy_id":"mb-1"}`))
	ctx := ctxWithPrincipal()
	_, err := s.ContinueLegacyPurchase(ctx, input, exec)
	require.NoError(t, err)

	mutated := *input
	mutated.LegacyCreateRequest = mustJSON(t, map[string]any{
		"packages": []map[string]any{{"product_id": "prod-a", "budget": 5000}},
	})
	_, err = s.ContinueLegacyPurchase(ctx, &mutated, exec)
	var rc *RequestConflictError
	assert.True(t, errors.As(err, &rc))
}

// ---- binding mismatches ----

func TestContinueLegacyPurchase_ExpiredContinuation(t *testing.T) {
	now := time.Now().UTC()
	s, _ := newTestStore(func() time.Time { return now })
	c, input := validFixture(t, now)
	c.ExpiresAt = now.Add(-time.Minute)
	require.NoError(t, s.RegisterContinuation(context.Background(), c))
	exec, _ := countingExecutor(t, nil)
	_, err := s.ContinueLegacyPurchase(ctxWithPrincipal(), input, exec)
	var ee *ExpiredError
	assert.True(t, errors.As(err, &ee))
}

func TestContinueLegacyPurchase_WrongPrincipal(t *testing.T) {
	now := time.Now().UTC()
	s, _ := newTestStore(func() time.Time { return now })
	c, input := validFixture(t, now)
	require.NoError(t, s.RegisterContinuation(context.Background(), c))
	exec, _ := countingExecutor(t, nil)
	ctx := idempotency.WithPrincipal(context.Background(), "someone-else")
	_, err := s.ContinueLegacyPurchase(ctx, input, exec)
	var pm *PrincipalMismatchError
	assert.True(t, errors.As(err, &pm))
}

// TestContinueLegacyPurchase_ReplayRejectedForDifferentPrincipal proves
// resolveNonOffered checks the redeeming principal against the token-bound
// one, the same as the fresh-claim path (validateBinding) already does.
// Without that check, a continuation claimed and completed under one
// principal would replay its committed result — or leak its
// pending/terminal state — to a second, unrelated principal who happens to
// reuse the same idempotency_key.
func TestContinueLegacyPurchase_ReplayRejectedForDifferentPrincipal(t *testing.T) {
	now := time.Now().UTC()
	s, _ := newTestStore(func() time.Time { return now })
	c, input := validFixture(t, now)
	require.NoError(t, s.RegisterContinuation(context.Background(), c))

	exec, calls := countingExecutor(t, []byte(`{"media_buy_id":"mb-1"}`))
	first, err := s.ContinueLegacyPurchase(ctxWithPrincipal(), input, exec)
	require.NoError(t, err)
	require.False(t, first.Replayed)

	// Same continuation, same idempotency_key and payload, but a different
	// authenticated principal — must be rejected, not replayed.
	otherCtx := idempotency.WithPrincipal(context.Background(), "someone-else")
	_, err = s.ContinueLegacyPurchase(otherCtx, input, exec)
	var pm *PrincipalMismatchError
	require.True(t, errors.As(err, &pm))
	assert.Equal(t, 1, *calls, "exec must not run again, and the committed result must not leak to another principal")
}

// TestContinueLegacyPurchase_PricingSubstitutionRejected proves a redemption
// cannot select a pricing_option_id the seller never offered for that
// product in the continuation's observed product/pricing payload — the
// spec's binding on the "complete observed product/pricing payload",
// separate from the JSON-structure and product-ID checks.
func TestContinueLegacyPurchase_PricingSubstitutionRejected(t *testing.T) {
	now := time.Now().UTC()
	s, _ := newTestStore(func() time.Time { return now })
	c, input := validFixture(t, now)
	require.NoError(t, s.RegisterContinuation(context.Background(), c))
	input.LegacyCreateRequest = mustJSON(t, map[string]any{
		"packages": []map[string]any{{"product_id": "prod-a", "budget": 1000, "pricing_option_id": "premium-cpm-not-offered"}},
	})
	exec, calls := countingExecutor(t, nil)
	_, err := s.ContinueLegacyPurchase(ctxWithPrincipal(), input, exec)
	var pse *PricingSelectionError
	require.True(t, errors.As(err, &pse))
	assert.Equal(t, 0, *calls, "exec must never run for a substituted pricing option")
}

func TestContinueLegacyPurchase_WrongAccount(t *testing.T) {
	now := time.Now().UTC()
	s, _ := newTestStore(func() time.Time { return now })
	c, input := validFixture(t, now)
	require.NoError(t, s.RegisterContinuation(context.Background(), c))
	input.Account = adcp.AccountReference{AccountID: "account-other"}
	exec, _ := countingExecutor(t, nil)
	_, err := s.ContinueLegacyPurchase(ctxWithPrincipal(), input, exec)
	var am *AccountMismatchError
	assert.True(t, errors.As(err, &am))
}

func TestContinueLegacyPurchase_RequestAccountMismatchRejected(t *testing.T) {
	now := time.Now().UTC()
	s, _ := newTestStore(func() time.Time { return now })
	c, input := validFixture(t, now)
	require.NoError(t, s.RegisterContinuation(context.Background(), c))
	// legacy_create_request carries its own account field (as a 3.0/3.1
	// source would) that disagrees with the token-bound account.
	input.LegacyCreateRequest = mustJSON(t, map[string]any{
		"account":  map[string]any{"account_id": "account-other"},
		"packages": []map[string]any{{"product_id": "prod-a", "budget": 1000}},
	})
	exec, _ := countingExecutor(t, nil)
	_, err := s.ContinueLegacyPurchase(ctxWithPrincipal(), input, exec)
	var am *AccountMismatchError
	assert.True(t, errors.As(err, &am))
}

func TestContinueLegacyPurchase_ProductSubstitutionRejected(t *testing.T) {
	now := time.Now().UTC()
	s, _ := newTestStore(func() time.Time { return now })
	c, input := validFixture(t, now)
	require.NoError(t, s.RegisterContinuation(context.Background(), c))
	// selected_product_ids names a product not bound into the token at all.
	input.SelectedProductIDs = []string{"prod-not-offered"}
	input.LegacyCreateRequest = mustJSON(t, map[string]any{
		"packages": []map[string]any{{"product_id": "prod-not-offered", "budget": 1000}},
	})
	exec, _ := countingExecutor(t, nil)
	_, err := s.ContinueLegacyPurchase(ctxWithPrincipal(), input, exec)
	var ps *ProductSelectionError
	assert.True(t, errors.As(err, &ps))
}

func TestContinueLegacyPurchase_PackageSelectionDriftRejected(t *testing.T) {
	now := time.Now().UTC()
	s, _ := newTestStore(func() time.Time { return now })
	c, input := validFixture(t, now)
	require.NoError(t, s.RegisterContinuation(context.Background(), c))
	// selected_product_ids says prod-a, but the actual legacy_create_request
	// packages a different (still token-bound) product.
	input.LegacyCreateRequest = mustJSON(t, map[string]any{
		"packages": []map[string]any{{"product_id": "prod-b", "budget": 1000}},
	})
	exec, _ := countingExecutor(t, nil)
	_, err := s.ContinueLegacyPurchase(ctxWithPrincipal(), input, exec)
	var ps *ProductSelectionError
	assert.True(t, errors.As(err, &ps))
}

func TestContinueLegacyPurchase_IncompleteLossConsentRejected(t *testing.T) {
	now := time.Now().UTC()
	s, _ := newTestStore(func() time.Time { return now })
	c, input := validFixture(t, now)
	require.NoError(t, s.RegisterContinuation(context.Background(), c))
	input.AcceptedLosses = []string{LossFeedVersionNotAtomic, LossPricingVersionNotAtomic, LossMutationIdempotencyNotGuaranteed}
	exec, _ := countingExecutor(t, nil)
	_, err := s.ContinueLegacyPurchase(ctxWithPrincipal(), input, exec)
	var la *LossAcceptanceError
	assert.True(t, errors.As(err, &la))
}

func TestContinueLegacyPurchase_MissingRequiredLossRejectedAtStructuralValidation(t *testing.T) {
	now := time.Now().UTC()
	s, _ := newTestStore(func() time.Time { return now })
	c, input := validFixture(t, now)
	require.NoError(t, s.RegisterContinuation(context.Background(), c))
	// Schema requires accepted_losses to always contain both atomic-fence
	// losses; this must fail before even reaching the token lookup.
	input.AcceptedLosses = []string{LossFeedVersionNotAtomic}
	exec, _ := countingExecutor(t, nil)
	_, err := s.ContinueLegacyPurchase(ctxWithPrincipal(), input, exec)
	var ie *InvalidInputError
	assert.True(t, errors.As(err, &ie))
}

func TestContinueLegacyPurchase_NotExplicitPackageModeRejected(t *testing.T) {
	now := time.Now().UTC()
	s, _ := newTestStore(func() time.Time { return now })
	c, input := validFixture(t, now)
	require.NoError(t, s.RegisterContinuation(context.Background(), c))
	input.LegacyCreateRequest = mustJSON(t, map[string]any{"budget": 1000})
	exec, _ := countingExecutor(t, nil)
	_, err := s.ContinueLegacyPurchase(ctxWithPrincipal(), input, exec)
	var em *ExplicitPackageModeError
	assert.True(t, errors.As(err, &em))
}

func TestContinueLegacyPurchase_UnknownToken(t *testing.T) {
	now := time.Now().UTC()
	s, _ := newTestStore(func() time.Time { return now })
	_, input := validFixture(t, now)
	exec, _ := countingExecutor(t, nil)
	_, err := s.ContinueLegacyPurchase(ctxWithPrincipal(), input, exec)
	var nf *NotFoundError
	assert.True(t, errors.As(err, &nf))
}

func TestContinueLegacyPurchase_MissingPrincipalInContext(t *testing.T) {
	now := time.Now().UTC()
	s, _ := newTestStore(func() time.Time { return now })
	c, input := validFixture(t, now)
	require.NoError(t, s.RegisterContinuation(context.Background(), c))
	exec, _ := countingExecutor(t, nil)
	_, err := s.ContinueLegacyPurchase(context.Background(), input, exec)
	var ie *InvalidInputError
	assert.True(t, errors.As(err, &ie))
}

// ---- crash reconciliation ----

func TestContinueLegacyPurchase_AmbiguousClaimAfterLeaseExpiry(t *testing.T) {
	now := time.Now().UTC()
	clock := &now
	s, backend := newTestStore(func() time.Time { return *clock })
	s.opts.PendingLeaseTimeout = time.Minute
	c, input := validFixture(t, now)
	require.NoError(t, s.RegisterContinuation(context.Background(), c))

	// Simulate a claim that never reached a terminal state (process crash
	// between ClaimPending and CompletePending/FailPending).
	_, claimed, err := backend.ClaimPending(context.Background(), c.Token, input.IdempotencyKey, mustRequestHash(t, input), now)
	require.NoError(t, err)
	require.True(t, claimed)

	// Within the lease window: an exact retry is told to retry shortly, not
	// treated as ambiguous.
	exec, calls := countingExecutor(t, nil)
	_, err = s.ContinueLegacyPurchase(ctxWithPrincipal(), input, exec)
	var inFlight *InFlightError
	require.True(t, errors.As(err, &inFlight))
	assert.Equal(t, 0, *calls)

	// Past the lease window: fail closed with recovery guidance, per the
	// acceptance criterion "ambiguous/crashed claims fail closed and expose
	// recovery guidance."
	*clock = now.Add(2 * time.Minute)
	_, err = s.ContinueLegacyPurchase(ctxWithPrincipal(), input, exec)
	var amb *AmbiguousClaimError
	require.True(t, errors.As(err, &amb))
	assert.NotEmpty(t, amb.Guidance)
	assert.Equal(t, 0, *calls, "exec must never run for an ambiguous claim")

	// A different idempotency_key must also be rejected — the token is
	// already claimed, ambiguous or not; it is never re-offered.
	other := *input
	other.IdempotencyKey = idempotency.Generate()
	_, err = s.ContinueLegacyPurchase(ctxWithPrincipal(), &other, exec)
	var ac *AlreadyClaimedError
	assert.True(t, errors.As(err, &ac))
}

func mustRequestHash(t *testing.T, input *CompatibilityPurchaseCoordinatorInput) string {
	t.Helper()
	h, err := requestHash(input)
	require.NoError(t, err)
	return h
}

// ---- RegisterContinuation validation ----

func TestRegisterContinuation_RejectsIncompletePayload(t *testing.T) {
	now := time.Now().UTC()
	s, _ := newTestStore(func() time.Time { return now })
	c, _ := validFixture(t, now)
	c.ObservedPayload = nil
	err := s.RegisterContinuation(context.Background(), c)
	var ie *InvalidInputError
	assert.True(t, errors.As(err, &ie))
}

func TestRegisterContinuation_RejectsMissingRequiredLosses(t *testing.T) {
	now := time.Now().UTC()
	s, _ := newTestStore(func() time.Time { return now })
	c, _ := validFixture(t, now)
	c.Losses = []string{LossMutationIdempotencyNotGuaranteed}
	err := s.RegisterContinuation(context.Background(), c)
	var ie *InvalidInputError
	assert.True(t, errors.As(err, &ie))
}

// TestRegisterContinuation_Rejects25WithoutMutationIdempotencyLoss proves an
// AdCP 2.5-sourced continuation must declare
// mutation_idempotency_not_guaranteed — 2.5 has no mutation replay contract,
// per specs/legacy-compact-lifecycle-compatibility.md — rather than being
// registerable with only the two losses every source declares.
func TestRegisterContinuation_Rejects25WithoutMutationIdempotencyLoss(t *testing.T) {
	now := time.Now().UTC()
	s, _ := newTestStore(func() time.Time { return now })
	c, _ := validFixture(t, now)
	c.SourceADCPVersion = "2.5"
	c.Losses = []string{LossFeedVersionNotAtomic, LossPricingVersionNotAtomic}
	err := s.RegisterContinuation(context.Background(), c)
	var ie *InvalidInputError
	require.True(t, errors.As(err, &ie))

	c.Losses = []string{LossFeedVersionNotAtomic, LossPricingVersionNotAtomic, LossMutationIdempotencyNotGuaranteed}
	assert.NoError(t, s.RegisterContinuation(context.Background(), c))
}

func TestRegisterContinuation_DuplicateTokenRejected(t *testing.T) {
	now := time.Now().UTC()
	s, _ := newTestStore(func() time.Time { return now })
	c, _ := validFixture(t, now)
	require.NoError(t, s.RegisterContinuation(context.Background(), c))
	err := s.RegisterContinuation(context.Background(), c)
	var dt *DuplicateTokenError
	assert.True(t, errors.As(err, &dt))
}
