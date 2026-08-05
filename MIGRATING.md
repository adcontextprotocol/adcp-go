# Migrating adcp-go

## Migrating to adcp-go v3 / sub-modules

`adcp-go` 3.0 reshapes the repository into a Go multi-module workspace. If you
consume this repository from a downstream Go service, read this section before
upgrading.

### What changed at a glance

- **`adcp/v3` replaces `adcp` for new work.** The generated schema types and
  MCP helpers now live under `github.com/adcontextprotocol/adcp-go/adcp/v3`.
  The legacy `github.com/adcontextprotocol/adcp-go/adcp` module is frozen at
  `v2.1.1` and will only receive security backports (see the support window
  section below).
- **`tmproto`, `tmpclient`, and `targeting` are their own modules.** Each has
  an independent tag prefix (`tmproto/vX.Y.Z`, etc.) and its own SemVer
  cadence. Their Go import paths did not change — only the module boundary
  did, so in-repo `.go` code keeps the same `import` statements. Downstream
  `go.mod` files, however, need explicit `require` lines for each sub-module
  the service actually imports.
- **`urlcanon` and the `registry` family became sub-modules.** `registry` and
  `urlcanon` are already published at `v0.1.0`. The optional
  `registry/redisstore` and `registry/glidestore` backends live in the tree
  and can be tagged on demand.
- **`adcp/vN` majors track the AdCP protocol spec's major version.** `adcp/v3`
  speaks AdCP 3.x. When the AdCP protocol spec bumps to 4.0 (upstream cadence
  targets early 2027), `adcp/v4` gets cut. There will be no Go-only major
  bumps of `adcp` inside a spec-major lifecycle. See the alignment policy
  below for details.

### Import path map

The table below covers every import path a downstream Go service is likely to
hold today. The right-hand column is the go-forward path.

| Old import path | New import path | Notes |
| --- | --- | --- |
| `github.com/adcontextprotocol/adcp-go/adcp` | `github.com/adcontextprotocol/adcp-go/adcp/v3` | Package name stays `adcp` (Go SIV). |
| `github.com/adcontextprotocol/adcp-go/adcp/signing` | `github.com/adcontextprotocol/adcp-go/adcp/v3/signing` | Moves with the parent module. |
| `github.com/adcontextprotocol/adcp-go/adcp/cmd/adcp-signing-keygen` | `github.com/adcontextprotocol/adcp-go/adcp/v3/cmd/adcp-signing-keygen` | `go run` target relocated. |
| `github.com/adcontextprotocol/adcp-go/tmproto` | `github.com/adcontextprotocol/adcp-go/tmproto` | Same path, now its own module. |
| `github.com/adcontextprotocol/adcp-go/tmpclient` | `github.com/adcontextprotocol/adcp-go/tmpclient` | Same path, now its own module. |
| `github.com/adcontextprotocol/adcp-go/targeting` | `github.com/adcontextprotocol/adcp-go/targeting` | Same path, now its own module. |
| `github.com/adcontextprotocol/adcp-go/targeting/fcap` | `github.com/adcontextprotocol/adcp-go/targeting/fcap` | Sub-package of the `targeting` module. |
| `github.com/adcontextprotocol/adcp-go/urlcanon` | `github.com/adcontextprotocol/adcp-go/urlcanon` | Same path, now its own module. |
| `github.com/adcontextprotocol/adcp-go/registry` | `github.com/adcontextprotocol/adcp-go/registry` | Same path, now its own module. |
| `github.com/adcontextprotocol/adcp-go/registry/redisstore` | `github.com/adcontextprotocol/adcp-go/registry/redisstore` | Nested module — separate `require` line if used. |
| `github.com/adcontextprotocol/adcp-go/registry/glidestore` | `github.com/adcontextprotocol/adcp-go/registry/glidestore` | Nested module — separate `require` line if used. |

For everything except `adcp`, the Go `import` statements in downstream code
stay byte-identical. Only the `go.mod` `require` block needs updating so the
Go toolchain resolves each sub-module against its own tag.

For `adcp`, every occurrence of `.../adcp` in an `import` line needs to become
`.../adcp/v3`. Per Go's Semantic Import Versioning rule, the package name is
still `adcp`; only the import path suffix changes. `adcp.Foo` in your code
does not need to change — the identifier resolves via the import.

### Currently-published modules

| Import path | Current version | Tag format |
| --- | --- | --- |
| `github.com/adcontextprotocol/adcp-go/urlcanon` | `v0.1.0` | `urlcanon/vX.Y.Z` |
| `github.com/adcontextprotocol/adcp-go/registry` | `v0.1.0` | `registry/vX.Y.Z` |
| `github.com/adcontextprotocol/adcp-go/registry/redisstore` | (not yet cut) | `registry/redisstore/vX.Y.Z` |
| `github.com/adcontextprotocol/adcp-go/registry/glidestore` | (not yet cut) | `registry/glidestore/vX.Y.Z` |
| `github.com/adcontextprotocol/adcp-go/tmproto` | `v0.1.0` | `tmproto/vX.Y.Z` |
| `github.com/adcontextprotocol/adcp-go/tmpclient` | `v0.1.0` | `tmpclient/vX.Y.Z` |
| `github.com/adcontextprotocol/adcp-go/targeting` | `v0.1.0` | `targeting/vX.Y.Z` |
| `github.com/adcontextprotocol/adcp-go/adcp/v3` | `v3.0.0` | `adcp/vX.Y.Z` |
| `github.com/adcontextprotocol/adcp-go/adcp` (frozen v2) | `v2.1.1` | `adcp-vX.Y.Z` (legacy hyphen — see history below) |

The top-level module `github.com/adcontextprotocol/adcp-go` remains untagged
and is not an intended external import target. It contains internal glue —
test helpers, workspace scaffolding, and cross-module examples — that stitch
the sub-modules together for in-repo development.

### Sample `go.mod` diff — downstream service

Assume a service today imports `.../adcp` for schema types and
`.../targeting/fcap` for the frequency-cap helpers. Before the reshape:

```gomod
module example.com/my-service

go 1.23

require (
    github.com/adcontextprotocol/adcp-go v0.0.0-20260701123456-abcdef012345
)
```

The service pinned the root module at a pseudo-version because no
`go get`-able `adcp` semver tag ever existed (see the tag-naming history
below). After the reshape:

```gomod
module example.com/my-service

go 1.23

require (
    github.com/adcontextprotocol/adcp-go/adcp/v3 v3.0.0
    github.com/adcontextprotocol/adcp-go/targeting v0.1.0
)
```

Each sub-module is now pinned to a real tag independently. Bumping the
targeting engine no longer forces a schema-types bump, and vice versa.

If the service also uses TMP message types, add the wire-types module:

```gomod
require github.com/adcontextprotocol/adcp-go/tmproto v0.1.0
```

If the service embeds the publisher-side TMP client, add:

```gomod
require github.com/adcontextprotocol/adcp-go/tmpclient v0.1.0
```

The corresponding `go.sum` entries are added automatically by `go mod tidy`.

### Recipe: keeping v2

