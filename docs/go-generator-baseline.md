# Go Generator Baseline

Snapshot date: 2026-05-28
Schema bundle: AdCP 3.0.12
Command:

```bash
cd adcp/schemas
python3 generate.py --coverage-summary
python3 generate.py --coverage-max-unreviewed-any 15
```

The generator currently reports 183 generated dynamic `any` uses:

| Class | Count | Status |
| --- | ---: | --- |
| Reviewed intentional `any` | 168 | Allowed by `INTENTIONAL_ANY_FIELD_NAMES`, `INTENTIONAL_ANY_FIELDS`, or `AdcpError` handling |
| Unreviewed generated `any` | 15 | CI baseline; every new unreviewed fallback is a regression |

CI enforces this baseline with:

```bash
python3 generate.py --coverage-max-unreviewed-any 15
```

Lower this number whenever a generator improvement removes an unreviewed
fallback. Do not raise it unless the schema bundle adds new protocol surface and
the new dynamic shape is reviewed in the same PR.

## Decision Rules

- `context`, `ext`, seller-defined creative assets, format manifests, event
  payloads, and AdCP error payloads are intentional dynamic escape hatches.
- Governance finding details are intentional dynamic escape hatches:
  `CheckGovernanceFinding.Details` and `ReportPlanOutcomeFinding.Details` stay
  `map[string]any` because their structured payloads are category-specific.
- `CheckGovernanceCondition.RequiredValue` stays `any` because a governance
  condition can require different JSON value types. The type is hand-written so
  `HasRequiredValue` preserves the difference between an absent advisory value
  and an explicit `required_value`, including JSON `null`. Callers must set
  `HasRequiredValue` when constructing any present `required_value`; decoded
  numeric values follow `encoding/json` and become `float64`.
- Inline object fallbacks are generator limitations unless the schema explicitly
  models open-ended data.
- Top-level `oneOf` aliases are generator limitations. They need generated
  result interfaces, concrete variants, and discriminator-aware marshal/unmarshal
  helpers.
- Unknown `$ref` fallbacks are generator registry limitations unless the target
  schema is intentionally open.
- Unspecified schema types are schema limitations first. Resolve them upstream or
  add a narrowly reviewed Go type only when the protocol meaning is clear.

## Unreviewed `any` Baseline

| Surface | JSON field | Go type | Reason | Schema |
| --- | --- | --- | --- | --- |
| `SyncGovernanceResponse` | n/a | `any` | `top_level_oneOf_alias` | `account/sync-governance-response.json` |
| `SyncGovernanceSuccess.Accounts` | `accounts` | `[]any` | `array_item:inline_object` | `account/sync-governance-response.json#/oneOf/0` |
| `GetProductsRequest.Refine` | `refine` | `[]map[string]any` | `array_item:freeform_object` | `media-buy/get-products-request.json` |
| `GetProductsResponse.RefinementApplied` | `refinement_applied` | `[]map[string]any` | `array_item:freeform_object` | `media-buy/get-products-response.json` |
| `CreateMediaBuyResponse` | n/a | `any` | `top_level_oneOf_alias` | `media-buy/create-media-buy-response.json` |
| `ProvidePerformanceFeedbackResponse` | n/a | `any` | `top_level_oneOf_alias` | `media-buy/provide-performance-feedback-response.json` |
| `PreviewCreativeRequest.Inputs` | `inputs` | `[]any` | `array_item:inline_object` | `creative/preview-creative-request.json` |
| `PreviewCreativeRequest.Requests` | `requests` | `[]any` | `array_item:inline_object` | `creative/preview-creative-request.json` |
| `GetSignalsResponse.Signals` | `signals` | `[]any` | `array_item:inline_object` | `signals/get-signals-response.json` |
| `ComplyTestControllerRequest.Params` | `params` | `any` | `inline_object` | `compliance/comply-test-controller-request.json` |
| `ComplyTestControllerResponse` | n/a | `any` | `top_level_oneOf_alias` | `compliance/comply-test-controller-response.json` |
| `ForcedDirectiveSuccess.Forced` | `forced` | `any` | `inline_object` | `compliance/comply-test-controller-response.json#/oneOf/3` |
| `ControllerError.CurrentState` | `current_state` | `any` | `unspecified_schema_type` | `compliance/comply-test-controller-response.json#/oneOf/5` |
| `ReportPlanOutcomeRequest.SellerResponse` | `seller_response` | `any` | `inline_object` | `governance/report-plan-outcome-request.json` |
| `ArtifactWebhookPayload.Artifacts` | `artifacts` | `[]any` | `array_item:inline_object` | `content-standards/artifact-webhook-payload.json` |

