package legacypurchase

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	adcp "github.com/adcontextprotocol/adcp-go/adcp/v3"
	"github.com/adcontextprotocol/adcp-go/adcp/v3/idempotency"
)

// DefaultPendingLeaseTimeout bounds how long a StatePending claim is
// treated as merely "in flight" (InFlightError, retry shortly) before it is
// instead treated as crash-suspected (AmbiguousClaimError, fail closed).
// Chosen generously relative to a typical legacy create_media_buy round
// trip; applications with slower legacy sellers should widen it via
// Options.PendingLeaseTimeout.
const DefaultPendingLeaseTimeout = 2 * time.Minute

// Options configures a Store.
type Options struct {
	// Backend stores continuation records. Required.
	Backend Backend

	// PendingLeaseTimeout is the boundary between InFlightError and
	// AmbiguousClaimError for a StatePending claim under the same
	// idempotency_key. Defaults to DefaultPendingLeaseTimeout.
	PendingLeaseTimeout time.Duration

	// Clock is injectable for tests. Defaults to time.Now.UTC.
	Clock func() time.Time
}

// Store coordinates durable legacy_create purchase continuations.
type Store struct {
	opts Options
}

// New returns a Store. Panics on misconfiguration, matching
// adcp/v3/idempotency.New's fail-fast convention: a coordinator that starts
// in a state where claims silently can't be recorded is worse than one that
// never starts.
func New(opts Options) *Store {
	if opts.Backend == nil {
		panic("legacypurchase: Options.Backend is required")
	}
	if opts.PendingLeaseTimeout < 0 {
		panic("legacypurchase: Options.PendingLeaseTimeout must be non-negative")
	}
	if opts.PendingLeaseTimeout == 0 {
		opts.PendingLeaseTimeout = DefaultPendingLeaseTimeout
	}
	if opts.Clock == nil {
		opts.Clock = func() time.Time { return time.Now().UTC() }
	}
	return &Store{opts: opts}
}

// RegisterContinuation durably records a newly minted legacy_create
// continuation. Call this from the application's compatibility-projection
// layer at the exact moment it decides to offer
// purchase_continuation.kind == "legacy_create" to a caller — before that
// outcome is returned over the wire, so a later ContinueLegacyPurchase call
// always has a durable record to resolve the token against.
func (s *Store) RegisterContinuation(ctx context.Context, c *Continuation) error {
	if c == nil {
		return &InvalidInputError{Reason: "continuation is required"}
	}
	if len(c.Token) < 16 {
		return &InvalidInputError{Field: "token", Reason: "must be at least 16 characters"}
	}
	if c.Principal == "" {
		return &InvalidInputError{Field: "principal", Reason: "is required"}
	}
	if !hasAccountIdentity(c.Account) {
		return &InvalidInputError{Field: "account", Reason: "must set account_id, or brand+operator"}
	}
	if c.SourceADCPVersion == "" {
		return &InvalidInputError{Field: "source_adcp_version", Reason: "is required"}
	}
	if c.ExpiresAt.IsZero() {
		return &InvalidInputError{Field: "expires_at", Reason: "is required"}
	}
	if len(c.ProductIDs) == 0 {
		return &InvalidInputError{Field: "product_ids", Reason: "must not be empty"}
	}
	if !containsAll(c.Losses, LossFeedVersionNotAtomic, LossPricingVersionNotAtomic) {
		return &InvalidInputError{Field: "losses", Reason: "must include feed_version_not_atomic and pricing_version_not_atomic"}
	}
	if strings.HasPrefix(c.SourceADCPVersion, "2.5") && !containsAll(c.Losses, LossMutationIdempotencyNotGuaranteed) {
		return &InvalidInputError{Field: "losses", Reason: "must include mutation_idempotency_not_guaranteed for an AdCP 2.5 source"}
	}
	if len(c.ObservedPayload) == 0 {
		return &InvalidInputError{Field: "observed_payload", Reason: "must be non-empty — a continuation cannot be minted without a complete observed product/pricing payload"}
	}

	rec := &ContinuationRecord{
		Continuation: *c,
		State:        StateOffered,
		RegisteredAt: s.opts.Clock(),
	}
	return s.opts.Backend.PutContinuation(ctx, rec)
}

