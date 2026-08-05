# adcp-go 3.0

adcp-go 3.0 introduces the SIV-correct `adcp/v3` module, extracts the TMP
wire types, client, and targeting engine into their own sub-modules with
independent SemVer cadences, and aligns the `adcp/vN` major with the AdCP
protocol spec's major version. This is the first `adcp` release that is
directly resolvable via `go get` at a real tag — every prior consumer was
either on a pseudo-version off `main` or a `replace` directive.

## Highlights

- **`adcp/v3.0.0` is the first `go get`-able `adcp` release.** The v1 and v2
  `adcp-v*` tags used a hyphen prefix that does not match Go's nested-module
  tag convention. `adcp/v3.0.0` uses the canonical slash-prefixed tag format
  and resolves cleanly:

  ```sh
  go get github.com/adcontextprotocol/adcp-go/adcp/v3@v3.0.0
  ```

- **Sub-modules with independent cadences.** `tmproto`, `tmpclient`,
  `targeting`, `urlcanon`, and `registry` are now standalone modules. Import
  paths in existing Go source are unchanged — only downstream `go.mod` files
  need a `require` line per sub-module in use. Bumping the targeting engine
  no longer forces a schema-types bump, and vice versa.

- **Spec-major alignment.** `adcp/vN` tracks the AdCP protocol spec's major
  version 1:1. `adcp/v3` speaks AdCP 3.x. When the AdCP protocol spec bumps
  to 4.0, `adcp/v4` gets cut. No Go-only major bumps of `adcp` will happen
  inside a spec-major lifecycle.

- **Frozen v2 stays available.** The `adcp` v2 module remains at `v2.1.1`
  under the legacy hyphen-tag path. It receives security backports only for
  a 12-month window post-3.0-GA. Existing downstream services on
  pseudo-version pins keep resolving unchanged.

- **Schema-backed typed SDK fields.** Buyer, seller, and governance SDK
  surfaces around the AdCP 3.0.12 and rc.4 schemas are now strongly typed.
  Fields that were previously `any` or `[]any` now use generated Go types.
  See `MIGRATING.md` for the full field-level rundown.

- **Governance / policy framework (rc.4).** New `sync_plans`,
  `check_governance`, `report_plan_outcome`, and `get_plan_audit_logs`
  tools. Client-side `Plan.Validate` enforces the schema's Annex III
  human-oversight invariants that most codegen tools drop.

- **RFC 9421 request signing.** `adcp/v3/signing` implements the AdCP
  request-signing profile — optional in AdCP 3.0, required for
  spend-committing operations in AdCP 4.0. Self-validated against the
  upstream conformance vectors (8 positive + 20 negative).

## Migration

Read `MIGRATING.md` for the full recipe. In brief:

```sh
go get github.com/adcontextprotocol/adcp-go/adcp/v3@v3.0.0
```

Then update every `import` statement's path suffix:

```go
// before
import "github.com/adcontextprotocol/adcp-go/adcp"

// after
import "github.com/adcontextprotocol/adcp-go/adcp/v3"
```

The package identifier stays `adcp` per Go's Semantic Import Versioning
rule — only the import path changes.

Add `require` lines for any additional sub-modules the service uses:

```gomod
require (
    github.com/adcontextprotocol/adcp-go/adcp/v3 v3.0.0
    github.com/adcontextprotocol/adcp-go/tmproto v0.1.0
    github.com/adcontextprotocol/adcp-go/tmpclient v0.1.0
    github.com/adcontextprotocol/adcp-go/targeting v0.1.0
    github.com/adcontextprotocol/adcp-go/registry v0.1.0
    github.com/adcontextprotocol/adcp-go/urlcanon v0.1.0
)
```

`go mod tidy` will resolve `go.sum` entries automatically.

If you need to stay on v2, no action is required — pseudo-version pins keep
resolving. If you need to roll back mid-migration, see the rollback recipe
in `MIGRATING.md`.

## Modules and current versions