Some downstream services will want to stay on the pre-reshape SDK for one
more release cycle. The frozen module continues to build; existing
pseudo-version pins keep resolving.

```sh
# Keep or add this to your go.mod:
require github.com/adcontextprotocol/adcp-go/adcp v2.1.1
```

Historical note: the `adcp-v2.1.1` tag on this repository does not match Go's
nested-module tag convention (see the tag-naming history section below), so
`go get github.com/adcontextprotocol/adcp-go/adcp@v2.1.1` was never
guaranteed to resolve to that exact tag. In practice, downstream services
pinned the root module at a pseudo-version off `main` at the commit that
carried the v2.1.1 tag, or used a `replace` directive against a local
checkout. That behaviour is stable and unchanged — v2.1.1 remains the last
non-security release of the v2 line, and no new v2 tags will be cut except
under the security backport policy below.

### Recipe: migrating to v3

The mechanical steps for a downstream service:

```sh
go get github.com/adcontextprotocol/adcp-go/adcp/v3@v3.0.0
```

Then update every `import` statement:

```go
// before
import "github.com/adcontextprotocol/adcp-go/adcp"

// after
import "github.com/adcontextprotocol/adcp-go/adcp/v3"
```

`gofmt`-friendly one-liner across the tree:

```sh
find . -name '*.go' -print0 | xargs -0 sed -i '' \
  's#github.com/adcontextprotocol/adcp-go/adcp"#github.com/adcontextprotocol/adcp-go/adcp/v3"#g'
```

(On GNU sed drop the `''` after `-i`.) Cover sub-packages under `adcp/` the
same way — e.g. `adcp/signing` becomes `adcp/v3/signing`.

The package identifier does not change. `adcp.CreateMediaBuyRequest` is still
`adcp.CreateMediaBuyRequest` after the switch; only the module resolution
path is different. Nothing in call sites, struct construction, or interface
implementations needs to be rewritten for the SIV rename alone.

The remainder of this document (below) describes the schema-level changes
introduced within the v3 line — those code updates are the actual work of
the upgrade. Field-level breaking changes are called out per section.

After updating imports, run:

```sh
go mod tidy
go build ./...
go test ./...
```

`go mod tidy` will pull in the new sub-module `require` lines automatically
based on the imports it discovers.

### Recipe: rollback

If a v3 upgrade fails in staging, reverting is safe:

```sh
# undo the require on adcp/v3
go mod edit -droprequire=github.com/adcontextprotocol/adcp-go/adcp/v3

# re-pin the frozen v2 module at a pseudo-version if you never had a
# resolvable adcp-v* tag (see history), or at v2.1.1 if you did
go get github.com/adcontextprotocol/adcp-go/adcp@v2.1.1

go mod tidy
```

Then revert the `import` path updates. `git checkout -- '*.go'` on a clean
branch is often faster than a scripted rename.

Caveat: the frozen v2 module receives security patches only. If the reason
for rollback is a v3 API mismatch, plan the re-migration within the v2
security-support window (12 months post-v3 GA — see below).

### Tag-naming history

The `adcp` v1 and v2 tags on this repository use a hyphen prefix
(`adcp-v1.0.0` through `adcp-v2.1.1`) rather than the slash prefix that Go's
nested-module tag convention requires (`adcp/vX.Y.Z`). The Go toolchain
resolves nested-module tags by looking for `<module-path>/vX.Y.Z` in the
repository's git tags; a tag named `adcp-v2.1.1` does not match that lookup
for the module `github.com/adcontextprotocol/adcp-go/adcp`.

Consequence: **no `adcp` semver tag has actually been resolvable via
`go get` on the v1 or v2 line.** Every downstream service that ever
integrated a versioned `adcp` was in practice on:

- a pseudo-version off `main` at a chosen commit (`v0.0.0-YYYYMMDDHHMMSS-<sha>`), or
- a `replace` directive against a local checkout or fork.

This is not being fixed retroactively. Retagging historical commits with the
slash-prefixed convention would create new module versions that resolve to
the same source, but any consumer already pinned by pseudo-version SHA would
see a superset of versions available and could accidentally upgrade past
what they had validated. The v2 line is frozen as it stands.

Going forward, **all `adcp/vN` releases use the canonical Go nested-module
tag format** (`adcp/vX.Y.Z`). `adcp/v3.0.0` is the first `adcp` version that
is directly resolvable via `go get github.com/adcontextprotocol/adcp-go/adcp/v3@v3.0.0`
without pseudo-version workarounds.

Do not expect new `adcp-v*` (hyphen) tags. If you see one on this
repository, it is either a legacy tag from the v1/v2 era or a security
backport cut from the `adcp-v2.1.1` maintenance branch (see below).

### Spec-major alignment policy

Starting with v3, the major version of `adcp/vN` tracks the major version of
the AdCP protocol specification 1:1:

| `adcp/vN` module version | AdCP protocol spec | Status |
| --- | --- | --- |
| `adcp/v3.x.y` | AdCP 3.x | Current |
| `adcp/v4.x.y` (future) | AdCP 4.x | When the spec bumps to 4.0 |

Rules that follow from this alignment:

- **No Go-only major bumps of `adcp` inside a spec-major lifecycle.**
  Breaking Go-side API changes — struct field type flips that are not
  additive, package restructures under `adcp/vN/`, or interface method
  additions on interfaces intended to be implemented by callers — either
  wait for the next AdCP protocol spec major, or ship inside the current
  major as deprecated-plus-added pairs.
- **Additive schema changes ship as minors.** New AdCP 3.x fields, new
  optional request parameters, new response variants, and new tools land as
  `adcp/v3.N.0` releases. Downstream services can adopt them at their own
  pace.
- **Bug fixes ship as patches.** `adcp/v3.x.Y` patch releases fix codegen
  bugs, validator issues, and non-breaking schema-conformance corrections.
- **Spec-major bumps are rare and telegraphed.** The AdCP protocol spec's
  own cadence commitment is a minimum 18-month floor between majors, and at
  least 12 months of security support for the prior major after a new one
  ships. `adcp-go` inherits that cadence: `adcp/v4` is not on the near-term
  roadmap and will not appear until AdCP 4.0 does.

This alignment applies **only to the `adcp/` module.** The other sub-modules
(`tmproto`, `tmpclient`, `targeting`, `urlcanon`, `registry`, and the
`registry/*` backends) stay on independent SemVer with their own cadences.
`tmproto v1.0.0` and `targeting v2.0.0` will happen when those modules'
public Go APIs require it, on schedules independent of the AdCP spec's own
major-version cadence.

### Version support windows

The maintainer commitments for the currently-published modules:

| Module | Current version | Support commitment |
| --- | --- | --- |
| `adcp/v3` | `v3.0.0` | Active development. Additive minors, patch fixes. |
| `adcp` (frozen v2) | `v2.1.1` | Security backports for 12 months post-`adcp/v3.0.0` GA. Hand-cut from a maintenance branch. |
| `tmproto` | `v0.1.0` | Standard SemVer. Patches safe; minors additive. |
| `tmpclient` | `v0.1.0` | Standard SemVer. Patches safe; minors additive. |
| `targeting` | `v0.1.0` | Standard SemVer. Patches safe; minors additive. |
| `urlcanon` | `v0.1.0` | Standard SemVer. Patches safe; minors additive. |
| `registry` | `v0.1.0` | Standard SemVer. Patches safe; minors additive. |
| `registry/redisstore` | (not yet cut) | Cut on demand. Standard SemVer once tagged. |
| `registry/glidestore` | (not yet cut) | Cut on demand. Standard SemVer once tagged. |

