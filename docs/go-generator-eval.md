# Go SDK Generator Evaluation

This note records the May 2026 `ogen` spike and the recommended path for making
the Go SDK first-class without inventing protocol shapes by hand.

## Decision

Do not adopt `ogen` as the AdCP Go SDK type generator.

Keep the repo-local JSON Schema generator and harden it into a schema-coverage
tool with explicit fallbacks. The generator we need is smaller than an OpenAPI
HTTP client/server generator, and the SDK constraints are different:

- AdCP source files are JSON Schema, not an OpenAPI document.
- The SDK needs stable request/response structs, not generated routers,
  clients, validators, tracing, or custom JSON codecs.
- Generated SDK code should remain boring Go with minimal runtime dependencies.
- Any fallback to `any` must be visible, explainable, and shrinking over time.

## `ogen` Spike Results

Commands were run from repo root against `github.com/ogen-go/ogen/cmd/ogen`
`v1.20.3`.

Direct JSON Schema input fails immediately because `ogen` expects an OpenAPI
document:

```bash
go run github.com/ogen-go/ogen/cmd/ogen@latest \
  --target .context/ogen-eval/direct \
  -package ogenadcp \
  adcp/schemas/media-buy/package-request.json
```

Result:

```text
invalid version: invalid major version: strconv.Atoi: parsing "": invalid syntax
```

A minimal OpenAPI wrapper around selected AdCP schemas required dialect shims
before parsing:

- remove `$schema` / `$id`
- rewrite `/schemas/<version>/...` refs into `#/components/schemas/...`
- convert `const` to single-value `enum`
- convert numeric `exclusiveMinimum` / `exclusiveMaximum` into the OpenAPI
  boolean form

After that, `ogen` could generate the delivery response, but skipped the request
schemas we care about most:

| Schema | Result |
| --- | --- |
| `media-buy/package-request.json` | operation skipped: `sum types with same names not implemented` |
| `media-buy/package-update.json` | operation skipped: `sum types with same names not implemented` |
| `media-buy/get-products-request.json` | operation skipped: `sum types with same names not implemented` |
| `media-buy/get-media-buy-delivery-response.json` | generated and compiled |

The successful delivery-response-only output is too heavy for this SDK:

- `54,862` generated lines with path features disabled
- `13,927` lines of schema structs
- `25,179` lines of custom JSON codec code
- `15,744` lines of validators
- `44` Go modules in a standalone `go mod tidy`
- generated names like
  `GetMediaBuyDeliveryResponseMediaBuyDeliveriesItemByPackageItemByAudienceItemByActionSourceItem`

For comparison, the current repo-local generator emits all generated AdCP SDK
types in `adcp/types_gen.go` in about `3,040` lines, using ordinary JSON tags.

## Current Generator Gap

The repo-local generator is the right base, but it still leaks too much `any`.
As of this evaluation:

| File | `any` occurrences | `[]any` occurrences | `type X = any` aliases |
| --- | ---: | ---: | ---: |
| `adcp/types_gen.go` | 297 | 57 | 4 |
| `adcp/types.go` | 24 | 1 | 0 |
| `adcp/inputs.go` | 6 | 0 | 0 |

This table is a raw source snapshot. The generator coverage command below
reports logical generator-owned fallback records, so its totals will not match
these raw occurrence counts one-for-one.

Some `any` fields are valid protocol escape hatches (`context`, `ext`,
open-ended maps). The problematic ones are schema-owned fields that fall back to
`any` because the generator cannot yet name inline objects, represent unions, or
recurse through a specific composition shape.

## Recommended Build Plan

1. Add generator coverage reporting.
   - Emit every generated `any`, grouped by schema pointer and reason.
   - Maintain an allowlist for intentional dynamic fields.
   - Fail CI on new unallowlisted `any` regressions.

2. Make inline objects first-class.
   - Generate stable names from schema pointer paths.
   - Prefer explicit hints only when the automatic name is poor or conflicts.
   - Reuse named inline types where the same pointer is referenced from multiple
     parent structs.

3. Improve composition handling.
   - Keep `allOf` flattening for struct fields and required fields.
   - Treat `anyOf` / `oneOf` as unions, not as required-field inheritance.
   - Continue flattening only where a hand-written type is explicitly justified.

4. Add discriminator-aware unions.
   - Generate tagged union wrappers for `oneOf` branches with a discriminator or
     branch-level `const` / enum marker.
   - Preserve raw JSON escape hatches for ambiguous unions until the schema gives
     enough information.

5. Replace hand-written structs only when schema ownership is exact.
   - Each migration should remove one hand-written type or one `any` cluster.
   - Every migration needs a round-trip test for required zero values and optional
     false/zero values.

## Difficulty

This is not a full compiler project. The current generator already has schema
loading, `$ref` resolution, required-field handling, enum generation, inline
schema pointers, and drift linting.

Practical estimate:

- Coverage report and CI gate: about 1 day.
- Stable inline object generation: 2-4 days.
- Better `allOf` / pointer sharing cleanup: 1-2 days.
- First useful discriminator/tagged-union pass: 4-7 days.

That gets the SDK out of the "made-up struct" trap without adopting a large
OpenAPI runtime or generating an HTTP stack that AdCP does not need.

## Coverage Command

The generator can report the current `any` fallback surface without changing
generated Go:

```bash
cd adcp/schemas
python3 generate.py --coverage-summary
python3 generate.py --coverage-json
```

The report marks intentional escape hatches separately from unreviewed generator
gaps. Use the JSON form for CI gates and issue filing.

Common reason codes:

- `inline_object`: the schema has an inline object with properties, but no
  generated name yet.
- `array_item:inline_object`: same issue inside an array item.
- `unknown_ref:<ref>`: the referenced schema is not hand-written, generated, or
  otherwise known to the generator.
- `unsupported_allOf`: the generator cannot flatten this composed shape yet.
- `union`: the field uses `oneOf` or `anyOf` and needs a union strategy.
- `top_level_oneOf_alias`: the whole schema is currently emitted as `type X =
  any`.
- `adcp_error_alias`: the field uses `AdcpError`, which aliases
  `map[string]any`.

Intentional escape hatches are listed in `INTENTIONAL_ANY_FIELD_NAMES` and
`INTENTIONAL_ANY_FIELDS` in `adcp/schemas/generate.py`. Add to those lists only
when the dynamic shape is intentionally part of the protocol surface.
