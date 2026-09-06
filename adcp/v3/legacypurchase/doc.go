// Package legacypurchase implements the SDK-local durable coordinator for
// redeeming a deprecated AdCP 3.2 "products_available" legacy_create
// purchase continuation — the Go SDK-local equivalent of the protocol
// contract's continueLegacyPurchase(CompatibilityPurchaseCoordinatorInput).
//
// # Source of the contract
//
// This package implements the normative rules in
// specs/legacy-compact-lifecycle-compatibility.md ("Products-only brief
// compatibility" / "legacy_create") from adcontextprotocol/adcp, added by
// https://github.com/adcontextprotocol/adcp/pull/6733 (merged 2026-08-20,
// resolving https://github.com/adcontextprotocol/adcp/issues/6716) and
// shipped in the AdCP 3.2.0-beta.9 schema bundle this SDK pins at
// adcp/v3/schemas/VERSION. CompatibilityPurchaseCoordinatorInput's field
// shape mirrors media-buy/legacy-purchase-continuation-input.json from that
// bundle exactly.
//
// The Go type is hand-written rather than generated: the schema is marked
// x-adcp-sdk-local (it is explicitly not an AdCP wire tool — "the input
// object MUST NOT be sent to the seller") and is not reachable by $ref from
// any tool request/response schema, so adcp/v3/schemas/generate.py's
// auto-discovery does not produce a Go type for it. Verified by downloading
// the pinned 3.2.0-beta.9 bundle and regenerating adcp/v3/types_gen.go
// locally: no CompatibilityPurchaseCoordinatorInput or PurchaseContinuation
// type is emitted, confirming this is by design rather than a generator
// gap.
//
// # Design precedent
//
// This coordinator follows the same claim-once, pluggable-Backend shape as
// adcp/v3/idempotency (PutIfAbsent-based replay) and
// adcp/v3/signing/pgreplay (atomic-insert replay-cap enforcement) — the two
// existing "durable, pluggable, atomic-claim" packages in this codebase.
// It widens that shape from a single PutIfAbsent into an explicit
// three-state FSM (StateOffered -> StatePending -> StateCommitted or
// StateFailed) because a legacy purchase continuation is a genuinely
// two-phase operation: claim the token, then execute an external legacy
// create_media_buy call whose outcome is not yet known at claim time. A
// crash between those two steps must be *observable* as StatePending
// rather than silently vanishing (double-purchase risk) or silently
// resolving on its own (a fabricated result) — see AmbiguousClaimError.
//
// # Scope of this package
//
// Implemented and tested here:
//   - Store.RegisterContinuation: durably records a legacy_create
//     continuation's bound facts (principal, account, source version,
//     expiry, product IDs, required loss set, and the observed
//     product/pricing payload it was minted against) at the moment an
//     application's compatibility-projection layer decides to offer one.
//   - Store.ContinueLegacyPurchase: validates a
//     CompatibilityPurchaseCoordinatorInput against every binding rule the
//     spec states (principal, account, expiry, exact loss-set acceptance,
//     selected-product-ID subset-and-equality against the legacy_create
//     request's explicit packages), atomically claims the token exactly
//     once, invokes a caller-supplied Executor, and durably records the
//     terminal result so an exact idempotency_key retry returns the
//     deterministic prior result rather than re-invoking Executor or the
//     legacy seller.
//   - Backend: the pluggable durable-store interface, plus MemoryBackend,
//     a fully concurrency-safe in-process reference implementation.
//
// Deliberately deferred, disclosed rather than silently omitted:
//
//  1. A persistent (e.g. Postgres) Backend implementation. This package
//     ships the interface and a well-tested in-memory reference backend
//     only, mirroring how adcp/v3/idempotency's Postgres adapter and
//     adcp/v3/signing/pgreplay shipped as follow-on work after their
//     respective in-memory/interface PRs. A distributed Backend is real,
//     separable work (schema design, connection lifecycle, a real
//     concurrency proof against a live database) — tracked in
//     https://github.com/adcontextprotocol/adcp-go/issues/482.
//
//  2. Full per-source-version (AdCP 2.5 / 3.0 / 3.1) JSON Schema validation
//     of legacy_create_request against each version's actual
//     create-media-buy-request schema. This package enforces the
//     structural invariants the spec ties directly to atomicity and
//     single-use-claim safety — explicit-package mode, and that the
//     request's distinct package product IDs exactly equal
//     selected_product_ids — plus the account cross-check when the source
//     version's request schema carries an account field. It does not
//     replicate each legacy version's full request schema, which this SDK
//     (adcp/v3, an AdCP 3.x-only module) does not vendor. Application code
//     remains responsible for full legacy-version request validation
//     before or inside its Executor.
//
//  3. The reverse compact-seller -> legacy-buyer server-side facade (the
//     spec's "Established buyers against a compact-backed seller"
//     section). This is seller-side adapter work — preserving a 3.2
//     seller's deprecated get_products/create_media_buy facades for
//     established buyers — a materially different and separable scope from
//     the buyer-side coordinator this package implements. Tracked in the
//     same https://github.com/adcontextprotocol/adcp-go/issues/482.
//
// listed_purchase is not a gap: per the spec, a listed_purchase
// continuation carries a seller-issued, account-scoped feed/pricing fence
// straight into ordinary buy_products, with no durable continuation state
// to claim. There is nothing for this coordinator to do on that path.
package legacypurchase
