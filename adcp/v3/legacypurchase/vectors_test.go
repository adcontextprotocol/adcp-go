package legacypurchase

import (
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/adcontextprotocol/adcp-go/adcp/v3/idempotency"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// See testdata/products-only-brief-compatibility/PROVENANCE.md for exactly
// where this fixture came from and which of its sections this file
// exercises against Store.

const vectorsPath = "testdata/products-only-brief-compatibility/vectors.json"

type vectorFile struct {
	Description               string            `json:"description"`
	Cases                     []vectorCase      `json:"cases"`
	ListedPurchaseCases       []json.RawMessage `json:"listed_purchase_cases"`
	ReverseCompatibilityCases []json.RawMessage `json:"reverse_compatibility_cases"`
}

type vectorCase struct {
	SourceVersion     string           `json:"source_version"`
	CompactProjection vectorProjection `json:"compact_projection"`
	ContinuationInput json.RawMessage  `json:"continuation_input"`
}

type vectorProjection struct {
	Outcome              string                     `json:"outcome"`
	Products             json.RawMessage            `json:"products"`
	PurchaseContinuation vectorPurchaseContinuation `json:"purchase_continuation"`
}

type vectorPurchaseContinuation struct {
	Kind                  string   `json:"kind"`
	ContinuationToken     string   `json:"continuation_token"`
	ContinuationExpiresAt string   `json:"continuation_expires_at"`
	ProductIDs            []string `json:"product_ids"`
	SourceADCPVersion     string   `json:"source_adcp_version"`
	Losses                []string `json:"losses"`
}

func loadVectors(t *testing.T) *vectorFile {
	t.Helper()
	b, err := os.ReadFile(vectorsPath)
	require.NoError(t, err, "products-only-brief-compatibility vectors must be present at %s — see testdata PROVENANCE.md", vectorsPath)
	var vf vectorFile
	require.NoError(t, json.Unmarshal(b, &vf))
	require.NotEmpty(t, vf.Cases, "vectors.json cases[] must not be empty")
	return &vf
}

const vectorPrincipal = "compatibility-vector-principal"

// registerVectorContinuation registers the Continuation described by a
// vectors.json case, bound to vectorPrincipal.
func registerVectorContinuation(t *testing.T, s *Store, tc vectorCase, account CompatibilityPurchaseCoordinatorInput) {
	t.Helper()
	pc := tc.CompactProjection.PurchaseContinuation
	expiresAt, err := time.Parse(time.RFC3339, pc.ContinuationExpiresAt)
	require.NoError(t, err)
	c := &Continuation{
		Token:             pc.ContinuationToken,
		Principal:         vectorPrincipal,
		Account:           account.Account, // token-bound account: the fixture is self-consistent by construction
		SourceADCPVersion: pc.SourceADCPVersion,
		ExpiresAt:         expiresAt,
		ProductIDs:        pc.ProductIDs,
		Losses:            pc.Losses,
		ObservedPayload:   tc.CompactProjection.Products,
	}
	require.NoError(t, s.RegisterContinuation(context.Background(), c))
}

// TestProductsOnlyBriefCompatibilityVectors runs every vectors.json
// cases[] entry — the AdCP 2.5 / 3.0 / 3.1 legacy_create continuations —
// end to end: register the continuation exactly as
// compact_projection.purchase_continuation describes it, redeem it via
// Store.ContinueLegacyPurchase using continuation_input verbatim from the
// fixture, and confirm the Executor receives exactly legacy_create_request
// and that a same-key retry returns the deterministic prior result.
func TestProductsOnlyBriefCompatibilityVectors(t *testing.T) {
	vf := loadVectors(t)
	for _, tc := range vf.Cases {
		tc := tc
		t.Run(tc.SourceVersion, func(t *testing.T) {
			pc := tc.CompactProjection.PurchaseContinuation
			require.Equal(t, KindLegacyCreate, pc.Kind, "this fixture's cases[] are documented to be legacy_create continuations")

			var input CompatibilityPurchaseCoordinatorInput
			require.NoError(t, json.Unmarshal(tc.ContinuationInput, &input))

			now := time.Now().UTC()
			s, _ := newTestStore(func() time.Time { return now })
			registerVectorContinuation(t, s, tc, input)

			var observedReq json.RawMessage
			calls := 0
			exec := func(_ context.Context, req json.RawMessage) ([]byte, error) {
				calls++
				observedReq = req
				return []byte(`{"media_buy_id":"mb-vector"}`), nil
			}

			ctx := idempotency.WithPrincipal(context.Background(), vectorPrincipal)
			res, err := s.ContinueLegacyPurchase(ctx, &input, exec)
			require.NoError(t, err)
			assert.False(t, res.Replayed)
			assert.Equal(t, 1, calls)
			assert.JSONEq(t, string(input.LegacyCreateRequest), string(observedReq),
				"Executor must receive exactly legacy_create_request, unmodified")

			// Retry after success returns the deterministic prior result.
			replay, err := s.ContinueLegacyPurchase(ctx, &input, exec)
			require.NoError(t, err)
			assert.True(t, replay.Replayed)
			assert.Equal(t, res.Response, replay.Response)
			assert.Equal(t, 1, calls, "exec must not run again on retry")
		})
	}
}

// TestProductsOnlyBriefCompatibilityVectors_NegativeMutations constructs
// the negative assertions the vector bundle's own README says SDK suites
// are expected to build from these fixtures ("product substitution,
// package-selection drift, and incomplete loss consent") — the upstream
// bundle does not ship separate negative-case JSON.
func TestProductsOnlyBriefCompatibilityVectors_NegativeMutations(t *testing.T) {
	vf := loadVectors(t)
	exec := func(context.Context, json.RawMessage) ([]byte, error) {
		t.Fatal("exec must not run for a rejected redemption")
		return nil, nil
	}
	ctx := idempotency.WithPrincipal(context.Background(), vectorPrincipal)

	setup := func(t *testing.T, tc vectorCase, input CompatibilityPurchaseCoordinatorInput) *Store {
		s, _ := newTestStore(func() time.Time { return time.Now().UTC() })
		registerVectorContinuation(t, s, tc, input)
		return s
	}

	// 3.0 case: exactly one product, one required loss pair, no third loss.
	tc30 := vf.Cases[1]
	var input30 CompatibilityPurchaseCoordinatorInput
	require.NoError(t, json.Unmarshal(tc30.ContinuationInput, &input30))

	t.Run("product substitution", func(t *testing.T) {
		s := setup(t, tc30, input30)
		in := input30
		in.SelectedProductIDs = []string{"a-product-never-offered"}
		in.LegacyCreateRequest = mustJSON(t, map[string]any{
			"packages": []map[string]any{{"product_id": "a-product-never-offered", "budget": 1000}},
		})
		_, err := s.ContinueLegacyPurchase(ctx, &in, exec)
		var ps *ProductSelectionError
		assert.ErrorAs(t, err, &ps)
	})

	t.Run("package selection drift", func(t *testing.T) {
		s := setup(t, tc30, input30)
		in := input30
		// selected_product_ids still names the real, token-bound product,
		// but the actual legacy_create_request packages a different one.
		in.LegacyCreateRequest = mustJSON(t, map[string]any{
			"packages": []map[string]any{{"product_id": "a-different-product", "budget": 1000}},
		})
		_, err := s.ContinueLegacyPurchase(ctx, &in, exec)
		var ps *ProductSelectionError
		assert.ErrorAs(t, err, &ps)
	})

	t.Run("stale loss consent - superset", func(t *testing.T) {
		s := setup(t, tc30, input30)
		in := input30
		// The 3.0 token requires exactly the two atomic-fence losses; add
		// the 2.5-only mutation-idempotency loss, which this token never
		// declared.
		in.AcceptedLosses = append(append([]string{}, input30.AcceptedLosses...), LossMutationIdempotencyNotGuaranteed)
		_, err := s.ContinueLegacyPurchase(ctx, &in, exec)
		var la *LossAcceptanceError
		assert.ErrorAs(t, err, &la)
	})

	// 2.5 case: has the extra mutation-idempotency loss, so dropping one
	// element yields a genuine incomplete-consent case.
	tc25 := vf.Cases[0]
	var input25 CompatibilityPurchaseCoordinatorInput
	require.NoError(t, json.Unmarshal(tc25.ContinuationInput, &input25))
	require.Len(t, input25.AcceptedLosses, 3, "the 2.5 vector is expected to declare 3 losses")

	t.Run("incomplete loss consent", func(t *testing.T) {
		s := setup(t, tc25, input25)
		in := input25
		in.AcceptedLosses = []string{LossFeedVersionNotAtomic, LossPricingVersionNotAtomic} // drops mutation_idempotency_not_guaranteed
		_, err := s.ContinueLegacyPurchase(ctx, &in, exec)
		var la *LossAcceptanceError
		assert.ErrorAs(t, err, &la)
	})

	t.Run("wrong account", func(t *testing.T) {
		s := setup(t, tc30, input30)
		in := input30
		in.Account.AccountID = "account-not-bound-to-this-token"
		_, err := s.ContinueLegacyPurchase(ctx, &in, exec)
		var am *AccountMismatchError
		assert.ErrorAs(t, err, &am)
	})

	t.Run("expired continuation", func(t *testing.T) {
		pc := tc30.CompactProjection.PurchaseContinuation
		expiresAt, err := time.Parse(time.RFC3339, pc.ContinuationExpiresAt)
		require.NoError(t, err)
		// Register and redeem against a clock fixed one hour past the
		// vector's own continuation_expires_at (which is 2099-dated, so an
		// unmodified "now" would never expire it).
		s := New(Options{
			Backend: newMemoryBackend(0, 0, func() time.Time { return expiresAt.Add(time.Hour) }),
			Clock:   func() time.Time { return expiresAt.Add(time.Hour) },
		})
		require.NoError(t, s.RegisterContinuation(context.Background(), &Continuation{
			Token:             pc.ContinuationToken,
			Principal:         vectorPrincipal,
			Account:           input30.Account,
			SourceADCPVersion: pc.SourceADCPVersion,
			ExpiresAt:         expiresAt,
			ProductIDs:        pc.ProductIDs,
			Losses:            pc.Losses,
			ObservedPayload:   tc30.CompactProjection.Products,
		}))
		_, err = s.ContinueLegacyPurchase(ctx, &input30, exec)
		var ee *ExpiredError
		assert.ErrorAs(t, err, &ee)
	})
}

// TestProductsOnlyBriefCompatibilityVectors_OutOfScopeSectionsParse proves
// vectors.json's listed_purchase_cases and reverse_compatibility_cases
// sections are read successfully (so a stale/misread fixture would be
// caught here) without claiming this package exercises them — see
// testdata/PROVENANCE.md for why they are structurally out of scope for a
// buyer-side claim coordinator.
func TestProductsOnlyBriefCompatibilityVectors_OutOfScopeSectionsParse(t *testing.T) {
	vf := loadVectors(t)
	assert.NotEmpty(t, vf.ListedPurchaseCases, "listed_purchase_cases should be present in the fixture even though this package does not exercise it")
	assert.NotEmpty(t, vf.ReverseCompatibilityCases, "reverse_compatibility_cases should be present in the fixture even though this package does not exercise it")
}