// ContinueLegacyPurchase redeems a legacy_create continuation: validates
// input against every binding rule in
// specs/legacy-compact-lifecycle-compatibility.md, atomically claims the
// token exactly once, invokes exec, and durably records the terminal result
// so an exact idempotency_key retry returns the deterministic prior result
// instead of re-invoking exec or the legacy seller.
//
// The caller's context must carry a principal via
// idempotency.WithPrincipal — the same context key
// adcp/v3/idempotency.Store's middleware uses, so a seller that already
// wraps its handlers with idempotency gets principal binding here for free.
func (s *Store) ContinueLegacyPurchase(ctx context.Context, input *CompatibilityPurchaseCoordinatorInput, exec Executor) (*Result, error) {
	if input == nil {
		return nil, &InvalidInputError{Reason: "input is required"}
	}
	if exec == nil {
		return nil, &InvalidInputError{Reason: "exec is required"}
	}
	if err := validateInputStructure(input); err != nil {
		return nil, err
	}

	principal := idempotency.PrincipalFromContext(ctx)
	if principal == "" {
		return nil, &InvalidInputError{Reason: "principal missing from context; call idempotency.WithPrincipal before invoking ContinueLegacyPurchase"}
	}

	pkgProductIDs, err := explicitPackageProductIDs(input.LegacyCreateRequest)
	if err != nil {
		return nil, err
	}
	if !stringSetEqual(pkgProductIDs, input.SelectedProductIDs) {
		return nil, &ProductSelectionError{
			Token:  input.ContinuationToken,
			Reason: fmt.Sprintf("selected_product_ids %v does not equal legacy_create_request's explicit package product IDs %v", sortedCopy(input.SelectedProductIDs), sortedCopy(pkgProductIDs)),
		}
	}

	reqHash, err := requestHash(input)
	if err != nil {
		return nil, err
	}

	rec, err := s.opts.Backend.GetContinuation(ctx, input.ContinuationToken)
	if err != nil {
		return nil, err
	}
	if rec == nil {
		return nil, &NotFoundError{Token: input.ContinuationToken}
	}

	now := s.opts.Clock()

	switch rec.State {
	case StateOffered:
		if err := s.validateBinding(rec, input, principal, now); err != nil {
			return nil, err
		}
		claimed, wonClaim, err := s.opts.Backend.ClaimPending(ctx, input.ContinuationToken, input.IdempotencyKey, reqHash, now)
		if err != nil {
			return nil, err
		}
		if !wonClaim {
			// Lost the race to another concurrent redemption attempt.
			// Re-evaluate against whatever the winner left behind, exactly
			// as if this call had observed that state from the start.
			return s.resolveNonOffered(ctx, claimed, input, principal, reqHash, now)
		}
		return s.execute(ctx, claimed, input, exec)

	default:
		return s.resolveNonOffered(ctx, rec, input, principal, reqHash, now)
	}
}

// validateBinding runs every check the spec requires before a StateOffered
// continuation may be claimed.
func (s *Store) validateBinding(rec *ContinuationRecord, input *CompatibilityPurchaseCoordinatorInput, principal string, now time.Time) error {
	if now.After(rec.ExpiresAt) {
		return &ExpiredError{Token: rec.Token, ExpiresAt: rec.ExpiresAt.Format(time.RFC3339)}
	}
	if rec.Principal != principal {
		return &PrincipalMismatchError{Token: rec.Token}
	}
	if !accountsEqual(rec.Account, input.Account) {
		return &AccountMismatchError{Token: rec.Token, Reason: "input.account does not equal the token-bound account"}
	}
	if !stringSetEqual(rec.Losses, input.AcceptedLosses) {
		return &LossAcceptanceError{Token: rec.Token, Required: sortedCopy(rec.Losses), Accepted: sortedCopy(input.AcceptedLosses)}
	}
	if !stringSetSubset(input.SelectedProductIDs, rec.ProductIDs) {
		return &ProductSelectionError{
			Token:  rec.Token,
			Reason: fmt.Sprintf("selected_product_ids %v is not a subset of the token-bound product IDs %v", sortedCopy(input.SelectedProductIDs), sortedCopy(rec.ProductIDs)),
		}
	}
	if reqAccount, ok, err := requestAccount(input.LegacyCreateRequest); err != nil {
		return err
	} else if ok && !accountsEqual(reqAccount, rec.Account) {
		// Source versions whose create_media_buy request carries an
		// account field must agree with the token-bound account. AdCP 2.5
		// has no such field, so ok is false there and this check is
		// skipped per the spec's explicit carve-out.
		return &AccountMismatchError{Token: rec.Token, Reason: "legacy_create_request.account does not equal the token-bound account"}
	}
	if err := validatePricingOptions(rec.Token, rec.ObservedPayload, input.LegacyCreateRequest); err != nil {
		return err
	}
	return nil
}