The 12-month v2 security window aligns with the AdCP protocol spec's own
"minimum 12 months of prior-major security support" commitment. If a
critical patch is required against `adcp v2.1.1` during that window, it is
hand-cut from a maintenance branch off the `adcp-v2.1.1` tag using the
legacy hyphen tag format (`adcp-v2.1.2`, etc.). Consumers on
pseudo-versions off the frozen v2 module path pick up the fix by rebasing
onto the new pseudo-version. After the 12-month window closes, no further
v2 patches will be cut except by mutual agreement.

Two-phase extraction note: every sub-module in the reshape was extracted in
two steps — first with `require v0.0.0 + replace` in consumers, then a real
tag plus dropping the replaces. Downstream services see only the finished
state; the transitional state was carried inside this repository's history.

### Where to file bugs

- Import-path or module-boundary confusion: file an issue on this
  repository and reference the module by its full path.
- Field-level schema questions (e.g. "what shape should `X` have?"):
  cross-check the AdCP protocol spec first, then file here.
- Spec-level ambiguity: file on the AdCP protocol spec repository, not
  here. `adcp-go` follows the spec — if two implementations disagree, the
  spec is the arbiter.

## Next: schema-backed typed SDK fields

This release tightens buyer, seller, and governance SDK surfaces around AdCP
3.0.12 schemas. Most wire payloads are unchanged, but several public Go structs
are more typed. Code that built these fields with `map[string]any` should move
to the generated structs below.

Optional object references are pointers: nil omits the field, and `&T{}` or
`adcp.Ptr(T{})` emits it. Required fields inside the nested struct still need to
be populated.

- Two inline object fields that previously used `any` are now typed:
  `ListCreativesRequest.Sort` is `*adcp.ListCreativesSort`, and
  `ArtifactWebhookPayload.Pagination` is `*adcp.ArtifactWebhookPagination`.
  Use nil to omit them.
- Two more generated inline object fields that previously used `any` are now
  typed: `PerformanceFeedback.MeasurementPeriod` is
  `adcp.DatetimeRange`, and `PlannedDelivery.Geo` is `*adcp.PlannedDeliveryGeo`.
  Populate `DatetimeRange.Start` and `DatetimeRange.End`; zero-value
  `DatetimeRange{}` is not a valid wire payload. Use nil to omit
  `PlannedDelivery.Geo`.
- Two additional inline object fields that previously used `any` are now typed:
  `Account.Setup` is `*adcp.AccountSetup`, and `CreativeBrief.Messaging` is
  `*adcp.CreativeBriefMessaging`. `AccountSetup` now includes `ExpiresAt`.
- `AccountSetup.Message` now always marshals when `AccountSetup` is present,
  matching the schema-required `message` field.
- `CreativeBriefMessaging.CTA` uses Go acronym casing for the `cta` JSON field.
- Three additional inline object fields that previously used `any` are now
  typed: `CreativeBrief.Compliance` is `*adcp.CreativeBriefCompliance`,
  `EventCustomData.Contents` is `[]adcp.EventContentItem`, and
  `PolicyCategoryDefinition.RegulatoryFrameworks` is
  `[]adcp.PolicyRegulatoryFramework`.
- Two more generated fields that previously used `any` are now typed:
  `CreativeFormat.SupportedMacros` is `[]string`, and `UserMatch.UIDs` is
  `[]adcp.UserMatchUID`.
- Product discovery filters are now typed:
  `GetProductsRequest.Filters` is `*adcp.ProductFilters`. Nested product
  filter objects such as `BudgetRange`, `TrustedMatch`, `GeoProximity`, and
  `Keywords` are generated types. `ProductFilters.RequiredFeatures` is
  `map[string]bool`, matching the schema's open feature-flag bag. GeoJSON
  coordinates remain `[]any` because Polygon and MultiPolygon coordinate arrays
  have different nesting shapes.
  `ProductFilters` does not include an `Ext` or `Extra` field in the AdCP
  3.0.12 bundle, so arbitrary unknown filter keys that were possible when
  `Filters` was `any` are no longer represented by the typed Go struct. Put
  seller-specific request metadata under a schema-backed field where one exists,
  or wait for the protocol to add an explicit `filters.ext` extension point
  before relying on typed SDK support.
- `Targeting.GeoProximity` is now `[]adcp.GeoProximityTarget` instead of
  `[]any`. Latitude and longitude are `*float64` so explicit zero coordinates
  still marshal. `GeoProximityGeometry.Coordinates` remains `[]any` for the
  same GeoJSON Polygon/MultiPolygon nesting reason.
- `CheckGovernanceRequest.DeliveryMetrics` is now
  `*adcp.GovernanceDeliveryMetrics` instead of `any`. Optional numeric delivery
  values such as `Spend` and `Impressions` are pointers so explicit zero values
  still marshal; use `adcp.Ptr(0.0)` or `adcp.Ptr(0)` when zero is meaningful.
- `ReportPlanOutcomeRequest.Delivery` is now
  `*adcp.ReportPlanOutcomeDelivery` instead of `any`. Optional numeric values
  such as `Spend`, `CPM`, and `Impressions` are pointers so explicit zero values
  still marshal. The schema's open `additionalProperties` allowance is not
  exposed as a synthetic `Extra`/`Ext` map; use the schema-authored fields.
- `ReportPlanOutcomeRequest.SellerResponse` is now
  `*adcp.ReportPlanOutcomeSellerResponse` instead of `any`. Optional budget
  values such as `CommittedBudget` and package `Budget` are pointers so explicit
  zero values still marshal. The schema's open `additionalProperties` allowance
  is not exposed as a synthetic `Extra`/`Ext` map; use the schema-authored
  fields.
- `ReportPlanOutcomeRequest.Error` is now
  `*adcp.ReportPlanOutcomeError` instead of `any`, with schema-backed `Code`
  and `Message` fields.
- `ReportPlanOutcomeResponse.PlanSummary` is now
  `*adcp.ReportPlanOutcomePlanSummary` instead of `any`. Its optional budget
  values, `TotalCommitted` and `BudgetRemaining`, are pointers so explicit zero
  values still marshal. `ReportPlanOutcomeResponse.CommittedBudget` remains a
  value field because it is a per-outcome delta where an omitted zero is not
  currently distinct from no committed budget.
- `PolicyEntry.Exemplars` is now `*adcp.PolicyExemplars` instead of `any`.
  `PolicyExemplars.Pass` and `PolicyExemplars.Fail` are
  `[]adcp.PolicyExemplar`.
- `SyncGovernanceSuccess.Accounts` is now
  `[]adcp.SyncGovernanceAccountResult` instead of `[]any`.
  `GovernanceAgents` is now `[]adcp.SyncGovernanceAgentResult`; per-account
  `Errors` remains `[]adcp.AdcpError`.
