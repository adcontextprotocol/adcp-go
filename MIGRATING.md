# Migrating adcp-go

## Next: typed seller media-buy helpers

This release tightens the Go seller SDK around AdCP 3.0.12 media-buy shapes.
Most wire payloads are unchanged, but several public Go structs are more typed.

- `UpdateMediaBuyRequest.Canceled` and `PackageUpdate.Canceled` are `*bool`.
  Use nil when the field is absent and `adcp.Bool(true)` when requesting
  cancellation. The AdCP schema constrains `canceled` to true; do not send
  `adcp.Bool(false)` to mean resume. Use `Paused: adcp.Bool(false)` for resume.
- `CreativeAssignments` is now `[]adcp.CreativeAssignment`. Use
  `adcp.Float64(0)` for an explicit paused creative weight; omitted weight
  still means equal rotation. Seller-specific assignment fields round-trip via
  `CreativeAssignment.Extra`.
- `SyncCreativesRequest.Assignments` is now `[]adcp.SyncCreativeAssignment`.
- `Config.CreateMediaBuy` now returns `adcp.CreateMediaBuyResult`, which is
  implemented by the generated schema variants. Return
  `*adcp.CreateMediaBuySuccess` for synchronous success,
  `*adcp.CreateMediaBuySubmitted` for async submission, or
  `*adcp.CreateMediaBuyError` when building the schema error branch directly.
- `CreateMediaBuySubmitted` carries async `task_id` / `message` fields:
  `return &adcp.CreateMediaBuySubmitted{Status: "submitted", TaskID: taskID, Message: msg}, nil`.
- `MediaBuyData` is now scoped to `get_media_buys` items. It carries fields such
  as `currency`, `total_budget`, `start_time`, `end_time`, `history`, and
  `valid_actions`, but not create-task fields like `task_id` / `message`.
- `MediaBuyData.Packages` is `[]adcp.PackageStatus` so `get_media_buys` can
  include creative approvals, pending formats, and delivery snapshots.
  `CreateMediaBuySuccess.Packages` remains `[]adcp.Package`.
- `PackageDelivery` is flat. Read package-level delivery metrics directly from
  `PackageDelivery.Impressions`, `Spend`, and `Clicks`; `Spend` remains present
  on the wire even when zero.

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