// validatePricingOptions checks that every package in legacyCreateRequest
// that names a pricing_option_id references an option actually present in
// observedPayload for that product — the spec's payload-substitution guard
// that prevents a caller from selecting a pricing option they never saw.
//
// observedPayload is a JSON array of product objects (compact_projection.products).
// The check is skipped entirely when no package carries a pricing_option_id,
// so requests that omit the optional field are unaffected.
func validatePricingOptions(token string, observedPayload []byte, legacyCreateRequest json.RawMessage) error {
	var req struct {
		Packages []struct {
			ProductID       string `json:"product_id"`
			PricingOptionID string `json:"pricing_option_id"`
		} `json:"packages"`
	}
	if err := json.Unmarshal(legacyCreateRequest, &req); err != nil {
		return &InvalidInputError{Field: "legacy_create_request", Reason: "not valid JSON: " + err.Error()}
	}

	// Collect only the packages that actually specify a pricing option.
	type pkgCheck struct{ productID, pricingOptionID string }
	var toCheck []pkgCheck
	for _, pkg := range req.Packages {
		if pkg.PricingOptionID != "" {
			toCheck = append(toCheck, pkgCheck{pkg.ProductID, pkg.PricingOptionID})
		}
	}
	if len(toCheck) == 0 {
		return nil
	}

	// Parse observedPayload (bare array of product objects from compact_projection.products).
	var products []struct {
		ProductID      string `json:"product_id"`
		PricingOptions []struct {
			PricingOptionID string `json:"pricing_option_id"`
		} `json:"pricing_options"`
	}
	if err := json.Unmarshal(observedPayload, &products); err != nil {
		return &InvalidInputError{Field: "observed_payload", Reason: "not valid JSON array: " + err.Error()}
	}

	// Build index: product_id → set of observed pricing_option_ids.
	pricingByProduct := make(map[string]map[string]bool, len(products))
	for _, p := range products {
		opts := make(map[string]bool, len(p.PricingOptions))
		for _, o := range p.PricingOptions {
			opts[o.PricingOptionID] = true
		}
		pricingByProduct[p.ProductID] = opts
	}

	for _, c := range toCheck {
		if !pricingByProduct[c.productID][c.pricingOptionID] {
			return &PricingOptionError{
				Token:           token,
				ProductID:       c.productID,
				PricingOptionID: c.pricingOptionID,
			}
		}
	}
	return nil
}

// resolveNonOffered handles a redemption attempt against a continuation
// that is StatePending, StateCommitted, or StateFailed — i.e. every case
// other than a fresh, winning claim.
func (s *Store) resolveNonOffered(ctx context.Context, rec *ContinuationRecord, input *CompatibilityPurchaseCoordinatorInput, principal, reqHash string, now time.Time) (*Result, error) {
	// Check the requesting principal before revealing any claim metadata —
	// a different buyer must not learn whether their idempotency_key matches
	// the claimant's, and must not receive the committed result.
	if rec.Principal != principal {
		return nil, &PrincipalMismatchError{Token: rec.Token}
	}
	if rec.ClaimantKey != input.IdempotencyKey {
		return nil, &AlreadyClaimedError{Token: rec.Token, State: rec.State}
	}
	// Same idempotency_key as the claimant — this is meant to be an exact
	// retry. It is only exact if the payload matches too.
	if rec.RequestHash != reqHash {
		return nil, &RequestConflictError{Token: rec.Token, IdempotencyKey: input.IdempotencyKey}
	}

	switch rec.State {
	case StatePending:
		if now.Sub(rec.ClaimedAt) > s.opts.PendingLeaseTimeout {
			return nil, &AmbiguousClaimError{
				Token:     rec.Token,
				ClaimedAt: rec.ClaimedAt.Format(time.RFC3339),
				Guidance:  "the claiming process did not record a terminal outcome within the pending lease window; reconcile directly against the legacy seller (e.g. list media buys for this account/idempotency context) before treating this continuation as available again — it MUST NOT be retried automatically",
			}
		}
		return nil, &InFlightError{
			Token:      rec.Token,
			ClaimedAt:  rec.ClaimedAt.Format(time.RFC3339),
			RetryAfter: s.opts.PendingLeaseTimeout.String(),
		}
	case StateCommitted:
		return &Result{Response: rec.Result, Replayed: true}, nil
	case StateFailed:
		return nil, &TerminalFailureError{Token: rec.Token, ErrCode: rec.ErrorCode, Message: rec.ErrorMessage}
	default:
		// Unreachable: StateOffered is handled by the caller before
		// resolveNonOffered is invoked, and these are the only states.
		return nil, &AmbiguousClaimError{Token: rec.Token, Guidance: fmt.Sprintf("unrecognized continuation state %q", rec.State)}
	}
}

