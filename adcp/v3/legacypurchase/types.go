package legacypurchase

import (
	"context"
	"encoding/json"
	"time"

	adcp "github.com/adcontextprotocol/adcp-go/adcp/v3"
)

// Continuation kinds from purchase_continuation.kind. Only KindLegacyCreate
// has durable state this package claims — see the package doc.
const (
	KindListedPurchase = "listed_purchase"
	KindLegacyCreate   = "legacy_create"
)

// Loss names media-buy/legacy-purchase-continuation-input.json's
// accepted_losses enum allows. LossFeedVersionNotAtomic and
// LossPricingVersionNotAtomic are required on every legacy_create
// continuation per the schema's own allOf/contains constraint;
// LossMutationIdempotencyNotGuaranteed is required only for an AdCP 2.5
// source (2.5 has no mutation replay contract) or when the actual 3.0/3.1
// peer does not provide one.
const (
	LossFeedVersionNotAtomic             = "feed_version_not_atomic"
	LossPricingVersionNotAtomic          = "pricing_version_not_atomic"
	LossMutationIdempotencyNotGuaranteed = "mutation_idempotency_not_guaranteed"
)

// ContinuationState is the lifecycle state of one durable legacy_create
// continuation record.
type ContinuationState string

const (
	// StateOffered: registered, not yet redeemed. Single-use — the only
	// state ClaimPending may transition out of.
	StateOffered ContinuationState = "offered"
	// StatePending: atomically claimed by one idempotency_key; the
	// coordinator's Executor call is in flight or the process that claimed
	// it crashed before recording a terminal outcome. See
	// AmbiguousClaimError and InFlightError.
	StatePending ContinuationState = "pending"
	// StateCommitted: Executor succeeded; Result holds its response.
	StateCommitted ContinuationState = "committed"
	// StateFailed: Executor (or the legacy seller behind it) failed
	// terminally; ErrorCode/ErrorMessage hold recovery guidance. A failed
	// continuation is still single-use-spent — it is never re-offered.
	StateFailed ContinuationState = "failed"
)

// Continuation is the set of seller-issued facts an application's
// compatibility-projection layer binds into a legacy_create
// purchase_continuation at the moment it decides to offer one to a caller,
// per specs/legacy-compact-lifecycle-compatibility.md's "legacy_create"
// section. RegisterContinuation persists these facts; ContinueLegacyPurchase
// verifies every field against a later CompatibilityPurchaseCoordinatorInput
// before allowing a single atomic claim.
type Continuation struct {
	// Token is the opaque continuation_token surfaced to the caller in
	// purchase_continuation.continuation_token. Must be at least 16
	// characters (matches the schema's continuation_token minLength) and
	// unique — RegisterContinuation fails closed on collision.
	Token string

	// Principal is the authenticated identity the token is bound to (the
	// same principal concept as adcp/v3/idempotency.WithPrincipal — this
	// package reads it from the same context key via
	// idempotency.PrincipalFromContext, so a seller that already wraps its
	// handlers with idempotency.Store gets principal binding for free).
	// ContinueLegacyPurchase rejects a redemption attempt from a different
	// principal — the spec's confused-deputy guard.
	Principal string

	// Account is the account identity bound into the token. AdCP 2.5 has
	// no wire account field on its legacy request, so its adapter must
	// still supply the same token-bound client/account session here — the
	// spec's "MUST NOT retarget the seller connection" rule.
	Account adcp.AccountReference

	// SourceADCPVersion is the legacy protocol version the continuation
	// targets (e.g. "2.5", "3.0", "3.1").
	SourceADCPVersion string

	// ExpiresAt is the continuation's absolute deadline
	// (purchase_continuation.continuation_expires_at). A redemption attempt
	// after this time fails closed with ExpiredError.
	ExpiresAt time.Time

	// ProductIDs is the complete, seller-issued set of product IDs bound
	// into the continuation (purchase_continuation.product_ids). A
	// redemption's selected_product_ids must be a non-empty subset of this
	// set.
	ProductIDs []string

	// Losses is the complete, exact loss set the continuation declares
	// (purchase_continuation.losses). A redemption's accepted_losses must
	// equal this set exactly — not a subset, not a superset.
	Losses []string

	// ObservedPayload is the canonical bytes of the products/pricing
	// payload actually observed when this continuation was minted (the
	// compact_projection.products the caller saw). It is bound durably at
	// registration time and never accepted from a later redemption
	// request, which is what makes it a payload-substitution guard rather
	// than a self-reported claim. Must be non-empty — RegisterContinuation
	// rejects an incomplete payload rather than minting a continuation it
	// cannot stand behind.
	ObservedPayload []byte
}