## Work Queues

### Inline Object Generation

This is the largest generator gap: 8 unreviewed fallbacks are direct inline
objects or arrays of inline objects. The generator needs stable naming for
inline schemas, pointer handling for optional inline object fields, and collision
detection across generated names.

The first reduction passes typed low-risk leaf objects. Continue with inline
objects that have stable, schema-owned property sets. The remaining inline
object fallbacks mix genuinely open/controller payloads, inline unions, and
schema-closed artifacts; keep those classes separate so the generator backlog
does not turn typed object work into protocol-design work.

### Top-Level Unions

Four response schemas still become `type X = any`:

- `SyncGovernanceResponse`
- `CreateMediaBuyResponse`
- `ProvidePerformanceFeedbackResponse`
- `ComplyTestControllerResponse`

These need a generated union interface and concrete branch types, matching the
hand-written pattern already used for selected oneOf responses.

### Unknown References

No unreviewed unknown `$ref` fallbacks remain. Future unknown refs should be
added to the generation graph unless the target schema is an intentionally open
or union-shaped payload with a specific allowlist reason.

## 3.1 Beta Smoke Check

Snapshot date: 2026-05-29
Schema bundle: AdCP 3.1.0-beta.7

This is a non-gating compatibility smoke check, not the checked-in baseline.
With the 3.1.0-beta.7 schemas overlaid onto the current generator, auto-discovery
found 21 clean referenced schema types and reduced generated unreviewed `any`
fallbacks from the initial beta evaluation's 100 to 64. Unknown refs are no
longer the dominant class; the remaining surface is mostly inline objects,
freeform payloads, unspecified schema types, and one mixed `allOf` account
extension that needs named inline composition support.

Do not pin the Go SDK to a beta schema bundle until the remaining 3.1 migration
work is reviewed as protocol surface:

- add named inline composition for mixed `allOf` shapes such as
  `ListAccountsResponse.accounts[]`;
- classify 3.1 vendor metric, signal-targeting, forecast dimension, and
  capability inline objects as either generated structs or intentional open
  payloads;
- update hand-written capability and governance structs for the beta schema
  drift; and
- re-run strict drift lint, generated coverage, and reference seller tests
  against the beta bundle.

### Schema Clarification

One fallback comes from a schema without enough type information:

- `ControllerError.CurrentState`

These should usually be fixed in the protocol schema. A Go-only type override is
acceptable only when the schema intent is clear and tested.

### Product Refinement Shapes

`GetProductsRequest.Refine` and `GetProductsResponse.RefinementApplied` are
currently unreviewed `[]map[string]any` fallbacks because each array item is an
inline discriminated `oneOf` object. The schema branches are closed and keyed by
`scope`; this is a generator gap for inline union/discriminator generation, not
a protocol extension-point decision.

## Manual Ownership Baseline

The dynamic-any report does not yet classify every manual override. Those are
still split across `KNOWN_TYPES`, `INLINE_TYPE_HINTS`, `REF_TYPE_HINTS`,
`TYPE_NAME_OVERRIDES`, and `INTENTIONAL_ANY_FIELDS` in
`adcp/schemas/generate.py`.

Current policy:

- `KNOWN_TYPES` is allowed only for hand-written structs with behavior,
  response builders, inline shapes that have no standalone schema, or oneOf
  flatteners the generator cannot yet produce.
- `INLINE_TYPE_HINTS` is transitional glue. Each entry should either become
  schema-generated or remain with a short comment explaining the protocol
  reason.
- `INTENTIONAL_ANY_FIELDS` is the only place to allow dynamic protocol payloads.
  New entries need a concrete reason, not "generator cannot handle this yet."

Next generator-hardening PR: make these ownership registries machine-readable so
CI can report counts by owner class, not just dynamic `any` coverage.