// execute runs exec exactly once for a freshly won claim and durably
// records its terminal outcome.
func (s *Store) execute(ctx context.Context, claimedRec *ContinuationRecord, input *CompatibilityPurchaseCoordinatorInput, exec Executor) (*Result, error) {
	resp, execErr := exec(ctx, input.LegacyCreateRequest)
	completedAt := s.opts.Clock()
	if execErr != nil {
		code, msg := classifyExecError(execErr)
		if _, ok, err := s.opts.Backend.FailPending(ctx, claimedRec.Token, input.IdempotencyKey, code, msg, completedAt); err != nil {
			return nil, err
		} else if !ok {
			// Could not record the failure — surface as ambiguous rather
			// than pretending the failure was cleanly recorded.
			return nil, &AmbiguousClaimError{
				Token:     claimedRec.Token,
				ClaimedAt: claimedRec.ClaimedAt.Format(time.RFC3339),
				Guidance:  "exec failed and the failure could not be durably recorded; reconcile directly before retrying",
			}
		}
		return nil, execErr
	}
	if _, ok, err := s.opts.Backend.CompletePending(ctx, claimedRec.Token, input.IdempotencyKey, resp, completedAt); err != nil {
		return nil, err
	} else if !ok {
		return nil, &AmbiguousClaimError{
			Token:     claimedRec.Token,
			ClaimedAt: claimedRec.ClaimedAt.Format(time.RFC3339),
			Guidance:  "exec succeeded but the result could not be durably recorded; reconcile directly against the legacy seller before treating this operation as unresolved",
		}
	}
	return &Result{Response: resp, Replayed: false}, nil
}

// classifyExecError extracts a (code, message) pair for FailPending's
// recovery guidance. Errors implementing an interface with a Code() string
// method (the convention every typed error in this SDK follows) contribute
// their code; everything else is recorded as a generic execution failure.
func classifyExecError(err error) (code, message string) {
	type coder interface{ Code() string }
	if c, ok := err.(coder); ok {
		return c.Code(), err.Error()
	}
	return "LEGACY_CREATE_FAILED", err.Error()
}

// ---- validation helpers ----

var uuidPattern = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

func validateInputStructure(input *CompatibilityPurchaseCoordinatorInput) error {
	if !uuidPattern.MatchString(input.IdempotencyKey) {
		return &InvalidInputError{Field: "idempotency_key", Reason: "must be a UUID (schema: format uuid)"}
	}
	if len(input.ContinuationToken) < 16 {
		return &InvalidInputError{Field: "continuation_token", Reason: "must be at least 16 characters"}
	}
	if !hasAccountIdentity(input.Account) {
		return &InvalidInputError{Field: "account", Reason: "must set account_id, or brand+operator"}
	}
	if len(input.SelectedProductIDs) == 0 {
		return &InvalidInputError{Field: "selected_product_ids", Reason: "must not be empty"}
	}
	if hasDuplicates(input.SelectedProductIDs) {
		return &InvalidInputError{Field: "selected_product_ids", Reason: "must not contain duplicates"}
	}
	if len(input.AcceptedLosses) < 2 {
		return &InvalidInputError{Field: "accepted_losses", Reason: "must contain at least 2 entries"}
	}
	if hasDuplicates(input.AcceptedLosses) {
		return &InvalidInputError{Field: "accepted_losses", Reason: "must not contain duplicates"}
	}
	if !containsAll(input.AcceptedLosses, LossFeedVersionNotAtomic, LossPricingVersionNotAtomic) {
		return &InvalidInputError{Field: "accepted_losses", Reason: "must contain feed_version_not_atomic and pricing_version_not_atomic (schema allOf/contains constraint)"}
	}
	for _, l := range input.AcceptedLosses {
		if l != LossFeedVersionNotAtomic && l != LossPricingVersionNotAtomic && l != LossMutationIdempotencyNotGuaranteed {
			return &InvalidInputError{Field: "accepted_losses", Reason: "unknown loss value: " + l}
		}
	}
	if len(input.LegacyCreateRequest) == 0 || bytes.Equal(bytes.TrimSpace(input.LegacyCreateRequest), []byte("{}")) {
		return &InvalidInputError{Field: "legacy_create_request", Reason: "must have at least one property (schema: minProperties 1)"}
	}
	return nil
}

func hasAccountIdentity(a adcp.AccountReference) bool {
	if a.AccountID != "" {
		return true
	}
	return a.Brand != nil && a.Brand.Domain != "" && a.Operator != ""
}

