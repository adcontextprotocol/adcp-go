# Go Generator Baseline

Snapshot date: 2026-05-28
Schema bundle: AdCP 3.0.12
Command:

```bash
cd adcp/schemas
python3 generate.py --coverage-summary
python3 generate.py --coverage-max-unreviewed-any 26
```

The generator currently reports 192 generated dynamic `any` uses:

| Class | Count | Status |
| --- | ---: | --- |
| Reviewed intentional `any` | 166 | Allowed by `INTENTIONAL_ANY_FIELD_NAMES`, `INTENTIONAL_ANY_FIELDS`, or `AdcpError` handling |
| Unreviewed generated `any` | 26 | CI baseline; every new unreviewed fallback is a regression |

CI enforces this baseline with:

```bash
python3 generate.py --coverage-max-unreviewed-any 26
```

Lower this number whenever a generator improvement removes an unreviewed
fallback. Do not raise it unless the schema bundle adds new protocol surface and
the new dynamic shape is reviewed in the same PR.

## Decision Rules

- `context`, `ext`, seller-defined creative assets, format manifests, event
  payloads, and AdCP error payloads are intentional dynamic escape hatches.
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
| `PolicyEntry.Exemplars` | `exemplars` | `any` | `inline_object` | `governance/policy-entry.json` |
| `SyncGovernanceResponse` | n/a | `any` | `top_level_oneOf_alias` | `account/sync-governance-response.json` |
| `SyncGovernanceSuccess.Accounts` | `accounts` | `[]any` | `array_item:inline_object` | `account/sync-governance-response.json#/oneOf/0` |
| `GetProductsRequest.Refine` | `refine` | `[]map[string]any` | `array_item:freeform_object` | `media-buy/get-products-request.json` |
| `GetProductsResponse.RefinementApplied` | `refinement_applied` | `[]map[string]any` | `array_item:freeform_object` | `media-buy/get-products-response.json` |
| `GetProductsResponse.Incomplete` | `incomplete` | `[]any` | `array_item:inline_object` | `media-buy/get-products-response.json` |
| `CreateMediaBuyResponse` | n/a | `any` | `top_level_oneOf_alias` | `media-buy/create-media-buy-response.json` |
| `ProvidePerformanceFeedbackResponse` | n/a | `any` | `top_level_oneOf_alias` | `media-buy/provide-performance-feedback-response.json` |
| `PreviewCreativeRequest.Inputs` | `inputs` | `[]any` | `array_item:inline_object` | `creative/preview-creative-request.json` |
| `PreviewCreativeRequest.Requests` | `requests` | `[]any` | `array_item:inline_object` | `creative/preview-creative-request.json` |
| `GetSignalsResponse.Signals` | `signals` | `[]any` | `array_item:inline_object` | `signals/get-signals-response.json` |
| `ComplyTestControllerRequest.Params` | `params` | `any` | `inline_object` | `compliance/comply-test-controller-request.json` |
| `ComplyTestControllerResponse` | n/a | `any` | `top_level_oneOf_alias` | `compliance/comply-test-controller-response.json` |
| `ForcedDirectiveSuccess.Forced` | `forced` | `any` | `inline_object` | `compliance/comply-test-controller-response.json#/oneOf/3` |
| `ControllerError.CurrentState` | `current_state` | `any` | `unspecified_schema_type` | `compliance/comply-test-controller-response.json#/oneOf/5` |
| `SyncPlansResponse.Plans` | `plans` | `[]any` | `array_item:inline_object` | `governance/sync-plans-response.json` |
| `CheckGovernanceRequest.DeliveryMetrics` | `delivery_metrics` | `any` | `inline_object` | `governance/check-governance-request.json` |
| `CheckGovernanceResponse.Findings` | `findings` | `[]any` | `array_item:inline_object` | `governance/check-governance-response.json` |
| `CheckGovernanceResponse.Conditions` | `conditions` | `[]any` | `array_item:inline_object` | `governance/check-governance-response.json` |
| `ReportPlanOutcomeRequest.SellerResponse` | `seller_response` | `any` | `inline_object` | `governance/report-plan-outcome-request.json` |
| `ReportPlanOutcomeRequest.Delivery` | `delivery` | `any` | `inline_object` | `governance/report-plan-outcome-request.json` |
| `ReportPlanOutcomeRequest.Error` | `error` | `any` | `inline_object` | `governance/report-plan-outcome-request.json` |
| `ReportPlanOutcomeResponse.Findings` | `findings` | `[]any` | `array_item:inline_object` | `governance/report-plan-outcome-response.json` |
| `ReportPlanOutcomeResponse.PlanSummary` | `plan_summary` | `any` | `inline_object` | `governance/report-plan-outcome-response.json` |
| `GetPlanAuditLogsResponse.Plans` | `plans` | `[]any` | `array_item:inline_object` | `governance/get-plan-audit-logs-response.json` |
| `ArtifactWebhookPayload.Artifacts` | `artifacts` | `[]any` | `array_item:inline_object` | `content-standards/artifact-webhook-payload.json` |

## Work Queues

### Inline Object Generation

This is the largest generator gap: 24 unreviewed fallbacks are direct inline
objects or arrays of inline objects. The generator needs stable naming for
inline schemas, pointer handling for optional inline object fields, and collision
detection across generated names.

The first reduction passes typed low-risk leaf objects. Continue with inline
objects that have stable, schema-owned property sets before moving into arrays
of inline objects. The next direct-object candidates are governance/reporting
shapes such as `CheckGovernanceRequest.DeliveryMetrics` and
`ReportPlanOutcomeRequest.Delivery`.

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

### Schema Clarification

One fallback comes from a schema without enough type information:

- `ControllerError.CurrentState`

These should usually be fixed in the protocol schema. A Go-only type override is
acceptable only when the schema intent is clear and tested.

### Product Refinement Shapes

`GetProductsRequest.Refine` and `GetProductsResponse.RefinementApplied` are
currently unreviewed `[]map[string]any` freeform objects. Decide whether these
are intentional protocol extension points or missing typed refinement schemas.

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
