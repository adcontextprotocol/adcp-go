# Go Generator Baseline

Snapshot date: 2026-06-18
Schema bundle: AdCP 3.1.0
Command:

```bash
cd adcp/schemas
python3 generate.py --coverage-summary
python3 generate.py --coverage-max-unreviewed-any 0
```

The generator currently reports 222 generated dynamic `any` uses:

| Class | Count | Status |
| --- | ---: | --- |
| Reviewed intentional `any` | 222 | Allowed by `INTENTIONAL_ANY_FIELD_NAMES`, `INTENTIONAL_ANY_FIELDS`, or `AdcpError` handling |
| Unreviewed generated `any` | 0 | CI baseline; every new unreviewed fallback is a regression |

CI enforces this baseline with:

```bash
python3 generate.py --coverage-max-unreviewed-any 0
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
- Top-level `oneOf` response schemas are generated as closed Go interfaces with
  schema-owned concrete variants. Add discriminator-aware marshal/unmarshal
  helpers only when a caller needs to decode directly into the interface type.
- Unknown `$ref` fallbacks are generator registry limitations unless the target
  schema is intentionally open.
- Unspecified schema types are schema limitations first. Resolve them upstream or
  add a narrowly reviewed Go type only when the protocol meaning is clear.

## Unreviewed `any` Baseline

There are no unreviewed generated `any` fallbacks. New dynamic fields must be
typed or reviewed as intentional escape hatches in the same PR that introduces
them.

## Work Queues

### Inline Object Generation

The generator now covers the known schema-owned inline objects. Continued work
should focus on making inline ownership less manual: stable naming for
inline schemas, pointer handling for optional inline object fields, and collision
detection across generated names.

Keep genuinely open/controller payloads, inline unions, and schema-closed
artifacts separate so the generator backlog does not turn typed object work into
protocol-design work.

### Top-Level Unions

Top-level response `oneOf` schemas now generate closed interfaces and concrete
branch types instead of `type X = any`. The remaining follow-up is optional
discriminator-aware unmarshal support for callers that want to decode directly
into the interface type instead of unmarshalling into a concrete branch or
`json.RawMessage` first.

### Unknown References

No unreviewed unknown `$ref` fallbacks remain. Future unknown refs should be
added to the generation graph unless the target schema is an intentionally open
or union-shaped payload with a specific allowlist reason.

## 3.1 GA Integration

Snapshot date: 2026-06-18
Schema bundle: AdCP 3.1.0

The checked-in baseline now uses the 3.1.0 bundle. The generator owns the new
named 3.1 schemas for account authorization, committed metrics, delivery metric
aggregates, missing metrics, signal targeting, forecast dimensions, provenance
audit observations, wholesale-feed capability blocks, and the newly named
inline delivery/reporting helper shapes.

The GA integration adds typed audio distribution declarations and filters,
plus YouTube channel handle and URL distribution identifiers, without raising
the unreviewed `any` baseline.

The 3.1 integration and subsequent generator passes reduced unreviewed
generated `any` fallbacks from the 3.0.12 baseline of 15 to 0 while adding the
3.1 protocol surface.

### Product Refinement Shapes

`GetProductsRequest.Refine` and `GetProductsResponse.RefinementApplied` now use
generated flattened structs for their inline `scope`-keyed `oneOf` items:
`GetProductsRefineItem` and `GetProductsRefinementAppliedItem`. These structs
preserve request-side `encoding/json` decoding while avoiding interface fields
on tool inputs.

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