- `PreviewCreativeRequest.Inputs` is now `[]adcp.PreviewCreativeInput` and
  `PreviewCreativeRequest.Requests` is now
  `[]adcp.PreviewCreativeBatchRequest` instead of `[]any`. Batch request
  `Inputs` also uses `[]adcp.PreviewCreativeInput`.
- `ForcedDirectiveSuccess.Forced` is now `adcp.ForcedDirective` instead of
  `any`. The reference seller now emits directive `message` at the response
  top level, matching the schema, instead of inside `forced`.
- Top-level response unions are now closed interfaces instead of `any` aliases:
  `SyncGovernanceResponse`, `CreateMediaBuyResponse`,
  `ProvidePerformanceFeedbackResponse`, and `ComplyTestControllerResponse`.
  Use the generated variant structs such as `CreateMediaBuySuccess`,
  `CreateMediaBuyError`, and `CreateMediaBuySubmitted`. Code that assigned
  arbitrary `map[string]any` values or custom dynamic wrapper types to these
  response names should switch to a schema-owned variant or keep the value as
  caller-owned dynamic JSON outside the generated response interface. Direct
  `encoding/json` unmarshalling into these interface names is not supported;
  decode into a concrete variant when the branch is known, or into
  `json.RawMessage` first when branch selection depends on the payload.
  `CreateMediaBuyResult` remains available as a deprecated alias for
  `CreateMediaBuyResponse`; new handler code should use
  `CreateMediaBuyResponse`.
- Product refinement arrays are now typed:
  `GetProductsRequest.Refine` is `[]adcp.GetProductsRefineItem`, and
  `GetProductsResponse.RefinementApplied` is
  `[]adcp.GetProductsRefinementAppliedItem` instead of `[]map[string]any`.
  These are flattened structs for the schema's `scope`-keyed branches; populate
  the fields that apply to the selected scope.
- `GetProductsResponse.Incomplete` is now
  `[]adcp.GetProductsIncompleteItem` instead of `[]any`. `EstimatedWait` is
  `*adcp.Duration`; use nil when the seller cannot estimate a retry interval.
- `SyncPlansResponse.Plans` is now `[]adcp.SyncPlansPlan` instead of
  `[]any`. Nested `Categories` and `ResolvedPolicies` are typed as
  `[]adcp.SyncPlansPlanCategory` and `[]adcp.SyncPlansResolvedPolicy`.
- Governance findings are now typed:
  `CheckGovernanceResponse.Findings` is `[]adcp.CheckGovernanceFinding`, and
  `ReportPlanOutcomeResponse.Findings` is `[]adcp.ReportPlanOutcomeFinding`.
  `CheckGovernanceFinding.Details` and `ReportPlanOutcomeFinding.Details`
  remain `map[string]any` because finding details are category-specific
  structured payloads. `CheckGovernanceFinding.Confidence` is `*float64`; use
  nil when confidence is absent, and use a pointer only when intentionally
  sending a schema-valid confidence value, including `adcp.Ptr(0.0)`.
- `CheckGovernanceResponse.Conditions` is now
  `[]adcp.CheckGovernanceCondition` instead of `[]any`.
  `CheckGovernanceCondition.RequiredValue` remains `any` because a governance
  condition can require different JSON value types. Treat decoded values as
  generic JSON data (`nil`, `bool`, `string`, `float64`, `[]any`, or
  `map[string]any`) or decode them into caller-owned types before use; numeric
  values decode as `float64`. Use `HasRequiredValue` to distinguish an absent
  advisory `required_value` from an explicit one, because `RequiredValue == nil`
  can mean either absent or explicit JSON `null`. When constructing responses,
  set `HasRequiredValue: true` whenever `required_value` should be present on
  the wire. To send `required_value: null`, set `HasRequiredValue: true` and
  leave `RequiredValue` nil.
- `GetPlanAuditLogsResponse.Plans` is now `[]adcp.PlanAuditLog` instead of
  `[]any`. Nested audit-log shapes are also typed, including
  `PlanAuditBudget`, `PlanAuditSummary`, `PlanAuditEntry`,
  `PlanAuditFinding`, and `PlanAuditGovernedAction`. Optional numeric counters
  and metrics across the audit log use pointers so absent values are not
  re-emitted as zero. Use `adcp.Ptr(0)` for explicit zero integer counters
  such as `ChecksPerformed` and `Statuses.Approved`, and `adcp.Ptr(0.0)` or
  `adcp.Float64(0)` for explicit zero float metrics such as `Budget.Authorized`
  and `PlanAuditEntry.CommittedBudget`.
- Optional numeric fields where explicit zero is meaningful are now pointers:
  `PricingOption.FixedPrice`, `AudienceSelector.MinValue`,
  `AudienceSelector.MaxValue`, `ForecastPoint.Budget`,
  `CreativeAsset.Weight`, `KeywordTarget.BidPrice`, `PackageInput.BidPrice`,
  `PackageInput.Impressions`, `PackageUpdate.Budget`,
  `PackageUpdate.BidPrice`, `PackageUpdate.Impressions`, and
  `KeywordTargetUpdate.BidPrice`. Use nil to omit the field, and
  `adcp.Ptr(0.0)` or `adcp.Float64(0)` when the wire payload must include an
  explicit zero.
  `PackageInput.Budget` remains `float64` because it is required by the create
  package schema; `PackageUpdate.Budget` is `*float64` because package updates
  can omit budget or explicitly set it to zero.
- `UpdateMediaBuyRequest.Canceled` and `PackageUpdate.Canceled` are `*bool`.
  Use nil when the field is absent and `adcp.Bool(true)` when requesting
  cancellation. The AdCP schema constrains `canceled` to true; do not send
  `adcp.Bool(false)` to mean resume. Use `Paused: adcp.Bool(false)` for resume.
- `CreativeAssignments` is now `[]adcp.CreativeAssignment`. Use
  `adcp.Float64(0)` for an explicit paused creative weight; omitted weight
  still means equal rotation. Seller-specific assignment fields round-trip via
  `CreativeAssignment.Extra`.
- `PackageInput` is now generated from `media-buy/package-request.json`. The
  non-protocol `BuyerRef` field is gone; use `Ext` for seller-specific
  correlation metadata. The generated type also exposes schema fields that were
  previously missing, including `FormatIDs`, `Pacing`, `Impressions`, `Paused`,
  `Catalogs`, `OptimizationGoals`, `Creatives`, `Context`, and `Ext`.
- `UpdateMediaBuyRequest` is now generated from
  `media-buy/update-media-buy-request.json`. `StartTime` is a `string` instead
  of `any`, matching the current schema's `start-timing` alias.
- `CreateMediaBuyRequest.StartTime` is also now a `string` instead of `any`.
  The schema's `start-timing` alias resolves to string in Go; `"asap"` remains
  valid wire data.
- `GetProductsRequest.PropertyList` is now `*adcp.PropertyListRef`, and
  `GetProductsRequest.TimeBudget` is now `*adcp.Duration`. Use nil when these
  filters are absent.