| Import path | Current version | Tag format |
| --- | --- | --- |
| `github.com/adcontextprotocol/adcp-go/adcp/v3` | `v3.0.0` | `adcp/vX.Y.Z` |
| `github.com/adcontextprotocol/adcp-go/adcp` (frozen v2) | `v2.1.1` | `adcp-vX.Y.Z` (legacy hyphen) |
| `github.com/adcontextprotocol/adcp-go/tmproto` | `v0.1.0` | `tmproto/vX.Y.Z` |
| `github.com/adcontextprotocol/adcp-go/tmpclient` | `v0.1.0` | `tmpclient/vX.Y.Z` |
| `github.com/adcontextprotocol/adcp-go/targeting` | `v0.1.0` | `targeting/vX.Y.Z` |
| `github.com/adcontextprotocol/adcp-go/urlcanon` | `v0.1.0` | `urlcanon/vX.Y.Z` |
| `github.com/adcontextprotocol/adcp-go/registry` | `v0.1.0` | `registry/vX.Y.Z` |
| `github.com/adcontextprotocol/adcp-go/registry/redisstore` | (not yet cut) | `registry/redisstore/vX.Y.Z` |
| `github.com/adcontextprotocol/adcp-go/registry/glidestore` | (not yet cut) | `registry/glidestore/vX.Y.Z` |

The top-level module `github.com/adcontextprotocol/adcp-go` is internal
glue and not an intended external import target.

## Cadence and support commitments

- **`adcp/vN` majors track the AdCP protocol spec's major version 1:1.**
  `adcp/v3` speaks AdCP 3.x. No Go-only major bumps of `adcp` will happen
  inside a spec-major lifecycle — breaking Go-side API changes wait for the
  next AdCP protocol spec major, or ship as deprecated-plus-added pairs
  within the current major.

- **Additive schema changes ship as minors.** `adcp/v3.N.0` releases carry
  new AdCP 3.x fields, new optional parameters, and new response variants.

- **Bug fixes ship as patches.** `adcp/v3.x.Y` releases fix codegen bugs,
  validator issues, and non-breaking schema-conformance corrections.

- **v2 gets 12 months of security backports.** Aligned with the AdCP
  protocol spec's own commitment to a minimum 12 months of prior-major
  security support. Critical patches are hand-cut from a maintenance branch
  off the `adcp-v2.1.1` tag using the legacy hyphen format.

- **Other sub-modules follow standard SemVer.** `tmproto`, `tmpclient`,
  `targeting`, `urlcanon`, and `registry` cadence is independent of the
  AdCP protocol spec's own major-version cadence.

## Tag-naming history

The legacy `adcp-v1.0.0` through `adcp-v2.1.1` tags used a hyphen prefix
that does not match Go's nested-module tag convention (`adcp/vX.Y.Z`). As a
result, no `adcp` semver tag on the v1 or v2 line was ever resolvable via
`go get` at a real tag. Downstream consumers were on pseudo-versions off
`main` or `replace` directives.

This is not being fixed retroactively — retagging historical commits with
the slash-prefixed convention would let existing pseudo-version pins
accidentally upgrade past what they had validated. Going forward, all
`adcp/vN` releases use the canonical Go nested-module tag format.
`adcp/v3.0.0` is the first correctly-tagged, `go get`-able `adcp` version.

## Acknowledgements

Thanks to everyone who filed issues, tested the reshape against downstream
services, and pushed for a proper module boundary between the schema types,
the TMP wire layer, and the targeting engine. Special thanks to reviewers
who caught the tag-naming quirk before `adcp/v3.0.0` shipped — pinning it
down publicly means downstream consumers can plan their upgrade with clear
eyes.

## Links

- [`MIGRATING.md`](../MIGRATING.md) — full migration guide, import-path map,
  sample `go.mod` diffs, rollback recipes, tag-naming history, spec-major
  alignment policy.
- [`README.md`](../README.md#modules--versioning) — module map at a glance.
- AdCP protocol spec — `github.com/adcontextprotocol/adcp`.
