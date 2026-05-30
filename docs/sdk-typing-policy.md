# Go SDK Typing Policy

This document records the decision rules for Go SDK protocol types. The goal is
to keep the SDK schema-faithful while still providing ergonomic helpers for
sellers.

## Status

Accepted for new Go SDK protocol typing work.

## Principles

1. Published AdCP JSON Schemas are the source of truth for protocol structs.
2. Generated protocol structs must not invent fields. Non-schema correlation or
   seller convenience data belongs under schema-backed `ext`, or in an internal
   helper type that is never exposed as an MCP tool schema.
3. Keep protocol structs, response builders, and reference seller internals
   separate. Protocol structs mirror the wire. Builders may add convenience.
   Reference seller structs may be local, but must not define public tool input
   schemas unless they are schema-backed.
4. If a generated field falls back to `any`, the fallback must be intentional or
   tracked by generator coverage. New unreviewed `any` fallbacks are regressions.
5. Hand-written protocol types are allowed only with an explicit reason and a
   drift check against the schema pointer they mirror.

## Package Request And Update Types

`PackageInput` and `PackageUpdate` are protocol types. They drive MCP input
schemas through `adcp.Register`, so they must stay schema-owned.

Decision:

- Generate `PackageInput` from `media-buy/package-request.json`.
- Generate `PackageUpdate` from `media-buy/package-update.json`.
- Do not add non-schema fields such as `buyer_ref` to either type.
- Use schema-backed fields such as `format_ids`, `paused`, `catalogs`,
  `optimization_goals`, `creative_assignments`, `creatives`, and keyword delta
  operations when those fields exist in the published bundle.
- Put buyer/seller correlation metadata under `ext` unless the protocol adds a
  first-class field.
- Add or keep generator tests for schema pointers that require inline helper
  names, such as package keyword update rows.

Required tests for future changes:

- Regenerating `adcp/types_gen.go` from `adcp/schemas` must preserve the package
  fields present in the current bundle.
- MCP tool schema changes for `create_media_buy` and `update_media_buy` must be
  explainable by schema changes, not local convenience fields.
- Optional false/zero values used in package create/update requests must round
  trip when represented by pointer fields.

## Optional Scalar Policy

Go value scalars with `omitempty` cannot distinguish absence from explicit zero
or false. The generator should preserve that distinction when the protocol can
observe it.

Decision:

- Optional booleans are pointers. `false` is often an explicit operation
  (`paused: false`, `canceled: false` must not be emitted accidentally).
- Optional request numbers are pointers when explicit zero is meaningful, when
  the schema declares a default, or when the description says omission inherits
  or leaves an existing value unchanged.
- Required numbers remain values.
- Optional response numbers may remain values when omission and zero are not
  semantically distinct for callers. Use a pointer if the schema marks the field
  nullable, gives omission semantics, or callers need to distinguish missing
  from zero.
- Optional strings remain values unless the schema or protocol text gives
  omission a different meaning from the empty string.
- `Float64`, `Bool`, and `Ptr` helpers are the public convenience path for
  setting explicit optional scalar values.

Migration rule:

- Avoid sweeping pointer changes across all generated response fields in one
  PR. Change request-side fields first, with round-trip tests, because those
  affect emitted wire payloads and MCP input schemas.
- `adcp/schemas/lint.py --strict` enforces optional boolean pointers and flags
  optional numeric pointer candidates when omission and explicit zero appear
  semantically distinct. Numeric exceptions require a documented
  `OPTIONAL_NUMERIC_SCALAR_OK` waiver in `adcp/schemas/lint.py`. Boolean
  exceptions have no waiver; file an upstream schema clarification first.

## Enum Policy

Generated enum types should be useful without making forward-compatible reads
impossible.

Decision:

- Emit named string types and constants for known schema values.
- Keep `KnownXValues`, `IsKnownX`, and strict `ParseX` helpers.
- Do not add custom JSON unmarshalers that reject unknown enum strings by
  default. Unknown future values must round-trip as their raw string value unless
  a caller explicitly uses `ParseX`.
- Reference sellers may reject unsupported actions or transitions at the
  business-logic layer, but generated protocol types should not reject future
  enum values during JSON decoding.

## `ext` And Typed Metadata Policy

`ext` is the only generic extension escape hatch. It must not silently override
typed protocol fields.

Decision:

- Add first-class fields only when they are schema-backed in the pinned bundle
  or accepted in the protocol version being added.
- Do not promote reference-seller convenience fields into SDK protocol structs
  just because one example needs them.
- Do not synthesize `Ext` on a typed request struct just because the schema has
  `additionalProperties: true`. The protocol must define an explicit `ext`
  field before the SDK exposes one. `ProductFilters` in AdCP 3.0.12 is the
  concrete precedent: it is typed as schema-authored fields only, and the
  protocol request for `filters.ext` is tracked upstream in
  adcontextprotocol/adcp#5120.
- Avoid `Extra` maps on buyer-constructed request types unless the protocol has
  a read-modify-write preservation requirement. `Extra` is for preserving
  unknown response metadata, not for inventing request-side extension surfaces.
- Avoid flattening `ext` into top-level response objects. If compatibility
  requires flattening temporarily, typed fields are authoritative on collision,
  and the follow-up must identify the schema fields needed to remove flattening.
- Vendor-specific metadata stays nested under `ext` unless the protocol defines
  a typed field.

## Generator Ownership Policy

The repo-local generator is the owner for schema-faithful structs. A third-party
generator should replace it only if it satisfies the acceptance tests in
`docs/go-generator-eval.md` without adding root-module runtime dependencies or
producing unstable names.

Decision:

- Continue hardening `adcp/schemas/generate.py`.
- Build toward generated inline object types, `allOf` merge support, and
  discriminator-aware union handling.
- Replace hand-written protocol types incrementally, one schema cluster at a
  time.
- Each migration needs:
  - a schema pointer owner,
  - generated output golden or behavioral tests,
  - JSON round-trip coverage for optional false/zero values,
  - no new unreviewed `any` coverage records.

## Decision Checklist

Before adding or changing a public Go protocol field:

1. Identify the exact schema file and JSON pointer.
2. Confirm the same field exists in the pinned AdCP bundle.
3. Decide whether absence and zero/false/empty are semantically distinct.
4. Check JS/Python SDK behavior for the same field when parity matters.
5. Add a generator, drift, or JSON round-trip test.
6. Keep convenience-only data out of MCP tool schemas.