- Schema-referenced core objects now use generated Go types instead of `any`:

| Field | New Go type |
| --- | --- |
| `Account.BillingEntity` | `*adcp.BusinessEntity` |
| `MediaBuyData.InvoiceRecipient` | `*adcp.BusinessEntity` |
| `CreateMediaBuyRequest.InvoiceRecipient` | `*adcp.BusinessEntity` |
| `CreateMediaBuySuccess.InvoiceRecipient` | `*adcp.BusinessEntity` |
| `UpdateMediaBuyRequest.InvoiceRecipient` | `*adcp.BusinessEntity` |
| `CheckGovernanceRequest.InvoiceRecipient` | `*adcp.BusinessEntity` |
| `CreateMediaBuySuccess.PlannedDelivery` | `*adcp.PlannedDelivery` |
| `CheckGovernanceRequest.PlannedDelivery` | `*adcp.PlannedDelivery` |
| `CreateMediaBuyRequest.PushNotificationConfig` | `*adcp.PushNotificationConfig` |
| `UpdateMediaBuyRequest.PushNotificationConfig` | `*adcp.PushNotificationConfig` |
| `SyncAccountsRequest.PushNotificationConfig` | `*adcp.PushNotificationConfig` |
| `SyncCatalogsRequest.PushNotificationConfig` | `*adcp.PushNotificationConfig` |
| `SyncCreativesRequest.PushNotificationConfig` | `*adcp.PushNotificationConfig` |
| `CreateMediaBuyRequest.ReportingWebhook` | `*adcp.ReportingWebhook` |
| `UpdateMediaBuyRequest.ReportingWebhook` | `*adcp.ReportingWebhook` |
| `GetProductsRequest.PropertyList` | `*adcp.PropertyListRef` |
| `Targeting.PropertyList` | `*adcp.PropertyListRef` |
| `GetProductsRequest.TimeBudget` | `*adcp.Duration` |
| `GetProductsRequest.Filters` | `*adcp.ProductFilters` |
| `ProductFilters.BudgetRange` | `*adcp.ProductFilterBudgetRange` |
| `ProductFilters.RequiredFeatures` | `map[string]bool` |
| `ProductFilters.SignalTargeting` | `[]adcp.SignalTargeting` |
| `ProductFilters.GeoProximity` | `[]adcp.ProductFilterGeoProximity` |
| `Targeting.GeoProximity` | `[]adcp.GeoProximityTarget` |
| `CheckGovernanceRequest.DeliveryMetrics` | `*adcp.GovernanceDeliveryMetrics` |
| `ReportPlanOutcomeRequest.Delivery` | `*adcp.ReportPlanOutcomeDelivery` |
| `ReportPlanOutcomeRequest.Error` | `*adcp.ReportPlanOutcomeError` |
| `ReportPlanOutcomeResponse.PlanSummary` | `*adcp.ReportPlanOutcomePlanSummary` |
| `PolicyEntry.Exemplars` | `*adcp.PolicyExemplars` |
| `GetProductsResponse.Incomplete` | `[]adcp.GetProductsIncompleteItem` |
| `SyncPlansResponse.Plans` | `[]adcp.SyncPlansPlan` |
| `CheckGovernanceResponse.Findings` | `[]adcp.CheckGovernanceFinding` |
| `ReportPlanOutcomeResponse.Findings` | `[]adcp.ReportPlanOutcomeFinding` |
| `CheckGovernanceResponse.Conditions` | `[]adcp.CheckGovernanceCondition` |
| `GetPlanAuditLogsResponse.Plans` | `[]adcp.PlanAuditLog` |
| `Targeting.FrequencyCap` | `*adcp.FrequencyCap` |
| `Targeting.DaypartTargets` | `[]adcp.DaypartTarget` |
| `Catalog.FeedFieldMappings` | `[]adcp.CatalogFieldMapping` |
| `Event.UserMatch` | `*adcp.UserMatch` |
| `Event.CustomData` | `*adcp.EventCustomData` |
| `ProvidePerformanceFeedbackRequest.MeasurementPeriod` | `adcp.DatetimeRange` |
| `BusinessEntity.Address` | `*adcp.BusinessAddress` |
| `BusinessEntity.Contacts` | `[]adcp.BusinessContact` |
| `BusinessEntity.Bank` | `*adcp.BankAccount` |
| `PushNotificationConfig.Authentication` | `*adcp.LegacyWebhookAuthentication` |
| `ReportingWebhook.Authentication` | `adcp.LegacyWebhookAuthentication` |
| `CreateMediaBuyRequest.TotalBudget` | `*adcp.MediaBuyBudget` |
| `CreateMediaBuyRequest.IoAcceptance` | `*adcp.IOAcceptance` |
| `CreateMediaBuyRequest.ArtifactWebhook` | `*adcp.ArtifactWebhookConfig` |
| `ArtifactWebhookConfig.Authentication` | `adcp.LegacyWebhookAuthentication` |
| `GetAdcpCapabilitiesResponse.Adcp` | `adcp.ADCPVersion` |
| `GetAdcpCapabilitiesResponse.Account` | `*adcp.AccountCapabilities` |
| `GetAdcpCapabilitiesResponse.MediaBuy` | `*adcp.MediaBuyCapabilities` |
| `GetAdcpCapabilitiesResponse.Signals` | `*adcp.SignalsCapabilities` |
| `GetAdcpCapabilitiesResponse.Governance` | `*adcp.GovernanceCapabilities` |
| `GetAdcpCapabilitiesResponse.SponsoredIntelligence` | `*adcp.SICapabilities` |
| `GetAdcpCapabilitiesResponse.Brand` | `*adcp.BrandCapabilities` |
| `GetAdcpCapabilitiesResponse.Creative` | `*adcp.CreativeCapabilities` |
| `GetAdcpCapabilitiesResponse.RequestSigning` | `*adcp.RequestSigningCapabilities` |
| `GetAdcpCapabilitiesResponse.WebhookSigning` | `*adcp.WebhookSigningCapabilities` |
| `GetAdcpCapabilitiesResponse.Identity` | `*adcp.IdentityCapabilities` |
| `GetAdcpCapabilitiesResponse.ComplianceTesting` | `*adcp.ComplianceTestingCapabilities` |
| `Product.Placements` | `[]adcp.Placement` |
| `Product.DeliveryMeasurement` | `*adcp.ProductDeliveryMeasurement` |
| `Product.ProductCard` | `*adcp.ProductCard` |
| `Product.ProductCardDetailed` | `*adcp.ProductCardDetailed` |
| `Product.CatalogMatch` | `*adcp.ProductCatalogMatch` |
| `Product.Forecast` | `*adcp.DeliveryForecast` |
| `DeliveryForecast.Points` | `[]adcp.ForecastPoint` |
| `ForecastPoint.Metrics` | `map[string]adcp.ForecastRange` |
| `Product.OutcomeMeasurement` | `*adcp.OutcomeMeasurement` |
| `Product.ReportingCapabilities` | `adcp.ReportingCapabilities` |
| `ReportingCapabilities.SupportsGeoBreakdown` | `*adcp.GeoBreakdownSupport` |
| `Product.CreativePolicy` | `*adcp.CreativePolicy` |
| `Product.MeasurementReadiness` | `*adcp.MeasurementReadiness` |
| `MeasurementReadiness.Issues` | `[]adcp.DiagnosticIssue` |
| `Product.MetricOptimization` | `*adcp.ProductMetricOptimization` |
| `Product.ConversionTracking` | `*adcp.ProductConversionTracking` |
| `Product.TrustedMatch` | `*adcp.ProductTrustedMatch` |
| `ProductTrustedMatch.Providers` | `[]adcp.ProductTrustedMatchProvider` |
| `Product.MaterialSubmission` | `*adcp.ProductMaterialSubmission` |
| `Product.Collections` | `[]adcp.CollectionSelector` |
| `Product.DataProviderSignals` | `[]adcp.DataProviderSignalSelector` |
| `Product.Installments` | `[]adcp.Installment` |
| `Installment.Special` | `*adcp.Special` |
| `Installment.GuestTalent` | `[]adcp.Talent` |
| `Installment.AdInventory` | `*adcp.AdInventoryConfig` |
| `Installment.Deadlines` | `*adcp.InstallmentDeadlines` |
| `InstallmentDeadlines.MaterialDeadlines` | `[]adcp.MaterialDeadline` |
| `Installment.DerivativeOf` | `*adcp.InstallmentDerivative` |
| `GetProductsResponse.Proposals` | `[]adcp.Proposal` |
| `Proposal.Allocations` | `[]adcp.ProductAllocation` |
| `Proposal.InsertionOrder` | `*adcp.InsertionOrder` |
| `Proposal.TotalBudgetGuidance` | `*adcp.ProposalBudgetGuidance` |
| `InsertionOrder.Terms` | `*adcp.InsertionOrderTerms` |
| `InsertionOrderTerms.TotalBudget` | `*adcp.InsertionOrderBudget` |
| `Package.PriceBreakdown` | `*adcp.PriceBreakdown` |
| `PriceBreakdown.Adjustments` | `[]adcp.PriceAdjustment` |
| `Package.Cancellation` | `*adcp.PackageCancellation` |
| `CreativeFormat.Accessibility` | `*adcp.CreativeFormatAccessibility` |
| `CreativeFormat.FormatCard` | `*adcp.CreativeFormatCard` |
| `CreativeFormat.FormatCardDetailed` | `*adcp.CreativeFormatCardDetailed` |
| `CreativeFormat.DisclosureCapabilities` | `[]adcp.CreativeFormatDisclosureCapability` |
| `CreativeAsset.Inputs` | `[]adcp.CreativeAssetInput` |
| `CreativeAsset.Provenance` | `*adcp.Provenance` |
| `CreativeManifest.Provenance` | `*adcp.Provenance` |
| `Provenance.AITool` | `*adcp.ProvenanceAITool` |
| `Provenance.DeclaredBy` | `*adcp.ProvenanceDeclaredBy` |
| `Provenance.C2PA` | `*adcp.ProvenanceC2PA` |
| `Provenance.Disclosure` | `*adcp.ProvenanceDisclosure` |
| `Provenance.Disclosure.Jurisdictions` | `[]adcp.ProvenanceDisclosureJurisdiction` |
| `Provenance.Verification` | `[]adcp.ProvenanceVerification` |
| `Signal.Range` | `*adcp.SignalRange` |
| `Targeting.StoreCatchments` | `[]adcp.TargetingStoreCatchment` |
| `DeliveryTotals.ByEventType` | `[]adcp.DeliveryEventTypeMetrics` |
| `DeliveryTotals.QuartileData` | `*adcp.DeliveryQuartileData` |
| `DeliveryTotals.DoohMetrics` | `*adcp.DeliveryDOOHMetrics` |
| `DeliveryTotals.Viewability` | `*adcp.DeliveryViewability` |
| `DeliveryTotals.ByActionSource` | `[]adcp.DeliveryActionSourceMetrics` |
| `MediaBuyDeliveryTotals.ByEventType` | `[]adcp.DeliveryEventTypeMetrics` |
| `MediaBuyDeliveryTotals.QuartileData` | `*adcp.DeliveryQuartileData` |
| `MediaBuyDeliveryTotals.DoohMetrics` | `*adcp.DeliveryDOOHMetrics` |
| `MediaBuyDeliveryTotals.Viewability` | `*adcp.DeliveryViewability` |
| `MediaBuyDeliveryTotals.ByActionSource` | `[]adcp.DeliveryActionSourceMetrics` |
| `PackageDelivery.ByEventType` | `[]adcp.DeliveryEventTypeMetrics` |
| `PackageDelivery.QuartileData` | `*adcp.DeliveryQuartileData` |
| `PackageDelivery.DoohMetrics` | `*adcp.DeliveryDOOHMetrics` |
| `PackageDelivery.Viewability` | `*adcp.DeliveryViewability` |
| `PackageDelivery.ByActionSource` | `[]adcp.DeliveryActionSourceMetrics` |
| `MediaBuyDelivery.DailyBreakdown` | `[]adcp.MediaBuyDailyBreakdown` |
| `PackageDelivery.ByCatalogItem` | `[]adcp.PackageCatalogItemDelivery` |
| `PackageDelivery.ByCreative` | `[]adcp.PackageCreativeDelivery` |
| `PackageDelivery.ByKeyword` | `[]adcp.PackageKeywordDelivery` |
| `PackageDelivery.ByGeo` | `[]adcp.PackageGeoDelivery` |
| `PackageDelivery.ByDeviceType` | `[]adcp.PackageDeviceTypeDelivery` |
| `PackageDelivery.ByDevicePlatform` | `[]adcp.PackageDevicePlatformDelivery` |
| `PackageDelivery.ByAudience` | `[]adcp.PackageAudienceDelivery` |
| `PackageDelivery.ByPlacement` | `[]adcp.PackagePlacementDelivery` |
| `PackageDelivery.DailyBreakdown` | `[]adcp.PackageDailyBreakdown` |
| `Package.OptimizationGoals` | `[]adcp.OptimizationGoal` |
| `PackageInput.OptimizationGoals` | `[]adcp.OptimizationGoal` |
| `PackageUpdate.OptimizationGoals` | `[]adcp.OptimizationGoal` |
| `OptimizationGoal.Target` | `adcp.OptimizationGoalTarget` |
| `GetMediaBuysRequest.StatusFilter` | `*adcp.MediaBuyStatusFilter` |
| `GetMediaBuyDeliveryRequest.StatusFilter` | `*adcp.MediaBuyStatusFilter` |
| `GetMediaBuyDeliveryRequest.AttributionWindow` | `*adcp.DeliveryAttributionWindow` |
| `DeliveryAttributionWindow.PostClick` | `*adcp.Duration` |
| `DeliveryAttributionWindow.PostView` | `*adcp.Duration` |
| `GetMediaBuyDeliveryRequest.ReportingDimensions` | `*adcp.DeliveryReportingDimensions` |
| `DeliveryReportingDimensions.Geo` | `*adcp.DeliveryReportingGeoDimension` |
| `DeliveryReportingDimensions.DeviceType` | `*adcp.DeliveryReportingDimension` |
| `DeliveryReportingDimensions.DevicePlatform` | `*adcp.DeliveryReportingDimension` |
| `DeliveryReportingDimensions.Audience` | `*adcp.DeliveryReportingDimension` |
| `DeliveryReportingDimensions.Placement` | `*adcp.DeliveryReportingDimension` |
| `ListCreativeFormatsResponse.CreativeAgents` | `[]adcp.CreativeAgentRef` |
| `BuildCreativeRequest.PreviewInputs` | `[]adcp.BuildCreativePreviewInput` |
| `CreativeBrief.ReferenceAssets` | `[]adcp.ReferenceAsset` |
| `CreativeManifest.Rights` | `[]adcp.RightsConstraint` |
| `RightsConstraint.RightsAgent` | `adcp.RightsAgentRef` |
| `AudienceConstraints.Include` | `[]adcp.AudienceSelector` |
| `AudienceConstraints.Exclude` | `[]adcp.AudienceSelector` |
| `PlannedDelivery.AudienceTargeting` | `[]adcp.AudienceSelector` |
| `GetSignalsRequest.Destinations` | `[]adcp.Destination` |
| `Targeting.GeoMetros` / `GeoMetrosExclude` | `[]adcp.GeoMetroTarget` |
| `Targeting.GeoPostalAreas` / `GeoPostalAreasExclude` | `[]adcp.GeoPostalAreaTarget` |
| `Targeting.AgeRestriction` | `*adcp.AgeRestriction` |
| `Targeting.KeywordTargets` | `[]adcp.KeywordTarget` |
| `Targeting.NegativeKeywords` | `[]adcp.NegativeKeywordTarget` |
| `Account.CreditLimit` | `*adcp.AccountCreditLimit` |
| `Account.GovernanceAgents` | `[]adcp.AccountGovernanceAgent` |
| `Account.ReportingBucket` | `*adcp.ReportingBucket` |
| `GetCollectionListRequest.Pagination` | `*adcp.CollectionRequestPagination` |
| `CollectionListChangedWebhook.ChangeSummary` | `*adcp.CollectionChangeSummary` |
| `PropertyListChangedWebhook.ChangeSummary` | `*adcp.PropertyChangeSummary` |