func containsAll(set []string, want ...string) bool {
	m := make(map[string]bool, len(set))
	for _, s := range set {
		m[s] = true
	}
	for _, w := range want {
		if !m[w] {
			return false
		}
	}
	return true
}

func hasDuplicates(items []string) bool {
	seen := make(map[string]bool, len(items))
	for _, it := range items {
		if seen[it] {
			return true
		}
		seen[it] = true
	}
	return false
}

func stringSetEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	sa, sb := sortedCopy(a), sortedCopy(b)
	for i := range sa {
		if sa[i] != sb[i] {
			return false
		}
	}
	return true
}

func stringSetSubset(subset, superset []string) bool {
	if len(subset) == 0 {
		return false
	}
	m := make(map[string]bool, len(superset))
	for _, s := range superset {
		m[s] = true
	}
	for _, s := range subset {
		if !m[s] {
			return false
		}
	}
	return true
}

func sortedCopy(items []string) []string {
	out := append([]string(nil), items...)
	sort.Strings(out)
	return out
}

func accountsEqual(a, b adcp.AccountReference) bool {
	ab, errA := json.Marshal(a)
	bb, errB := json.Marshal(b)
	if errA != nil || errB != nil {
		return false
	}
	return bytes.Equal(ab, bb)
}

// explicitPackageProductIDs parses legacyCreateRequest and requires
// explicit-package mode: a non-empty top-level "packages" array where every
// entry has a non-empty string "product_id". Returns the distinct product
// IDs found, in the order first seen.
func explicitPackageProductIDs(legacyCreateRequest json.RawMessage) ([]string, error) {
	var probe struct {
		Packages []struct {
			ProductID string `json:"product_id"`
		} `json:"packages"`
	}
	if err := json.Unmarshal(legacyCreateRequest, &probe); err != nil {
		return nil, &InvalidInputError{Field: "legacy_create_request", Reason: "not valid JSON: " + err.Error()}
	}
	if len(probe.Packages) == 0 {
		return nil, &ExplicitPackageModeError{Reason: "legacy_create_request.packages must be a non-empty array"}
	}
	seen := map[string]bool{}
	var ids []string
	for i, p := range probe.Packages {
		if p.ProductID == "" {
			return nil, &ExplicitPackageModeError{Reason: fmt.Sprintf("packages[%d].product_id is required for explicit-package mode", i)}
		}
		if !seen[p.ProductID] {
			seen[p.ProductID] = true
			ids = append(ids, p.ProductID)
		}
	}
	return ids, nil
}

// requestAccount extracts a top-level "account" field from
// legacyCreateRequest, when present. AdCP 2.5's create_media_buy has no
// wire account field, so ok is false there by design — the spec's explicit
// carve-out that a 2.5 adapter relies on the token-bound session instead.
func requestAccount(legacyCreateRequest json.RawMessage) (adcp.AccountReference, bool, error) {
	var probe struct {
		Account *adcp.AccountReference `json:"account"`
	}
	if err := json.Unmarshal(legacyCreateRequest, &probe); err != nil {
		return adcp.AccountReference{}, false, &InvalidInputError{Field: "legacy_create_request", Reason: "not valid JSON: " + err.Error()}
	}
	if probe.Account == nil {
		return adcp.AccountReference{}, false, nil
	}
	return *probe.Account, true, nil
}

// requestHash returns a stable, canonicalized hash of the logical
// redemption request, used to detect an idempotency_key reused with a
// different payload. selected_product_ids and accepted_losses are sorted
// before hashing since they are logically sets; legacy_create_request is
// run through idempotency.Canonicalize (JCS) so byte-level reordering of an
// equivalent JSON object does not register as a conflict.
func requestHash(input *CompatibilityPurchaseCoordinatorInput) (string, error) {
	canon, err := idempotency.Canonicalize(input.LegacyCreateRequest)
	if err != nil {
		return "", &InvalidInputError{Field: "legacy_create_request", Reason: "not valid JSON: " + err.Error()}
	}
	acctBytes, err := json.Marshal(input.Account)
	if err != nil {
		return "", &InvalidInputError{Field: "account", Reason: "could not encode: " + err.Error()}
	}

	h := sha256.New()
	h.Write(acctBytes)
	h.Write([]byte{0})
	for _, id := range sortedCopy(input.SelectedProductIDs) {
		h.Write([]byte(id))
		h.Write([]byte{0})
	}
	h.Write([]byte{0})
	for _, l := range sortedCopy(input.AcceptedLosses) {
		h.Write([]byte(l))
		h.Write([]byte{0})
	}
	h.Write([]byte{0})
	h.Write(canon)
	return hex.EncodeToString(h.Sum(nil)), nil
}