// ContinuationRecord is the durable record a Backend stores: the bound
// Continuation facts plus its claim/completion state.
type ContinuationRecord struct {
	Continuation

	State ContinuationState

	RegisteredAt time.Time

	// ClaimantKey is the idempotency_key of the redemption call that
	// claimed this continuation. Empty while State == StateOffered.
	ClaimantKey string
	// RequestHash is a canonical hash of the full redemption input claimed
	// under ClaimantKey, used to detect an idempotency_key reused with a
	// different payload (RequestConflictError) versus an exact retry.
	RequestHash string
	ClaimedAt   time.Time

	// Result holds the Executor's response once State == StateCommitted.
	Result []byte
	// ErrorCode / ErrorMessage hold recovery guidance once
	// State == StateFailed.
	ErrorCode    string
	ErrorMessage string
	CompletedAt  time.Time
}

// CompatibilityPurchaseCoordinatorInput is the SDK-local input for
// redeeming a legacy_create continuation. Its field shape mirrors
// media-buy/legacy-purchase-continuation-input.json from the AdCP
// 3.2.0-beta.9 schema bundle exactly (see doc.go for why it is hand-written
// rather than generated). Per the schema: "This object is consumed by the
// compatibility coordinator and MUST NOT be sent as an AdCP tool payload."
type CompatibilityPurchaseCoordinatorInput struct {
	// IdempotencyKey is the replay identity for this logical coordinator
	// operation (schema: format uuid). Exact retries resume the durable
	// operation record instead of redeeming the continuation again.
	IdempotencyKey string `json:"idempotency_key"`

	// ContinuationToken is the opaque token from
	// products_available.purchase_continuation.continuation_token (schema:
	// minLength 16).
	ContinuationToken string `json:"continuation_token"`

	// Account must match the account bound into the continuation token.
	Account adcp.AccountReference `json:"account"`

	// SelectedProductIDs is a non-empty subset of the product IDs bound
	// into the continuation, and must equal the distinct explicit-package
	// product IDs in LegacyCreateRequest.
	SelectedProductIDs []string `json:"selected_product_ids"`

	// AcceptedLosses must equal the continuation's exact loss set. Per
	// schema it must always contain at least
	// feed_version_not_atomic and pricing_version_not_atomic.
	AcceptedLosses []string `json:"accepted_losses"`

	// LegacyCreateRequest is the proposed create_media_buy payload for
	// SourceADCPVersion. Validated structurally by this package
	// (explicit-package mode, package product IDs) — see doc.go's scope
	// note on what full per-version schema validation remains
	// application-owned.
	LegacyCreateRequest json.RawMessage `json:"legacy_create_request"`
}

// Result is the outcome of a successful ContinueLegacyPurchase call.
type Result struct {
	// Response is the Executor's response bytes — either freshly produced
	// (Replayed == false) or the durably recorded prior result of an exact
	// idempotency_key retry (Replayed == true).
	Response []byte
	Replayed bool
}

// Executor performs the actual legacy create_media_buy call — against the
// real legacy seller, or an application's own legacy facade — and is
// invoked by ContinueLegacyPurchase at most once per distinct continuation
// claim. legacyCreateRequest is exactly
// CompatibilityPurchaseCoordinatorInput.LegacyCreateRequest, already
// structurally validated (explicit-package mode, product IDs) before
// Executor is called.
type Executor func(ctx context.Context, legacyCreateRequest json.RawMessage) (response []byte, err error)