Buyer request migration example:

```go
req := adcp.CreateMediaBuyRequest{
    InvoiceRecipient: adcp.Ptr(adcp.BusinessEntity{
        LegalName: "Acme Corporation",
        TaxID:     "12-3456789",
    }),
    PushNotificationConfig: adcp.Ptr(adcp.PushNotificationConfig{
        URL: "https://buyer.example/webhooks/tasks",
    }),
    ReportingWebhook: adcp.Ptr(adcp.ReportingWebhook{
        URL:                "https://buyer.example/webhooks/reports",
        Authentication:     adcp.LegacyWebhookAuthentication{Schemes: []string{"Bearer"}, Credentials: "0123456789abcdef0123456789abcdef"},
        ReportingFrequency: "daily",
    }),
}
```

Product lookup and targeting migration example:

```go
req := adcp.GetProductsRequest{
    PropertyList: adcp.Ptr(adcp.PropertyListRef{
        AgentURL: "https://lists.example/mcp",
        ListID:   "pl-123",
    }),
    TimeBudget: adcp.Ptr(adcp.Duration{Interval: 5, Unit: "minutes"}),
}
```

Status filter migration example:

```go
req := adcp.GetMediaBuysRequest{
    StatusFilter: adcp.NewMediaBuyStatusFilter(
        adcp.MediaBuyStatusActive,
        adcp.MediaBuyStatusPaused,
    ),
}
```

