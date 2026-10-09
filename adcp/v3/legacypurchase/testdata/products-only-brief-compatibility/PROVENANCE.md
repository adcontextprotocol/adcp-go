# Provenance

`vectors.json` and `UPSTREAM_README.md` (renamed from the upstream `README.md`
to avoid colliding with this package's own `testdata` conventions) are a
byte-for-byte copy of
`static/compliance/source/test-vectors/products-only-brief-compatibility/`
from `adcontextprotocol/adcp`, added by
[adcp#6733](https://github.com/adcontextprotocol/adcp/pull/6733) ("Define
legacy and compact lifecycle compatibility", merged 2026-08-20, commit
`e72ff10b5bdd1da1491918120656395b5d395a7e`) and shipped in the
`3.2.0-beta.9` schema bundle this repo pins at `adcp/v3/schemas/VERSION`
(confirmed identical: `diff`'d against the locally-downloaded pinned bundle's
copy of the same file with zero content differences).

Do not hand-edit `vectors.json`. Re-sync it from the `adcp` repo (or a future
signed bundle release that vendors compliance vectors alongside schemas)
when the upstream vectors change.

## What this package exercises against the vectors

- `cases[]` (`legacy_create` continuations for AdCP 2.5.3 / 3.0.18 / 3.1.15
  sources) — exercised end-to-end in `vectors_test.go`: register the
  continuation from `compact_projection`, redeem it via
  `Store.ContinueLegacyPurchase` using `continuation_input` verbatim, and
  assert the `Executor` receives exactly `legacy_create_request` and a
  retry with the same `idempotency_key` returns the deterministic prior
  result. Negative variants (product substitution, package-selection drift,
  stale/incomplete loss consent, wrong account, wrong principal) are
  constructed in `vectors_test.go` and `store_test.go` by mutating these
  same fixtures, per this vector set's own `UPSTREAM_README.md` ("SDK suites
  consume the same vectors to exercise expiry, principal/account binding,
  atomic token claim, exact retry, and crash reconciliation against their
  durable coordinator implementations") — the upstream bundle does not ship
  the negative cases as separate JSON fixtures.

- `listed_purchase_cases[]` — intentionally **not** exercised against
  `Store`. Per
  `specs/legacy-compact-lifecycle-compatibility.md#listed_purchase`, a
  `listed_purchase` continuation carries a seller-issued, account-scoped
  feed/pricing fence straight into ordinary `buy_products` — "The
  coordinator passes those seller-issued values to ordinary buy_products."
  There is no durable continuation state to claim for this branch; it is
  structurally out of scope for a claim coordinator, not a deferred gap.

- `reverse_compatibility_cases[]` — **not** exercised. This is the seller
  side of the compatibility contract (an AdCP 3.2 seller preserving its
  deprecated `get_products`/`create_media_buy` facades for established
  buyers, described in the spec's "Established buyers against a
  compact-backed seller" section). It is a materially separate,
  substantial scope of server-side adapter work from the buyer-side
  coordinator this package implements, and is out of scope for this PR — see
  the linked follow-up issue in the package README.
