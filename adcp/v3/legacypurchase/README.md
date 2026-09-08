# adcp/v3/legacypurchase

Durable coordinator for redeeming a deprecated AdCP 3.2 `products_available`
`legacy_create` purchase continuation — the Go SDK-local equivalent of the
protocol's `continueLegacyPurchase(CompatibilityPurchaseCoordinatorInput)`.

Spec: [`specs/legacy-compact-lifecycle-compatibility.md`](https://github.com/adcontextprotocol/adcp/blob/main/specs/legacy-compact-lifecycle-compatibility.md)
("Products-only brief compatibility" / `legacy_create`), added by
[adcp#6733](https://github.com/adcontextprotocol/adcp/pull/6733) and shipped
in the `3.2.0-beta.9` schema bundle this SDK pins at
[`adcp/v3/schemas/VERSION`](../schemas/VERSION). Tracks issue
[adcp-go#466](https://github.com/adcontextprotocol/adcp-go/issues/466).

## What this is for

AdCP 3.2 splits legacy `get_products`-with-a-brief compound purchase flows
into compact lifecycle tasks. When an application's compatibility
projection layer can only offer the caller a `products_available` outcome
with a `legacy_create` continuation (no truthful account-scoped feed/pricing
fence is available), that continuation is single-use, principal- and
account-bound, and short-lived. This package is the durable coordinator
that:

- records the continuation's bound facts the moment it is offered
  (`Store.RegisterContinuation`);
- validates a later redemption attempt against every one of those facts,
  atomically claims the continuation exactly once, invokes your legacy
  `create_media_buy` call, and durably records the terminal outcome so an
  exact `idempotency_key` retry returns the same result instead of buying
  twice (`Store.ContinueLegacyPurchase`).

```go
store := legacypurchase.New(legacypurchase.Options{
    Backend: legacypurchase.NewMemoryBackend(time.Minute, 24*time.Hour),
})

// At the moment your compatibility projection decides to offer
// products_available with a legacy_create continuation:
err := store.RegisterContinuation(ctx, &legacypurchase.Continuation{
    Token:             continuationToken,
    Principal:         authenticatedPrincipalID,
    Account:           account,
    SourceADCPVersion: "3.0",
    ExpiresAt:         time.Now().Add(5 * time.Minute),
    ProductIDs:        []string{"seller-product-1"},
    Losses:            []string{legacypurchase.LossFeedVersionNotAtomic, legacypurchase.LossPricingVersionNotAtomic},
    ObservedPayload:   observedProductsJSON, // the compact_projection.products payload actually returned
})

// Later, when the caller redeems it:
ctx = idempotency.WithPrincipal(ctx, authenticatedPrincipalID)
result, err := store.ContinueLegacyPurchase(ctx, input, func(ctx context.Context, legacyReq json.RawMessage) ([]byte, error) {
    return callLegacySellerCreateMediaBuy(ctx, legacyReq) // exactly-once
})
```

## Scope

Implemented and tested:

- `Store.RegisterContinuation` / `Store.ContinueLegacyPurchase` — the
  coordinator API.
- `Backend` — the pluggable durable-store interface.
- `MemoryBackend` — a fully concurrency-safe in-process reference
  implementation.
- Every binding check the spec states: principal, account (including the
  `legacy_create_request`-carried account field cross-check, with AdCP
  2.5's no-wire-account-field carve-out), expiry, exact loss-set
  acceptance, selected-product-ID subset-and-equality against the
  request's explicit packages.
- Atomic single-use claim, proven under `-race` with concurrent distinct
  `idempotency_key`s racing the same token (`store_race_test.go`).
- Deterministic replay: an exact retry (same `idempotency_key`, same
  payload) after success or terminal failure returns the recorded result,
  never a fresh `Executor` call.
- Fail-closed crash reconciliation: a claim left `StatePending` past
  `Options.PendingLeaseTimeout` returns `AmbiguousClaimError` with recovery
  guidance rather than being silently retried or silently expiring.
- The `products-only-brief-compatibility` vectors from the AdCP 3.2 schema
  bundle (`testdata/products-only-brief-compatibility/`, see its
  `PROVENANCE.md`), run end to end in `vectors_test.go`, plus the negative
  cases (product substitution, package-selection drift, incomplete/stale
  loss consent, wrong account, expiry) the vector bundle's own README
  documents as SDK-suite-constructed.

Deliberately deferred — disclosed, not silently dropped:

1. **A persistent (e.g. Postgres) `Backend`.** This package ships the
   interface and `MemoryBackend` only, matching how
   [`adcp/v3/idempotency`](../idempotency)'s Postgres adapter and
   [`adcp/v3/signing/pgreplay`](../signing) shipped as their own follow-on
   work. Tracked in
   [adcp-go#482](https://github.com/adcontextprotocol/adcp-go/issues/482).
2. **Full per-source-version (2.5/3.0/3.1) `create_media_buy` request
   schema validation.** This package enforces the structural rules tied
   directly to atomicity and single-use-claim safety (explicit-package
   mode, exact package-product-ID match, the account cross-check), not a
   complete replica of each legacy version's request schema — `adcp/v3` is
   an AdCP 3.x-only module and does not vendor those schemas. **Your
   `Executor` remains responsible for full legacy-request validation**
   before or as part of calling the real legacy seller.
3. **The reverse compact-seller → legacy-buyer server-side facade** (the
   spec's "Established buyers against a compact-backed seller" section,
   and `vectors.json`'s `reverse_compatibility_cases`). Materially separate
   scope from this buyer-side coordinator — tracked in the same
   [adcp-go#482](https://github.com/adcontextprotocol/adcp-go/issues/482).

Not a gap: `listed_purchase` continuations pass seller-issued,
account-scoped feed/pricing values straight into ordinary `buy_products`
per the spec — there is no durable continuation state for this coordinator
to claim on that path.

## Migration guidance — what remains application-owned

- **Minting the continuation.** This package does not decide *when* to
  offer `products_available`/`legacy_create` versus a native
  `request_proposals` result, or compute `ObservedPayload` — that is your
  compatibility-projection logic, per the spec's classification rules.
  `RegisterContinuation` only requires the result to be complete
  (non-empty `ObservedPayload`, both required atomic-fence losses present).
- **Legacy request construction and validation.** Building
  `legacy_create_request` for the caller's selected products, and
  validating it in full against the source version's actual
  `create_media_buy` schema, stays application-owned (see deferred item 2
  above). This package only enforces explicit-package mode and product-ID
  agreement before calling your `Executor`.
- **The actual legacy seller call.** `Executor` is where you place the real
  HTTP/MCP call to the legacy seller (or your own legacy facade). This
  package guarantees it runs at most once per claimed continuation; it does
  not implement retries, timeouts, or transport concerns for that call —
  bring your own `http.Client` / MCP client conventions.
- **Crash reconciliation.** `AmbiguousClaimError` tells you a claim's
  outcome is unknown; *how* to reconcile it (e.g. querying the legacy
  seller's own idempotent `get_media_buys` for a buyer-ref/idempotency
  marker your `Executor` embedded) is necessarily seller-specific and stays
  application-owned. Embed a stable, discoverable marker in your
  `legacy_create_request` (e.g. `buyer_ref`) specifically so this
  reconciliation is possible.
- **Backend durability and retention.** `MemoryBackend` is process-local —
  a restart loses in-flight and recent continuation state. Production
  deployments needing cross-restart or cross-instance durability need a
  persistent `Backend` (deferred item 1 above) or their own implementation
  of the `Backend` interface.