Price adjustment migration example:

```go
breakdown := adcp.PriceBreakdown{
    ListPrice: 20,
    Adjustments: []adcp.PriceAdjustment{{
        Kind:   "discount",
        Name:   "volume",
        Amount: 5,
    }},
}
```

Seller response and governance migration example:

```go
success := &adcp.CreateMediaBuySuccess{
    MediaBuyID: "mb-123",
    Packages:   []adcp.Package{pkg},
    PlannedDelivery: adcp.Ptr(adcp.PlannedDelivery{
        TotalBudget: 1000,
        Currency:    "USD",
    }),
}

feedback := adcp.ProvidePerformanceFeedbackRequest{
    MeasurementPeriod: adcp.DatetimeRange{
        Start: "2026-06-01T00:00:00Z",
        End:   "2026-06-30T23:59:59Z",
    },
}
```
- `DeliveryTotals.ReachUnit` is now `string` instead of `any`, matching the
  reach-unit enum's string wire form.
- `PackageUpdate` is now generated from `media-buy/package-update.json`. It
  exposes schema-backed package update fields such as `Pacing`, `Catalogs`,
  `OptimizationGoals`, keyword add/remove operations, `Creatives`, `Context`,
  and `Ext`.
- `OptimizationGoals` fields now use `[]adcp.OptimizationGoal` instead of
  `[]any`. Nested `event_sources`, `target_frequency`, and `attribution_window`
  are typed, and the nested `target` oneOf is now the
  `adcp.OptimizationGoalTarget` interface. Use concrete target variants such as
  `adcp.OptimizationGoalCostPerTarget`,
  `adcp.OptimizationGoalThresholdRateTarget`,
  `adcp.OptimizationGoalPerAdSpendTarget`, and
  `adcp.OptimizationGoalMaximizeValueTarget`. Unknown future target variants
  round-trip through `adcp.OptimizationGoalRawTarget`.
  `OptimizationGoal.Extra` preserves unknown top-level fields when
  round-tripping newer goal variants through replacement-style updates.

```go
goal := adcp.OptimizationGoal{
  Kind:   "metric",
  Metric: "reach",
  Target: adcp.OptimizationGoalThresholdRateTarget{Value: 0.7},
}

switch target := goal.Target.(type) {
case *adcp.OptimizationGoalThresholdRateTarget:
  _ = target.Value
case adcp.OptimizationGoalThresholdRateTarget:
  _ = target.Value
}
```

JSON unmarshal always produces the pointer form; the value form is what you get
when constructing goals directly in Go.

- `SyncCreativesRequest.Assignments` is now `[]adcp.SyncCreativeAssignment`.
- `Config.CreateMediaBuy` now returns `adcp.CreateMediaBuyResult`, which is
  implemented by the generated schema variants. Return
  `*adcp.CreateMediaBuySuccess` for synchronous success,
  `*adcp.CreateMediaBuySubmitted` for async submission, or
  `*adcp.CreateMediaBuyError` when building the schema error branch directly.
- `CreateMediaBuySubmitted` carries async `task_id` / `message` fields:
  `return &adcp.CreateMediaBuySubmitted{Status: "submitted", TaskID: taskID, Message: msg}, nil`.
- `Config.GetMediaBuys` now returns `*adcp.GetMediaBuysResponse` instead of
  `[]adcp.MediaBuyData`. Read pagination, context, and error envelope fields
  from the response struct; extract items via `response.MediaBuys`.
- `MediaBuyData` is now scoped to `get_media_buys` items. It carries fields such
  as `currency`, `total_budget`, `start_time`, `end_time`, `history`, and
  `valid_actions`, plus typed `invoice_recipient`, but not create-task fields
  like `task_id` / `message`.
- `MediaBuyData.Packages` is `[]adcp.PackageStatus` so `get_media_buys` can
  include creative approvals, pending formats, and delivery snapshots.
  `CreateMediaBuySuccess.Packages` remains `[]adcp.Package`.
- `PackageDelivery` and `MediaBuyDelivery` are generated from the delivery
  response inline schemas. Package-level metrics remain flat on
  `PackageDelivery`, and `pricing_model`, `rate`, `currency`, and `spend` are
  emitted as schema-required fields even when their Go values are zero.
  `MediaBuyDelivery.Totals` is now `adcp.MediaBuyDeliveryTotals`, which includes
  the schema-specific `effective_rate` field.
- Delivery metric breakdowns use the same generated metric helper types across
  `DeliveryTotals`, `MediaBuyDeliveryTotals`, package rows, and package
  breakdown rows: `DeliveryEventTypeMetrics`, `DeliveryQuartileData`,
  `DeliveryDOOHMetrics`, `DeliveryViewability`, and
  `DeliveryActionSourceMetrics`.
- `GetMediaBuyDeliveryResponse.ReportingPeriod`,
  `GetMediaBuyDeliveryResponse.AggregatedTotals`, and
  `GetMediaBuyDeliveryResponse.MediaBuyDeliveries` are now typed as
  `adcp.ReportingPeriod`, `*adcp.DeliveryAggregatedTotals`, and
  `[]adcp.MediaBuyDelivery` instead of `any` shapes.

## v3.0.0-rc.4 (governance / policy framework)

rc.4 lands the AdCP governance plan schema with breaking changes. If you
hand-construct `Plan` or `Budget` payloads, read the first section.

### Breaking: `budget.authority_level` is gone

The `authority_level` enum (`agent_full | agent_limited | human_required`) has
been split into two orthogonal concepts:

- `budget.reallocation_threshold` (`*float64`) — reallocation autonomy,
  denominated in `budget.currency`
- `budget.reallocation_unlimited` (`bool`) — full-autonomy sentinel, mutually
  exclusive with `reallocation_threshold`
- `plan.human_review_required` (`bool`) — decisions affecting data subjects
  must escalate to a human (GDPR Art 22, EU AI Act Annex III)

Mapping:

| was | now |
| --- | --- |
| `authority_level: agent_full`     | `Budget{ReallocationUnlimited: true}` |
| `authority_level: agent_limited`  | `Budget{ReallocationThreshold: &amount}` |
| `authority_level: human_required` | `Plan{HumanReviewRequired: true}` (+ threshold 0 if strict) |

**Enforcement.** Exactly one of `ReallocationThreshold` or `ReallocationUnlimited`
must be set. Go's type system cannot enforce this — call `plan.Validate()`
before sending:

```go
plan := adcp.Plan{
    PlanID:     "campaign-q4",
    Brand:      &adcp.BrandReference{Domain: "example.com"},
    Objectives: "brand awareness",
    Budget: adcp.PlanBudget{
        Total:                 500000,
        Currency:              "USD",
        ReallocationThreshold: ptr(25000.0),
    },
    Flight: adcp.PlanFlight{Start: start, End: end},
}
if errs := plan.Validate(); len(errs) > 0 {
    // Return stable codes, not raw messages. Messages may echo the caller's
    // input, which you don't want to reflect back to an untrusted sender.
    return adcp.NewError(errs[0].Code, adcp.ErrorOptions{Field: errs[0].Field})
}
```

### New: `plan.human_review_required` and Annex III invariants

The schema encodes two `if/then` rules that some codegen tools drop. `Plan.Validate`
enforces them client-side:

- `policy_categories` ∋ `fair_housing` / `fair_lending` / `fair_employment` /
  `pharmaceutical_advertising` ⇒ `human_review_required: true`
- `policy_ids` ∋ `eu_ai_act_annex_iii` ⇒ `human_review_required: true`

The exported lists are `adcp.RegulatedHumanReviewCategories` and
`adcp.AnnexIIIPolicyIDs` — use them in your own checks if you need to
introspect a plan before construction.

### New: `Plan.HumanOverride`

Downgrading `human_review_required` from `true` to `false` on re-sync requires
an artifact. Build one with `adcp.HumanOverride{Reason, Approver, ApprovedAt}`.
`Plan.Validate` enforces: `Reason` ≥ 20 characters (after trim), `Approver`
parses as an email address, and `ApprovedAt` (when non-empty) parses as RFC
3339. An empty `HumanOverride` is rejected — the artifact exists to evidence
a human decision, and shipping a blank one defeats the Art 22 audit trail.

### Expanded: `BrandReference`

`BrandReference` now carries rc.4's inline overrides:

- `BrandID` — scope to a specific brand within a house portfolio
- `Industries` — override for Annex III vertical detection when you can't
  modify the canonical `brand.json`
- `DataSubjectContestation` — Art 22(3) contestation contact point

Existing `BrandReference{Domain: "..."}` construction is source-compatible.

### Expanded: `restricted-attribute` enum

Two values added:

- `RestrictedAttributeAge` — FHA/ADEA (housing + employment)
- `RestrictedAttributeFamilialStatus` — FHA

If you hardcoded a list of 8 restricted-attribute values, widen it to 10.

### New tools

Types generated for all four governance tools; tool handlers are not yet
registered via `adcp.Config` and must be wired manually with `adcp.AddTool` if
you are building a governance agent:

- `sync_plans` — `SyncPlansRequest` / `SyncPlansResponse`
- `check_governance` — `CheckGovernanceRequest` / `CheckGovernanceResponse`
- `report_plan_outcome` — `ReportPlanOutcomeRequest` / `ReportPlanOutcomeResponse`
- `get_plan_audit_logs` — `GetPlanAuditLogsRequest` / `GetPlanAuditLogsResponse`

### Guidance for governance-agent implementors

`Plan.Validate` is the SDK's backstop for the invariants codegen tools drop. It
is advisory, not enforcing — a governance agent that accepts a plan without
calling it ships a server that violates the schema's load-bearing
human-oversight rules. Call it on receipt, before persisting anything.

Two invariants live outside the SDK and must be enforced in your governance
agent:

- **Industry normalization.** `BrandReference.Industries` is a freeform
  `[]string`. Normalize values (NFKC, strip combining marks, lowercase) before
  matching against Annex III vertical categories — a buyer shipping `"phárma"`
  or homoglyphed text will otherwise bypass vertical detection.
- **Registry vs inline policy segmentation.** `Plan.CustomPolicies` and
  registry-resolved policies share the `PolicyEntry` type. When assembling LLM
  evaluation prompts, pin registry-sourced policies (`Source == "registry"`)
  as system-level instructions and treat inline policies as
  additive-only — the schema is explicit that custom policies MUST NOT relax
  registry policies, and concatenating them into the same prompt section
  invites prompt-injection attacks via buyer-authored policy text.
